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

package binding

import (
	"context"
	"fmt"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"

	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/meta"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// reserveClusterGUIDs fills the empty pool from the GUIDs already on live pods, then from the
// in-range GUIDs the subnet manager still reports, so neither is generated for a new pod.
// It runs once per process, before the first reconcile can generate a GUID.
// A list failure is returned and retried by the next reconcile.
func (reconciler *Reconciler) reserveClusterGUIDs(ctx context.Context) error {
	reconciler.seedMu.Lock()
	defer reconciler.seedMu.Unlock()
	if reconciler.seeded {
		return nil
	}
	var pods corev1.PodList
	if err := reconciler.List(ctx, &pods); err != nil {
		return fmt.Errorf("list pods to reserve guids: %w", err)
	}
	fabric, err := reconciler.fabricPKeys()
	if err != nil {
		return err
	}
	nadPKeys, err := reconciler.nadPKeys(ctx)
	if err != nil {
		return err
	}
	carried := map[string]struct{}{}
	for podIndex := range pods.Items {
		reconciler.reservePodGUIDs(&pods.Items[podIndex], fabric, nadPKeys, carried)
	}
	reconciler.reserveFabricGUIDs(fabric, carried)
	reconciler.seeded = true
	return nil
}

// reserveFabricGUIDs reserves the in-range GUIDs on the fabric that the pod pass left unreserved.
// They stay unowned: the orphan sweep releases them once it removes them from their pkey, and the
// ones no live pod carries are also freed on pool exhaustion once the fabric stops listing them.
func (reconciler *Reconciler) reserveFabricGUIDs(fabric map[string]string, carried map[string]struct{}) {
	reconciler.fabricOnlyGUIDs = map[string]struct{}{}
	for allocated, pkey := range fabric {
		if !reconciler.inPoolRange(allocated) {
			continue
		}
		existing, getErr := reconciler.Pool.Get(allocated)
		if getErr != nil || existing != "" {
			continue
		}
		if allocErr := reconciler.Pool.AllocateGUID(allocated, pkey); allocErr != nil {
			if !alreadyAllocated(allocErr) {
				log.Error().Err(allocErr).Str("guid", allocated).Msg("skipping fabric guid while reserving the pool")
			}
			continue
		}
		if _, onPod := carried[allocated]; !onPod {
			reconciler.fabricOnlyGUIDs[allocated] = struct{}{}
		}
	}
}

// releaseFreedFabricGUIDs frees the fabric-only startup reservations that no interface owns and a
// fresh membership read no longer lists. It reports whether any GUID was freed.
func (reconciler *Reconciler) releaseFreedFabricGUIDs() (bool, error) {
	reconciler.seedMu.Lock()
	defer reconciler.seedMu.Unlock()
	if len(reconciler.fabricOnlyGUIDs) == 0 {
		return false, nil
	}
	reconciler.invalidateMembershipCache()
	fabric, err := reconciler.fabricPKeys()
	if err != nil {
		return false, err
	}
	freed := false
	for allocated := range reconciler.fabricOnlyGUIDs {
		if reconciler.Owners.ownerOf(allocated) != "" {
			delete(reconciler.fabricOnlyGUIDs, allocated)
			continue
		}
		if _, listed := fabric[allocated]; listed {
			continue
		}
		if releaseErr := reconciler.Pool.ReleaseGUID(allocated); releaseErr != nil {
			log.Debug().Err(releaseErr).Str("guid", allocated).Msg("guid was not reserved in the pool")
		}
		delete(reconciler.fabricOnlyGUIDs, allocated)
		freed = true
	}
	return freed, nil
}

// nadPKeys maps each ib-sriov NAD "namespace/name" to the pkeys its pods may be configured on.
func (reconciler *Reconciler) nadPKeys(ctx context.Context) (map[string][]string, error) {
	var nads netv1.NetworkAttachmentDefinitionList
	if err := reconciler.List(ctx, &nads); err != nil {
		return nil, fmt.Errorf("list nads to reserve guids: %w", err)
	}
	out := make(map[string][]string, len(nads.Items))
	for nadIndex := range nads.Items {
		nad := &nads.Items[nadIndex]
		if !meta.IsIbSriov(nad) {
			continue
		}
		if spec, err := meta.Spec(nad); err == nil {
			out[nad.Namespace+"/"+nad.Name] = trustedPKeys(nad, spec)
		}
	}
	return out, nil
}

// reservePodGUIDs claims the GUIDs on one live pod under the pkey the fabric lists them on.
// A conflicting GUID is logged and skipped. The pod annotation is user-writable, so a GUID listed
// on a pkey its NAD does not trust is not claimed: the pod gains no right to keep or remove that
// membership, and the GUID stays an unowned orphan for the orphan policy. Every GUID the pod
// carries is added to carried.
func (reconciler *Reconciler) reservePodGUIDs(
	pod *corev1.Pod, fabric map[string]string, nadPKeys map[string][]string, carried map[string]struct{},
) {
	if !index.Live(pod) {
		return
	}
	networks, err := netutils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		return
	}
	for _, network := range networks {
		allocated, guidErr := utils.GetPodNetworkGUID(network)
		if guidErr != nil {
			continue
		}
		carried[canonicalGUID(allocated)] = struct{}{}
		pkey := fabric[canonicalGUID(allocated)]
		if pkey != "" && !isTrustedPKey(pkey, nadPKeys[networkKey(pod, network)]) {
			continue
		}
		if _, claimErr := reconciler.claimGUID(allocated, ownerID(pod, networks, network), pkey); claimErr != nil {
			log.Error().Err(claimErr).Str("guid", allocated).Str("pod", pod.Namespace+"/"+pod.Name).
				Msg("skipping guid while reserving the pool")
		}
	}
}

