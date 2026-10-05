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
	"encoding/json"
	"errors"
	"fmt"
	"net"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
	kapi "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher"
)

// Temporary struct used to proceed pods' networks
type podNetworkInfo struct {
	pod       *kapi.Pod
	ibNetwork *v1.NetworkSelectionElement
	networks  []*v1.NetworkSelectionElement
	addr      net.HardwareAddr // GUID allocated for ibNetwork and saved as net.HardwareAddr
	annotated bool
}

type podGUIDInfo struct {
	addr         net.HardwareAddr
	podNetworkID string
}

type networksMap struct {
	theMap map[types.UID][]*v1.NetworkSelectionElement
}

// getPodNetworks returns the parsed networks for a pod, caching the parse
// result so callers iterating over many networks for the same pod don't
// re-parse the annotation.
func (n *networksMap) getPodNetworks(pod *kapi.Pod) ([]*v1.NetworkSelectionElement, error) {
	networks, ok := n.theMap[pod.UID]
	if !ok {
		var err error
		networks, err = netAttUtils.ParsePodNetworkAnnotation(pod)
		if err != nil {
			return nil, fmt.Errorf("failed to read pod networkName annotations pod namespace %s name %s, with error: %v",
				pod.Namespace, pod.Name, err)
		}
		n.theMap[pod.UID] = networks
	}
	return networks, nil
}

type pendingGUID struct {
	pod  *kapi.Pod // nil means cleanup is required; otherwise annotation or membership repair is pending
	pkey string
}

var errPodGone = errors.New("pod deleted or replaced")

// podController owns pod-side state: the GUID pool, the (GUID -> podNetworkID)
// map, the pod informer/watcher, and both add/delete periodic loops. It reads
// NAD state through a narrow PartitionReader, never writing the PKey
// annotation or other NAD fields.
type podController struct {
	pendingGUIDs      map[string]pendingGUID
	kubeClient        k8sClient.Client
	smClient          plugins.SubnetManagerClient
	guidPool          guid.Pool
	guidPodNetworkMap map[string]string // GUID -> podNetworkID
	podWatcher        watcher.Watcher
	partition         PartitionReader
}

// newPodController returns a podController. partition must be non-nil; pass
// the partitionController instance returned from newPartitionController.
func newPodController(
	kubeClient k8sClient.Client,
	smClient plugins.SubnetManagerClient,
	guidPool guid.Pool,
	podWatcher watcher.Watcher,
	partition PartitionReader,
) *podController {
	return &podController{
		kubeClient:        kubeClient,
		smClient:          smClient,
		guidPool:          guidPool,
		guidPodNetworkMap: make(map[string]string),
		podWatcher:        podWatcher,
		partition:         partition,
	}
}

// livePod prevents detached queue snapshots from configuring a deleted owner.
func (c *podController) livePod(pod *kapi.Pod) (*kapi.Pod, error) {
	current, err := c.kubeClient.GetPod(pod.Namespace, pod.Name)
	if kerrors.IsNotFound(err) {
		return nil, errPodGone
	}
	if err != nil {
		return nil, err
	}
	if current == nil || current.UID != pod.UID || current.DeletionTimestamp != nil || utils.PodIsFinished(current) {
		return nil, errPodGone
	}
	return current.DeepCopy(), nil
}

// Retry independently of informer queues: deletion may precede annotation and
// therefore carry no GUID. Original PKeys survive NAD deletion or changes.
func (c *podController) retryPendingCleanup() {
	groups := make(map[string][]net.HardwareAddr)
	for addr, pending := range c.pendingGUIDs {
		if pending.pod != nil {
			_, readErr := c.livePod(pending.pod)
			if readErr == nil {
				// Recover interface identity from the retained pre-annotation Pod,
				// so removed selections are cleaned even without another add event.
				networks, _ := netAttUtils.ParsePodNetworkAnnotation(pending.pod)
				for _, network := range networks {
					requested, _ := utils.GetPodNetworkGUID(network)
					parsed, parseErr := guid.ParseGUID(requested)
					if parseErr == nil && parsed.String() == addr {
						pi := &podNetworkInfo{pod: pending.pod, networks: networks, ibNetwork: network, addr: parsed.HardWareAddress()}
						_, _, _, readErr = c.currentPodNetwork(pi)
						break
					}
				}
			}
			if !errors.Is(readErr, errPodGone) && !errors.Is(readErr, errNetworkChanged) {
				continue
			}
			pending.pod = nil
			c.pendingGUIDs[addr] = pending
		}
		parsed, err := net.ParseMAC(addr)
		if err == nil {
			groups[pending.pkey] = append(groups[pending.pkey], parsed)
		}
	}
	for pkey, addresses := range groups {
		if err := c.removePKeyAndReleaseGUIDs(&utils.IbSriovCniSpec{PKey: pkey}, addresses); err != nil {
			log.Warn().Err(err).Msg("retaining GUIDs for cleanup retry")
			continue
		}
		for _, addr := range addresses {
			delete(c.pendingGUIDs, addr.String())
		}
	}
}

