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
	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

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