// claimGUID records owner as the holder of allocated and reserves it in the pool. orphan reports that
// the GUID was already reserved without an owner, such as a GUID left on the fabric.
func (reconciler *Reconciler) claimGUID(allocated, owner, pkey string) (orphan bool, err error) {
	allocated = canonicalGUID(allocated)
	reconciler.Owners.mu.Lock()
	defer reconciler.Owners.mu.Unlock()
	_, owned := reconciler.Owners.ownerByGUID[allocated]
	if err = reconciler.Owners.setOwnerLocked(allocated, owner); err != nil {
		return false, err
	}
	existing, err := reconciler.Pool.Get(allocated)
	if err != nil {
		delete(reconciler.Owners.ownerByGUID, allocated)
		return false, err
	}
	if existing != "" {
		return !owned, nil
	}
	if err = reconciler.reserveGUID(allocated, pkey); err != nil {
		delete(reconciler.Owners.ownerByGUID, allocated)
		return false, err
	}
	return false, nil
}

func networkKey(pod *corev1.Pod, network *netv1.NetworkSelectionElement) string {
	ns := network.Namespace
	if ns == "" {
		ns = pod.Namespace
	}
	return ns + "/" + network.Name
}

func samePKey(first, second string) bool {
	left, leftErr := utils.ParsePKey(first)
	right, rightErr := utils.ParsePKey(second)
	return leftErr == nil && rightErr == nil && left == right
}

// reserveGUID records allocated under pkey in the pool. A GUID already reserved without a pkey
// takes this one, so releasing it later also removes it from the pkey.
func (reconciler *Reconciler) reserveGUID(allocated, pkey string) error {
	err := reconciler.Pool.AllocateGUID(allocated, pkey)
	if !alreadyAllocated(err) {
		return err
	}
	if pkey == "" {
		return nil
	}
	if err = reconciler.Pool.ReleaseGUID(allocated); err != nil {
		return err
	}
	return reconciler.Pool.AllocateGUID(allocated, pkey)
}
