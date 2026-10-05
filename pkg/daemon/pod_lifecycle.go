// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"net"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
	kapi "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

var errAbandonedPod = errors.New("pod no longer desires allocation")

// guidAllocation is process-local ownership, not a desired-state cache. It
// outlives informer queue entries and retains the ORIGINAL cleanup coordinates.
// Unknown backend outcomes quarantine the GUID for the remaining leader lifetime:
// an absent read cannot fence a request which timed out but may still execute.
type guidAllocation struct {
	info            *podNetworkInfo
	pkey            string
	cleanup         bool
	programmed      bool
	removalAccepted bool
	uncertain       bool
}

func (c *podController) stopped() error {
	if c.stopErr != nil {
		return c.stopErr()
	}
	return nil
}

func isAbandonedPod(err error) bool {
	return kerrors.IsNotFound(err) || errors.Is(err, errAbandonedPod)
}

func (c *podController) livePod(snapshot *kapi.Pod) (*kapi.Pod, error) {
	if err := c.stopped(); err != nil {
		return nil, err
	}
	fresh, err := c.kubeClient.GetPod(snapshot.Namespace, snapshot.Name)
	if err != nil {
		return nil, err
	}
	if fresh == nil || fresh.UID != snapshot.UID || fresh.DeletionTimestamp != nil ||
		utils.PodIsFinished(fresh) || !utils.PodWantsNetwork(fresh) || !utils.PodScheduled(fresh) {
		return nil, errAbandonedPod
	}
	return fresh.DeepCopy(), nil
}

// liveNetwork locates the same interface in a fresh Pod without overwriting
// other interfaces or carrying old resourceVersions into a conditional patch.
func (c *podController) liveNetwork(pi *podNetworkInfo) (
	*kapi.Pod, []*v1.NetworkSelectionElement, *v1.NetworkSelectionElement, error,
) {
	fresh, err := c.livePod(pi.pod)
	if err != nil {
		return nil, nil, nil, err
	}
	networks, err := netutils.ParsePodNetworkAnnotation(fresh)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: invalid network selection: %v", errAbandonedPod, err)
	}
	interfaceName := utils.GetPodNetworkInterfaceName(pi.networks, pi.ibNetwork)
	for _, network := range networks {
		if network.Name != pi.ibNetwork.Name || network.Namespace != pi.ibNetwork.Namespace ||
			utils.GetPodNetworkInterfaceName(networks, network) != interfaceName {
			continue
		}
		if requested, guidErr := utils.GetPodNetworkGUID(network); guidErr == nil && pi.addr != nil {
			parsed, parseErr := net.ParseMAC(requested)
			if parseErr != nil || !bytes.Equal(parsed, pi.addr) {
				return nil, nil, nil, errAbandonedPod
			}
		}
		return fresh, networks, network, nil
	}
	return nil, nil, nil, errAbandonedPod
}

func (c *podController) trackAllocation(pi *podNetworkInfo, pkey string) {
	if c.allocations == nil {
		c.allocations = make(map[string]*guidAllocation)
	}
	addr := pi.addr.String()
	if c.allocations[addr] == nil {
		c.allocations[addr] = &guidAllocation{info: pi, pkey: pkey}
	}
}

func (c *podController) markCleanup(addr string) {
	if a := c.allocations[addr]; a != nil {
		a.cleanup = true
	}
}

// reconcileAllocations catches lost delete events and partially completed
// setup. API failures preserve ownership; only authoritative absence or
// withdrawn Pod intent initiates cleanup.
func (c *podController) reconcileAllocations() {
	for _, a := range c.allocations {
		if c.stopped() != nil {
			return
		}
		if a.cleanup || a.info == nil {
			continue
		}
		if _, _, _, err := c.liveNetwork(a.info); isAbandonedPod(err) {
			a.cleanup = true
		}
	}
}

