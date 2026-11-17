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

// Package index maps pods to the NetworkAttachmentDefinitions they reference.
package index

import (
	"context"

	netutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// Field is the pod index of "namespace/name" network keys.
const Field = "ib.pod.networks"

// Register indexes pods by the networks they select. Both reconcilers share it.
func Register(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &corev1.Pod{}, Field, PodNetworks)
}

// PodNetworks is the index extract function.
func PodNetworks(obj client.Object) []string {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	return NetworkKeys(pod)
}

// NetworkKeys returns namespace/name for each network the pod selects.
func NetworkKeys(pod *corev1.Pod) []string {
	networks, err := netutils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		return nil
	}
	keys := map[string]struct{}{}
	for _, network := range networks {
		addNetworkKey(keys, pod.Namespace, network.Namespace, network.Name)
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	return out
}

// Live is a pod the binding reconciler still owes a GUID or PF membership.
func Live(pod *corev1.Pod) bool {
	return utils.PodScheduled(pod) &&
		utils.PodWantsNetwork(pod) &&
		!utils.PodIsFinished(pod)
}

// Key is the index value for one NAD.
func Key(namespace, name string) string {
	return namespace + "/" + name
}

func addNetworkKey(keys map[string]struct{}, podNamespace, networkNamespace, name string) {
	if name == "" {
		return
	}
	if networkNamespace == "" {
		networkNamespace = podNamespace
	}
	keys[Key(networkNamespace, name)] = struct{}{}
}