// getIbSriovNetwork returns the network name and parsed CNI spec for a
// networkID. The NAD itself is read through the PartitionReader so podController
// never touches the NAD cache directly.
func (c *podController) getIbSriovNetwork(networkID string) (string, *utils.IbSriovCniSpec, error) {
	_, networkName, err := utils.ParseNetworkID(networkID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse network id %s with error: %v", networkID, err)
	}

	netAttInfo, err := c.partition.GetCachedNAD(networkID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get network attachment %s: %w", networkName, err)
	}
	log.Debug().Msgf("networkName attachment %v", netAttInfo)

	networkSpec := make(map[string]interface{})
	err = json.Unmarshal([]byte(netAttInfo.Spec.Config), &networkSpec)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse networkName attachment %s with error: %v", networkName, err)
	}
	log.Debug().Msgf("networkName attachment spec %+v", networkSpec)

	ibCniSpec, err := utils.GetIbSriovCniFromNetwork(networkSpec)
	if err != nil {
		return "", nil, fmt.Errorf(
			"failed to get InfiniBand SR-IOV CNI spec from network attachment %+v, with error %v",
			networkSpec, err)
	}

	log.Debug().Msgf("ib-sriov CNI spec %+v", ibCniSpec)
	return networkName, ibCniSpec, nil
}

// getAllPodNetworkInfos returns one podNetworkInfo per matching interface on
// the pod, so multi-interface pods produce multiple entries to process.
func getAllPodNetworkInfos(netName string, pod *kapi.Pod, netMap networksMap) ([]*podNetworkInfo, error) {
	networks, err := netMap.getPodNetworks(pod)
	if err != nil {
		return nil, err
	}

	matchingNetworks, err := utils.GetAllPodNetworks(networks, netName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod network specs for network %s with error: %v", netName, err)
	}

	podNetworkInfos := make([]*podNetworkInfo, 0, len(matchingNetworks))
	for _, network := range matchingNetworks {
		podNetworkInfos = append(podNetworkInfos, &podNetworkInfo{
			pod:       pod,
			networks:  networks,
			ibNetwork: network,
		})
	}

	return podNetworkInfos, nil
}

// getAllPodGUIDInfosForNetwork extracts (GUID, podNetworkID) pairs for every
// configured InfiniBand interface on the pod matching networkName.
func getAllPodGUIDInfosForNetwork(pod *kapi.Pod, networkName string) ([]podGUIDInfo, error) {
	networks, netErr := netAttUtils.ParsePodNetworkAnnotation(pod)
	if netErr != nil {
		return nil, fmt.Errorf("failed to read pod networkName annotations pod namespace %s name %s, with error: %v",
			pod.Namespace, pod.Name, netErr)
	}

	matchingNetworks, netErr := utils.GetAllPodNetworks(networks, networkName)
	if netErr != nil {
		return nil, fmt.Errorf("failed to get pod networkName specs %s with error: %v", networkName, netErr)
	}

	guidInfos := make([]podGUIDInfo, 0, len(matchingNetworks))
	for _, network := range matchingNetworks {
		if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
			log.Debug().Msgf("network %+v is not InfiniBand configured, skipping", network)
			continue
		}

		allocatedGUID, netErr := utils.GetPodNetworkGUID(network)
		if netErr != nil {
			log.Debug().Msgf("failed to get GUID for network interface %s: %v", network.InterfaceRequest, netErr)
			continue
		}

		guidAddr, guidErr := net.ParseMAC(allocatedGUID)
		if guidErr != nil {
			log.Error().Msgf("failed to parse allocated Pod GUID %s, error: %v", allocatedGUID, guidErr)
			continue
		}

		podNetworkID := utils.GeneratePodNetworkInterfaceID(
			pod,
			networkName,
			utils.GetPodNetworkInterfaceName(networks, network),
		)
		guidInfos = append(guidInfos, podGUIDInfo{
			addr:         guidAddr,
			podNetworkID: podNetworkID,
		})
	}

	return guidInfos, nil
}

