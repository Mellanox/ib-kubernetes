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
	existingPkey, _ := c.guidPool.Get(allocatedGUID)
	if existingPkey != "" {
		if err := c.removeStaleGUID(allocatedGUID, existingPkey); err != nil {
			log.Warn().Msgf("failed to remove stale GUID %s from pkey %s: %v", allocatedGUID, existingPkey, err)
		}
	}
	if mappedID, exist := c.guidPodNetworkMap[allocatedGUID]; exist {
		if podNetworkID != mappedID {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
				allocatedGUID, mappedID)
		}
	} else if err := c.guidPool.AllocateGUID(allocatedGUID, targetPkey); err != nil {
		return fmt.Errorf("failed to allocate GUID for pod ID %s, with error: %v", podUID, err)
	} else {
		c.guidPodNetworkMap[allocatedGUID] = podNetworkID
	}

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
	if err == nil {
		guidAddr, err = guid.ParseGUID(allocatedGUID)
		if err != nil {
			return fmt.Errorf("failed to parse user allocated guid %s with error: %v", allocatedGUID, err)
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
			default:
				return fmt.Errorf("failed to generate GUID for pod ID %s, with error: %v", pi.pod.UID, err)
			}
		}

		allocatedGUID = guidAddr.String()
		err = c.allocatePodNetworkGUID(allocatedGUID, podNetworkID, pi.pod.UID, spec.PKey)
		if err != nil {
			return err
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
	}

	pi.addr = guidAddr.HardWareAddress()
	return nil
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

// updatePodNetworkAnnotation writes the pod's network annotation back to the
// API server. On failure it releases the GUID from the pool so the next pass
// can re-allocate; the failing pod's GUID is appended to *removedList so the
// caller can drop it from the PKey.
//
// partitionController invokes this through the function reference wired in
// daemon.NewDaemon — pod state mutations stay here regardless of which loop
// triggered them.
func (c *podController) updatePodNetworkAnnotation(
	pi *podNetworkInfo, removedList *[]net.HardwareAddr, pkey string,
) error {
	if pi.ibNetwork.CNIArgs == nil {
		pi.ibNetwork.CNIArgs = &map[string]interface{}{}
	}

	(*pi.ibNetwork.CNIArgs)[utils.InfiniBandAnnotation] = utils.ConfiguredInfiniBandPod
	if pkey != "" {
		(*pi.ibNetwork.CNIArgs)[utils.PkeyAnnotation] = pkey
	}

	netAnnotations, err := json.Marshal(pi.networks)
	if err != nil {
		return fmt.Errorf("failed to dump networks %+v of pod into json with error: %v", pi.networks, err)
	}

	pi.pod.Annotations[v1.NetworkAttachmentAnnot] = string(netAnnotations)

	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		if err = c.kubeClient.SetAnnotationsOnPod(pi.pod, pi.pod.Annotations); err != nil {
			if kerrors.IsNotFound(err) {
				return false, err
			}
			log.Warn().Msgf("failed to update pod annotations with err: %v", err)
			return false, nil
		}

		return true, nil
	}); err != nil {
		log.Error().Err(err).Msg("failed to update pod annotations")

		if pi.addr != nil {
			if relErr := c.guidPool.ReleaseGUID(pi.addr.String()); relErr != nil {
				log.Warn().Msgf("failed to release guid \"%s\" from removed pod \"%s\" in namespace "+
					"\"%s\" with error: %v", pi.addr.String(), pi.pod.Name, pi.pod.Namespace, relErr)
			} else {
				delete(c.guidPodNetworkMap, pi.addr.String())
			}
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

		if c.partition.ProcessAddIfManaged(networkID, networkName, pods, netMap, addMap) {
			continue
		}

		guidList, passedPods := c.processPodsForNetwork(pods, networkName, ibCniSpec, netMap)

		if err = c.addPKeyAndUpdatePods(ibCniSpec, guidList, passedPods); err != nil {
			continue
		}

		// Reconcile against the live queue: drop only the pods we just
		// processed, leave any the informer appended after the snapshot.
		removeProcessedPodsFromQueue(addMap, networkID, pods)
	}
	log.Info().Msg("add periodic update finished")
}

// addPKeyAndUpdatePods is the legacy (UFM/noop) add path: send the freshly
// allocated GUIDs to the subnet manager, then write back per-pod annotations,
// and remove any pod whose annotation write failed.
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
	for _, pi := range passedPods {
		if err := c.updatePodNetworkAnnotation(pi, &removedGUIDList, ibCniSpec.PKey); err != nil {
			log.Error().Msgf("%v", err)
		}
	}

	if ibCniSpec.PKey != "" && len(removedGUIDList) != 0 {
		pKey, _ := utils.ParsePKey(ibCniSpec.PKey)

		if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
			if rmErr := c.smClient.RemoveGuidsFromPKey(pKey, removedGUIDList); rmErr != nil {
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
	}

	return nil
}

// DeletePeriodicUpdate is the pod-delete periodic tick. Partition-managed
// networks are delegated to PartitionReader.DeletePartitionPods; everything
// else falls through to the legacy collect-and-release GUID path.
func (c *podController) DeletePeriodicUpdate() {
	log.Info().Msg("running delete periodic update")
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
		if err = c.removePKeyAndReleaseGUIDs(ibCniSpec, guidList); err != nil {
			continue
		}
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
	}

	for _, guidAddr := range guidList {
		if err := c.guidPool.ReleaseGUID(guidAddr.String()); err != nil {
			log.Error().Msgf("%v", err)
			continue
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

// syncWithSubnetManager resets the GUID pool from the subnet manager's view
// and prunes map entries whose GUIDs are no longer used (pod gone). Called
// during init and whenever the pool is exhausted at runtime.
func (c *podController) syncWithSubnetManager() error {
	usedGuids, err := c.smClient.ListGuidsInUse()
	if err != nil {
		return err
	}

	if err = c.guidPool.Reset(usedGuids); err != nil {
		return err
	}

	for allocatedGUID, podNetworkID := range c.guidPodNetworkMap {
		if _, found := usedGuids[allocatedGUID]; !found {
			log.Info().Msgf("removing stale GUID %s for pod network %s", allocatedGUID, podNetworkID)
			if err = c.guidPool.ReleaseGUID(allocatedGUID); err != nil {
				log.Warn().Msgf("failed to release stale guid \"%s\" with error: %v", allocatedGUID, err)
			} else {
				delete(c.guidPodNetworkMap, allocatedGUID)
				log.Info().Msgf("successfully cleaned up stale GUID %s", allocatedGUID)
			}
		}
	}

	return nil
}
