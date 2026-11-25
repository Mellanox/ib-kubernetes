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
	SM                 plugins.SubnetManagerClient
	Pool               guid.Pool
	Owners             *Owners
	RetryInterval      time.Duration
	DriftCheckInterval time.Duration

	// membershipMu guards membershipCache, membershipCachedAt and verifiedAt.
	membershipMu       sync.Mutex
	membershipCachedAt time.Time
	membershipCache    map[string]string
	verifiedAt         map[string]time.Time
}

// Reconcile implements reconcile.Reconciler.
func (reconciler *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nad := &netv1.NetworkAttachmentDefinition{}
	err := reconciler.Get(ctx, req.NamespacedName, nad)
	if apierrors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	if !meta.IsIbSriov(nad) {
		return reconcile.Result{}, nil
	}
	spec, err := meta.Spec(nad)
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconciler.reconcilePool(ctx, nad, spec)
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
	return setupController(mgr, reconciler)
}
