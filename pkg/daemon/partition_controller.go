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
	"sync"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/rs/zerolog/log"
	kapi "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"

	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher"
)

// PartitionReader is the read-only view podController uses to query NAD and
// partition state owned by partitionController. Keeping it narrow prevents
// the pod side from accidentally mutating NAD caches or the PKey annotation.
type PartitionReader interface {
	GetCachedNAD(networkID string) (*v1.NetworkAttachmentDefinition, error)
	IsPartitionManaged(networkID string) bool
	ProcessAddIfManaged(networkID, networkName string, pods []*kapi.Pod,
		netMap networksMap, addMap *utils.SynchronizedMap) bool
	// DeletePartitionPods returns a non-nil error (including plugins.ErrPending)
	// when any pod's detach failed or the backend is still converging, so the
	// caller keeps the delete-queue entry and retries on the next tick.
	DeletePartitionPods(networkID string, pods []*kapi.Pod) error
}

// partitionController owns NAD lifecycle for ibKubernetesEnabled networks:
// the NAD cache, partition create/delete, PKey annotation, and the
// PartitionNADFinalizer. It is the sole writer of utils.PartitionKeyAnnotation
// on any NAD — pod-side code reads through PartitionReader.
type partitionController struct {
	// nadCache stores *v1.NetworkAttachmentDefinition keyed by networkID
	// ("namespace_name"). Written exclusively by cacheNAD/deleteCachedNAD.
	nadCache sync.Map

	// partitionManagedCache stores the ibKubernetesEnabled bool keyed by
	// networkID. Parsed once on cache write so isPartitionManagedNAD is a
	// single Load — no JSON unmarshal on every hot-path call.
	partitionManagedCache sync.Map

	fabricClient plugins.FabricClient // nil for plugins that don't manage partitions
	smClient     plugins.SubnetManagerClient
	kubeClient   k8sClient.Client
	nadWatcher   watcher.Watcher

	// updatePodAnnotation is a function reference into podController. It lets
	// processPartitionPods write pod annotations without owning pod state
	// (GUID pool, kubeClient for pod calls live in podController). Set by
	// daemon.NewDaemon after both controllers are constructed.
	updatePodAnnotation func(*podNetworkInfo, *[]net.HardwareAddr, string) error
}

// newPartitionController returns a partitionController. updatePodAnnotation is
// wired by the daemon constructor after podController exists; nil at this
// point means partition-aware code paths haven't started yet.
func newPartitionController(
	fabricClient plugins.FabricClient,
	smClient plugins.SubnetManagerClient,
	kubeClient k8sClient.Client,
	nadWatcher watcher.Watcher,
) *partitionController {
	return &partitionController{
		fabricClient: fabricClient,
		smClient:     smClient,
		kubeClient:   kubeClient,
		nadWatcher:   nadWatcher,
	}
}

// networkIDForNAD returns the "namespace_name" key used everywhere the daemon
// addresses a NAD. Single helper so the format never drifts.
func networkIDForNAD(nad *v1.NetworkAttachmentDefinition) string {
	return fmt.Sprintf("%s_%s", nad.Namespace, nad.Name)
}

// cacheNAD writes both the NAD and its parsed ibKubernetesEnabled bool
// atomically. Doing both writes here is what lets isPartitionManagedNAD stay
// a single Load.
func (p *partitionController) cacheNAD(networkID string, nad *v1.NetworkAttachmentDefinition) {
	p.nadCache.Store(networkID, nad)
	p.partitionManagedCache.Store(networkID, isPartitionManagedConfig(networkID, nad.Spec.Config))
}

func (p *partitionController) deleteCachedNAD(networkID string) {
	p.nadCache.Delete(networkID)
	p.partitionManagedCache.Delete(networkID)
}

