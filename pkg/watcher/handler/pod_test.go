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
	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Pod Event Handler", func() {
	Context("Create new Pod Event Handler", func() {
		It("Create new Pod Event Handler", func() {
			podEventHandler := NewPodEventHandler()
			Expect(podEventHandler.GetResourceObject().GetObjectKind().GroupVersionKind().Kind).To(Equal("pods"))
		})
	})
	Context("OnAdd", func() {
		It("On add pod event", func() {
			pod1 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[
                       {"name":"test", 
                        "namespace":"default"},
					   {"name":"test2",
                        "cni-args":{"mellanox.infiniband.app":"configured"}}
                     ]`}},
				Spec: kapi.PodSpec{NodeName: "test"}}
			pod2 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test", "namespace":"default"}]`}},
				Spec: kapi.PodSpec{NodeName: "test"}}
			pod3 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test", "namespace":"kube-system"}]`}},
				Spec: kapi.PodSpec{NodeName: "test"}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnAdd(pod1, true)
			podEventHandler.OnAdd(pod2, true)
			podEventHandler.OnAdd(pod3, true)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(2))
			pods := addMap.Items["default_test"].([]*kapi.Pod)
			Expect(len(pods)).To(Equal(2))
			pods = addMap.Items["kube-system_test"].([]*kapi.Pod)
			Expect(len(pods)).To(Equal(1))
		})
		It("On add pod invalid cases", func() {
			// No network needed
			pod1 := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: true}}
			// Running pod
			pod2 := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodRunning}}
			// Finished pod (succeeded)
			pod3 := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodSucceeded}}
			// Finished pod (failed)
			pod4 := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodFailed}}
			// No network attachment annotation
			pod5 := &kapi.Pod{}
			// Not scheduled
			pod6 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test"}]`}}}
			// Invalid network attachment annotation
			pod7 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[invalid]`}},
				Spec: kapi.PodSpec{NodeName: "test"}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnAdd(pod1, true)
			podEventHandler.OnAdd(pod2, true)
			podEventHandler.OnAdd(pod3, true)
			podEventHandler.OnAdd(pod4, true)
			podEventHandler.OnAdd(pod5, true)
			podEventHandler.OnAdd(pod6, true)
			podEventHandler.OnAdd(pod7, true)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(0))
		})
	})
	Context("OnUpdate", func() {
		It("On update pod event", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[
                  {"name":"test", "namespace":"default"},{"name":"test2", "namespace":"default"}]`}}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnAdd(pod, true)
			pod.Spec = kapi.PodSpec{NodeName: "test"}
			podEventHandler.OnUpdate(nil, pod)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(2))
			Expect(len(addMap.Items["default_test"].([]*kapi.Pod))).To(Equal(1))
			Expect(len(addMap.Items["default_test2"].([]*kapi.Pod))).To(Equal(1))
		})
		It("On update pod invalid cases", func() {
			// No network needed
			pod1 := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: true}}
			// Running pod
			pod2 := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodRunning}}
			// No network attachment annotation
			pod3 := &kapi.Pod{}
			// Not scheduled
			pod4 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test"}]`}}}
			// Invalid network attachment annotation
			pod5 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[invalid]`}},
				Spec: kapi.PodSpec{}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnUpdate(nil, pod1)
			podEventHandler.OnUpdate(nil, pod2)
			podEventHandler.OnUpdate(nil, pod3)
			podEventHandler.OnUpdate(nil, pod4)
			podEventHandler.OnAdd(pod5, true)
			pod5.Spec.NodeName = "test"
			podEventHandler.OnUpdate(nil, pod5)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(0))
		})
		It("On update finished pod should trigger delete", func() {
			// Finished pod that should trigger OnDelete
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test", "namespace":"default", "cni-args":{"guid":"02:00:00:00:02:00:00:00", "mellanox.infiniband.app":"configured"}}]`}},
				Status: kapi.PodStatus{Phase: kapi.PodSucceeded}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnUpdate(nil, pod)

			// Should be added to delete map
			_, delMap := podEventHandler.GetResults()
			Expect(len(delMap.Items)).To(Equal(1))
			Expect(len(delMap.Items["default_test"].([]*kapi.Pod))).To(Equal(1))
		})
	})
	Context("OnDelete", func() {
		It("On delete pod event", func() {
			pod1 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[
                      {"name":"test",
                       "namespace":"default",
                       "cni-args":{"guid":"02:00:00:00:02:00:00:00", "mellanox.infiniband.app":"configured"}},
				      {"name":"test2",
                       "namespace":"default",
                       "mellanox.infiniband.app":"configured"},
                      {"name":"test3",
                       "namespace":"default"}
                     ]`}}}
			pod2 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[
                       {"name":"test",
                        "namespace":"default",
                        "cni-args":{"guid":"02:00:00:00:02:00:00:01", "mellanox.infiniband.app":"configured"}}
                     ]`}}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnDelete(pod1)
			podEventHandler.OnDelete(pod2)

			_, delMap := podEventHandler.GetResults()
			Expect(len(delMap.Items)).To(Equal(1))
			Expect(len(delMap.Items["default_test"].([]*kapi.Pod))).To(Equal(2))
		})
		It("On delete pod invalid cases", func() {
			// No network needed
			pod1 := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: true}}
			// No network attachment annotation
			pod2 := &kapi.Pod{}
			// Invalid network attachment annotation
			pod3 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[invalid]`}},
				Spec: kapi.PodSpec{}}
			// InfiniBand configured without guid
			pod4 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test", "cni-args":{"mellanox.infiniband.app":"configured"}}]`}},
				Spec: kapi.PodSpec{}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnDelete(pod1)
			podEventHandler.OnDelete(pod2)
			podEventHandler.OnDelete(pod3)
			podEventHandler.OnDelete(pod4)

			_, delMap := podEventHandler.GetResults()
			Expect(len(delMap.Items)).To(Equal(0))
		})
	})
	Context("Multi-network pod support", func() {
		It("should process pods with multiple interfaces of the same network", func() {
			// This test validates that the pod handler correctly processes pods with
			// multiple network attachments using the same network name
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID: types.UID("a1b2c3d4-e5f6-7890-abcd-ef1234567890"),
					Annotations: map[string]string{
						v1.NetworkAttachmentAnnot: `[
							{"name":"ib-vf-network", "namespace":"default", "interfaceRequest":"net1"},
							{"name":"ib-vf-network", "namespace":"default", "interfaceRequest":"net2"},
							{"name":"ib-vf-network-1", "namespace":"default", "interfaceRequest":"net3"}
						]`,
					},
				},
				Spec: kapi.PodSpec{NodeName: "test-node"},
			}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnAdd(pod, true)

			addMap, _ := podEventHandler.GetResults()
			// The pod handler groups by networkID (namespace_networkname)
			// So we should have 2 entries: default_ib-vf-network and default_ib-vf-network-1
			Expect(len(addMap.Items)).To(Equal(2))

			// Verify both networks are processed
			Expect(addMap.Items).To(HaveKey("default_ib-vf-network"))
			Expect(addMap.Items).To(HaveKey("default_ib-vf-network-1"))

			// Verify default_ib-vf-network has 2 entries (same pod, 2 interfaces)
			pods := addMap.Items["default_ib-vf-network"].([]*kapi.Pod)
			Expect(len(pods)).To(Equal(2))
			Expect(pods[0].UID).To(Equal(pod.UID))
			Expect(pods[1].UID).To(Equal(pod.UID))

			// Verify default_ib-vf-network-1 has 1 entry (same pod, 1 interface)
			pods = addMap.Items["default_ib-vf-network-1"].([]*kapi.Pod)
			Expect(len(pods)).To(Equal(1))
			Expect(pods[0].UID).To(Equal(pod.UID))
		})

		It("should cleanup all interfaces on pod deletion", func() {
			// This test validates that all interfaces are properly cleaned up
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID: types.UID("c3d4e5f6-g7h8-9012-cdef-345678901234"),
					Annotations: map[string]string{
						v1.NetworkAttachmentAnnot: `[
							{"name":"ib-vf-network", "namespace":"default", "interfaceRequest":"net1", "cni-args":{"guid":"02:00:00:00:00:00:00:01", "mellanox.infiniband.app":"configured"}},
							{"name":"ib-vf-network", "namespace":"default", "interfaceRequest":"net2", "cni-args":{"guid":"02:00:00:00:00:00:00:02", "mellanox.infiniband.app":"configured"}},
							{"name":"ib-vf-network-1", "namespace":"default", "interfaceRequest":"net3", "cni-args":{"guid":"02:00:00:00:00:00:00:03", "mellanox.infiniband.app":"configured"}}
						]`,
					},
				},
			}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnDelete(pod)

			_, delMap := podEventHandler.GetResults()
			// Both networks should be marked for cleanup
			Expect(len(delMap.Items)).To(Equal(2))
			Expect(delMap.Items).To(HaveKey("default_ib-vf-network"))
			Expect(delMap.Items).To(HaveKey("default_ib-vf-network-1"))
		})
	})
})
