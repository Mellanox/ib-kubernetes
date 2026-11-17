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

// Package nad owns partition lifecycle for ibKubernetesEnabled networks.
// It is the only writer of the PKey annotation and the partition finalizer.
package nad

import (
	"context"
	"fmt"
	"time"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/meta"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// Reconciler creates and deletes the IB partition behind one NAD.
type Reconciler struct {
	client.Client
	Fabric        plugins.FabricClient
	RetryInterval time.Duration
}

// Reconcile implements reconcile.Reconciler.
func (reconciler *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if reconciler.Fabric == nil {
		return reconcile.Result{}, nil
	}
	nad := &netv1.NetworkAttachmentDefinition{}
	if err := reconciler.Get(ctx, req.NamespacedName, nad); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	if nad.DeletionTimestamp != nil {
		return reconciler.finalizePartition(ctx, nad)
	}
	if !meta.PartitionEnabled(nad) {
		return reconcile.Result{}, nil
	}
	return reconciler.ensurePartition(ctx, nad)
}

// SetupWithManager watches ib-sriov NADs.
func (reconciler *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("ib-nad").
		For(&netv1.NetworkAttachmentDefinition{}, builder.WithPredicates(ibSriovPredicate())).
		Complete(reconciler)
}

func (reconciler *Reconciler) ensurePartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition,
) (reconcile.Result, error) {
	if err := reconciler.ensureFinalizer(ctx, nad); err != nil {
		return reconcile.Result{}, err
	}
	if nad.Annotations[utils.PartitionKeyAnnotation] != "" {
		return reconcile.Result{}, nil
	}
	name := meta.NetworkID(nad)
	created, err := reconciler.Fabric.CreateIBPartition(name)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("create partition %s: %w", name, err)
	}
	ready, pkey, err := reconciler.Fabric.IsPartitionReady(name)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !ready {
		return reconciler.requeueSoon(), nil
	}
	if pkey == "" {
		pkey = created
	}
	if pkey == "" {
		return reconciler.requeueSoon(), nil
	}
	if err := reconciler.patchPKey(ctx, nad, pkey); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (reconciler *Reconciler) finalizePartition(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition,
) (reconcile.Result, error) {
	if !hasFinalizer(nad) {
		return reconcile.Result{}, nil
	}
	live, err := reconciler.hasLivePods(ctx, nad)
	if err != nil {
		return reconcile.Result{}, err
	}
	// Blocking finalizer: a live pod still using the network keeps the partition.
	if live {
		log.Info().Str("nad", nad.Namespace+"/"+nad.Name).
			Msg("NAD is terminating but live pods still reference it")
		return reconciler.requeueSoon(), nil
	}
	name := meta.NetworkID(nad)
	if err := reconciler.Fabric.DeleteIBPartition(name); err != nil {
		return reconcile.Result{}, fmt.Errorf("delete partition %s: %w", name, err)
	}
	if err := reconciler.removeFinalizer(ctx, nad); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (reconciler *Reconciler) hasLivePods(ctx context.Context, nad *netv1.NetworkAttachmentDefinition) (bool, error) {
	var pods corev1.PodList
	err := reconciler.List(ctx, &pods, client.MatchingFields{index.Field: index.Key(nad.Namespace, nad.Name)})
	if err != nil {
		return false, fmt.Errorf("list pods for %s/%s: %w", nad.Namespace, nad.Name, err)
	}
	for podIndex := range pods.Items {
		if index.Live(&pods.Items[podIndex]) {
			return true, nil
		}
	}
	return false, nil
}

func (reconciler *Reconciler) ensureFinalizer(ctx context.Context, nad *netv1.NetworkAttachmentDefinition) error {
	if hasFinalizer(nad) {
		return nil
	}
	base := nad.DeepCopy()
	nad.Finalizers = append(nad.Finalizers, utils.PartitionNADFinalizer)
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if err := reconciler.Patch(ctx, nad, patch); err != nil {
		return fmt.Errorf("add partition finalizer: %w", err)
	}
	return reconciler.Get(ctx, types.NamespacedName{Namespace: nad.Namespace, Name: nad.Name}, nad)
}

func (reconciler *Reconciler) patchPKey(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, pkey string,
) error {
	if nad.Annotations[utils.PartitionKeyAnnotation] == pkey {
		return nil
	}
	base := nad.DeepCopy()
	if nad.Annotations == nil {
		nad.Annotations = map[string]string{}
	}
	nad.Annotations[utils.PartitionKeyAnnotation] = pkey
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if err := reconciler.Patch(ctx, nad, patch); err != nil {
		return fmt.Errorf("annotate partition key: %w", err)
	}
	return nil
}

func (reconciler *Reconciler) removeFinalizer(ctx context.Context, nad *netv1.NetworkAttachmentDefinition) error {
	base := nad.DeepCopy()
	kept := make([]string, 0, len(nad.Finalizers))
	for _, finalizer := range nad.Finalizers {
		if finalizer != utils.PartitionNADFinalizer {
			kept = append(kept, finalizer)
		}
	}
	nad.Finalizers = kept
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if err := reconciler.Patch(ctx, nad, patch); err != nil {
		return fmt.Errorf("remove partition finalizer: %w", err)
	}
	return nil
}

func (reconciler *Reconciler) requeueSoon() reconcile.Result {
	period := reconciler.RetryInterval
	if period <= 0 {
		period = 5 * time.Second
	}
	return reconcile.Result{RequeueAfter: period}
}

func hasFinalizer(nad *netv1.NetworkAttachmentDefinition) bool {
	for _, finalizer := range nad.Finalizers {
		if finalizer == utils.PartitionNADFinalizer {
			return true
		}
	}
	return false
}

// ibSriovPredicate passes ib-sriov NADs, and NADs that still carry the partition finalizer after
// switching to another CNI, so deleting them still deletes the partition and the finalizer.
func ibSriovPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		nad, ok := obj.(*netv1.NetworkAttachmentDefinition)
		return ok && (meta.IsIbSriov(nad) || hasFinalizer(nad))
	})
}
