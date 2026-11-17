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
	"errors"
	"fmt"
	"net"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/rs/zerolog/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// errLeavingOldPKey reports a GUID whose removal from its old pkey is not confirmed by the fabric yet.
var errLeavingOldPKey = errors.New("guid is still leaving its old pkey")

type pkeyGroup struct {
	pkey string
	// guidInRuntimeConfig is set when the NAD enables the infinibandGUID capability, so CNI reads
	// the GUID from the runtime config instead of cni-args.
	guidInRuntimeConfig bool
	ifaces              []*iface
	// fabric maps each GUID the fabric lists to its pkey.
	fabric map[string]string
}

func (reconciler *Reconciler) reconcilePool(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, spec *utils.IbSriovCniSpec,
) (reconcile.Result, error) {
	nadNS, nadName := nad.Namespace, nad.Name
	pods, err := reconciler.livePods(ctx, nadNS, nadName)
	if err != nil {
		return reconcile.Result{}, err
	}
	// PF GUIDs added while partitions were enabled leave that partition with their node's last pod.
	if _, err = reconciler.dropIdleNodes(nadNS, nadName, podsByNode(pods)); err != nil {
		return reconcile.Result{}, err
	}
	ifaces := collectInterfaces(pods, nadNS, nadName)
	removed, pending, err := reconciler.releaseTracked(nadNS+"/"+nadName, ifaces)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(removed) == 0 && reconciler.poolSettled(ifaces) && reconciler.recentlyVerified(nadNS, nadName) {
		return reconciler.requeueAfterDrift(), nil
	}
	groups := reconciler.groupByPKey(ifaces, spec)
	if err = reconciler.recordPKeys(ctx, nad, groups); err != nil {
		return reconcile.Result{}, err
	}
	requeue := pending
	for _, group := range groups {
		again, syncErr := reconciler.syncPKeyGroup(ctx, group, removed)
		if syncErr != nil {
			return reconcile.Result{}, syncErr
		}
		requeue = requeue || again
	}
	if requeue {
		return reconciler.requeueSoon(), nil
	}
	ok, err := reconciler.cacheShowsConfigured(ctx, ifaces, nadNS, nadName)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !ok {
		return reconciler.requeueSoon(), nil
	}
	reconciler.markVerified(nadNS, nadName)
	return reconciler.requeueAfterDrift(), nil
}

// groupByPKey keeps a configured interface on the pkey it was configured with, so a NAD pkey
// change applies only to interfaces that are not configured yet. The NAD pkey always has a group,
// so its membership is swept even with no pods.
func (reconciler *Reconciler) groupByPKey(ifaces []iface, spec *utils.IbSriovCniSpec) []pkeyGroup {
	guidInRuntimeConfig := usesRuntimeGUID(spec)
	grouped := map[string][]*iface{spec.PKey: nil}
	order := []string{spec.PKey}
	for ifaceIndex := range ifaces {
		pkey := spec.PKey
		if kept := reconciler.keptPKey(&ifaces[ifaceIndex]); kept != "" {
			pkey = kept
		}
		if _, ok := grouped[pkey]; !ok {
			order = append(order, pkey)
		}
		grouped[pkey] = append(grouped[pkey], &ifaces[ifaceIndex])
	}
	out := make([]pkeyGroup, 0, len(order))
	for _, pkey := range order {
		out = append(out, pkeyGroup{
			pkey: pkey, guidInRuntimeConfig: guidInRuntimeConfig, ifaces: grouped[pkey],
		})
	}
	return out
}

// keptPKey is the pkey a configured interface keeps. The annotation is user-writable, so the pkey
// comes from the pool reservation this controller made, and only for the GUID's recorded owner.
func (reconciler *Reconciler) keptPKey(podIface *iface) string {
	if !utils.IsPodNetworkConfiguredWithInfiniBand(podIface.network) {
		return ""
	}
	allocated, err := utils.GetPodNetworkGUID(podIface.network)
	if err != nil || reconciler.Owners.ownerOf(allocated) != podIface.owner {
		return ""
	}
	stored, err := reconciler.Pool.Get(allocated)
	if err != nil {
		return ""
	}
	return stored
}

