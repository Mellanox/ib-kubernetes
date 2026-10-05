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
	annotated bool
	addr      net.HardwareAddr // GUID allocated for ibNetwork and saved as net.HardwareAddr
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

// podController owns pod-side state: the GUID pool, the (GUID -> podNetworkID)
// map, the pod informer/watcher, and both add/delete periodic loops. It reads
// NAD state through a narrow PartitionReader, never writing the PKey
// annotation or other NAD fields.
type podController struct {
	stopErr           func() error
	allocations       map[string]*guidAllocation
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

			guidList = append(guidList, pi.addr)
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
	if mappedID, exists := c.guidPodNetworkMap[allocatedGUID]; exists {
		if mappedID != podNetworkID {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s", allocatedGUID, mappedID)
		}
		if allocation := c.allocations[allocatedGUID]; allocation != nil && allocation.cleanup {
			return fmt.Errorf("GUID %s is awaiting cleanup", allocatedGUID)
		}
		existingPkey, _ := c.guidPool.Get(allocatedGUID)
		if existingPkey != "" && existingPkey != targetPkey {
			if err := c.removeStaleGUID(allocatedGUID, existingPkey); err != nil {
				return err
			}
			return fmt.Errorf("GUID %s previous PKey cleanup scheduled; retry allocation", allocatedGUID)
		}
		return nil
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
	if err = utils.SetPodNetworkGUID(pi.ibNetwork, allocatedGUID, spec.Capabilities["infinibandGUID"]); err != nil {
		return err
	}

	pi.addr = guidAddr.HardWareAddress()
	c.trackAllocation(pi, spec.PKey)
	return nil
}

// updatePodNetworkAnnotation patches a fresh Pod using identity/version
// preconditions. Authoritative deletion transfers cleanup to the allocation
// record; other errors retain ownership and retry the potentially committed write.
func (c *podController) updatePodNetworkAnnotation(
	pi *podNetworkInfo, removedList *[]net.HardwareAddr, pkey string,
) error {
	err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		fresh, networks, target, readErr := c.liveNetwork(pi)
		if readErr != nil {
			return false, readErr
		}
		if target.CNIArgs == nil {
			target.CNIArgs = &map[string]interface{}{}
		}
		if pi.addr != nil {
			if setErr := utils.SetPodNetworkGUID(target, pi.addr.String(),
				pi.ibNetwork.InfinibandGUIDRequest != ""); setErr != nil {
				return false, setErr
			}
		}
		(*target.CNIArgs)[utils.InfiniBandAnnotation] = utils.ConfiguredInfiniBandPod
		if pkey != "" {
			(*target.CNIArgs)[utils.PkeyAnnotation] = pkey
		}
		data, marshalErr := json.Marshal(networks)
		if marshalErr != nil {
			return false, marshalErr
		}
		if stopErr := c.stopped(); stopErr != nil {
			return false, stopErr
		}
		patchErr := c.kubeClient.SetAnnotationsOnPod(fresh, map[string]string{v1.NetworkAttachmentAnnot: string(data)})
		if kerrors.IsConflict(patchErr) {
			return false, nil
		}
		return patchErr == nil, patchErr
	})
	if err != nil {
		if pi.addr != nil && isAbandonedPod(err) {
			c.markCleanup(pi.addr.String())
			*removedList = append(*removedList, pi.addr)
		}
		return fmt.Errorf("failed to set pod annotations on %s/%s: %w", pi.pod.Namespace, pi.pod.Name, err)
	}
	return nil
}

// AddPeriodicUpdate is the pod-add periodic tick. For each network with
// pending pods, it delegates the partition-aware path to PartitionReader and
// only falls through to the legacy GUID-pool flow when partition is not
// applicable (returns false from ProcessAddIfManaged).
func (c *podController) AddPeriodicUpdate() {
	log.Info().Msgf("running periodic add update")
	defer c.processPendingCleanup()
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
		seen := make(map[types.UID]bool)
		for _, queued := range pods {
			if seen[queued.UID] {
				continue
			}
			seen[queued.UID] = true
			live, readErr := c.livePod(queued)
			if readErr != nil {
				if isAbandonedPod(readErr) {
					removeProcessedPodsFromQueue(addMap, networkID, []*kapi.Pod{queued})
				}
				continue
			}
			livePods = append(livePods, live)
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
			log.Warn().Err(err).Msg("pod setup incomplete; retrying pending owners")
		}
		// Dequeue each completed Pod independently of unrelated allocation/patch
		// failures on the same network. Partial multi-interface Pods stay queued.
		annotated := make(map[types.UID]int)
		for _, pi := range passedPods {
			if pi.annotated {
				annotated[pi.pod.UID]++
			}
		}
		for _, pod := range pods {
			expected := countNetworkInterfaces([]*kapi.Pod{pod}, networkName, netMap)
			if expected > 0 && annotated[pod.UID] == expected {
				removeProcessedPodsFromQueue(addMap, networkID, []*kapi.Pod{pod})
			}
		}
	}
	log.Info().Msg("add periodic update finished")
}

