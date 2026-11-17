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

package nad

import (
	"context"
	"testing"
	"time"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	smfake "github.com/Mellanox/ib-kubernetes/pkg/controllers/fake"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
	"github.com/Mellanox/ib-kubernetes/pkg/scheme"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

func TestReconcile(t *testing.T) {
	const (
		ns   = "default"
		name = "ib"
	)
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}

	t.Run("nil fabric is a no-op", func(t *testing.T) {
		nad := testNAD(`{"type":"ib-sriov","ibKubernetesEnabled":true}`, false)
		reconciler := &Reconciler{Client: testClient(t, nad), RetryInterval: time.Second}
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Zero(t, result.RequeueAfter)
		require.NotContains(t, current(t, reconciler, req).Finalizers, utils.PartitionNADFinalizer)
	})

	t.Run("non-ib and disabled networks are left alone", func(t *testing.T) {
		for _, config := range []string{
			`{"type":"macvlan"}`,
			`{"type":"ib-sriov","ibKubernetesEnabled":false}`,
		} {
			fabric := smfake.New()
			nad := testNAD(config, false)
			reconciler := &Reconciler{Client: testClient(t, nad), Fabric: fabric, RetryInterval: time.Second}
			_, err := reconciler.Reconcile(context.Background(), req)
			require.NoError(t, err)
			require.Empty(t, fabric.Created())
			require.NotContains(t, current(t, reconciler, req).Finalizers, utils.PartitionNADFinalizer)
		}
	})

	t.Run("enabled nad gets a finalizer and pkey", func(t *testing.T) {
		fabric := smfake.New()
		nad := testNAD(`{"type":"ib-sriov","ibKubernetesEnabled":true}`, false)
		reconciler := &Reconciler{Client: testClient(t, nad), Fabric: fabric, RetryInterval: time.Second}
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		got := current(t, reconciler, req)
		require.Equal(t, []string{"default_ib"}, fabric.Created())
		require.Contains(t, got.Finalizers, utils.PartitionNADFinalizer)
		require.Equal(t, "0x5", got.Annotations[utils.PartitionKeyAnnotation])
	})

	t.Run("live pod blocks partition deletion", func(t *testing.T) {
		fabric := smfake.New()
		nad := testNAD(`{"type":"ib-sriov","ibKubernetesEnabled":true}`, true)
		pod := testPod("pod", "node-a", `[{"name":"ib","namespace":"default"}]`)
		reconciler := &Reconciler{Client: testClient(t, nad, pod), Fabric: fabric, RetryInterval: time.Second}
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, time.Second, result.RequeueAfter)
		require.Empty(t, fabric.Deleted())
		require.Contains(t, current(t, reconciler, req).Finalizers, utils.PartitionNADFinalizer)
	})

	t.Run("nad disabled after creation still deletes its partition", func(t *testing.T) {
		fabric := smfake.New()
		nad := testNAD(`{"type":"ib-sriov","ibKubernetesEnabled":false}`, true)
		reconciler := &Reconciler{Client: testClient(t, nad), Fabric: fabric, RetryInterval: time.Second}
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, []string{"default_ib"}, fabric.Deleted())
	})

	t.Run("delete removes the partition and the finalizer", func(t *testing.T) {
		fabric := smfake.New()
		nad := testNAD(`{"type":"ib-sriov","ibKubernetesEnabled":true}`, true)
		reconciler := &Reconciler{Client: testClient(t, nad), Fabric: fabric, RetryInterval: time.Second}
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, []string{"default_ib"}, fabric.Deleted())
		err = reconciler.Get(context.Background(), req.NamespacedName, &netv1.NetworkAttachmentDefinition{})
		require.True(t, apierrors.IsNotFound(err))
	})
}

func testNAD(config string, deleting bool) *netv1.NetworkAttachmentDefinition {
	obj := &netv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "ib", Namespace: "default"},
		Spec:       netv1.NetworkAttachmentDefinitionSpec{Config: config},
	}
	if deleting {
		now := metav1.Now()
		obj.DeletionTimestamp = &now
		obj.Finalizers = []string{utils.PartitionNADFinalizer}
	}
	return obj
}

func testPod(name, node, networks string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "default",
			UID:         types.UID(name),
			Annotations: map[string]string{netv1.NetworkAttachmentAnnot: networks},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func testClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	sch, err := scheme.New()
	require.NoError(t, err)
	return fake.NewClientBuilder().
		WithScheme(sch).
		WithIndex(&corev1.Pod{}, index.Field, index.PodNetworks).
		WithObjects(objs...).
		Build()
}

func current(t *testing.T, reconciler *Reconciler, req reconcile.Request) *netv1.NetworkAttachmentDefinition {
	t.Helper()
	got := &netv1.NetworkAttachmentDefinition{}
	require.NoError(t, reconciler.Get(context.Background(), req.NamespacedName, got))
	return got
}