// processPodsForNetwork iterates the pods of a network and, for each
// interface, allocates a GUID via processNetworkGUID. Returns the list of
// successfully allocated GUIDs and the corresponding podNetworkInfo objects.
func (c *podController) processPodsForNetwork(
	pods []*kapi.Pod, networkName string, ibCniSpec *utils.IbSriovCniSpec, netMap networksMap,
) ([]net.HardwareAddr, []*podNetworkInfo) {
	var guidList []net.HardwareAddr
	var passedPods []*podNetworkInfo

	for _, pod := range pods {
		log.Debug().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)

		podNetworkInfos, err := getAllPodNetworkInfos(networkName, pod, netMap)
		if err != nil {
			log.Error().Msgf("%v", err)
			continue
		}

		for _, pi := range podNetworkInfos {
			interfaceName := utils.GetPodNetworkInterfaceName(pi.networks, pi.ibNetwork)
			log.Debug().Msgf("processing interface %s for network %s on pod %s", interfaceName, networkName, pod.Name)

			if err = c.processNetworkGUID(networkName, ibCniSpec, pi); err != nil {
				log.Error().Msgf("failed to process network GUID for interface %s: %v", interfaceName, err)
				continue
			}

			if !pi.annotated {
				guidList = append(guidList, pi.addr)
			}
			passedPods = append(passedPods, pi)
		}
	}

	return guidList, passedPods
}

// allocatePodNetworkGUID makes the GUID-to-pod mapping idempotent: if the
// GUID is already mapped to a different pod, return an error; otherwise either
// keep the existing mapping or allocate a new one from the pool.
func (c *podController) allocatePodNetworkGUID(
	allocatedGUID, podNetworkID string, podUID types.UID, targetPkey string,
) error {
	if mappedID, exists := c.guidPodNetworkMap[allocatedGUID]; exists && mappedID != podNetworkID {
		return fmt.Errorf("GUID %s already belongs to %s", allocatedGUID, mappedID)
	}
	if pending, exists := c.pendingGUIDs[allocatedGUID]; exists {
		if pending.pkey != targetPkey {
			// Retry the new PKey only after removing the original membership.
			pending.pod = nil
			c.pendingGUIDs[allocatedGUID] = pending
		}
		if pending.pod == nil {
			return fmt.Errorf("GUID %s awaits cleanup", allocatedGUID)
		}
	}
	if _, exists := c.guidPodNetworkMap[allocatedGUID]; exists {
		if pkey, _ := c.guidPool.Get(allocatedGUID); pkey != targetPkey {
			return fmt.Errorf("GUID %s still belongs to PKey %s", allocatedGUID, pkey)
		}
		return nil
	}
	if existingPkey, _ := c.guidPool.Get(allocatedGUID); existingPkey != "" {
		if err := c.removeStaleGUID(allocatedGUID, existingPkey); err != nil {
			return err
		}
	}
	if err := c.guidPool.AllocateGUID(allocatedGUID, targetPkey); err != nil {
		return fmt.Errorf("failed to allocate GUID for pod ID %s: %w", podUID, err)
	}
	c.guidPodNetworkMap[allocatedGUID] = podNetworkID

	return nil
}