// addPKeyAndUpdatePods is the legacy (UFM/noop) add path: send the freshly
// allocated GUIDs to the subnet manager, then write back per-pod annotations,
// and remove any pod whose annotation write failed.
func (c *podController) addPKeyAndUpdatePods(
	ibCniSpec *utils.IbSriovCniSpec, _ []net.HardwareAddr, passedPods []*podNetworkInfo,
) error {
	var ready []*podNetworkInfo
	var guids []net.HardwareAddr
	for _, pi := range passedPods {
		if _, _, _, err := c.liveNetwork(pi); err != nil {
			if isAbandonedPod(err) {
				c.markCleanup(pi.addr.String())
			}
			return err
		}
		ready = append(ready, pi)
		if !c.allocations[pi.addr.String()].programmed {
			guids = append(guids, pi.addr)
		}
	}
	if ibCniSpec.PKey != "" && len(guids) != 0 {
		pkey, err := utils.ParsePKey(ibCniSpec.PKey)
		if err != nil {
			return err
		}
		// One attempt per tick. An error may mean the request was applied; never
		// discard its ownership just because the Pod's queue entry was canceled.
		if err = c.stopped(); err != nil {
			return err
		}
		if err = c.smClient.AddGuidsToPKey(pkey, guids); err != nil {
			for _, addr := range guids {
				a := c.allocations[addr.String()]
				a.uncertain = true
				// Retry the same GUID for a live owner. Only abandoned owners
				// transition to cleanup; quarantine prevents reuse by a new owner.
				if _, _, _, readErr := c.liveNetwork(a.info); isAbandonedPod(readErr) {
					a.cleanup = true
				}
			}
			return err
		}
	}
	for _, addr := range guids {
		c.allocations[addr.String()].programmed = true
	}
	var removed []net.HardwareAddr
	var annotationErr error
	for _, pi := range ready {
		if err := c.updatePodNetworkAnnotation(pi, &removed, ibCniSpec.PKey); err != nil {
			annotationErr = err
			log.Warn().Err(err).Msg("pod annotation failed; retaining allocation ownership")
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
	c.reconcileAllocations()
	defer c.processPendingCleanup()
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

		// Legacy path: use pool-allocated GUIDs from pod annotations.
		networkName, ibCniSpec, err := c.getIbSriovNetwork(networkID)
		if err != nil {
			deleteMap.Remove(networkID)
			log.Warn().Msgf("droping network: %v", err)
			continue
		}

		guidList := c.collectMatchedGUIDs(pods, networkName)
		c.queueCleanup(ibCniSpec, guidList)
		removeProcessedPodsFromQueue(deleteMap, networkID, pods)
	}

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

// queueCleanup transfers ownership from delete events to independent records.
func (c *podController) queueCleanup(ibCniSpec *utils.IbSriovCniSpec, guidList []net.HardwareAddr) {
	for _, addr := range guidList {
		key := addr.String()
		if c.allocations == nil {
			c.allocations = make(map[string]*guidAllocation)
		}
		if c.allocations[key] == nil {
			c.allocations[key] = &guidAllocation{pkey: ibCniSpec.PKey}
		}
		c.markCleanup(key)
	}
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
			parsedGUID, err := guid.ParseGUID(podGUID)
			if err != nil {
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
			addr, parseErr := net.ParseMAC(podGUID)
			if parseErr != nil {
				return parseErr
			}
			c.trackAllocation(&podNetworkInfo{pod: &pod, ibNetwork: network, networks: networks, addr: addr}, podPkey)
			c.allocations[podGUID].programmed = true
			if utils.PodIsFinished(&pod) || pod.DeletionTimestamp != nil {
				c.markCleanup(podGUID)
			}
		}
	}

	return c.syncWithSubnetManager()
}

// syncWithSubnetManager merges observed reservations with process-local owners.
// Backend absence alone never releases an active or quarantined GUID.
func (c *podController) syncWithSubnetManager() error {
	usedGuids, err := c.observedGUIDs()
	if err != nil {
		return err
	}

	// Backend absence is not ownership release. Preserve both active and
	// quarantined allocations, including ones whose add is still in flight.
	for addr := range c.guidPodNetworkMap {
		if _, exists := usedGuids[addr]; !exists {
			pkey, getErr := c.guidPool.Get(addr)
			if getErr != nil {
				return getErr
			}
			usedGuids[addr] = pkey
		}
	}
	for addr, a := range c.allocations {
		usedGuids[addr] = a.pkey
	}
	return c.guidPool.Reset(usedGuids)
}
