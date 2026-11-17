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
	"net"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// partitionPlan is the live pods and the pkey membership for one partition network.
type partitionPlan struct {
	ifaces  []iface
	nodes   map[string]struct{}
	actual  map[string]struct{}
	desired map[string]struct{}
	perNode map[string][]net.HardwareAddr
	// waiting holds live nodes whose PF GUIDs are not in the pkey yet: the node reports no IB
	// devices, or the subnet manager has not finished adding them. Their pods and the sweep wait.
	waiting map[string]struct{}
}

func (reconciler *Reconciler) reconcilePartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition,
) (reconcile.Result, error) {
	pkey, found, err := partitionKey(nad)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !found {
		return reconciler.requeueSoon(), nil
	}
	plan, result, err := reconciler.planPartition(ctx, nad, pkey)
	if plan == nil {
		return result, err
	}
	return reconciler.applyPartition(ctx, nad, pkey, plan)
}

func partitionKey(nad *netv1.NetworkAttachmentDefinition) (int, bool, error) {
	value := nad.Annotations[utils.PartitionKeyAnnotation]
	if value == "" {
		return 0, false, nil
	}
	pkey, err := utils.ParsePKey(value)
	if err != nil {
		return 0, false, err
	}
	return pkey, true, nil
}

func (reconciler *Reconciler) planPartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, pkey int,
) (*partitionPlan, reconcile.Result, error) {
	pods, err := reconciler.livePods(ctx, nad.Namespace, nad.Name)
	if err != nil {
		return nil, reconcile.Result{}, err
	}
	plan := &partitionPlan{nodes: podsByNode(pods), ifaces: collectInterfaces(pods, nad.Namespace, nad.Name)}
	if reconciler.skipPartition(nad, pkey, plan.ifaces, plan.nodes) {
		return nil, reconciler.requeueAfterDrift(), nil
	}
	plan.actual, err = reconciler.pkeyMembers(pkey)
	if err != nil {
		return nil, reconcile.Result{}, err
	}
	plan.desired, plan.perNode, plan.waiting, err = reconciler.desiredNodeGUIDs(nad, pkey, plan.nodes)
	if err != nil {
		return nil, reconcile.Result{}, err
	}
	return plan, reconcile.Result{}, nil
}

func (reconciler *Reconciler) applyPartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, pkey int, plan *partitionPlan,
) (reconcile.Result, error) {
	removed, err := reconciler.dropIdleNodes(nad.Namespace, nad.Name, plan.nodes)
	if err != nil {
		return reconcile.Result{}, err
	}
	for _, removedGUID := range removed {
		delete(plan.actual, canonicalGUID(removedGUID))
	}
	pending, err := reconciler.addNodeGUIDs(pkey, plan.perNode, plan.actual)
	if err != nil {
		return reconcile.Result{}, err
	}
	for node := range pending {
		plan.waiting[node] = struct{}{}
	}
	return reconciler.finishPartition(ctx, nad, pkey, plan)
}

func (reconciler *Reconciler) finishPartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, pkey int, plan *partitionPlan,
) (reconcile.Result, error) {
	failed := reconciler.markConfigured(ctx, pkeyGroup{ifaces: ifacePointers(plan.readyIfaces())})
	if failed || len(plan.waiting) > 0 {
		return reconciler.requeueSoon(), nil
	}
	ok, err := reconciler.cacheShowsConfigured(ctx, plan.ifaces, nad.Namespace, nad.Name)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !ok {
		return reconciler.requeueSoon(), nil
	}
	if err = reconciler.sweepPartition(pkey, plan.desired, plan.actual); err != nil {
		return reconcile.Result{}, err
	}
	reconciler.markVerified(nad.Namespace, nad.Name)
	return reconciler.requeueAfterDrift(), nil
}

// readyIfaces returns the interfaces whose node PF GUIDs are in the pkey.
func (plan *partitionPlan) readyIfaces() []iface {
	ready := make([]iface, 0, len(plan.ifaces))
	for ifaceIndex := range plan.ifaces {
		if _, waiting := plan.waiting[plan.ifaces[ifaceIndex].pod.Spec.NodeName]; !waiting {
			ready = append(ready, plan.ifaces[ifaceIndex])
		}
	}
	return ready
}

func (reconciler *Reconciler) skipPartition(
	nad *netv1.NetworkAttachmentDefinition, pkey int, ifaces []iface, live map[string]struct{},
) bool {
	return reconciler.recentlyVerified(nad.Namespace, nad.Name) &&
		podsConfigured(ifaces) &&
		reconciler.pfNodesMatchLivePods(nad.Namespace, nad.Name, live, pkey)
}