// IsPartitionManaged satisfies PartitionReader. Returns false for any NAD that
// hasn't been cached yet — the parsed bit is the only source of truth.
func (p *partitionController) IsPartitionManaged(networkID string) bool {
	if val, ok := p.partitionManagedCache.Load(networkID); ok {
		enabled, _ := val.(bool)
		return enabled
	}
	return false
}

// getPartitionKeyFromNAD reads the PartitionKeyAnnotation off the cached NAD,
// or empty if the NAD isn't cached / has no annotation.
func (p *partitionController) getPartitionKeyFromNAD(networkID string) string {
	if val, ok := p.nadCache.Load(networkID); ok {
		nad := val.(*v1.NetworkAttachmentDefinition)
		if nad.Annotations != nil {
			return nad.Annotations[utils.PartitionKeyAnnotation]
		}
	}
	return ""
}

// GetCachedNAD satisfies PartitionReader. Cache-then-API read; populates the
// cache on miss so subsequent lookups are free.
func (p *partitionController) GetCachedNAD(networkID string) (*v1.NetworkAttachmentDefinition, error) {
	if val, ok := p.nadCache.Load(networkID); ok {
		return val.(*v1.NetworkAttachmentDefinition), nil
	}

	networkNamespace, networkName, err := utils.ParseNetworkID(networkID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse network id %s with error: %v", networkID, err)
	}

	var netAttInfo *v1.NetworkAttachmentDefinition
	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		var getErr error
		netAttInfo, getErr = p.kubeClient.GetNetworkAttachmentDefinition(networkNamespace, networkName)
		if getErr != nil {
			if kerrors.IsNotFound(getErr) {
				return false, getErr
			}
			log.Warn().Str("namespace", networkNamespace).Str("name", networkName).Err(getErr).
				Msg("failed to get network attachment, retrying")
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to get network attachment %s/%s: %w", networkNamespace, networkName, err)
	}

	p.cacheNAD(networkID, netAttInfo)
	return netAttInfo, nil
}

// isPartitionManagedConfig parses a NAD's CNI config and returns true when
// ibKubernetesEnabled=true. Package-level because cacheNAD calls it eagerly.
func isPartitionManagedConfig(networkID, config string) bool {
	networkSpec := make(map[string]interface{})
	if err := json.Unmarshal([]byte(config), &networkSpec); err != nil {
		log.Warn().Str("network_id", networkID).Err(err).Msg("failed to parse NAD config")
		return false
	}
	enabled, ok := networkSpec[utils.IBKubernetesEnabled].(bool)
	return ok && enabled
}

// hasPartitionFinalizer reports whether the NAD carries the partition finalizer
// the controller uses to gate backend partition deletion.
func hasPartitionFinalizer(nad *v1.NetworkAttachmentDefinition) bool {
	for _, f := range nad.Finalizers {
		if f == utils.PartitionNADFinalizer {
			return true
		}
	}
	return false
}

// ProcessNADChanges is the periodic tick that drives the NAD lifecycle. It
// drains the added/deleted queues from the NAD watcher and delegates to
// processNADResults, which contains the actual logic (split out for
// testability — tests pass crafted maps without standing up a watcher).
func (p *partitionController) ProcessNADChanges() {
	nadHandler := p.nadWatcher.GetHandler().(nadResultsProvider)
	addedNADs, deletedNADs := nadHandler.GetResults()
	p.processNADResults(addedNADs, deletedNADs)
}