// processNetworkGUID resolves the GUID for a single pod interface — either
// honoring a user-provided value or generating one from the pool — and
// records it on the podNetworkInfo for the caller.
func (c *podController) processNetworkGUID(networkID string, spec *utils.IbSriovCniSpec, pi *podNetworkInfo) error {
	var guidAddr guid.GUID
	allocatedGUID, err := utils.GetPodNetworkGUID(pi.ibNetwork)
	interfaceName := utils.GetPodNetworkInterfaceName(pi.networks, pi.ibNetwork)
	podNetworkID := utils.GeneratePodNetworkInterfaceID(pi.pod, networkID, interfaceName)
	if err != nil {
		for addr, owner := range c.guidPodNetworkMap {
			if owner == podNetworkID {
				allocatedGUID, err = addr, nil
				break
			}
		}
	}
	if err == nil {
		guidAddr, err = guid.ParseGUID(allocatedGUID)
		if err != nil {
			return fmt.Errorf("failed to parse user allocated guid %s with error: %v", allocatedGUID, err)
		}

		allocatedGUID = guidAddr.String()
		pending, pendingExists := c.pendingGUIDs[allocatedGUID]
		if (!pendingExists || pending.pod != nil) && c.guidPodNetworkMap[allocatedGUID] == podNetworkID &&
			utils.IsPodNetworkConfiguredWithInfiniBand(pi.ibNetwork) {
			originalPKey, _ := c.guidPool.Get(allocatedGUID)
			if pendingExists {
				originalPKey = pending.pkey
			}
			if pkey, pkeyErr := utils.GetPodNetworkPkey(pi.ibNetwork); pkeyErr == nil && pkey == originalPKey {
				// A timed-out patch may have committed. Restore only its original PKey.
				pi.addr = guidAddr.HardWareAddress()
				if c.pendingGUIDs == nil {
					c.pendingGUIDs = make(map[string]pendingGUID)
				}
				c.pendingGUIDs[allocatedGUID] = pendingGUID{pod: pi.pod, pkey: originalPKey}
				if restoreErr := c.ensureConfiguredMembership(pi, originalPKey); restoreErr != nil {
					return restoreErr
				}
				if _, _, _, readErr := c.currentPodNetwork(pi); readErr != nil {
					return readErr
				}
				delete(c.pendingGUIDs, allocatedGUID)
				pi.annotated = true
				return nil
			}
		}
		err = c.allocatePodNetworkGUID(allocatedGUID, podNetworkID, pi.pod.UID, spec.PKey)
		if err != nil {
			return err
		}
	} else {
		guidAddr, err = c.guidPool.GenerateGUID()
		if err != nil {
			switch err {
			// Pool exhausted — resync with SM in case there are unsynced changes.
			case guid.ErrGUIDPoolExhausted:
				err = c.syncWithSubnetManager()
				if err != nil {
					return err
				}
				guidAddr, err = c.guidPool.GenerateGUID()
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("failed to generate GUID for pod ID %s, with error: %v", pi.pod.UID, err)
			}
		}

		allocatedGUID = guidAddr.String()
		err = c.allocatePodNetworkGUID(allocatedGUID, podNetworkID, pi.pod.UID, spec.PKey)
		if err != nil {
			return err
		}
	}
	err = utils.SetPodNetworkGUID(pi.ibNetwork, allocatedGUID, spec.Capabilities["infinibandGUID"])
	if err != nil {
		return fmt.Errorf("failed to set pod network guid with error: %v ", err)
	}

	// Record on the pod so a reschedule doesn't re-allocate.
	netAnnotations, err := json.Marshal(pi.networks)
	if err != nil {
		return fmt.Errorf("failed to dump networks %+v of pod into json with error: %v", pi.networks, err)
	}

	pi.pod.Annotations[v1.NetworkAttachmentAnnot] = string(netAnnotations)

	pi.addr = guidAddr.HardWareAddress()
	if c.pendingGUIDs == nil {
		c.pendingGUIDs = make(map[string]pendingGUID)
	}
	if _, exists := c.pendingGUIDs[allocatedGUID]; !exists {
		c.pendingGUIDs[allocatedGUID] = pendingGUID{pod: pi.pod, pkey: spec.PKey}
	}
	return nil
}

// ensureConfiguredMembership restores absent membership but rejects unexpected PKeys.
func (c *podController) ensureConfiguredMembership(pi *podNetworkInfo, originalPKey string) error {
	if originalPKey == "" {
		return nil
	}
	pkey, err := utils.ParsePKey(originalPKey)
	if err != nil {
		return err
	}
	used, err := c.smClient.ListGuidsInUse()
	if err != nil {
		return err
	}
	for addr, observedPKey := range used {
		parsed, parseErr := guid.ParseGUID(addr)
		if parseErr != nil {
			return parseErr
		}
		if parsed.String() == pi.addr.String() {
			observed, keyErr := utils.ParsePKey(observedPKey)
			if keyErr != nil {
				return keyErr
			}
			if observed == pkey {
				return nil
			}
			return fmt.Errorf("GUID %s is in unexpected PKey %s instead of %s; retaining ownership until corrected",
				pi.addr, observedPKey, originalPKey)
		}
	}
	if _, _, _, err = c.currentPodNetwork(pi); err != nil {
		return err
	}
	return c.smClient.AddGuidsToPKey(pkey, []net.HardwareAddr{pi.addr})
}

// removeStaleGUID drops a GUID from a stale PKey via the subnet manager and
// releases it from the pool. Used when a pod is being rescheduled across
// pkeys and the previous binding still exists.
func (c *podController) removeStaleGUID(allocatedGUID, existingPkey string) error {
	parsedPkey, err := utils.ParsePKey(existingPkey)
	if err != nil {
		log.Error().Msgf("failed to parse PKey %s with error: %v", existingPkey, err)
		return err
	}
	guidAddr, err := guid.ParseGUID(allocatedGUID)
	if err != nil {
		return fmt.Errorf("failed to parse user allocated guid %s with error: %v", allocatedGUID, err)
	}
	allocatedGUIDList := []net.HardwareAddr{guidAddr.HardWareAddress()}
	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		log.Info().Msgf("removing guids of previous pods from pKey %s"+
			" with subnet manager %s", existingPkey,
			c.smClient.Name())
		if err = c.smClient.RemoveGuidsFromPKey(parsedPkey, allocatedGUIDList); err != nil {
			log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
				" with subnet manager %s with error: %v", existingPkey,
				c.smClient.Name(), err)
			return false, nil //nolint:nilerr // retry pattern for exponential backoff
		}
		return true, nil
	}); err != nil {
		log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
			" with subnet manager %s", existingPkey, c.smClient.Name())
		return err
	}

	if err = c.guidPool.ReleaseGUID(allocatedGUID); err != nil {
		log.Warn().Msgf("failed to release guid \"%s\" with error: %v", allocatedGUID, err)
		return err
	}
	delete(c.guidPodNetworkMap, allocatedGUID)
	log.Info().Msgf("successfully released %s from pkey %s", allocatedGUID, existingPkey)
	return nil
}

