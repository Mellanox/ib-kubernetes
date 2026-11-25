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

package index_test

import (
	"testing"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
)

func podWithNetworks(networks string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "pod-a",
			Namespace:   "team",
			Annotations: map[string]string{netv1.NetworkAttachmentAnnot: networks},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
}

func TestNetworkKeys(t *testing.T) {
	t.Run("json selections default to the pod namespace and are deduplicated", func(t *testing.T) {
		pod := podWithNetworks(`[{"name":"ib"},{"name":"ib","interface":"net2"},{"name":"other","namespace":"shared"}]`)
		require.ElementsMatch(t, []string{"team/ib", "shared/other"}, index.NetworkKeys(pod))
	})

	t.Run("comma-separated selections are parsed", func(t *testing.T) {
		pod := podWithNetworks("ib, shared/other")
		require.ElementsMatch(t, []string{"team/ib", "shared/other"}, index.NetworkKeys(pod))
	})

	t.Run("a malformed annotation selects no network", func(t *testing.T) {
		require.Empty(t, index.NetworkKeys(podWithNetworks(`[{"name":`)))
	})

	t.Run("a pod without the annotation selects no network", func(t *testing.T) {
		require.Empty(t, index.NetworkKeys(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}}))
	})
}

func TestPodNetworks(t *testing.T) {
	require.Equal(t, []string{"team/ib"}, index.PodNetworks(podWithNetworks(`[{"name":"ib"}]`)))
	require.Nil(t, index.PodNetworks(&netv1.NetworkAttachmentDefinition{}))
}

func TestLive(t *testing.T) {
	scheduled := podWithNetworks(`[{"name":"ib"}]`)
	require.True(t, index.Live(scheduled))

	unscheduled := scheduled.DeepCopy()
	unscheduled.Spec.NodeName = ""
	require.False(t, index.Live(unscheduled))

	hostNetwork := scheduled.DeepCopy()
	hostNetwork.Spec.HostNetwork = true
	require.False(t, index.Live(hostNetwork))

	for _, phase := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed} {
		finished := scheduled.DeepCopy()
		finished.Status.Phase = phase
		require.False(t, index.Live(finished), phase)
	}

	terminating := scheduled.DeepCopy()
	terminating.DeletionTimestamp = &metav1.Time{}
	require.True(t, index.Live(terminating), "a terminating pod keeps its GUID until it is deleted")
}

func TestKey(t *testing.T) {
	require.Equal(t, "team/ib", index.Key("team", "ib"))
}