// processNADResults applies the partition lifecycle to a set of added and
// deleted NAD queue contents. handleNADDeletion is only called when the NAD
// carries PartitionNADFinalizer — the signal that *we* created the partition
// (resolves A3 from PR #228 review without pushing idempotency onto plugins).
func (p *partitionController) processNADResults(addedNADs, deletedNADs *utils.SynchronizedMap) {
	log.Debug().Msg("Processing NAD changes...")

	type nadEntry struct {
		networkID string
		nad       *v1.NetworkAttachmentDefinition
	}
	addedNADs.Lock()
	pendingNADs := make([]nadEntry, 0, len(addedNADs.Items))
	for networkID, nad := range addedNADs.Items {
		pendingNADs = append(pendingNADs, nadEntry{networkID, nad.(*v1.NetworkAttachmentDefinition)})
	}
	addedNADs.Unlock()

	for _, entry := range pendingNADs {
		p.cacheNAD(entry.networkID, entry.nad)

		if p.fabricClient != nil {
			if err := p.setupPartitionForNAD(entry.nad); err != nil {
				log.Warn().Msgf("Failed to setup partition for NAD %s/%s: %v (will retry)",
					entry.nad.Namespace, entry.nad.Name, err)
				continue
			}
		}

		log.Info().Msgf("Successfully processed NAD add event: %s", entry.networkID)
		addedNADs.Remove(entry.networkID)
	}

	// Backfill PKey annotation for partition-managed NADs whose partition
	// exists but isn't annotated yet (e.g. annotation lost after a daemon
	// restart). updateNADPartitionKey re-stores into nadCache; sync.Map
	// permits mutation during Range, so the write-back is safe.
	if p.fabricClient != nil {
		p.nadCache.Range(func(key, value interface{}) bool {
			networkID := key.(string)
			nad := value.(*v1.NetworkAttachmentDefinition)
			if !p.IsPartitionManaged(networkID) {
				return true
			}
			if nad.Annotations != nil && nad.Annotations[utils.PartitionKeyAnnotation] != "" {
				return true
			}
			ready, partitionKey, err := p.fabricClient.IsPartitionReady(networkID)
			if err != nil {
				log.Debug().Str("network_id", networkID).Err(err).
					Msg("partition readiness check failed")
				return true
			}
			if ready && partitionKey != "" {
				log.Info().Str("network_id", networkID).Str("pkey", partitionKey).
					Msg("partition ready, updating NAD with PKey")
				if upErr := p.updateNADPartitionKey(networkID, partitionKey); upErr != nil {
					log.Warn().Str("network_id", networkID).Err(upErr).
						Msg("failed to update NAD partition key")
				}
			}
			return true
		})
	}

	if deletedNADs != nil {
		// Snapshot the queue so the informer can keep enqueuing while we
		// run blocking backend calls + retries below. Holding the lock
		// across handleNADDeletion (which uses wait.ExponentialBackoff)
		// would block OnUpdate/OnAdd for tens of seconds per pass.
		deletedNADs.Lock()
		pendingDeletes := make([]nadEntry, 0, len(deletedNADs.Items))
		for networkID, nad := range deletedNADs.Items {
			pendingDeletes = append(pendingDeletes, nadEntry{networkID, nad.(*v1.NetworkAttachmentDefinition)})
		}
		deletedNADs.Unlock()

		for _, entry := range pendingDeletes {
			// Plugins without a FabricClient never create partitions, so there
			// is nothing to clean up backend-side — just drop the cache entry
			// and dequeue so the deletedNADs queue does not accumulate.
			if p.fabricClient == nil {
				p.deleteCachedNAD(entry.networkID)
				deletedNADs.Remove(entry.networkID)
				continue
			}

			if !hasPartitionFinalizer(entry.nad) {
				p.deleteCachedNAD(entry.networkID)
				deletedNADs.Remove(entry.networkID)
				log.Debug().Str("network_id", entry.networkID).
					Msg("NAD has no partition finalizer, nothing to clean up")
				continue
			}

			log.Info().Msgf("Processing NAD delete event: %s", entry.networkID)
			if err := p.handleNADDeletion(entry.nad); err != nil {
				log.Warn().Msgf("Failed to cleanup partition for NAD %s/%s: %v (will retry)",
					entry.nad.Namespace, entry.nad.Name, err)
				continue
			}

			p.deleteCachedNAD(entry.networkID)
			deletedNADs.Remove(entry.networkID)
			log.Info().Msgf("Successfully processed NAD delete event: %s", entry.networkID)
		}
	}

	log.Debug().Msg("NAD changes processing completed")
}

