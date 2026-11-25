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
	"strings"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/meta"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

func setupController(mgr ctrl.Manager, rec *Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("ib-binding").
		For(&netv1.NetworkAttachmentDefinition{}, builder.WithPredicates(ibPredicate())).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(podRequests), builder.WithPredicates(podPredicate())).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(rec)
}

func ibPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		nad, ok := obj.(*netv1.NetworkAttachmentDefinition)
		return ok && meta.IsIbSriov(nad)
	})
}

func podPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(created event.CreateEvent) bool { return interestingPod(created.Object) },
		DeleteFunc:  func(deleted event.DeleteEvent) bool { return interestingPod(deleted.Object) },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc:  podChanged,
	}
}

func interestingPod(obj client.Object) bool {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return false
	}
	return utils.PodWantsNetwork(pod) && utils.HasNetworkAttachmentAnnot(pod)
}

func podChanged(update event.UpdateEvent) bool {
	oldPod, okOld := update.ObjectOld.(*corev1.Pod)
	newPod, okNew := update.ObjectNew.(*corev1.Pod)
	if !okOld || !okNew {
		return false
	}
	if !interestingPod(oldPod) && !interestingPod(newPod) {
		return false
	}
	if oldPod.Spec.NodeName != newPod.Spec.NodeName {
		return true
	}
	if oldPod.Annotations[netv1.NetworkAttachmentAnnot] != newPod.Annotations[netv1.NetworkAttachmentAnnot] {
		return true
	}
	return utils.PodIsFinished(oldPod) != utils.PodIsFinished(newPod)
}

func podRequests(_ context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	keys := index.NetworkKeys(pod)
	out := make([]reconcile.Request, 0, len(keys))
	for _, key := range keys {
		namespace, name, _ := strings.Cut(key, "/")
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}})
	}
	return out
}