func usesRuntimeGUID(spec *utils.IbSriovCniSpec) bool {
	return spec.Capabilities != nil && spec.Capabilities["infinibandGUID"]
}

func ifacePointers(ifaces []iface) []*iface {
	out := make([]*iface, len(ifaces))
	for ifaceIndex := range ifaces {
		out[ifaceIndex] = &ifaces[ifaceIndex]
	}
	return out
}

func (reconciler *Reconciler) syncPKeyGroup(
	ctx context.Context, group pkeyGroup, removed map[string]struct{},
) (bool, error) {
	pkey, useSM, err := parseOptionalPKey(group.pkey)
	if err != nil {
		return false, err
	}
	// Keyless networks load membership too, so a pod GUID listed on another pkey is not claimed.
	if group.fabric, err = reconciler.fabricPKeys(); err != nil {
		return false, err
	}
	again := reconciler.assignGUIDs(ctx, group)
	desired := guidsOf(group.ifaces)
	if useSM {
		// Read after assignment: a full pool may have refreshed membership and reused a freed GUID.
		actual, membersErr := reconciler.pkeyMembers(pkey)
		if membersErr != nil {
			return false, membersErr
		}
		if err = reconciler.addMissingMembers(pkey, desired, actual); err != nil {
			if errors.Is(err, plugins.ErrPending) {
				return true, nil
			}
			return false, err
		}
		if err = reconciler.sweepUnknown(pkey, desired, actual, removed); err != nil {
			return false, err
		}
	}
	failed := reconciler.markConfigured(ctx, group)
	return again || failed, nil
}

// assignGUIDs gives each pod of the group its GUIDs and reports whether any pod needs another pass.
func (reconciler *Reconciler) assignGUIDs(ctx context.Context, group pkeyGroup) bool {
	byPod := map[string][]*iface{}
	var order []string
	for _, podIface := range group.ifaces {
		key := string(podIface.pod.UID)
		if _, ok := byPod[key]; !ok {
			order = append(order, key)
		}
		byPod[key] = append(byPod[key], podIface)
	}
	requeue := false
	for _, key := range order {
		again, err := reconciler.assignPod(ctx, group, byPod[key])
		if err != nil {
			skipPod(byPod[key], err, "assigning guids")
			again = true
		}
		requeue = requeue || again
	}
	return requeue
}

// skipPod logs a failure for one pod and skips its interfaces for the rest of this pass, so a pod
// the API server keeps rejecting does not hold back the other pods of the network.
func skipPod(ifaces []*iface, err error, action string) {
	log.Error().Err(err).Str("pod", ifaces[0].pod.Namespace+"/"+ifaces[0].pod.Name).
		Msg("retrying pod after a failure " + action)
	for _, podIface := range ifaces {
		podIface.skip = true
	}
}

// assignPod gives each interface of one pod a GUID. An interface the full pool cannot serve, or
// whose GUID is still leaving its old pkey, is skipped and retried later, so it does not hold back
// the rest of the network.
func (reconciler *Reconciler) assignPod(ctx context.Context, group pkeyGroup, ifaces []*iface) (bool, error) {
	var fresh []string
	retry, copied := false, false
	for _, podIface := range ifaces {
		if utils.PodNetworkHasGUID(podIface.network) {
			retry = reconciler.claimExisting(podIface, group.pkey, group.fabric) || retry
			copied = copyToRuntimeConfig(podIface, group.guidInRuntimeConfig) || copied
			continue
		}
		allocated, full, err := reconciler.generateGUID(podIface, group)
		if err != nil {
			reconciler.releaseGenerated(fresh)
			return false, err
		}
		if full {
			podIface.skip = true
			retry = true
			continue
		}
		fresh = append(fresh, allocated)
	}
	if len(fresh) == 0 && !copied {
		return retry, nil
	}
	// A failed patch may still have been saved, so the GUIDs stay reserved. If the pod does not
	// carry them on the next pass, releaseTracked frees them.
	if err := patchNetworks(ctx, reconciler.Client, ifaces[0].pod, ifaces[0].networks); err != nil {
		return false, err
	}
	return retry || !cacheHasGUIDs(ctx, reconciler.Client, ifaces[0].pod, fresh), nil
}