// setupPartitionForNAD creates an IB partition for a NAD with
// ibKubernetesEnabled. partitionName and the eventual networkID are the same
// "namespace_name" string by construction (see networkIDForNAD).
//
// Defensive: returns nil if fabricClient is unavailable. The only caller
// (processNADResults) already gates on fabricClient != nil, but the guard
// here means a future refactor can't accidentally NPE.
func (p *partitionController) setupPartitionForNAD(nad *v1.NetworkAttachmentDefinition) error {
	if p.fabricClient == nil {
		return nil
	}
	if nad.Annotations != nil && nad.Annotations[utils.PartitionKeyAnnotation] != "" {
		log.Debug().Msgf("NAD %s/%s already has partition-key annotation", nad.Namespace, nad.Name)
		return nil
	}

	networkSpec := make(map[string]interface{})
	if err := json.Unmarshal([]byte(nad.Spec.Config), &networkSpec); err != nil {
		return fmt.Errorf("failed to parse NAD spec: %v", err)
	}

	if !utils.IsIbSriovCniSpec(networkSpec) {
		log.Debug().Msgf("NAD %s/%s is not an ib-sriov network, skipping partition setup", nad.Namespace, nad.Name)
		return nil
	}

	if enabled, ok := networkSpec[utils.IBKubernetesEnabled]; !ok || enabled != true {
		log.Debug().Msgf("NAD %s/%s does not have ibKubernetesEnabled, skipping", nad.Namespace, nad.Name)
		return nil
	}

	partitionName := networkIDForNAD(nad)
	log.Info().Msgf("Creating IB partition for NAD %s/%s with name: %s", nad.Namespace, nad.Name, partitionName)

	partitionKey, err := p.fabricClient.CreateIBPartition(partitionName)
	if err != nil {
		return fmt.Errorf("failed to create IB partition: %v", err)
	}

	log.Info().Msgf("Created IB partition '%s' with PKey: %s for NAD %s/%s",
		partitionName, partitionKey, nad.Namespace, nad.Name)

	if partitionKey != "" {
		// partitionName == networkID by construction (networkIDForNAD).
		return p.updateNADPartitionKey(partitionName, partitionKey)
	}

	// PKey not assigned yet (partition not ready) — just plant the finalizer.
	return p.addNADFinalizer(nad)
}

// addNADFinalizer adds the partition finalizer to a NAD. The post-update
// cacheNAD call is intentional: it refreshes the cached object with the
// resourceVersion that now carries the finalizer.
func (p *partitionController) addNADFinalizer(nad *v1.NetworkAttachmentDefinition) error {
	return wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		freshNAD, err := p.kubeClient.GetNetworkAttachmentDefinition(nad.Namespace, nad.Name)
		if err != nil {
			return false, fmt.Errorf("failed to fetch fresh NAD: %v", err)
		}

		if hasPartitionFinalizer(freshNAD) {
			return true, nil
		}

		freshNAD.Finalizers = append(freshNAD.Finalizers, utils.PartitionNADFinalizer)
		if err := p.kubeClient.UpdateNetworkAttachmentDefinition(freshNAD); err != nil {
			if kerrors.IsConflict(err) {
				return false, nil
			}
			return false, fmt.Errorf("failed to add finalizer to NAD: %v", err)
		}

		p.cacheNAD(networkIDForNAD(freshNAD), freshNAD)
		return true, nil
	})
}