// currentPodNetwork finds the same interface in the live selection. Never copy
// a stale whole-annotation snapshot over another writer's network changes.
func (c *podController) currentPodNetwork(pi *podNetworkInfo) (
	*kapi.Pod, []*v1.NetworkSelectionElement, *v1.NetworkSelectionElement, error,
) {
	pod, err := c.livePod(pi.pod)
	if err != nil {
		return nil, nil, nil, err
	}
	if pod.Annotations[v1.NetworkAttachmentAnnot] == "" {
		return nil, nil, nil, errNetworkChanged
	}
	networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		return nil, nil, nil, err
	}
	interfaceName := utils.GetPodNetworkInterfaceName(pi.networks, pi.ibNetwork)
	namespace := pi.ibNetwork.Namespace
	if namespace == "" {
		namespace = pod.Namespace
	}
	for _, network := range networks {
		networkNamespace := network.Namespace
		if networkNamespace == "" {
			networkNamespace = pod.Namespace
		}
		if network.Name != pi.ibNetwork.Name || networkNamespace != namespace ||
			utils.GetPodNetworkInterfaceName(networks, network) != interfaceName {
			continue
		}
		if requested, guidErr := utils.GetPodNetworkGUID(network); guidErr == nil && pi.addr != nil {
			parsed, parseErr := guid.ParseGUID(requested)
			if parseErr != nil || parsed.String() != pi.addr.String() {
				return nil, nil, nil, errNetworkChanged
			}
		}
		return pod, networks, network, nil
	}
	return nil, nil, nil, errNetworkChanged
}

var errNetworkChanged = errors.New("pod network selection changed")

func (c *podController) updatePodNetworkAnnotation(
	pi *podNetworkInfo, removedList *[]net.HardwareAddr, pkey string,
) error {
	if pi.addr != nil {
		if c.pendingGUIDs == nil {
			c.pendingGUIDs = make(map[string]pendingGUID)
		}
		if _, exists := c.pendingGUIDs[pi.addr.String()]; !exists {
			c.pendingGUIDs[pi.addr.String()] = pendingGUID{pod: pi.pod, pkey: pkey}
		}
	}
	err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		fresh, networks, network, readErr := c.currentPodNetwork(pi)
		if readErr != nil {
			return false, readErr
		}
		if network.CNIArgs == nil {
			network.CNIArgs = &map[string]interface{}{}
		}
		if pi.addr != nil {
			setErr := utils.SetPodNetworkGUID(network, pi.addr.String(), pi.ibNetwork.InfinibandGUIDRequest != "")
			if setErr != nil {
				return false, setErr
			}
		}
		(*network.CNIArgs)[utils.InfiniBandAnnotation] = utils.ConfiguredInfiniBandPod
		if pkey != "" {
			(*network.CNIArgs)[utils.PkeyAnnotation] = pkey
		}
		data, marshalErr := json.Marshal(networks)
		if marshalErr != nil {
			return false, marshalErr
		}
		patchErr := c.kubeClient.SetAnnotationsOnPod(fresh, map[string]string{v1.NetworkAttachmentAnnot: string(data)})
		if kerrors.IsConflict(patchErr) {
			return false, nil // Re-read and merge against the new resourceVersion.
		}
		return patchErr == nil, patchErr
	})
	if err != nil {
		if pi.addr != nil && (errors.Is(err, errPodGone) || errors.Is(err, errNetworkChanged) || kerrors.IsNotFound(err)) {
			pending := c.pendingGUIDs[pi.addr.String()]
			pending.pod = nil
			c.pendingGUIDs[pi.addr.String()] = pending
			*removedList = append(*removedList, pi.addr)
		}
		return fmt.Errorf("failed to set pod annotations on %s/%s: %w", pi.pod.Namespace, pi.pod.Name, err)
	}
	if pi.addr != nil {
		delete(c.pendingGUIDs, pi.addr.String())
	}
	return nil
}

