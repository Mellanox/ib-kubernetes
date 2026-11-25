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
	"fmt"
	"strings"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// iface is one IB network entry on a live pod.
// network is that entry. networks is the whole annotation, kept so a patch writes it back intact.
type iface struct {
	pod      *corev1.Pod
	networks []*netv1.NetworkSelectionElement
	network  *netv1.NetworkSelectionElement
	owner    string
	skip     bool
}

func (reconciler *Reconciler) livePods(ctx context.Context, namespace, name string) ([]corev1.Pod, error) {
	var list corev1.PodList
	err := reconciler.List(ctx, &list, client.MatchingFields{index.Field: index.Key(namespace, name)})
	if err != nil {
		return nil, fmt.Errorf("list pods for %s/%s: %w", namespace, name, err)
	}
	out := make([]corev1.Pod, 0, len(list.Items))
	for podIndex := range list.Items {
		if index.Live(&list.Items[podIndex]) {
			out = append(out, list.Items[podIndex])
		}
	}
	return out, nil
}

func collectInterfaces(pods []corev1.Pod, nadNS, nadName string) []iface {
	out := make([]iface, 0, len(pods))
	for podIndex := range pods {
		pod := pods[podIndex].DeepCopy()
		networks, err := netutils.ParsePodNetworkAnnotation(pod)
		if err != nil {
			continue
		}
		for _, network := range networks {
			if !sameNetwork(pod.Namespace, network, nadNS, nadName) {
				continue
			}
			out = append(out, iface{
				pod:      pod,
				networks: networks,
				network:  network,
				owner:    ownerID(pod, networks, network),
			})
		}
	}
	return out
}

func sameNetwork(podNS string, network *netv1.NetworkSelectionElement, nadNS, nadName string) bool {
	if network == nil || network.Name != nadName {
		return false
	}
	ns := network.Namespace
	if ns == "" {
		ns = podNS
	}
	return ns == nadNS
}

func ownerID(
	pod *corev1.Pod, networks []*netv1.NetworkSelectionElement, network *netv1.NetworkSelectionElement,
) string {
	return string(pod.UID) + "/" + networkKey(pod, network) + "/" + utils.GetPodNetworkInterfaceName(networks, network)
}

func patchNetworks(
	ctx context.Context, kubeClient client.Client, pod *corev1.Pod, networks []*netv1.NetworkSelectionElement,
) error {
	raw, err := json.Marshal(networks)
	if err != nil {
		return fmt.Errorf("marshal networks: %w", err)
	}
	base := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[netv1.NetworkAttachmentAnnot] = string(raw)
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if err := kubeClient.Patch(ctx, pod, patch); err != nil {
		return fmt.Errorf("patch pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return nil
}

func setConfigured(network *netv1.NetworkSelectionElement, pkey string) {
	if network.CNIArgs == nil {
		args := map[string]interface{}{}
		network.CNIArgs = &args
	}
	(*network.CNIArgs)[utils.InfiniBandAnnotation] = utils.ConfiguredInfiniBandPod
	if pkey != "" {
		(*network.CNIArgs)[utils.PkeyAnnotation] = pkey
	}
}

func (reconciler *Reconciler) cacheShowsConfigured(
	ctx context.Context, ifaces []iface, nadNS, nadName string,
) (bool, error) {
	seen := map[types.UID]struct{}{}
	for ifaceIndex := range ifaces {
		if ifaces[ifaceIndex].skip {
			continue
		}
		if _, ok := seen[ifaces[ifaceIndex].pod.UID]; ok {
			continue
		}
		seen[ifaces[ifaceIndex].pod.UID] = struct{}{}
		ok, err := reconciler.podShowsConfigured(ctx, ifaces[ifaceIndex].pod, nadNS, nadName)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (reconciler *Reconciler) podShowsConfigured(
	ctx context.Context, pod *corev1.Pod, nadNS, nadName string,
) (bool, error) {
	fresh := &corev1.Pod{}
	if err := reconciler.Get(ctx, client.ObjectKeyFromObject(pod), fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	// An unreadable selection is not configured yet; the reconcile retries it.
	networks, parseErr := netutils.ParsePodNetworkAnnotation(fresh)
	return parseErr == nil && networksConfigured(networks, fresh.Namespace, nadNS, nadName), nil
}

func networksConfigured(networks []*netv1.NetworkSelectionElement, podNS, nadNS, nadName string) bool {
	for _, network := range networks {
		if sameNetwork(podNS, network, nadNS, nadName) && !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
			return false
		}
	}
	return true
}

func cacheHasGUIDs(ctx context.Context, kubeClient client.Client, pod *corev1.Pod, want []string) bool {
	fresh := &corev1.Pod{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(pod), fresh); err != nil {
		return false
	}
	raw := fresh.Annotations[netv1.NetworkAttachmentAnnot]
	for _, allocated := range want {
		if !strings.Contains(raw, allocated) {
			return false
		}
	}
	return true
}