// updateNADPartitionKey annotates a NAD with its partition key and adds the
// partition finalizer in one update. partitionController is the *only* writer
// of this annotation; pod-side code reads it via PartitionReader.
func (p *partitionController) updateNADPartitionKey(networkID, partitionKey string) error {
	networkNamespace, networkName, err := utils.ParseNetworkID(networkID)
	if err != nil {
		return fmt.Errorf("failed to parse network id %s: %v", networkID, err)
	}

	return wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		freshNAD, err := p.kubeClient.GetNetworkAttachmentDefinition(networkNamespace, networkName)
		if err != nil {
			return false, fmt.Errorf("failed to fetch fresh NAD: %v", err)
		}

		if freshNAD.Annotations != nil &&
			freshNAD.Annotations[utils.PartitionKeyAnnotation] == partitionKey &&
			hasPartitionFinalizer(freshNAD) {
			p.cacheNAD(networkID, freshNAD)
			return true, nil
		}

		if freshNAD.Annotations == nil {
			freshNAD.Annotations = make(map[string]string)
		}

		freshNAD.Annotations[utils.PartitionKeyAnnotation] = partitionKey

		if !hasPartitionFinalizer(freshNAD) {
			freshNAD.Finalizers = append(freshNAD.Finalizers, utils.PartitionNADFinalizer)
		}

		if err := p.kubeClient.UpdateNetworkAttachmentDefinition(freshNAD); err != nil {
			if kerrors.IsConflict(err) {
				return false, nil
			}
			return false, fmt.Errorf("failed to update NAD: %v", err)
		}

		log.Info().Msgf("Updated NAD %s/%s with partition-key: %s",
			freshNAD.Namespace, freshNAD.Name, partitionKey)

		p.cacheNAD(networkID, freshNAD)
		return true, nil
	})
}

// handleNADDeletion deletes the backend partition for a Terminating NAD and
// then removes our finalizer so K8s can complete deletion.
//
// Invariant (enforced at the only caller, ProcessNADChanges): nad has
// utils.PartitionNADFinalizer. This is the controller's contract that *we*
// created the partition — plugins don't need DeleteIBPartition to be
// idempotent across NADs we never touched.
//
// Defensive: returns nil if fabricClient is unavailable. The caller already
// gates the call on a fabricClient != nil check; this guard ensures a future
// refactor can't reach DeleteIBPartition through a nil pointer.
func (p *partitionController) handleNADDeletion(nad *v1.NetworkAttachmentDefinition) error {
	if p.fabricClient == nil {
		return nil
	}
	partitionName := networkIDForNAD(nad)
	log.Info().Msgf("Deleting IB partition '%s' for NAD %s/%s", partitionName, nad.Namespace, nad.Name)

	if err := p.fabricClient.DeleteIBPartition(partitionName); err != nil {
		return fmt.Errorf("failed to delete partition '%s': %v", partitionName, err)
	}

	log.Info().Msgf("Successfully deleted IB partition '%s'", partitionName)
	return p.removeNADFinalizer(nad, utils.PartitionNADFinalizer)
}

// removeNADFinalizer drops finalizer from the NAD, retrying on conflict so a
// late informer view doesn't strand a NAD in Terminating.
func (p *partitionController) removeNADFinalizer(nad *v1.NetworkAttachmentDefinition, finalizer string) error {
	return wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		freshNAD, err := p.kubeClient.GetNetworkAttachmentDefinition(nad.Namespace, nad.Name)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return true, nil
			}
			return false, fmt.Errorf("failed to fetch fresh NAD: %v", err)
		}

		newFinalizers := make([]string, 0, len(freshNAD.Finalizers))
		for _, f := range freshNAD.Finalizers {
			if f != finalizer {
				newFinalizers = append(newFinalizers, f)
			}
		}

		if len(newFinalizers) == len(freshNAD.Finalizers) {
			return true, nil
		}

		freshNAD.Finalizers = newFinalizers
		if err := p.kubeClient.UpdateNetworkAttachmentDefinition(freshNAD); err != nil {
			if kerrors.IsConflict(err) {
				return false, nil
			}
			return false, fmt.Errorf("failed to remove finalizer from NAD: %v", err)
		}

		log.Info().Msgf("Removed finalizer %s from NAD %s/%s", finalizer, nad.Namespace, nad.Name)
		return true, nil
	})
}

