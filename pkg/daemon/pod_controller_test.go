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

package daemon

import (
	"strconv"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	k8sMocks "github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher/handler"
)

var _ = Describe("podController with same-named NADs in different namespaces", func() {
	DescribeTable("adds, recovers and removes each interface with its own NAD PKey", func(selections string) {
		useFastBackoff()
		deleted := false
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "two-tenants", Namespace: "default", UID: "two-tenants-uid",
				ResourceVersion: "1", Annotations: map[string]string{netv1.NetworkAttachmentAnnot: selections},
			},
			Spec:   corev1.PodSpec{NodeName: "worker"},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		}
		nadPKeys := map[string]string{"tenant-a": "0x1234", "tenant-b": "0x5678"}

		kube := &k8sMocks.Client{}
		kube.On("GetPod", pod.Namespace, pod.Name).Return(func(_, _ string) (*corev1.Pod, error) {
			if deleted {
				return nil, kerrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod.Name)
			}
			return pod.DeepCopy(), nil
		})
		for namespace, pkey := range nadPKeys {
			kube.On("GetNetworkAttachmentDefinition", namespace, "ib-net").Return(&netv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "ib-net", Namespace: namespace},
				Spec:       netv1.NetworkAttachmentDefinitionSpec{Config: `{"type":"ib-sriov","pkey":"` + pkey + `"}`},
			}, nil)
		}
		kube.On("SetAnnotationsOnPod", mock.Anything, mock.Anything).Return(
			func(current *corev1.Pod, annotations map[string]string) error {
				Expect(current.ResourceVersion).To(Equal(pod.ResourceVersion))
				for key, value := range annotations {
					pod.Annotations[key] = value
				}
				version, err := strconv.Atoi(pod.ResourceVersion)
				Expect(err).NotTo(HaveOccurred())
				pod.ResourceVersion = strconv.Itoa(version + 1)
				return nil
			})

		newPool := func() guid.Pool {
			pool, err := guid.NewPool(&config.GUIDPoolConfig{
				RangeStart: "02:00:00:00:00:00:00:01", RangeEnd: "02:00:00:00:00:00:00:10",
			})
			Expect(err).NotTo(HaveOccurred())
			return pool
		}
		backend := &lifecycleSM{mockSMClient: mockSMClient{name: "lifecycle-sm", listGuidsInUseResult: map[string]string{}}}
		events := handler.NewPodEventHandler()
		controller := newPodController(kube, backend, newPool(), &stubWatcher{handler: events},
			newPartitionController(nil, backend, kube, nil))

		events.OnAdd(pod.DeepCopy(), false)
		controller.AddPeriodicUpdate()

		networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(networks).To(HaveLen(2))
		for _, network := range networks {
			Expect(utils.IsPodNetworkConfiguredWithInfiniBand(network)).To(BeTrue())
			pkey, pkeyErr := utils.GetPodNetworkPkey(network)
			Expect(pkeyErr).NotTo(HaveOccurred())
			Expect(pkey).To(Equal(nadPKeys[network.Namespace]), "pkey of %s/%s", network.Namespace, network.Name)
			podGUID, guidErr := utils.GetPodNetworkGUID(network)
			Expect(guidErr).NotTo(HaveOccurred())
			Expect(backend.listGuidsInUseResult).To(HaveKeyWithValue(podGUID, nadPKeys[network.Namespace]))
		}
		Expect(backend.listGuidsInUseResult).To(HaveLen(2))
		add, _ := events.GetResults()
		Expect(add.Items).To(BeEmpty())

		// A new leader rebuilds the same GUID ownership from the annotation.
		pod.Status.Phase = corev1.PodRunning
		kube.On("GetPods", corev1.NamespaceAll).Return(func(_ string) (*corev1.PodList, error) {
			return &corev1.PodList{Items: []corev1.Pod{*pod.DeepCopy()}}, nil
		})
		restarted := newPodController(kube, backend, newPool(), &stubWatcher{handler: handler.NewPodEventHandler()},
			newPartitionController(nil, backend, kube, nil))
		Expect(restarted.initGUIDPool()).To(Succeed())
		Expect(restarted.guidPodNetworkMap).To(Equal(controller.guidPodNetworkMap))

		deleted = true
		events.OnDelete(pod.DeepCopy())
		controller.DeletePeriodicUpdate()

		Expect(backend.listGuidsInUseResult).To(BeEmpty())
		Expect(controller.guidPodNetworkMap).To(BeEmpty())
	},
		Entry("with interface names", `[`+
			`{"name":"ib-net","namespace":"tenant-a","interface":"net1"},`+
			`{"name":"ib-net","namespace":"tenant-b","interface":"net2"}]`),
		// Without names the interface index counts same-named selections across namespaces.
		Entry("without interface names", `[{"name":"ib-net","namespace":"tenant-a"},{"name":"ib-net","namespace":"tenant-b"}]`),
	)
})
