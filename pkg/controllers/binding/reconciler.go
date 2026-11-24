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

// Package binding reconciles one NetworkAttachmentDefinition at a time:
// live pods are desired, subnet-manager membership is actual.
package binding

import (
	"context"
	"fmt"
	"sync"
	"time"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Mellanox/ib-kubernetes/pkg/controllers/meta"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
)

const (
	orphanLog     = "log"
	orphanEnforce = "enforce"
	defaultPeriod = 5 * time.Second
	defaultDrift  = 10 * time.Minute
	membershipTTL = 5 * time.Second
)

// Reconciler binds live pods on one NAD to the subnet manager.
//
// SetupWithManager runs it with MaxConcurrentReconciles of 1, so one Reconcile
// does not overlap another. membershipMu still guards the membership cache and
// verifiedAt, which the fabric read and the verification helpers share.
type Reconciler struct {
	client.Client
	SM plugins.SubnetManagerClient
	// Fabric is the partition client. It must be a true nil when the plugin has
	// none: a typed nil still passes the nil check and panics on use. The
	// process wiring has to pass a bare nil in that case.
	Fabric             plugins.FabricClient
	Pool               guid.Pool
	Owners             *Owners
	Nodes              *Nodes
	RetryInterval      time.Duration
	DriftCheckInterval time.Duration
	OrphanGC           string
	RangeStart         guid.GUID
	RangeEnd           guid.GUID
	// APIReader reads pods from the API server. Release decisions use it when
	// the cached pod list disagrees with the owner map, so a list that has not
	// yet caught up with a patch cannot remove a running pod from its pkey.
	APIReader client.Reader

	seedMu sync.Mutex
	seeded bool
	// fabricOnlyChecked is when a full pool last refreshed membership to free
	// fabric-only reservations. Refreshes are at least membershipTTL apart.
	fabricOnlyChecked time.Time
	// fabricOnlyGUIDs are startup reservations of fabric members no live pod carries. They are
	// freed on pool exhaustion once the fabric stops listing them and no interface owns them.
	fabricOnlyGUIDs map[string]struct{}

	// membershipMu guards membershipCache, membershipCachedAt and verifiedAt.
	membershipMu       sync.Mutex
	membershipCachedAt time.Time
	membershipCache    map[string]string
	verifiedAt         map[string]time.Time
	sweptPKeyByGUID    map[string]int
}

// Reconcile implements reconcile.Reconciler.
func (reconciler *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if err := reconciler.reserveClusterGUIDs(ctx); err != nil {
		return reconcile.Result{}, err
	}
	if err := reconciler.releaseSwept(); err != nil {
		return reconcile.Result{}, err
	}
	nad := &netv1.NetworkAttachmentDefinition{}
	err := reconciler.Get(ctx, req.NamespacedName, nad)
	if apierrors.IsNotFound(err) {
		return reconciler.reconcileMissing(ctx, req.Namespace, req.Name)
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	if !meta.IsIbSriov(nad) {
		return reconciler.reconcileNotIbSriov(ctx, nad)
	}
	// A plugin that cannot manage partitions serves ibKubernetesEnabled networks from the GUID pool.
	if meta.PartitionEnabled(nad) && reconciler.Fabric != nil {
		return reconciler.reconcileWithPartition(ctx, nad)
	}
	spec, err := meta.Spec(nad)
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconciler.reconcilePool(ctx, nad, spec)
}

// reconcileMissing only cleans up a NAD that is gone or no longer ib-sriov: no GUID is assigned
// here. GUIDs still on live pods are kept; the NAD create or update event starts the full flow.
func (reconciler *Reconciler) reconcileMissing(ctx context.Context, namespace, name string) (reconcile.Result, error) {
	live, pending, err := reconciler.releasePool(ctx, namespace, name)
	if err != nil {
		return reconcile.Result{}, err
	}
	if pending {
		return reconciler.requeueSoon(), nil
	}
	if !live {
		reconciler.dropNADState(namespace, name)
	}
	return reconcile.Result{}, nil
}

// reconcileNotIbSriov cleans up a NAD that switched to another CNI. Node PF GUIDs it added while
// partitions were enabled leave that partition with their node's last pod; a failed removal keeps
// the node records for the next pass. The NAD finalizer deletes the partition itself.
func (reconciler *Reconciler) reconcileNotIbSriov(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition,
) (reconcile.Result, error) {
	pods, err := reconciler.livePods(ctx, nad.Namespace, nad.Name)
	if err != nil {
		return reconcile.Result{}, err
	}
	_, waiting, err := reconciler.dropIdleNodes(ctx, nad.Namespace, nad.Name, podsByNode(pods))
	if err != nil {
		return reconcile.Result{}, err
	}
	if waiting {
		// Keep the node records. reconcileMissing would drop them while the
		// removal is still pending, and the GUIDs would be left on the pkey.
		return reconciler.requeueSoon(), nil
	}
	return reconciler.reconcileMissing(ctx, nad.Namespace, nad.Name)
}

// reconcileWithPartition binds the network with node PF GUIDs.
// A pool GUID a live pod still carries is kept, membership included, until that
// pod is gone. The partition sweep does not remove a GUID Owners still tracks,
// so enforce leaves those pods alone. Once the pod is gone, releasePool removes
// the membership. A terminating NAD is not bound; idle nodes are still removed.
func (reconciler *Reconciler) reconcileWithPartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition,
) (reconcile.Result, error) {
	if nad.DeletionTimestamp != nil {
		return reconciler.reconcileNotIbSriov(ctx, nad)
	}
	_, pending, err := reconciler.releasePool(ctx, nad.Namespace, nad.Name)
	if err != nil {
		return reconcile.Result{}, err
	}
	result, err := reconciler.reconcilePartition(ctx, nad)
	if err == nil && pending {
		return reconciler.requeueSoon(), nil
	}
	return result, err
}

// releasePool frees the pool GUIDs of a network that no live pod carries. live reports whether any
// pod still selects the network.
func (reconciler *Reconciler) releasePool(ctx context.Context, namespace, name string) (live, pending bool, err error) {
	pods, err := reconciler.livePods(ctx, namespace, name)
	if err != nil {
		return false, false, err
	}
	_, pending, _, err = reconciler.releaseTracked(
		ctx, namespace+"/"+name, collectInterfaces(pods, namespace, name),
	)
	return len(pods) > 0, pending, err
}

func (reconciler *Reconciler) requeueSoon() reconcile.Result {
	period := reconciler.RetryInterval
	if period <= 0 {
		period = defaultPeriod
	}
	return reconcile.Result{RequeueAfter: period}
}

func (reconciler *Reconciler) requeueAfterDrift() reconcile.Result {
	after := reconciler.DriftCheckInterval
	if after <= 0 {
		after = defaultDrift
	}
	return reconcile.Result{RequeueAfter: after}
}

// SetupWithManager watches the NAD and the pods that select it.
func (reconciler *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := reconciler.validatePoolRange(); err != nil {
		return err
	}
	if reconciler.APIReader == nil {
		reconciler.APIReader = mgr.GetAPIReader()
	}
	return setupController(mgr, reconciler)
}

// validatePoolRange fails when the pool bounds were left at zero. inPoolRange
// would then match nothing and the orphan sweep would silently do no work.
func (reconciler *Reconciler) validatePoolRange() error {
	if reconciler.RangeStart == 0 && reconciler.RangeEnd == 0 {
		return fmt.Errorf("guid pool range is unset")
	}
	return nil
}
