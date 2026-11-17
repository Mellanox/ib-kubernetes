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
	"fmt"
	"net"
	"time"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// guidsInUse returns the last fabric membership when it is younger than membershipTTL.
// A miss downloads every pkey once and shares that response across networks.
func (reconciler *Reconciler) guidsInUse() (map[string]string, error) {
	reconciler.membershipMu.Lock()
	defer reconciler.membershipMu.Unlock()
	if reconciler.membershipCache != nil && time.Since(reconciler.membershipCachedAt) < membershipTTL {
		return reconciler.membershipCache, nil
	}
	listed, err := reconciler.SM.ListGuidsInUse()
	if err != nil {
		return nil, fmt.Errorf("list guids in use: %w", err)
	}
	if listed == nil {
		listed = map[string]string{}
	}
	reconciler.membershipCache = listed
	reconciler.membershipCachedAt = time.Now()
	return listed, nil
}

// addToPKey and removeFromPKey change the fabric and drop the cached membership,
// so the next read sees the change instead of a copy taken before it.
func (reconciler *Reconciler) addToPKey(pkey int, guids []net.HardwareAddr) error {
	defer reconciler.invalidateMembershipCache()
	return reconciler.SM.AddGuidsToPKey(pkey, guids)
}

func (reconciler *Reconciler) removeFromPKey(pkey int, guids []net.HardwareAddr) error {
	defer reconciler.invalidateMembershipCache()
	return reconciler.SM.RemoveGuidsFromPKey(pkey, guids)
}

func (reconciler *Reconciler) invalidateMembershipCache() {
	reconciler.membershipMu.Lock()
	defer reconciler.membershipMu.Unlock()
	reconciler.membershipCache = nil
}

func (reconciler *Reconciler) recentlyVerified(namespace, name string) bool {
	at, ok := reconciler.verifiedAt[namespace+"/"+name]
	return ok && time.Since(at) < reconciler.requeueAfterDrift().RequeueAfter
}

func (reconciler *Reconciler) markVerified(namespace, name string) {
	if reconciler.verifiedAt == nil {
		reconciler.verifiedAt = map[string]time.Time{}
	}
	reconciler.verifiedAt[namespace+"/"+name] = time.Now()
}

// dropNADState drops what this process remembers about a deleted NAD.
func (reconciler *Reconciler) dropNADState(namespace, name string) {
	delete(reconciler.verifiedAt, namespace+"/"+name)
	for node := range reconciler.Nodes.nodePFGUIDsForNAD(namespace, name) {
		reconciler.Nodes.dropNodePFGUIDs(namespace, name, node)
	}
}

// poolSettled is true when every live interface is configured with a GUID it owns. Pod annotations
// alone are not enough: a pod created already configured has no owner or reservation yet.
func (reconciler *Reconciler) poolSettled(ifaces []iface) bool {
	for ifaceIndex := range ifaces {
		podIface := &ifaces[ifaceIndex]
		if podIface.skip || !utils.IsPodNetworkConfiguredWithInfiniBand(podIface.network) {
			return false
		}
		allocated, err := utils.GetPodNetworkGUID(podIface.network)
		if err != nil || reconciler.Owners.ownerOf(allocated) != podIface.owner {
			return false
		}
	}
	return true
}

// podsConfigured is true when every live interface is already marked configured.
func podsConfigured(ifaces []iface) bool {
	for ifaceIndex := range ifaces {
		if ifaces[ifaceIndex].skip || !utils.IsPodNetworkConfiguredWithInfiniBand(ifaces[ifaceIndex].network) {
			return false
		}
	}
	return true
}

// pfNodesMatchLivePods reports that the remembered PF nodes are exactly the nodes that still have a
// live pod, all recorded under pkey.
func (reconciler *Reconciler) pfNodesMatchLivePods(namespace, name string, live map[string]struct{}, pkey int) bool {
	remembered := reconciler.Nodes.nodePFGUIDsForNAD(namespace, name)
	if len(remembered) != len(live) {
		return false
	}
	for node := range live {
		if pfs, ok := remembered[node]; !ok || pfs.pkey != pkey {
			return false
		}
	}
	return true
}