func podsByNode(pods []corev1.Pod) map[string]struct{} {
	out := map[string]struct{}{}
	for podIndex := range pods {
		out[pods[podIndex].Spec.NodeName] = struct{}{}
	}
	return out
}

func (reconciler *Reconciler) desiredNodeGUIDs(
	nad *netv1.NetworkAttachmentDefinition, pkey int, nodes map[string]struct{},
) (desired map[string]struct{}, perNode map[string][]net.HardwareAddr, unready map[string]struct{}, err error) {
	desired = map[string]struct{}{}
	perNode = map[string][]net.HardwareAddr{}
	unready = map[string]struct{}{}
	recorded := reconciler.Nodes.nodePFGUIDsForNAD(nad.Namespace, nad.Name)
	for node := range nodes {
		// A pkey change moves the node: its record keeps the old pkey until the removal succeeds.
		if previous, ok := recorded[node]; ok && previous.pkey != pkey {
			if err = reconciler.removeGUIDs(previous.pkey, previous.guids); err != nil {
				return nil, nil, nil, err
			}
		}
		devices, devicesErr := reconciler.Fabric.GetNodeIBDevices(node)
		if devicesErr != nil {
			return nil, nil, nil, devicesErr
		}
		if len(devices) == 0 {
			log.Info().Str("node", node).Msg("no IB devices on node yet")
			unready[node] = struct{}{}
			continue
		}
		strs := make([]string, 0, len(devices))
		for _, device := range devices {
			key := canonicalGUID(device.GUID.String())
			desired[key] = struct{}{}
			perNode[node] = append(perNode[node], device.GUID)
			strs = append(strs, device.GUID.String())
		}
		reconciler.Nodes.recordNodePFGUIDs(nad.Namespace, nad.Name, node, pkey, strs)
	}
	return desired, perNode, unready, nil
}

// dropIdleNodes removes the PF GUIDs of nodes with no live pod from the pkey they were added to,
// whatever mode the network is in now, and returns the removed GUIDs.
func (reconciler *Reconciler) dropIdleNodes(namespace, name string, live map[string]struct{}) ([]string, error) {
	var removed []string
	for node, pfs := range reconciler.Nodes.nodePFGUIDsForNAD(namespace, name) {
		if _, ok := live[node]; ok {
			continue
		}
		if err := reconciler.removeGUIDs(pfs.pkey, pfs.guids); err != nil {
			return nil, err
		}
		removed = append(removed, pfs.guids...)
		reconciler.Nodes.dropNodePFGUIDs(namespace, name, node)
	}
	return removed, nil
}

func (reconciler *Reconciler) removeGUIDs(pkey int, guids []string) error {
	if len(guids) == 0 {
		return nil
	}
	addrs := make([]net.HardwareAddr, 0, len(guids))
	for _, allocated := range guids {
		addr, err := guidToHardwareAddr(allocated)
		if err != nil {
			return err
		}
		addrs = append(addrs, addr)
	}
	return reconciler.removeFromPKey(pkey, addrs)
}

// addNodeGUIDs adds each node's missing PF GUIDs and returns the nodes whose add is still pending.
func (reconciler *Reconciler) addNodeGUIDs(
	pkey int, perNode map[string][]net.HardwareAddr, actual map[string]struct{},
) (map[string]struct{}, error) {
	pending := map[string]struct{}{}
	for node, addrs := range perNode {
		missing := missingAddrs(addrs, actual)
		if len(missing) == 0 {
			continue
		}
		if err := reconciler.addToPKey(pkey, missing); err != nil {
			if !errors.Is(err, plugins.ErrPending) {
				return nil, err
			}
			pending[node] = struct{}{}
		}
	}
	return pending, nil
}

func missingAddrs(addrs []net.HardwareAddr, actual map[string]struct{}) []net.HardwareAddr {
	var missing []net.HardwareAddr
	for _, addr := range addrs {
		if _, ok := actual[canonicalGUID(addr.String())]; ok {
			continue
		}
		missing = append(missing, addr)
	}
	return missing
}

func (reconciler *Reconciler) sweepPartition(pkey int, desired, actual map[string]struct{}) error {
	var unknown []net.HardwareAddr
	for allocated := range actual {
		if _, ok := desired[allocated]; ok {
			continue
		}
		log.Info().Str("guid", allocated).Int("pkey", pkey).Msg("untracked partition guid")
		if !reconciler.orphanGCEnforced() {
			continue
		}
		addr, err := guidToHardwareAddr(allocated)
		if err != nil {
			continue
		}
		unknown = append(unknown, addr)
	}
	if len(unknown) == 0 {
		return nil
	}
	return reconciler.removeFromPKey(pkey, unknown)
}
