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
	// devices, reading devices failed, or the subnet manager has not finished adding or
	// removing them. Their pods and the sweep wait, and the other nodes still proceed.
	waiting map[string]struct{}
	// followUp is set when an idle node's removal is still pending, so this pass requeues
	// without treating the network as verified.
	followUp bool
}

func (reconciler *Reconciler) reconcilePartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition,
) (reconcile.Result, error) {
	pkey, found, err := partitionKey(nad)
	if err != nil {
		// A bad annotation cannot succeed by backing off harder. Log it and try again
		// on the normal interval, once the annotation is fixed.
		log.Error().Err(err).Str("nad", nad.Namespace+"/"+nad.Name).
			Msg("partition annotation is not a pkey")
		return reconciler.requeueSoon(), nil
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
	plan.desired, plan.perNode, plan.waiting = reconciler.desiredNodeGUIDs(nad, pkey, plan.nodes)
	return plan, reconcile.Result{}, nil
}

func (reconciler *Reconciler) applyPartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, pkey int, plan *partitionPlan,
) (reconcile.Result, error) {
	removed, dropWaiting, err := reconciler.dropIdleNodes(ctx, nad.Namespace, nad.Name, plan.nodes)
	if err != nil {
		return reconcile.Result{}, err
	}
	plan.followUp = dropWaiting
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
	// Record a node only after its add succeeded or its GUIDs were already members.
	// A pending add keeps the previous record, so the next pass retries the move
	// instead of treating the new pkey as done for the drift interval.
	// A PF that left the node's device list is removed first. Until that removal
	// succeeds the old GUID stays in the record, so a later idle-node cleanup
	// still knows about it.
	departedWaiting := reconciler.recordReadyNodes(nad, pkey, plan.perNode, plan.waiting)
	plan.followUp = plan.followUp || departedWaiting
	return reconciler.finishPartition(ctx, nad, pkey, plan)
}

// recordReadyNodes stores PF GUIDs for nodes whose membership is in place.
// It reports whether a departed PF is still waiting to leave the pkey.
func (reconciler *Reconciler) recordReadyNodes(
	nad *netv1.NetworkAttachmentDefinition, pkey int,
	perNode map[string][]net.HardwareAddr, waiting map[string]struct{},
) bool {
	recorded := reconciler.Nodes.nodePFGUIDsForNAD(nad.Namespace, nad.Name)
	departedWaiting := false
	for node, addrs := range perNode {
		if _, skip := waiting[node]; skip {
			continue
		}
		guids := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			guids = append(guids, addr.String())
		}
		if previous, ok := recorded[node]; ok && previous.pkey == pkey {
			var departed []string
			current := map[string]struct{}{}
			for _, allocated := range guids {
				current[canonicalGUID(allocated)] = struct{}{}
			}
			for _, allocated := range previous.guids {
				if _, still := current[canonicalGUID(allocated)]; !still {
					departed = append(departed, allocated)
				}
			}
			if len(departed) > 0 {
				mine := reconciler.Nodes.withoutShared(nad.Namespace, nad.Name, node, pkey, departed)
				if err := reconciler.removeGUIDs(pkey, mine); err != nil {
					if !errors.Is(err, plugins.ErrPending) {
						log.Error().Err(err).Str("node", node).Msg("removing pf the node no longer has")
					}
					guids = append(guids, departed...)
					departedWaiting = true
				}
			}
		}
		reconciler.Nodes.recordNodePFGUIDs(nad.Namespace, nad.Name, node, pkey, guids)
	}
	return departedWaiting
}

func (reconciler *Reconciler) finishPartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, pkey int, plan *partitionPlan,
) (reconcile.Result, error) {
	failed := reconciler.markConfigured(ctx, pkeyGroup{ifaces: plan.readyIfaces()})
	if failed || len(plan.waiting) > 0 || plan.followUp {
		return reconciler.requeueSoon(), nil
	}
	ok, err := reconciler.cacheShowsConfigured(ctx, plan.ifaces, nad.Namespace, nad.Name)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !ok {
		return reconciler.requeueSoon(), nil
	}
	if reconciler.sweepPartition(pkey, plan.desired, plan.actual) {
		return reconciler.requeueSoon(), nil
	}
	reconciler.markVerified(nad.Namespace, nad.Name)
	return reconciler.requeueAfterDrift(), nil
}