// AddPeriodicUpdate is the pod-add periodic tick. For each network with
// pending pods, it delegates the partition-aware path to PartitionReader and
// only falls through to the legacy GUID-pool flow when partition is not
// applicable (returns false from ProcessAddIfManaged).
func (c *podController) AddPeriodicUpdate() {
	log.Info().Msgf("running periodic add update")
	defer c.retryPendingCleanup()
	addMap, _ := c.podWatcher.GetHandler().GetResults()

	// Snapshot under lock so the pod informer keeps enqueuing while we process
	// network calls. Holding the lock through backend calls would stall
	// OnAdd/OnDelete events behind every backend call.
	addMap.Lock()
	snapshot := make(map[string]interface{}, len(addMap.Items))
	for k, v := range addMap.Items {
		snapshot[k] = v
	}
	addMap.Unlock()

	netMap := networksMap{theMap: make(map[types.UID][]*v1.NetworkSelectionElement)}
	for networkID, podsInterface := range snapshot {
		log.Info().Msgf("processing network networkID %s", networkID)
		pods, ok := podsInterface.([]*kapi.Pod)
		if !ok {
			log.Error().Msgf(
				"invalid value for add map networks expected pods array \"[]*kubernetes.Pod\", found %T",
				podsInterface)
			continue
		}

		if len(pods) == 0 {
			continue
		}
		networkName, ibCniSpec, err := c.getIbSriovNetwork(networkID)
		if err != nil {
			if kerrors.IsNotFound(err) {
				log.Info().Str("network_id", networkID).Msg("NAD not found, dropping from add queue")
				addMap.Remove(networkID)
			} else {
				log.Warn().Str("network_id", networkID).Err(err).Msg("NAD not ready, will retry")
			}
			continue
		}

		livePods := make([]*kapi.Pod, 0, len(pods))
		for _, pod := range pods {
			current, readErr := c.livePod(pod)
			if readErr != nil {
				if errors.Is(readErr, errPodGone) {
					removeProcessedPodsFromQueue(addMap, networkID, []*kapi.Pod{pod})
				}
				continue
			}
			livePods = append(livePods, current)
		}
		pods = livePods
		if len(pods) == 0 {
			continue
		}

		if c.partition.ProcessAddIfManaged(networkID, networkName, pods, netMap, addMap) {
			continue
		}

		guidList, passedPods := c.processPodsForNetwork(pods, networkName, ibCniSpec, netMap)

		if err = c.addPKeyAndUpdatePods(ibCniSpec, guidList, passedPods); err != nil {
			log.Warn().Err(err).Msg("some pod networks remain pending")
		}

		// Cached network elements identify interfaces within this pass, even
		// when the queue contains the same Pod more than once.
		annotated := make(map[*v1.NetworkSelectionElement]bool)
		for _, pi := range passedPods {
			if pi.annotated {
				annotated[pi.ibNetwork] = true
			}
		}
		var processed []*kapi.Pod
		for _, pod := range pods {
			infos, _ := getAllPodNetworkInfos(networkName, pod, netMap)
			complete := len(infos) > 0
			for _, pi := range infos {
				if !annotated[pi.ibNetwork] {
					complete = false
				}
			}
			if complete {
				processed = append(processed, pod)
			}
		}
		removeProcessedPodsFromQueue(addMap, networkID, processed)
	}
	log.Info().Msg("add periodic update finished")
}

// addPKeyAndUpdatePods is the legacy (UFM/noop) add path: send the freshly
// allocated GUIDs to the subnet manager, then write back per-pod annotations,
// while retaining failed writes for retry or independent cleanup.
func (c *podController) addPKeyAndUpdatePods(
	ibCniSpec *utils.IbSriovCniSpec, guidList []net.HardwareAddr, passedPods []*podNetworkInfo,
) error {
	if ibCniSpec.PKey != "" && len(guidList) != 0 {
		pKey, err := utils.ParsePKey(ibCniSpec.PKey)
		if err != nil {
			log.Error().Msgf("failed to parse PKey %s with error: %v", ibCniSpec.PKey, err)
			return err
		}

		if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
			for _, pi := range passedPods {
				if _, _, _, readErr := c.currentPodNetwork(pi); readErr != nil {
					return false, readErr
				}
			}
			if err = c.smClient.AddGuidsToPKey(pKey, guidList); err != nil {
				log.Warn().Msgf("failed to config pKey with subnet manager %s with error : %v",
					c.smClient.Name(), err)
				return false, nil //nolint:nilerr // retry on next backoff iteration
			}
			return true, nil
		}); err != nil {
			log.Error().Msgf("failed to config pKey with subnet manager %s", c.smClient.Name())
			return err
		}
	}

	var removedGUIDList []net.HardwareAddr
	var annotationErr error
	for _, pi := range passedPods {
		if pi.annotated {
			continue
		}
		if err := c.updatePodNetworkAnnotation(pi, &removedGUIDList, ibCniSpec.PKey); err != nil {
			annotationErr = err
			log.Error().Err(err).Msg("pod annotation pending")
		} else {
			pi.annotated = true
		}
	}
	return annotationErr
}