// ProcessAddIfManaged satisfies PartitionReader. Outer gate of the
// partition-aware pod-add path: ensures the partition has a PKey (creating
// the annotation if needed), then delegates pod GUID work to
// processPartitionPods. Returns true when this controller has taken
// responsibility for the network entry — caller (podController) must then
// skip the legacy GUID path.
func (p *partitionController) ProcessAddIfManaged(
	networkID string,
	networkName string,
	pods []*kapi.Pod,
	netMap networksMap,
	addMap *utils.SynchronizedMap,
) bool {
	if p.fabricClient == nil || !p.IsPartitionManaged(networkID) {
		return false
	}

	partitionKey := p.getPartitionKeyFromNAD(networkID)
	if partitionKey == "" {
		ready, pkey, partErr := p.fabricClient.IsPartitionReady(networkID)
		if partErr != nil {
			log.Warn().Str("network_id", networkID).Err(partErr).
				Msg("partition readiness check failed, will retry")
			return true
		}
		if !ready || pkey == "" {
			log.Debug().Str("network_id", networkID).Int("pods", len(pods)).
				Msg("partition not ready, skipping pods")
			return true
		}
		partitionKey = pkey
		if err := p.updateNADPartitionKey(networkID, partitionKey); err != nil {
			log.Warn().Str("network_id", networkID).Err(err).
				Msg("failed to persist PKey to NAD")
		}
	}

	p.processPartitionPods(pods, networkName, partitionKey, netMap, addMap, networkID)
	return true
}

// processPartitionPods runs the per-pod IB association for a network with a
// known PKey. Uses hardware PF GUIDs from GetNodeIBDevices (PF/fat-VM model)
// and tolerates ErrPending for non-blocking backend provisioning.
func (p *partitionController) processPartitionPods(
	pods []*kapi.Pod,
	networkName string,
	partitionKey string,
	netMap networksMap,
	addMap *utils.SynchronizedMap,
	networkID string,
) {
	if p.fabricClient == nil {
		log.Debug().Str("network_id", networkID).Msg("fabric client unavailable, skipping partition add")
		return
	}

	pKey, err := utils.ParsePKey(partitionKey)
	if err != nil {
		log.Error().Msgf("failed to parse partition key %s: %v", partitionKey, err)
		return
	}

	allProcessed := true
	for _, pod := range pods {
		devices, devErr := p.fabricClient.GetNodeIBDevices(pod.Spec.NodeName)
		if devErr != nil {
			log.Error().Str("node", pod.Spec.NodeName).Err(devErr).
				Msg("failed to get IB devices for node")
			allProcessed = false
			continue
		}

		guids := make([]net.HardwareAddr, 0, len(devices))
		for _, dev := range devices {
			guids = append(guids, dev.GUID)
		}

		if len(guids) == 0 {
			log.Warn().Str("node", pod.Spec.NodeName).
				Str("pod", pod.Name).Str("namespace", pod.Namespace).
				Msg("no IB devices found on node")
			allProcessed = false
			continue
		}

		addErr := p.smClient.AddGuidsToPKey(pKey, guids)
		if addErr != nil {
			if errors.Is(addErr, plugins.ErrPending) {
				log.Debug().Str("pod", pod.Name).Str("namespace", pod.Namespace).
					Str("node", pod.Spec.NodeName).Int("interfaces", len(guids)).
					Msg("backend provisioning pending")
				allProcessed = false
				continue
			}
			log.Error().Str("pod", pod.Name).Str("namespace", pod.Namespace).
				Err(addErr).Msg("AddGuidsToPKey failed")
			allProcessed = false
			continue
		}

		podNetworkInfos, piErr := getAllPodNetworkInfos(networkName, pod, netMap)
		if piErr != nil {
			log.Error().Err(piErr).Msg("failed to get pod network infos")
			allProcessed = false
			continue
		}

		log.Info().Str("pod", pod.Name).Str("namespace", pod.Namespace).
			Str("node", pod.Spec.NodeName).Str("network", networkName).
			Str("pkey", partitionKey).Int("interfaces", len(podNetworkInfos)).
			Msg("IB ready, annotating pod")

		// PF mode: only set mellanox.infiniband.app=configured (no pkey in cni-args).
		// pi.addr is unset so the removedGUIDList sink stays empty.
		var removedGUIDList []net.HardwareAddr
		for _, pi := range podNetworkInfos {
			if updateErr := p.updatePodAnnotation(pi, &removedGUIDList, ""); updateErr != nil {
				log.Error().Err(updateErr).Msg("failed to update pod annotation")
				allProcessed = false
			}
		}
	}

	if allProcessed {
		// Reconcile against the live queue: drop only the pods we just
		// processed, leave any the informer appended after the snapshot.
		removeProcessedPodsFromQueue(addMap, networkID, pods)
	}
}