// processPendingCleanup batches removals by the original PKey and observes
// the fabric once per pass, rather than once per GUID under Pod churn.
func (c *podController) processPendingCleanup() {
	if c.stopped() != nil {
		return
	}
	// Re-observe previously accepted removals before retrying them. If an
	// accepted request has since converged, do not enqueue a redundant remove
	// that might arrive after GUID reuse. Retried accepted operations keep the
	// reservation quarantined because this API provides no completion token.
	var observed map[string]string
	for _, a := range c.allocations {
		if a.cleanup && a.removalAccepted {
			var err error
			observed, err = c.observedGUIDs()
			if err != nil {
				log.Warn().Err(err).Msg("cannot observe pending removal")
				return
			}
			break
		}
	}
	for addr, a := range c.allocations {
		if a.cleanup && a.removalAccepted {
			if _, present := observed[addr]; present {
				a.removalAccepted = false
				a.uncertain = true
			}
		}
	}
	groups := make(map[int][]net.HardwareAddr)
	needsObservation := false
	for addr, a := range c.allocations {
		if !a.cleanup || a.pkey == "" {
			continue
		}
		needsObservation = true
		pkey, err := utils.ParsePKey(a.pkey)
		if err != nil {
			log.Warn().Err(err).Str("guid", addr).Msg("invalid cleanup PKey")
			continue
		}
		hardwareAddr, err := net.ParseMAC(addr)
		if err != nil {
			continue
		}
		if !a.removalAccepted {
			groups[pkey] = append(groups[pkey], hardwareAddr)
		}
	}
	for pkey, addresses := range groups {
		if c.stopped() != nil {
			return
		}
		err := c.smClient.RemoveGuidsFromPKey(pkey, addresses)
		for _, addr := range addresses {
			a := c.allocations[addr.String()]
			if err != nil {
				a.uncertain = true
			} else {
				a.removalAccepted = true
			}
		}
		if err != nil {
			log.Warn().Err(err).Int("pkey", pkey).Msg("GUID removal failed; retaining reservations")
		}
	}
	if needsObservation && (observed == nil || len(groups) > 0) {
		var err error
		observed, err = c.observedGUIDs()
		if err != nil {
			log.Warn().Err(err).Msg("cannot observe GUID cleanup; retaining reservations")
			return
		}
	}
	for addr, a := range c.allocations {
		if !a.cleanup {
			continue
		}
		if err := c.releaseCleanAllocation(addr, a, observed); err != nil {
			log.Warn().Str("guid", addr).Err(err).Msg("GUID remains reserved for cleanup")
		}
	}
}

func (c *podController) releaseCleanAllocation(addr string, a *guidAllocation, observed map[string]string) error {
	if a.pkey != "" {
		if !a.removalAccepted {
			return fmt.Errorf("removal not yet accepted")
		}
		if _, present := observed[addr]; present {
			// An uncertain add can arrive after an earlier successful removal.
			// Retry while visible, but never recycle its reservation.
			if a.uncertain {
				a.removalAccepted = false
			}
			return fmt.Errorf("GUID is still present in subnet manager")
		}
		if a.uncertain {
			return fmt.Errorf("backend operation outcome uncertain; GUID quarantined")
		}
	}
	if err := c.guidPool.ReleaseGUID(addr); err != nil {
		return err
	}
	delete(c.guidPodNetworkMap, addr)
	delete(c.allocations, addr)
	return nil
}

func countNetworkInterfaces(pods []*kapi.Pod, networkName string, netMap networksMap) int {
	count := 0
	for _, pod := range pods {
		infos, err := getAllPodNetworkInfos(networkName, pod, netMap)
		if err == nil {
			count += len(infos)
		}
	}
	return count
}

// removeStaleGUID is only called after ownership validation. Cleanup remains
// independently retryable even when the caller's add entry disappears.
func (c *podController) removeStaleGUID(addr, pkey string) error {
	if _, err := utils.ParsePKey(pkey); err != nil {
		return err
	}
	parsed, err := net.ParseMAC(addr)
	if err != nil {
		return fmt.Errorf("failed to parse user allocated guid: %w", err)
	}
	c.queueCleanup(&utils.IbSriovCniSpec{PKey: pkey}, []net.HardwareAddr{parsed})
	return nil
}

// observedGUIDs canonicalizes backend spelling before any presence check.
// Unknown/malformed observations must never authorize GUID recycling.
func (c *podController) observedGUIDs() (map[string]string, error) {
	observed, err := c.smClient.ListGuidsInUse()
	if err != nil {
		return nil, err
	}
	normalized := make(map[string]string, len(observed))
	for addr, pkey := range observed {
		parsed, parseErr := net.ParseMAC(addr)
		if parseErr != nil || len(parsed) != 8 {
			return nil, fmt.Errorf("invalid observed GUID %q", addr)
		}
		normalized[parsed.String()] = pkey
	}
	return normalized, nil
}
