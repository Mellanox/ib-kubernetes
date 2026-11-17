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
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	smfake "github.com/Mellanox/ib-kubernetes/pkg/controllers/fake"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	"github.com/Mellanox/ib-kubernetes/pkg/scheme"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

func TestReconcile(t *testing.T) {
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "ib"}}

	t.Run("one add for two pods and a reserved guid", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		other := testPod("pod-b", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), other))

		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, 10*time.Minute, result.RequeueAfter)
		require.Equal(t, 1, fabric.AddCount())
		require.Equal(t, 2, fabric.LastAddLen())
		requireConfiguredGUID(t, kubeClient, "pod-a", "")
	})

	t.Run("user guid is kept", func(t *testing.T) {
		const user = "02:00:00:00:00:00:00:02"
		_, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{"guid":"`+user+`"}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, user, requireConfiguredGUID(t, kubeClient, "pod-a", ""))
	})

	t.Run("configured pod missing from the subnet manager is re-added", func(t *testing.T) {
		const existing = "02:00:00:00:00:00:00:03"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		networks := `[{"name":"ib","namespace":"default","cni-args":{"guid":"` + existing +
			`","pkey":"0x5","mellanox.infiniband.app":"configured"}}]`
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", networks)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, 1, fabric.AddCount())
		require.Equal(t, existing, requireConfiguredGUID(t, kubeClient, "pod-a", ""))
		require.True(t, fabric.Has(existing))
		stored, err := reconciler.Pool.Get(existing)
		require.NoError(t, err)
		require.NotEmpty(t, stored)
	})

	t.Run("tracked delete removes the guid", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.True(t, fabric.Has(allocated))
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(allocated))
	})

	t.Run("unknown guid is log-only until enforce", func(t *testing.T) {
		const extra = "02:00:00:00:00:00:00:09"
		fabric, reconciler, _ := newReconciler(t, poolNAD())
		require.NoError(t, fabric.AddGuidsToPKey(0x5, []net.HardwareAddr{mustGUID(t, extra)}))
		fabric.ResetCounts()
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.True(t, fabric.Has(extra))

		reconciler.OrphanGC = orphanEnforce
		reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(extra))
	})

	t.Run("pending add requeues without configured", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		reconciler.RetryInterval = time.Second
		fabric.SetPending(true)
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, time.Second, result.RequeueAfter)
		selected := network(t, kubeClient, "pod-a")
		require.False(t, utils.IsPodNetworkConfiguredWithInfiniBand(selected))
		_, err = utils.GetPodNetworkGUID(selected)
		require.NoError(t, err)
	})

	t.Run("pending pkey move waits until the fabric drops the old membership", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", withGUID(held))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		requireConfiguredGUID(t, kubeClient, "pod-a", "")

		pod := &corev1.Pod{}
		require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "pod-a"}, pod))
		pod.Annotations[netv1.NetworkAttachmentAnnot] = withGUID(held)
		require.NoError(t, kubeClient.Update(context.Background(), pod))
		nad := &netv1.NetworkAttachmentDefinition{}
		require.NoError(t, kubeClient.Get(context.Background(), req.NamespacedName, nad))
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x7"}`
		require.NoError(t, kubeClient.Update(context.Background(), nad))

		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		require.Contains(t, fabric.Removed(), "0x5 "+held)
		stored, err := reconciler.Pool.Get(held)
		require.NoError(t, err)
		require.Equal(t, "0x5", stored)
		require.False(t, utils.IsPodNetworkConfiguredWithInfiniBand(network(t, kubeClient, "pod-a")))

		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x7", listed[held])
		require.True(t, utils.IsPodNetworkConfiguredWithInfiniBand(network(t, kubeClient, "pod-a")))
	})

	t.Run("failed pkey move retries soon", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", withGUID(held))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)

		pod := &corev1.Pod{}
		require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "pod-a"}, pod))
		pod.Annotations[netv1.NetworkAttachmentAnnot] = withGUID(held)
		require.NoError(t, kubeClient.Update(context.Background(), pod))
		nad := &netv1.NetworkAttachmentDefinition{}
		require.NoError(t, kubeClient.Get(context.Background(), req.NamespacedName, nad))
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x7"}`
		require.NoError(t, kubeClient.Update(context.Background(), nad))
		fabric.SetRemoveError(errors.New("subnet manager unavailable"))

		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		stored, err := reconciler.Pool.Get(held)
		require.NoError(t, err)
		require.Equal(t, "0x5", stored)
	})

	t.Run("interface name that looks like another network keeps its guid", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a",
			`[{"name":"ib","namespace":"default","interface":"x_a_b_y","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		other := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "a", Name: "b"}}
		for pass := 0; pass < 2; pass++ {
			_, err = reconciler.Reconcile(context.Background(), other)
			require.NoError(t, err)
		}
		require.True(t, fabric.Has(allocated))
		stored, err := reconciler.Pool.Get(allocated)
		require.NoError(t, err)
		require.NotEmpty(t, stored)
	})

	t.Run("owned guid moved to another pkey outside the controller is not added back", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NoError(t, fabric.AddGuidsToPKey(0x9, []net.HardwareAddr{mustGUID(t, allocated)}))
		reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x9", listed[allocated])
		stored, err := reconciler.Pool.Get(allocated)
		require.NoError(t, err)
		require.NotEmpty(t, stored)
	})

	t.Run("steady network reuses one download until the drift interval", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NoError(t, fabric.RemoveGuidsFromPKey(0x5, []net.HardwareAddr{mustGUID(t, allocated)}))
		fabric.ResetCounts()

		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, 0, fabric.ListCount())
		require.False(t, fabric.Has(allocated))

		reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, 1, fabric.ListCount())
		require.True(t, fabric.Has(allocated))
	})

	t.Run("one download serves two pkeys", func(t *testing.T) {
		fabric, reconciler, _ := newReconciler(t, poolNAD())
		_, err := reconciler.pkeyMembers(0x5)
		require.NoError(t, err)
		_, err = reconciler.pkeyMembers(0x7)
		require.NoError(t, err)
		require.Equal(t, 1, fabric.ListCount())
	})

	t.Run("new pod skips a guid another network's pod holds", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:00"
		_, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-old", "node-a", configured("other", held))))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NotEqual(t, held, requireConfiguredGUID(t, kubeClient, "pod-a", ""))
	})

	t.Run("duplicate guid stays with the first pod", func(t *testing.T) {
		const shared = "02:00:00:00:00:00:00:04"
		_, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", configured("ib", shared))))
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-b", "node-a", configured("ib", shared))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, reconciler.Owners.ownerOf(shared), "pod-a")
	})

	t.Run("bad guid skips only its pod", func(t *testing.T) {
		for name, bad := range map[string]string{"malformed": "not-a-guid", "out of range": "02:00:00:00:00:00:ff:ff"} {
			t.Run(name, func(t *testing.T) {
				fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
				require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a",
					`[{"name":"ib","namespace":"default","cni-args":{"guid":"`+bad+`"}}]`)))
				require.NoError(t, kubeClient.Create(context.Background(),
					testPod("pod-b", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
				_, err := reconciler.Reconcile(context.Background(), req)
				require.NoError(t, err)
				require.False(t, utils.IsPodNetworkConfiguredWithInfiniBand(network(t, kubeClient, "pod-a")))
				require.True(t, fabric.Has(requireConfiguredGUID(t, kubeClient, "pod-b", "")))
			})
		}
	})

	t.Run("replacement pod takes over its predecessor's guid", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		_, reconciler, kubeClient := newReconciler(t, poolNAD())
		old := testPod("pod-a", "node-a", withGUID(held))
		require.NoError(t, kubeClient.Create(context.Background(), old))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NoError(t, kubeClient.Delete(context.Background(), old))
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a2", "node-a", withGUID(held))))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		requireConfiguredGUID(t, kubeClient, "pod-a2", held)
	})

	t.Run("full pool does not hold back a pod that has a guid", func(t *testing.T) {
		_, reconciler, kubeClient := newReconciler(t, poolNAD())
		for i := 0; i < 16; i++ {
			held := fmt.Sprintf("02:00:00:00:00:00:00:%02x", i)
			require.NoError(t, kubeClient.Create(context.Background(),
				testPod(fmt.Sprintf("held-%02d", i), "node-a", configured("ib", held))))
		}
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("a-user", "node-a", withGUID("02:00:00:00:00:00:00:10"))))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("b-new", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		requireConfiguredGUID(t, kubeClient, "a-user", "02:00:00:00:00:00:00:10")
		require.False(t, utils.IsPodNetworkConfiguredWithInfiniBand(network(t, kubeClient, "b-new")))
	})

	t.Run("guid removed and reused inside the cache window goes back on the pkey", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		old := testPod("pod-a", "node-a", withGUID(held))
		require.NoError(t, kubeClient.Create(context.Background(), old))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-c", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NoError(t, kubeClient.Delete(context.Background(), old))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(held))
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-b", "node-a", withGUID(held))))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		requireConfiguredGUID(t, kubeClient, "pod-b", held)
		require.True(t, fabric.Has(held))
	})

	t.Run("guid reserved at startup leaves the pkey with its pod", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", withGUID(held))
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.True(t, fabric.Has(held))
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(held))
	})

	t.Run("terminating pod keeps its guid until it is deleted", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		pod.Finalizers = []string{"test/hold"}
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.True(t, fabric.Has(allocated))
		require.Contains(t, reconciler.Owners.ownerOf(allocated), "pod-a")

		releaseFinalizers(t, kubeClient, "pod-a")
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(allocated))
	})

	t.Run("guid still on the fabric is not generated for a new pod", func(t *testing.T) {
		const leftover = "02:00:00:00:00:00:00:00"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, fabric.AddGuidsToPKey(0x7, []net.HardwareAddr{mustGUID(t, leftover)}))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NotEqual(t, leftover, requireConfiguredGUID(t, kubeClient, "pod-a", ""))
	})

	t.Run("enforce keeps guids another network on the same pkey owns", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		reconciler.OrphanGC = orphanEnforce
		other := poolNAD()
		other.Name = "ib2"
		require.NoError(t, kubeClient.Create(context.Background(), other))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-b", "node-a", `[{"name":"ib2","namespace":"default","cni-args":{}}]`)))
		otherReq := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "ib2"}}
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		_, err = reconciler.Reconcile(context.Background(), otherReq)
		require.NoError(t, err)
		reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.True(t, fabric.Has(requireConfiguredGUID(t, kubeClient, "pod-a", "")))
		require.True(t, fabric.Has(requireConfiguredGUID(t, kubeClient, "pod-b", "")))
	})

	t.Run("missing nad assigns no guid", func(t *testing.T) {
		_, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ghost","namespace":"default","cni-args":{}}]`)))
		ghost := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "ghost"}}
		_, err := reconciler.Reconcile(context.Background(), ghost)
		require.NoError(t, err)
		selected := network(t, kubeClient, "pod-a")
		require.False(t, utils.PodNetworkHasGUID(selected))
		require.False(t, utils.IsPodNetworkConfiguredWithInfiniBand(selected))
	})

	t.Run("configured pod stays on its pkey when the nad pkey changes", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		nad := &netv1.NetworkAttachmentDefinition{}
		require.NoError(t, kubeClient.Get(context.Background(), req.NamespacedName, nad))
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x7"}`
		require.NoError(t, kubeClient.Update(context.Background(), nad))
		reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x5", listed[allocated])
	})

	t.Run("configured pod keeps its pkey across a restart after the nad pkey changes", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		nad := &netv1.NetworkAttachmentDefinition{}
		require.NoError(t, kubeClient.Get(context.Background(), req.NamespacedName, nad))
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x7"}`
		require.NoError(t, kubeClient.Update(context.Background(), nad))
		restarted := restart(t, reconciler)
		_, err = restarted.Reconcile(context.Background(), req)
		require.NoError(t, err)
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x5", listed[allocated])
	})

	for _, beforeStart := range []bool{true, false} {
		t.Run(fmt.Sprintf("pod naming a guid on another pkey neither moves nor removes it, before start %v",
			beforeStart), func(t *testing.T) {
			const foreign = "02:00:00:00:00:00:00:03"
			fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
			require.NoError(t, fabric.AddGuidsToPKey(0x9, []net.HardwareAddr{mustGUID(t, foreign)}))
			pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{"guid":"`+
				foreign+`","pkey":"0x9","mellanox.infiniband.app":"configured"}}]`)
			if !beforeStart {
				_, err := reconciler.Reconcile(context.Background(), req)
				require.NoError(t, err)
				reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
			}
			require.NoError(t, kubeClient.Create(context.Background(), pod))
			_, err := reconciler.Reconcile(context.Background(), req)
			require.NoError(t, err)
			require.NoError(t, kubeClient.Delete(context.Background(), pod))
			_, err = reconciler.Reconcile(context.Background(), req)
			require.NoError(t, err)
			listed, err := fabric.ListGuidsInUse()
			require.NoError(t, err)
			require.Equal(t, "0x9", listed[foreign])
		})
	}

	t.Run("pod of a missing nad does not remove the fabric guid it names", func(t *testing.T) {
		const foreign = "02:00:00:00:00:00:00:03"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Delete(context.Background(), poolNAD()))
		require.NoError(t, fabric.AddGuidsToPKey(0x5, []net.HardwareAddr{mustGUID(t, foreign)}))
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{"guid":"`+
			foreign+`","pkey":"0x5","mellanox.infiniband.app":"configured"}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		for i := 0; i < 2; i++ {
			_, err = reconciler.Reconcile(context.Background(), req)
			require.NoError(t, err)
		}
		require.True(t, fabric.Has(foreign))
		stored, err := reconciler.Pool.Get(foreign)
		require.NoError(t, err)
		require.NotEmpty(t, stored)
	})

	t.Run("swept guid returns to the pool after its nad is gone", func(t *testing.T) {
		const extra = "02:00:00:00:00:00:00:09"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		reconciler.OrphanGC = orphanEnforce
		require.NoError(t, fabric.AddGuidsToPKey(0x5, []net.HardwareAddr{mustGUID(t, extra)}))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(extra))
		require.NoError(t, kubeClient.Delete(context.Background(), poolNAD()))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		stored, err := reconciler.Pool.Get(extra)
		require.NoError(t, err)
		require.Empty(t, stored)
	})

	t.Run("same guid in another letter case has one owner", func(t *testing.T) {
		_, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", withGUID("02:00:00:00:00:00:00:0a"))))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-b", "node-a", withGUID("02:00:00:00:00:00:00:0A"))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		requireConfiguredGUID(t, kubeClient, "pod-a", "02:00:00:00:00:00:00:0a")
		require.False(t, utils.IsPodNetworkConfiguredWithInfiniBand(network(t, kubeClient, "pod-b")))
	})

	t.Run("generated guid skips an identity the fabric already lists", func(t *testing.T) {
		const taken = "02:00:00:00:00:00:00:00"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, fabric.AddGuidsToPKey(0x9, []net.HardwareAddr{mustGUID(t, taken)}))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NotEqual(t, taken, allocated)
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x9", listed[taken])
		require.Equal(t, "0x5", listed[allocated])
		stored, ok, err := reconciler.Pool.Lookup(taken)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "0x9", stored)
		require.Empty(t, reconciler.Owners.ownerOf(taken))
	})

	t.Run("configured keyless interface keeps its empty pkey when the nad gains one", func(t *testing.T) {
		keyless := poolNAD()
		keyless.Spec.Config = `{"type":"ib-sriov"}`
		fabric, reconciler, kubeClient := newReconciler(t, keyless)
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		stored, ok, err := reconciler.Pool.Lookup(allocated)
		require.NoError(t, err)
		require.True(t, ok)
		require.Empty(t, stored)
		require.False(t, fabric.Has(allocated))

		nad := &netv1.NetworkAttachmentDefinition{}
		require.NoError(t, kubeClient.Get(context.Background(), req.NamespacedName, nad))
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x5"}`
		require.NoError(t, kubeClient.Update(context.Background(), nad))
		reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(allocated))
		stored, ok, err = reconciler.Pool.Lookup(allocated)
		require.NoError(t, err)
		require.True(t, ok)
		require.Empty(t, stored)
		require.Equal(t, allocated, requireConfiguredGUID(t, kubeClient, "pod-a", allocated))
	})

	t.Run("pool exhaustion skips only the pod that cannot be served", func(t *testing.T) {
		const kept = "02:00:00:00:00:00:00:05"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		for n := 0; n <= 0x10; n++ {
			value := fmt.Sprintf("02:00:00:00:00:00:00:%02x", n)
			if value == kept {
				continue
			}
			require.NoError(t, reconciler.Pool.AllocateGUID(value, "0x5"))
		}
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", withGUID(kept))))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-b", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		require.Equal(t, kept, requireConfiguredGUID(t, kubeClient, "pod-a", kept))
		require.True(t, fabric.Has(kept))
		require.False(t, utils.PodNetworkHasGUID(network(t, kubeClient, "pod-b")))
		require.False(t, utils.IsPodNetworkConfiguredWithInfiniBand(network(t, kubeClient, "pod-b")))
	})

	t.Run("one pod keeps two interfaces to the same nad on different pkeys", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a",
			`[{"name":"ib","namespace":"default","interface":"net1","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		first := requireConfiguredGUID(t, kubeClient, "pod-a", "")

		pod := &corev1.Pod{}
		require.NoError(t, kubeClient.Get(context.Background(),
			types.NamespacedName{Namespace: "default", Name: "pod-a"}, pod))
		networks, err := netutils.ParsePodNetworkAnnotation(pod)
		require.NoError(t, err)
		networks = append(networks, &netv1.NetworkSelectionElement{
			Name: "ib", Namespace: "default", InterfaceRequest: "net2",
		})
		raw, err := json.Marshal(networks)
		require.NoError(t, err)
		pod.Annotations[netv1.NetworkAttachmentAnnot] = string(raw)
		require.NoError(t, kubeClient.Update(context.Background(), pod))

		nad := &netv1.NetworkAttachmentDefinition{}
		require.NoError(t, kubeClient.Get(context.Background(), req.NamespacedName, nad))
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x7"}`
		require.NoError(t, kubeClient.Update(context.Background(), nad))
		reconciler.verifiedAt["default/ib"] = time.Now().Add(-time.Hour)
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)

		require.NoError(t, kubeClient.Get(context.Background(),
			types.NamespacedName{Namespace: "default", Name: "pod-a"}, pod))
		networks, err = netutils.ParsePodNetworkAnnotation(pod)
		require.NoError(t, err)
		require.Len(t, networks, 2)
		gotFirst, err := utils.GetPodNetworkGUID(networks[0])
		require.NoError(t, err)
		gotSecond, err := utils.GetPodNetworkGUID(networks[1])
		require.NoError(t, err)
		require.Equal(t, first, gotFirst)
		require.NotEqual(t, first, gotSecond)
		require.True(t, utils.IsPodNetworkConfiguredWithInfiniBand(networks[0]))
		require.True(t, utils.IsPodNetworkConfiguredWithInfiniBand(networks[1]))
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x5", listed[first])
		require.Equal(t, "0x7", listed[gotSecond])
	})

	t.Run("guid stays reserved when the patch reply is lost", func(t *testing.T) {
		_, reconciler, _ := newReconciler(t, poolNAD())
		lose := true
		sch, err := scheme.New()
		require.NoError(t, err)
		kubeClient := k8sfake.NewClientBuilder().
			WithScheme(sch).
			WithIndex(&corev1.Pod{}, index.Field, index.PodNetworks).
			WithObjects(poolNAD(), testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)).
			WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object,
					patch client.Patch, opts ...client.PatchOption) error {
					_, isPod := obj.(*corev1.Pod)
					if err := cl.Patch(ctx, obj, patch, opts...); err != nil || !lose || !isPod {
						return err
					}
					lose = false
					return fmt.Errorf("reply lost")
				},
			}).
			Build()
		reconciler.Client = kubeClient
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		saved, err := utils.GetPodNetworkGUID(network(t, kubeClient, "pod-a"))
		require.NoError(t, err)
		stored, err := reconciler.Pool.Get(saved)
		require.NoError(t, err)
		require.NotEmpty(t, stored)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		requireConfiguredGUID(t, kubeClient, "pod-a", saved)
	})

	t.Run("pod the api server keeps rejecting does not block the others", func(t *testing.T) {
		fabric, reconciler, _ := newReconciler(t, poolNAD())
		sch, err := scheme.New()
		require.NoError(t, err)
		kubeClient := k8sfake.NewClientBuilder().
			WithScheme(sch).
			WithIndex(&corev1.Pod{}, index.Field, index.PodNetworks).
			WithObjects(poolNAD(),
				testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`),
				testPod("pod-b", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)).
			WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object,
					patch client.Patch, opts ...client.PatchOption) error {
					if obj.GetName() == "pod-a" {
						return fmt.Errorf("annotation too large")
					}
					return cl.Patch(ctx, obj, patch, opts...)
				},
			}).
			Build()
		reconciler.Client = kubeClient
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-b", "")
		require.True(t, fabric.Has(allocated))
		require.False(t, utils.PodNetworkHasGUID(network(t, kubeClient, "pod-a")))
	})

	t.Run("deleted pod's guid returns to the pool after the fabric drops it", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		require.False(t, fabric.Has(allocated))
		stored, err := reconciler.Pool.Get(allocated)
		require.NoError(t, err)
		require.NotEmpty(t, stored)
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		stored, err = reconciler.Pool.Get(allocated)
		require.NoError(t, err)
		require.Empty(t, stored)
	})

	t.Run("nad changed to another type still releases its guids", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		nad := &netv1.NetworkAttachmentDefinition{}
		require.NoError(t, kubeClient.Get(context.Background(), req.NamespacedName, nad))
		nad.Spec.Config = `{"type":"macvlan"}`
		require.NoError(t, kubeClient.Update(context.Background(), nad))
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		for i := 0; i < 2; i++ {
			_, err = reconciler.Reconcile(context.Background(), req)
			require.NoError(t, err)
		}
		require.False(t, fabric.Has(allocated))
		stored, err := reconciler.Pool.Get(allocated)
		require.NoError(t, err)
		require.Empty(t, stored)
	})

	t.Run("keyless network does not take a guid listed on another pkey", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		keyless := poolNAD()
		keyless.Spec.Config = `{"type":"ib-sriov"}`
		fabric, reconciler, kubeClient := newReconciler(t, keyless)
		require.NoError(t, fabric.AddGuidsToPKey(0x9, []net.HardwareAddr{mustGUID(t, held)}))
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", withGUID(held))))
		for pass := 0; pass < 2; pass++ {
			_, err := reconciler.Reconcile(context.Background(), req)
			require.NoError(t, err)
		}
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x9", listed[held])
		require.Empty(t, reconciler.Owners.ownerOf(held))
	})

	t.Run("full pool frees fabric-only guids the fabric no longer lists", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		var members []net.HardwareAddr
		for i := 0; i <= 0x10; i++ {
			members = append(members, mustGUID(t, fmt.Sprintf("02:00:00:00:00:00:00:%02x", i)))
		}
		require.NoError(t, fabric.AddGuidsToPKey(0x5, members))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		const stillListed = "02:00:00:00:00:00:00:10"
		require.NoError(t, fabric.RemoveGuidsFromPKey(0x5, members[:len(members)-1]))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		reused := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NotEqual(t, stillListed, reused)
		require.True(t, fabric.Has(reused))
		stored, err := reconciler.Pool.Get(stillListed)
		require.NoError(t, err)
		require.Equal(t, "0x5", stored)
	})

	t.Run("requested guid is copied to the runtime config when the capability is on", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		runtimeNAD := poolNAD()
		runtimeNAD.Spec.Config = `{"type":"ib-sriov","pkey":"0x5","capabilities":{"infinibandGUID":true}}`
		fabric, reconciler, kubeClient := newReconciler(t, runtimeNAD)
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", withGUID(held))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		selected := network(t, kubeClient, "pod-a")
		require.Equal(t, held, selected.InfinibandGUIDRequest)
		require.True(t, utils.IsPodNetworkConfiguredWithInfiniBand(selected))
		require.True(t, fabric.Has(held))
	})

	t.Run("pod created already configured is claimed inside the verified window", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", configured("ib", held))))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, reconciler.Owners.ownerOf(held), "pod-a")
		require.True(t, fabric.Has(held))
	})

	t.Run("live pod takes the guid once its terminating holder is gone", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		gone := testPod("pod-0", "node-a", configured("ib", held))
		gone.Finalizers = []string{"test/hold"}
		require.NoError(t, kubeClient.Create(context.Background(), gone))
		require.NoError(t, kubeClient.Delete(context.Background(), gone))
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", configured("ib", held))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, reconciler.Owners.ownerOf(held), "pod-0")

		releaseFinalizers(t, kubeClient, "pod-0")
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, reconciler.Owners.ownerOf(held), "pod-a")
		require.True(t, fabric.Has(held))
	})

	t.Run("stale pod list does not remove a guid the api still shows", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.True(t, fabric.Has(allocated))

		reconciler.Client = stalePodListClient(t, kubeClient)
		reconciler.APIReader = kubeClient
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.True(t, fabric.Has(allocated))
		require.Equal(t, allocated, requireConfiguredGUID(t, kubeClient, "pod-a", allocated))
		_, ok, err := reconciler.Pool.Lookup(allocated)
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("failed guid removal does not block binding another pod", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		require.NoError(t, kubeClient.Create(context.Background(),
			testPod("pod-b", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)))
		fabric.SetRemoveError(errors.New("subnet manager unavailable"))

		result, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, reconciler.requeueSoon(), result)
		require.True(t, fabric.Has(allocated))
		require.True(t, fabric.Has(requireConfiguredGUID(t, kubeClient, "pod-b", "")))
	})

	t.Run("guid moved to another pkey stays reserved when its pod is gone", func(t *testing.T) {
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		pod := testPod("pod-a", "node-a", `[{"name":"ib","namespace":"default","cni-args":{}}]`)
		require.NoError(t, kubeClient.Create(context.Background(), pod))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		allocated := requireConfiguredGUID(t, kubeClient, "pod-a", "")
		require.NoError(t, fabric.AddGuidsToPKey(0x9, []net.HardwareAddr{mustGUID(t, allocated)}))
		require.NoError(t, kubeClient.Delete(context.Background(), pod))
		reconciler.membershipCachedAt = time.Now().Add(-time.Hour)

		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		listed, err := fabric.ListGuidsInUse()
		require.NoError(t, err)
		require.Equal(t, "0x9", listed[allocated])
		_, ok, err := reconciler.Pool.Lookup(allocated)
		require.NoError(t, err)
		require.True(t, ok)
		require.NotEmpty(t, reconciler.Owners.ownerOf(allocated))
	})

	t.Run("missing nad keeps tracking the pod that still carries the guid", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		owner := testPod("pod-0", "node-a", configured("ib", held))
		require.NoError(t, kubeClient.Create(context.Background(), owner))
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", configured("ib", held))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, reconciler.Owners.ownerOf(held), "pod-0")

		require.NoError(t, kubeClient.Delete(context.Background(), owner))
		require.NoError(t, kubeClient.Delete(context.Background(), poolNAD()))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, reconciler.Owners.ownerOf(held), "pod-a")
		require.True(t, fabric.Has(held))
		_, ok, err := reconciler.Pool.Lookup(held)
		require.NoError(t, err)
		require.True(t, ok)

		require.NoError(t, kubeClient.Delete(context.Background(), testPod("pod-a", "node-a", "")))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.False(t, fabric.Has(held))
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		_, ok, err = reconciler.Pool.Lookup(held)
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("swept guid listed on another pkey stays reserved", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, _ := newReconciler(t, poolNAD())
		require.NoError(t, fabric.AddGuidsToPKey(0x9, []net.HardwareAddr{mustGUID(t, held)}))
		require.NoError(t, reconciler.Pool.AllocateGUID(held, "0x5"))
		reconciler.sweptPKeyByGUID = map[string]int{canonicalGUID(held): 0x5}

		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		_, ok, err := reconciler.Pool.Lookup(held)
		require.NoError(t, err)
		require.True(t, ok)
		require.Contains(t, reconciler.sweptPKeyByGUID, canonicalGUID(held))

		require.NoError(t, fabric.RemoveGuidsFromPKey(0x9, []net.HardwareAddr{mustGUID(t, held)}))
		reconciler.membershipCache = nil
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		_, ok, err = reconciler.Pool.Lookup(held)
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("startup reservation runs once", func(t *testing.T) {
		const extra = "02:00:00:00:00:00:00:09"
		fabric, reconciler, _ := newReconciler(t, poolNAD())
		require.NoError(t, fabric.AddGuidsToPKey(0x5, []net.HardwareAddr{mustGUID(t, extra)}))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NoError(t, reconciler.Pool.ReleaseGUID(extra))

		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		_, ok, err := reconciler.Pool.Lookup(extra)
		require.NoError(t, err)
		require.False(t, ok, "a second reconcile must not reserve the guid again")
	})

	t.Run("a failed pod list leaves seeding to retry", func(t *testing.T) {
		const extra = "02:00:00:00:00:00:00:09"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, fabric.AddGuidsToPKey(0x5, []net.HardwareAddr{mustGUID(t, extra)}))
		fail := true
		sch, err := scheme.New()
		require.NoError(t, err)
		reconciler.Client = k8sfake.NewClientBuilder().
			WithScheme(sch).
			WithIndex(&corev1.Pod{}, index.Field, index.PodNetworks).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return kubeClient.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if fail {
						return fmt.Errorf("list failed")
					}
					return kubeClient.List(ctx, list, opts...)
				},
				Create: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					return kubeClient.Create(ctx, obj, opts...)
				},
				Update: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					return kubeClient.Update(ctx, obj, opts...)
				},
				Patch: func(ctx context.Context, _ client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					return kubeClient.Patch(ctx, obj, patch, opts...)
				},
				Delete: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					return kubeClient.Delete(ctx, obj, opts...)
				},
			}).
			Build()
		_, err = reconciler.Reconcile(context.Background(), req)
		require.Error(t, err)
		require.False(t, reconciler.seeded)

		fail = false
		_, err = reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		_, ok, err := reconciler.Pool.Lookup(extra)
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("restart drops a live guid whose nad is gone when another network sweeps the pkey", func(t *testing.T) {
		const held = "02:00:00:00:00:00:00:04"
		fabric, reconciler, kubeClient := newReconciler(t, poolNAD())
		require.NoError(t, kubeClient.Create(context.Background(), testPod("pod-a", "node-a", configured("ib", held))))
		_, err := reconciler.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, reconciler.Owners.ownerOf(held), "pod-a")

		sibling := poolNAD()
		sibling.Name = "other"
		require.NoError(t, kubeClient.Create(context.Background(), sibling))
		require.NoError(t, kubeClient.Delete(context.Background(), poolNAD()))
		reconciler.OrphanGC = orphanEnforce
		_, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: "default", Name: "other",
		}})
		require.NoError(t, err)
		require.True(t, fabric.Has(held), "the in-memory owner keeps the guid while the pod is live")

		reconciler = restart(t, reconciler)
		_, err = reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: "default", Name: "other",
		}})
		require.NoError(t, err)
		require.False(t, fabric.Has(held), "after a restart the deleted nad no longer makes the guid trusted")
	})
}

func TestRecordPKeys(t *testing.T) {
	_, reconciler, kubeClient := newReconciler(t, poolNAD())
	key := types.NamespacedName{Namespace: "default", Name: "ib"}
	stored := &netv1.NetworkAttachmentDefinition{}
	require.NoError(t, kubeClient.Get(context.Background(), key, stored))
	groups := []pkeyGroup{
		{pkey: "0x6", ifaces: []*iface{{}}},
		{pkey: "0x5", ifaces: []*iface{{}}},
		{pkey: "0x5", ifaces: []*iface{{}}},
		{pkey: "", ifaces: []*iface{{}}},
	}
	require.NoError(t, reconciler.recordPKeys(context.Background(), stored, groups))
	require.NoError(t, kubeClient.Get(context.Background(), key, stored))
	require.Equal(t, "0x5,0x6", stored.Annotations[utils.ConfiguredPKeysAnnotation])

	require.NoError(t, reconciler.recordPKeys(context.Background(), stored, nil))
	require.NoError(t, kubeClient.Get(context.Background(), key, stored))
	_, found := stored.Annotations[utils.ConfiguredPKeysAnnotation]
	require.False(t, found)
}

func TestTrustedPKey(t *testing.T) {
	require.True(t, isTrustedPKey("0x0005", []string{"0x5"}))
	require.True(t, isTrustedPKey("0x5", []string{"0x0005"}))
	require.False(t, isTrustedPKey("0x9", []string{"0x5"}))
}

func TestIbPredicate(t *testing.T) {
	pred := ibPredicate()
	ib := poolNAD()
	other := poolNAD()
	other.Spec.Config = `{"type":"macvlan"}`

	require.True(t, pred.Update(event.UpdateEvent{ObjectOld: ib, ObjectNew: other}))
	require.True(t, pred.Update(event.UpdateEvent{ObjectOld: other, ObjectNew: ib}))
	require.False(t, pred.Update(event.UpdateEvent{ObjectOld: other, ObjectNew: other.DeepCopy()}))
	require.True(t, pred.Create(event.CreateEvent{Object: ib}))
	require.False(t, pred.Create(event.CreateEvent{Object: other}))
	require.True(t, pred.Delete(event.DeleteEvent{Object: ib}))
	require.False(t, pred.Delete(event.DeleteEvent{Object: other}))
}

func TestUnsetPoolRange(t *testing.T) {
	err := (&Reconciler{}).validatePoolRange()
	require.Error(t, err)
	start, end := poolRange(t)
	require.NoError(t, (&Reconciler{RangeStart: start, RangeEnd: end}).validatePoolRange())
}

// stalePodListClient lists pods without the GUID the API server already stored.
func stalePodListClient(t *testing.T, real client.Client) client.Client {
	t.Helper()
	sch, err := scheme.New()
	require.NoError(t, err)
	return k8sfake.NewClientBuilder().
		WithScheme(sch).
		WithIndex(&corev1.Pod{}, index.Field, index.PodNetworks).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return real.Get(ctx, key, obj, opts...)
			},
			List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if err := real.List(ctx, list, opts...); err != nil {
					return err
				}
				pods, ok := list.(*corev1.PodList)
				if !ok {
					return nil
				}
				for i := range pods.Items {
					if pods.Items[i].Annotations == nil {
						continue
					}
					raw := pods.Items[i].Annotations[netv1.NetworkAttachmentAnnot]
					if raw == "" {
						continue
					}
					pods.Items[i].Annotations[netv1.NetworkAttachmentAnnot] =
						`[{"name":"ib","namespace":"default","cni-args":{}}]`
				}
				return nil
			},
			Patch: func(ctx context.Context, _ client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				return real.Patch(ctx, obj, patch, opts...)
			},
			Update: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				return real.Update(ctx, obj, opts...)
			},
			Delete: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				return real.Delete(ctx, obj, opts...)
			},
		}).
		Build()
}

func releaseFinalizers(t *testing.T, kubeClient client.Client, name string) {
	t.Helper()
	pod := &corev1.Pod{}
	require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, pod))
	pod.Finalizers = nil
	require.NoError(t, kubeClient.Update(context.Background(), pod))
}

func configured(network, allocated string) string {
	return `[{"name":"` + network + `","namespace":"default","cni-args":{"guid":"` + allocated +
		`","pkey":"0x5","mellanox.infiniband.app":"configured"}}]`
}

func withGUID(allocated string) string {
	return `[{"name":"ib","namespace":"default","cni-args":{"guid":"` + allocated + `"}}]`
}

func newReconciler(
	t *testing.T, nad *netv1.NetworkAttachmentDefinition,
) (*smfake.Fabric, *Reconciler, client.Client) {
	t.Helper()
	fabric := smfake.New()
	kubeClient := testClient(t, nad)
	start, end := poolRange(t)
	reconciler := &Reconciler{
		Client:             kubeClient,
		APIReader:          kubeClient,
		SM:                 fabric,
		Pool:               testPool(t),
		Owners:             NewOwners(),
		RetryInterval:      time.Second,
		DriftCheckInterval: 10 * time.Minute,
		OrphanGC:           orphanLog,
		RangeStart:         start,
		RangeEnd:           end,
	}
	return fabric, reconciler, kubeClient
}

// restart returns a reconciler with the same cluster and fabric but none of its in-memory state.
func restart(t *testing.T, reconciler *Reconciler) *Reconciler {
	t.Helper()
	return &Reconciler{
		Client:             reconciler.Client,
		APIReader:          reconciler.APIReader,
		SM:                 reconciler.SM,
		Pool:               testPool(t),
		Owners:             NewOwners(),
		RetryInterval:      reconciler.RetryInterval,
		DriftCheckInterval: reconciler.DriftCheckInterval,
		OrphanGC:           reconciler.OrphanGC,
		RangeStart:         reconciler.RangeStart,
		RangeEnd:           reconciler.RangeEnd,
	}
}

func poolRange(t *testing.T) (guid.GUID, guid.GUID) {
	t.Helper()
	start, err := guid.ParseGUID("02:00:00:00:00:00:00:00")
	require.NoError(t, err)
	end, err := guid.ParseGUID("02:00:00:00:00:00:00:10")
	require.NoError(t, err)
	return start, end
}

func testPool(t *testing.T) guid.Pool {
	t.Helper()
	pool, err := guid.NewPool(&config.GUIDPoolConfig{
		RangeStart: "02:00:00:00:00:00:00:00",
		RangeEnd:   "02:00:00:00:00:00:00:10",
	})
	require.NoError(t, err)
	return pool
}

func poolNAD() *netv1.NetworkAttachmentDefinition {
	return &netv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "ib", Namespace: "default"},
		Spec:       netv1.NetworkAttachmentDefinitionSpec{Config: `{"type":"ib-sriov","pkey":"0x5"}`},
	}
}

func testPod(name, node, networks string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name)},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if networks != "" {
		pod.Annotations = map[string]string{netv1.NetworkAttachmentAnnot: networks}
	}
	return pod
}

func testClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	sch, err := scheme.New()
	require.NoError(t, err)
	return k8sfake.NewClientBuilder().
		WithScheme(sch).
		WithIndex(&corev1.Pod{}, index.Field, index.PodNetworks).
		WithObjects(objs...).
		Build()
}

func requireConfiguredGUID(t *testing.T, kubeClient client.Client, name, want string) string {
	t.Helper()
	selected := network(t, kubeClient, name)
	require.True(t, utils.IsPodNetworkConfiguredWithInfiniBand(selected))
	got, err := utils.GetPodNetworkGUID(selected)
	require.NoError(t, err)
	if want != "" {
		require.Equal(t, want, got)
	}
	require.NotEmpty(t, got)
	return got
}

func network(t *testing.T, kubeClient client.Client, name string) *netv1.NetworkSelectionElement {
	t.Helper()
	pod := &corev1.Pod{}
	require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, pod))
	networks, err := netutils.ParsePodNetworkAnnotation(pod)
	require.NoError(t, err)
	require.NotEmpty(t, networks)
	return networks[0]
}

func mustGUID(t *testing.T, value string) net.HardwareAddr {
	t.Helper()
	parsed, err := guid.ParseGUID(value)
	require.NoError(t, err)
	return parsed.HardWareAddress()
}