// DeletePartitionPods satisfies PartitionReader. Removes each deleted pod's
// node-level PF GUIDs from the partition. Assumes one pod per node per
// partition (fat-VM); a per-(networkID,nodeName) refcount over live pods
// would be needed to lift that.
//
// Returns the first error from PKey parse, GetNodeIBDevices, or
// RemoveGuidsFromPKey so the caller can retain its delete-queue entry.
// ErrPending from RemoveGuidsFromPKey (backend still moving interfaces back
// to default) propagates through so DeletePeriodicUpdate keeps the entry
// queued until the backend converges.
//
// Best-effort across pods: a per-pod failure is recorded but doesn't stop
// the remaining pods from being processed in the same pass.
//
// Returns nil — entry can be dequeued — when fabricClient is absent or the
// NAD has no PKey annotation (the partition wasn't ours).
func (p *partitionController) DeletePartitionPods(networkID string, pods []*kapi.Pod) error {
	if p.fabricClient == nil {
		log.Debug().Str("network_id", networkID).Msg("fabric client unavailable, skipping partition delete")
		return nil
	}

	partitionKey := p.getPartitionKeyFromNAD(networkID)
	if partitionKey == "" {
		log.Debug().Str("network_id", networkID).Msg("no partition key on NAD, skipping delete")
		return nil
	}
	pKey, pkeyErr := utils.ParsePKey(partitionKey)
	if pkeyErr != nil {
		return fmt.Errorf("failed to parse partition key %q for network %s: %w",
			partitionKey, networkID, pkeyErr)
	}

	var firstErr error
	recordErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for _, pod := range pods {
		devices, devErr := p.fabricClient.GetNodeIBDevices(pod.Spec.NodeName)
		if devErr != nil {
			log.Error().Str("node", pod.Spec.NodeName).Err(devErr).
				Msg("failed to get IB devices for node on delete")
			recordErr(fmt.Errorf("get IB devices for node %s: %w", pod.Spec.NodeName, devErr))
			continue
		}
		guids := make([]net.HardwareAddr, 0, len(devices))
		for _, dev := range devices {
			guids = append(guids, dev.GUID)
		}
		if len(guids) == 0 {
			continue
		}
		if rmErr := p.smClient.RemoveGuidsFromPKey(pKey, guids); rmErr != nil {
			log.Warn().Str("pod", pod.Name).Str("node", pod.Spec.NodeName).
				Err(rmErr).Msg("failed to remove GUIDs from partition on delete")
			recordErr(fmt.Errorf("remove GUIDs from pkey %s on node %s: %w",
				partitionKey, pod.Spec.NodeName, rmErr))
			continue
		}
		log.Info().Str("pod", pod.Name).Str("node", pod.Spec.NodeName).
			Str("pkey", partitionKey).Int("guids", len(guids)).
			Msg("removed node GUIDs from partition")
	}
	return firstErr
}

// nadResultsProvider is the slice of NADEventHandler that ProcessNADChanges
// uses. Keeping the interface narrow so the watcher handler isn't required
// to expose its full type just for this one consumer.
type nadResultsProvider interface {
	GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap)
}