// DeletePeriodicUpdate is the pod-delete periodic tick. Partition-managed
// networks are delegated to PartitionReader.DeletePartitionPods; everything
// else falls through to the legacy collect-and-release GUID path.
func (c *podController) DeletePeriodicUpdate() {
	log.Info().Msg("running delete periodic update")
	c.retryPendingCleanup()
	_, deleteMap := c.podWatcher.GetHandler().GetResults()

	// Snapshot pattern as in AddPeriodicUpdate.
	deleteMap.Lock()
	snapshot := make(map[string]interface{}, len(deleteMap.Items))
	for k, v := range deleteMap.Items {
		snapshot[k] = v
	}
	deleteMap.Unlock()

	for networkID, podsInterface := range snapshot {
		log.Info().Msgf("processing network networkID %s", networkID)
		pods, ok := podsInterface.([]*kapi.Pod)
		if !ok {
			log.Error().Msgf("invalid value for add map networks expected pods array \"[]*kubernetes.Pod\", found %T",
				podsInterface)
			continue
		}

		if len(pods) == 0 {
			continue
		}

		// Warm the cache before the managed-network check: on daemon start or
		// leader failover the first delete tick can beat the NAD informer, and a
		// cold IsPartitionManaged would misroute a partition-managed detach to
		// the legacy path and dequeue it without detaching (PF-mode pods carry no
		// pool GUID). GetCachedNAD hits the API on a miss; on failure the check
		// stays false and the legacy path handles retry. Doing this before
		// getIbSriovNetwork also keeps a NAD fetch failure during teardown from
		// dropping a managed detach.
		_, _ = c.partition.GetCachedNAD(networkID)

		if c.partition.IsPartitionManaged(networkID) {
			if err := c.partition.DeletePartitionPods(networkID, pods); err != nil {
				// Retain the queue entry so the next periodic tick retries the
				// detach. Dropping it here would leave the node/instance
				// attached to a partition we asked the backend to release.
				// ErrPending also flows through here — keeps the entry until
				// the backend confirms convergence to default partition.
				log.Warn().Str("network_id", networkID).Err(err).
					Msg("partition detach failed/pending, retrying on next tick")
				continue
			}
			removeProcessedPodsFromQueue(deleteMap, networkID, pods)
			continue
		}

		// The NAD can change or disappear after allocation. The pool retains
		// the original PKey; transfer cleanup before dropping the Pod event.
		_, networkName, err := utils.ParseNetworkID(networkID)
		if err != nil {
			log.Warn().Err(err).Msg("invalid deleted network ID")
			continue
		}
		if c.pendingGUIDs == nil {
			c.pendingGUIDs = make(map[string]pendingGUID)
		}
		for _, addr := range c.collectMatchedGUIDs(pods, networkName) {
			pkey, _ := c.guidPool.Get(addr.String())
			if pending, exists := c.pendingGUIDs[addr.String()]; exists {
				pkey = pending.pkey
			}
			c.pendingGUIDs[addr.String()] = pendingGUID{pkey: pkey}
		}
		removeProcessedPodsFromQueue(deleteMap, networkID, pods)
	}

	c.retryPendingCleanup()
	log.Info().Msg("delete periodic update finished")
}

// collectMatchedGUIDs collects GUIDs from pods that match the given network
// and are tracked in guidPodNetworkMap. Uses interface-aware podNetworkIDs
// so multi-interface pods don't have GUIDs incorrectly skipped.
func (c *podController) collectMatchedGUIDs(pods []*kapi.Pod, networkName string) []net.HardwareAddr {
	var guidList []net.HardwareAddr
	for _, pod := range pods {
		log.Debug().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)

		podGUIDInfos, err := getAllPodGUIDInfosForNetwork(pod, networkName)
		if err != nil {
			log.Error().Msgf("%v", err)
			continue
		}

		for _, info := range podGUIDInfos {
			if guidPodEntry, exist := c.guidPodNetworkMap[info.addr.String()]; exist {
				if info.podNetworkID == guidPodEntry {
					log.Info().Msgf("matched guid %s to pod %s, removing", info.addr, guidPodEntry)
					guidList = append(guidList, info.addr)
				} else {
					log.Warn().Msgf("guid %s is allocated to another pod %s not %s, not removing",
						info.addr, guidPodEntry, info.podNetworkID)
				}
			} else {
				log.Warn().Msgf("guid %s is not allocated to any pod on delete", info.addr)
			}
		}
	}
	return guidList
}

