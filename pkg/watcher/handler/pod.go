// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
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

package handler

import (
	"fmt"
	"sync"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

type podEventHandler struct {
	retryPods   sync.Map
	addedPods   *utils.SynchronizedMap
	deletedPods *utils.SynchronizedMap
}

func NewPodEventHandler() ResourceEventHandler {
	eventHandler := &podEventHandler{
		retryPods:   sync.Map{},
		addedPods:   utils.NewSynchronizedMap(),
		deletedPods: utils.NewSynchronizedMap(),
	}

	return eventHandler
}

func (p *podEventHandler) GetResourceObject() runtime.Object {
	return &kapi.Pod{TypeMeta: metav1.TypeMeta{Kind: kapi.ResourcePods.String()}}
}

func (p *podEventHandler) OnAdd(obj interface{}, _ bool) {
	log.Debug().Msgf("pod add Event: pod %v", obj)
	pod := obj.(*kapi.Pod)
	log.Info().Msgf("pod add Event: namespace %s name %s", pod.Namespace, pod.Name)

	if !utils.PodWantsNetwork(pod) {
		log.Debug().Msg("pod doesn't require network")
		return
	}

	if utils.PodIsRunning(pod) {
		log.Debug().Msg("pod is already in running state")
		return
	}

	if utils.PodIsFinished(pod) {
		log.Debug().Msg("pod is already in finished state")
		return
	}

	if !utils.HasNetworkAttachmentAnnot(pod) {
		log.Debug().Msgf("pod doesn't have network annotation \"%v\"", v1.NetworkAttachmentAnnot)
		return
	}

	if !utils.PodScheduled(pod) {
		p.retryPods.Store(pod.UID, true)
		return
	}

	if err := p.addNetworksFromPod(pod); err != nil {
		log.Error().Msgf("%v", err)
		return
	}

	log.Info().Msgf("pod add event: successfully added namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) OnUpdate(oldObj, newObj interface{}) {
	log.Debug().Msgf("pod update event: oldPod %v, newPod %v", oldObj, newObj)
	pod := newObj.(*kapi.Pod)
	log.Info().Msgf("pod update event: namespace %s name %s", pod.Namespace, pod.Name)

	if !utils.PodWantsNetwork(pod) {
		log.Debug().Msg("pod doesn't require network")
		return
	}

	if utils.PodIsRunning(pod) {
		log.Debug().Msg("pod is already in running state")
		p.retryPods.Delete(pod.UID)
		return
	}

	if utils.PodIsFinished(pod) {
		log.Debug().Msg("pod is finished")
		p.OnDelete(newObj)
		return
	}

	if !utils.HasNetworkAttachmentAnnot(pod) {
		log.Debug().Msgf("pod doesn't have network annotation \"%v\"", v1.NetworkAttachmentAnnot)
		return
	}

	_, retry := p.retryPods.Load(pod.UID)
	if !retry || !utils.PodScheduled(pod) {
		return
	}

	if err := p.addNetworksFromPod(pod); err != nil {
		log.Error().Msgf("%v", err)
		return
	}

	p.retryPods.Delete(pod.UID)
	log.Info().Msgf("pod update event: successfully updated namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) OnDelete(obj interface{}) {
	log.Debug().Msgf("pod delete event: pod %v", obj)
	pod := obj.(*kapi.Pod)
	log.Info().Msgf("pod delete event: namespace %s name %s", pod.Namespace, pod.Name)

	// make sure this pod won't be in the retry pods
	p.retryPods.Delete(pod.UID)

	if !utils.PodWantsNetwork(pod) {
		log.Debug().Msg("pod doesn't require network")
		return
	}

	// Try primary path: parse cni-args from network annotation
	if utils.HasNetworkAttachmentAnnot(pod) {
		if p.deleteFromNetworkAnnotation(pod) {
			return
		}
	}

	// Fallback: KubeVirt virt-handler may strip cni-args from the network annotation
	// while the VMI is running. Use network-status (written by multus, never stripped)
	// to identify IB networks for delete queue.
	p.deleteFromNetworkStatus(pod)
}

// deleteFromNetworkAnnotation queues pod for delete using cni-args from k8s.v1.cni.cncf.io/networks.
// Returns true if at least one IB network was found and queued.
func (p *podEventHandler) deleteFromNetworkAnnotation(pod *kapi.Pod) bool {
	networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		log.Debug().Msgf("failed to parse network annotations for pod %s: %v", pod.Name, err)
		return false
	}

	queued := false
	for _, network := range networks {
		if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
			continue
		}

		if !utils.PodNetworkHasGUID(network) {
			log.Debug().Msgf("pod %s network %s has no guid (PF mode), queuing for delete",
				pod.Name, network.Name)
		}

		networkID := utils.GenerateNetworkID(network)
		p.queuePodForDelete(pod, networkID)
		queued = true
	}

	if queued {
		log.Info().Msgf("successfully queued pod %s/%s for delete via network annotation",
			pod.Namespace, pod.Name)
	}
	return queued
}

// deleteFromNetworkStatus queues pod for delete using k8s.v1.cni.cncf.io/network-status.
// This is the fallback path when cni-args are stripped (e.g. by KubeVirt virt-handler).
func (p *podEventHandler) deleteFromNetworkStatus(pod *kapi.Pod) {
	statuses, err := netAttUtils.GetNetworkStatus(pod)
	if err != nil {
		log.Debug().Msgf("pod %s has no network-status annotation: %v", pod.Name, err)
		return
	}

	queued := false
	seen := make(map[string]bool)
	for _, status := range statuses {
		// Skip non-SR-IOV networks (no device-info means no PCI passthrough)
		if status.DeviceInfo == nil {
			continue
		}
		// network-status name is "namespace/nadName" — convert to networkID "namespace_nadName"
		networkID := utils.NetworkStatusNameToNetworkID(status.Name)
		if networkID == "" {
			continue
		}
		if seen[networkID] {
			continue
		}
		seen[networkID] = true
		p.queuePodForDelete(pod, networkID)
		queued = true
	}

	if queued {
		log.Info().Msgf("successfully queued pod %s/%s for delete via network-status fallback",
			pod.Namespace, pod.Name)
	}
}

func (p *podEventHandler) queuePodForDelete(pod *kapi.Pod, networkID string) {
	var pods []*kapi.Pod
	if existing, ok := p.deletedPods.Get(networkID); ok {
		pods = existing.([]*kapi.Pod)
		// Dedup by UID only when the pod actually has one. Tests may construct
		// bare pods without a UID; in that case, fall through to append so we
		// don't collapse distinct test pods into a single entry.
		if pod.UID != "" {
			for _, queued := range pods {
				if queued.UID == pod.UID {
					return
				}
			}
		}
	}
	p.deletedPods.Set(networkID, append(pods, pod))
}

func (p *podEventHandler) GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap) {
	return p.addedPods, p.deletedPods
}

func (p *podEventHandler) addNetworksFromPod(pod *kapi.Pod) error {
	networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		p.retryPods.Store(pod.UID, true)
		return fmt.Errorf("failed to parse network annotations with error: %v", err)
	}

	for _, network := range networks {
		// check if pod network is configured
		if utils.IsPodNetworkConfiguredWithInfiniBand(network) {
			continue
		}

		networkID := utils.GenerateNetworkID(network)
		pods, ok := p.addedPods.Get(networkID)
		if !ok {
			pods = []*kapi.Pod{pod}
		} else {
			pods = append(pods.([]*kapi.Pod), pod)
		}

		p.addedPods.Set(networkID, pods)
	}

	return nil
}