// readyIfaces returns the interfaces whose node PF GUIDs are in the pkey.
// The pointers address plan.ifaces, so a later skip is visible to the cache check.
func (plan *partitionPlan) readyIfaces() []*iface {
	ready := make([]*iface, 0, len(plan.ifaces))
	for ifaceIndex := range plan.ifaces {
		if _, waiting := plan.waiting[plan.ifaces[ifaceIndex].pod.Spec.NodeName]; waiting {
			continue
		}
		ready = append(ready, &plan.ifaces[ifaceIndex])
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
) (desired map[string]struct{}, perNode map[string][]net.HardwareAddr, unready map[string]struct{}) {
	desired = map[string]struct{}{}
	perNode = map[string][]net.HardwareAddr{}
	unready = map[string]struct{}{}
	recorded := reconciler.Nodes.nodePFGUIDsForNAD(nad.Namespace, nad.Name)
	for node := range nodes {
		// A pkey change moves the node. The record keeps the old pkey until the
		// removal succeeds, and this node waits without blocking the others.
		if previous, ok := recorded[node]; ok && previous.pkey != pkey {
			// Another NAD on this node still wants the PF on the old pkey.
			// Leave the record there and retry; moving it would take the GUID
			// away from that network.
			if reconciler.Nodes.keptByOther(nad.Namespace, nad.Name, node, previous.pkey, previous.guids) {
				log.Info().Str("node", node).Int("pkey", previous.pkey).
					Msg("keeping node on its pkey while another network uses it")
				unready[node] = struct{}{}
				continue
			}
			mine := reconciler.Nodes.withoutShared(nad.Namespace, nad.Name, node, previous.pkey, previous.guids)
			if removeErr := reconciler.removeGUIDs(previous.pkey, mine); removeErr != nil {
				if !errors.Is(removeErr, plugins.ErrPending) {
					log.Error().Err(removeErr).Str("node", node).Msg("removing node from its old pkey")
				}
				unready[node] = struct{}{}
				continue
			}
		}
		devices, devicesErr := reconciler.Fabric.GetNodeIBDevices(node)
		if devicesErr != nil {
			log.Error().Err(devicesErr).Str("node", node).Msg("reading node ib devices")
			unready[node] = struct{}{}
			continue
		}
		if len(devices) == 0 {
			log.Info().Str("node", node).Msg("no IB devices on node yet")
			unready[node] = struct{}{}
			continue
		}
		for _, device := range devices {
			key := canonicalGUID(device.GUID.String())
			desired[key] = struct{}{}
			perNode[node] = append(perNode[node], device.GUID)
		}
	}
	return desired, perNode, unready
}

// dropIdleNodes removes the PF GUIDs of nodes with no live pod from the pkey they were added to,
// whatever mode the network is in now, and returns the removed GUIDs.
// A node the cache calls idle is checked against the API server before its membership is removed,
// so a replacement pod that has not reached the cache keeps the node. A GUID another NAD still
// records on the same pkey is left in place; only this NAD's record is dropped.
// A pending or failed removal keeps that node's record and reports waiting so the pass requeues.
// The other nodes are still removed.
func (reconciler *Reconciler) dropIdleNodes(
	ctx context.Context, namespace, name string, live map[string]struct{},
) ([]string, bool, error) {
	confirmed, changed, err := reconciler.confirmLiveNodes(ctx, namespace, name, live)
	if err != nil {
		return nil, false, err
	}
	var removed []string
	waiting := changed
	for node, pfs := range reconciler.Nodes.nodePFGUIDsForNAD(namespace, name) {
		if _, ok := confirmed[node]; ok {
			continue
		}
		mine := reconciler.Nodes.withoutShared(namespace, name, node, pfs.pkey, pfs.guids)
		if err = reconciler.removeGUIDs(pfs.pkey, mine); err != nil {
			if !errors.Is(err, plugins.ErrPending) {
				log.Error().Err(err).Str("node", node).Msg("removing idle node from its pkey")
			}
			waiting = true
			continue
		}
		removed = append(removed, mine...)
		reconciler.Nodes.dropNodePFGUIDs(namespace, name, node)
	}
	return removed, waiting, nil
}

// confirmLiveNodes asks the API server about recorded nodes the cache no longer lists.
// changed is true when the API still has a pod on one of those nodes.
func (reconciler *Reconciler) confirmLiveNodes(
	ctx context.Context, namespace, name string, live map[string]struct{},
) (map[string]struct{}, bool, error) {
	if reconciler.APIReader == nil || reconciler.Nodes == nil {
		return live, false, nil
	}
	missed := false
	for node := range reconciler.Nodes.nodePFGUIDsForNAD(namespace, name) {
		if _, ok := live[node]; !ok {
			missed = true
			break
		}
	}
	if !missed {
		return live, false, nil
	}
	pods, err := reconciler.livePodsUncached(ctx, namespace, name)
	if err != nil {
		return nil, false, err
	}
	confirmed := podsByNode(pods)
	for node := range confirmed {
		if _, ok := live[node]; !ok {
			return confirmed, true, nil
		}
	}
	return confirmed, false, nil
}

// removeGUIDs removes the GUIDs the pkey still lists. A GUID that was never a member,
// or has already left, is not sent to the subnet manager.
func (reconciler *Reconciler) removeGUIDs(pkey int, guids []string) error {
	if len(guids) == 0 {
		return nil
	}
	members, err := reconciler.pkeyMembers(pkey)
	if err != nil {
		return err
	}
	addrs := make([]net.HardwareAddr, 0, len(guids))
	for _, allocated := range guids {
		if _, listed := members[canonicalGUID(allocated)]; !listed {
			continue
		}
		addr, addrErr := guidToHardwareAddr(allocated)
		if addrErr != nil {
			return addrErr
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		return nil
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

// sweepPartition handles pkey members this network does not want.
// A member is kept when this network wants it, when Owners still names it for any
// network, or when Nodes still records it on this pkey for any NAD. Enforce then
// cannot remove another network's pods or PF GUIDs from a shared pkey.
// In log mode the rest are warned about and left in place. After a restart the
// node map is empty, so a PF whose pods already left stays until enforce runs.
// again reports that a removal is still pending.
func (reconciler *Reconciler) sweepPartition(pkey int, desired, actual map[string]struct{}) bool {
	owned := map[string]struct{}{}
	if reconciler.Owners != nil {
		owned = reconciler.Owners.ownedGUIDs()
	}
	recorded := map[string]struct{}{}
	if reconciler.Nodes != nil {
		for allocated := range reconciler.Nodes.guidsOnPKey(pkey) {
			recorded[canonicalGUID(allocated)] = struct{}{}
		}
	}
	var unknown []net.HardwareAddr
	for allocated := range actual {
		if _, ok := desired[allocated]; ok {
			continue
		}
		if _, ok := owned[allocated]; ok {
			continue
		}
		if _, ok := recorded[allocated]; ok {
			continue
		}
		if !reconciler.orphanGCEnforced() {
			log.Warn().Str("guid", allocated).Int("pkey", pkey).
				Msg("partition guid has no owner; DAEMON_ORPHAN_GC=enforce removes it")
			continue
		}
		addr, err := guidToHardwareAddr(allocated)
		if err != nil {
			continue
		}
		unknown = append(unknown, addr)
	}
	if len(unknown) == 0 {
		return false
	}
	if err := reconciler.removeFromPKey(pkey, unknown); err != nil {
		if !errors.Is(err, plugins.ErrPending) {
			log.Error().Err(err).Int("pkey", pkey).Msg("removing untracked partition guids")
		}
		return true
	}
	return false
}