// removePKeyAndReleaseGUIDs removes GUIDs from PKey via subnet manager and
// releases them from the pool.
func (c *podController) removePKeyAndReleaseGUIDs(ibCniSpec *utils.IbSriovCniSpec, guidList []net.HardwareAddr) error {
	if ibCniSpec.PKey != "" && len(guidList) != 0 {
		pKey, pkeyErr := utils.ParsePKey(ibCniSpec.PKey)
		if pkeyErr != nil {
			log.Error().Msgf("failed to parse PKey %s with error: %v", ibCniSpec.PKey, pkeyErr)
			return pkeyErr
		}

		if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
			if rmErr := c.smClient.RemoveGuidsFromPKey(pKey, guidList); rmErr != nil {
				log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
					" with subnet manager %s with error: %v", ibCniSpec.PKey,
					c.smClient.Name(), rmErr)
				return false, nil
			}
			return true, nil
		}); err != nil {
			log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
				" with subnet manager %s", ibCniSpec.PKey, c.smClient.Name())
			return err
		}
		// A successful request can precede fabric convergence. Keep ownership
		// until none of these GUIDs appears in the backend's current view.
		used, err := c.smClient.ListGuidsInUse()
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(used))
		for addr := range used {
			parsed, parseErr := net.ParseMAC(addr)
			if parseErr != nil || len(parsed) != 8 {
				return fmt.Errorf("invalid observed GUID %q", addr)
			}
			present[parsed.String()] = true
		}
		for _, addr := range guidList {
			if present[addr.String()] {
				return fmt.Errorf("GUID %s still present in subnet manager", addr)
			}
		}
	}

	for _, guidAddr := range guidList {
		if err := c.guidPool.ReleaseGUID(guidAddr.String()); err != nil {
			return err
		}

		delete(c.guidPodNetworkMap, guidAddr.String())
	}

	return nil
}

// initGUIDPool rebuilds guidPodNetworkMap from running pods, then resyncs
// with the subnet manager and prunes stale GUIDs. Called once when this
// daemon becomes leader.
func (c *podController) initGUIDPool() error {
	log.Info().Msg("Initializing GUID pool.")

	var pods *kapi.PodList
	if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		var err error
		if pods, err = c.kubeClient.GetPods(kapi.NamespaceAll); err != nil {
			log.Warn().Msgf("failed to get pods from kubernetes: %v", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		err = fmt.Errorf("failed to get pods from kubernetes")
		log.Error().Msgf("%v", err)
		return err
	}

	for index := range pods.Items {
		log.Debug().Msgf("checking pod for network annotations %v", pods.Items[index])
		pod := pods.Items[index]
		if utils.PodIsFinished(&pod) {
			continue
		}
		networks, err := netAttUtils.ParsePodNetworkAnnotation(&pod)
		if err != nil {
			continue
		}

		for _, network := range networks {
			if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
				continue
			}

			podGUID, err := utils.GetPodNetworkGUID(network)
			if err != nil {
				continue
			}

			parsedGUID, parseErr := guid.ParseGUID(podGUID)
			if parseErr != nil {
				log.Warn().Err(parseErr).Str("guid", podGUID).Msg("skipping invalid configured GUID")
				continue
			}
			podGUID = parsedGUID.String()

			podNetworkID := utils.GeneratePodNetworkInterfaceID(
				&pod,
				network.Name,
				utils.GetPodNetworkInterfaceName(networks, network),
			)
			if _, exist := c.guidPodNetworkMap[podGUID]; exist {
				if podNetworkID != c.guidPodNetworkMap[podGUID] {
					return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
						podGUID, c.guidPodNetworkMap[podGUID])
				}
				continue
			}
			podPkey, _ := utils.GetPodNetworkPkey(network)
			if err = c.guidPool.AllocateGUID(podGUID, podPkey); err != nil {
				err = fmt.Errorf("failed to allocate guid for running pod: %v", err)
				log.Error().Msgf("%v", err)
				continue
			}

			c.guidPodNetworkMap[podGUID] = podNetworkID
		}
	}

	return c.syncWithSubnetManager()
}

// syncWithSubnetManager reserves observed GUIDs without losing the original
// PKey or reservation of an owned GUID when the backend view changes.
func (c *podController) syncWithSubnetManager() error {
	observed, err := c.smClient.ListGuidsInUse()
	if err != nil {
		return err
	}
	usedGuids := make(map[string]string, len(observed))
	for addr, pkey := range observed {
		parsed, parseErr := guid.ParseGUID(addr)
		if parseErr != nil {
			return fmt.Errorf("invalid observed GUID %q: %w", addr, parseErr)
		}
		usedGuids[parsed.String()] = pkey
	}
	for addr := range c.guidPodNetworkMap {
		pkey, getErr := c.guidPool.Get(addr)
		if getErr != nil {
			return getErr
		}
		usedGuids[addr] = pkey
	}
	for addr, pending := range c.pendingGUIDs {
		usedGuids[addr] = pending.pkey
	}
	return c.guidPool.Reset(usedGuids)
}
