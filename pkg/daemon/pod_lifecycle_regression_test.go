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
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	k8sMocks "github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher/handler"
	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Models a backend whose removal can fail or be acknowledged before it takes
// effect. Membership is independent of the daemon's local allocation map.
type lifecycleSM struct {
	mockSMClient
	membership     map[string]int
	beforeAdd      func()
	holdRemove     bool
	applyThenError error
	listCalls      int
}

func (s *lifecycleSM) AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error {
	if s.beforeAdd != nil {
		s.beforeAdd()
	}
	if err := s.mockSMClient.AddGuidsToPKey(pkey, guids); err != nil {
		return err
	}
	for _, addr := range guids {
		s.membership[addr.String()] = pkey
	}
	return s.applyThenError
}

func (s *lifecycleSM) RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error {
	if err := s.mockSMClient.RemoveGuidsFromPKey(pkey, guids); err != nil {
		return err
	}
	if !s.holdRemove {
		for _, addr := range guids {
			delete(s.membership, addr.String())
		}
	}
	return nil
}

func (s *lifecycleSM) ListGuidsInUse() (map[string]string, error) {
	s.listCalls++
	result := make(map[string]string, len(s.membership))
	for addr, pkey := range s.membership {
		result[addr] = fmt.Sprintf("0x%04x", pkey)
	}
	return result, s.listGuidsInUseError
}

var _ = Describe("Redmine 5230162 pod lifecycle", func() {
	var (
		pod                 *corev1.Pod
		nad                 *netv1.NetworkAttachmentDefinition
		kube                *k8sMocks.Client
		events              handler.ResourceEventHandler
		backend             *lifecycleSM
		controller          *podController
		patchError          error
		applyPatchThenError bool
		readHook            func()
		deleted             bool
	)

	BeforeEach(func() {
		useFastBackoff()
		deleted = false
		patchError = nil
		applyPatchThenError = false
		readHook = nil
		pod = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "short-lived", Namespace: "default", UID: "original-owner", ResourceVersion: "1",
				Annotations: map[string]string{
					netv1.NetworkAttachmentAnnot: `[{"name":"ib-net","namespace":"default","interface":"net1"}]`,
				},
			},
			Spec:   corev1.PodSpec{NodeName: "worker", HostNetwork: false},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		}
		nad = &netv1.NetworkAttachmentDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "ib-net", Namespace: "default"},
			Spec: netv1.NetworkAttachmentDefinitionSpec{
				Config: `{"type":"ib-sriov","pkey":"0x1234","capabilities":{"infinibandGUID":true}}`,
			},
		}
		pool, err := guid.NewPool(&config.GUIDPoolConfig{
			RangeStart: "02:00:00:00:00:00:00:01", RangeEnd: "02:00:00:00:00:00:00:02",
		})
		Expect(err).NotTo(HaveOccurred())
		kube = &k8sMocks.Client{}
		kube.On("GetPod", "default", "short-lived").Return(func(_, _ string) (*corev1.Pod, error) {
			if readHook != nil {
				readHook()
			}
			if deleted {
				return nil, kerrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod.Name)
			}
			return pod.DeepCopy(), nil
		})
		events = handler.NewPodEventHandler()
		backend = &lifecycleSM{
			mockSMClient: mockSMClient{
				name: "lifecycle-sm", addGuidsToPKeyError: nil, addGuidsCallCount: 0,
				addGuidsPKey: 0, addGuids: nil, removeGuidsFromPKeyError: nil,
				removeGuidsCallCount: 0, removeGuidsPKey: 0, removedGuids: nil,
				listGuidsInUseResult: nil, listGuidsInUseError: nil, validateError: nil,
			},
			membership: make(map[string]int), beforeAdd: nil, holdRemove: false,
		}
		partition := newPartitionController(nil, backend, kube, nil)
		controller = newPodController(kube, backend, pool, &stubWatcher{handler: events}, partition)
		// Keep the informer snapshot separate from the deleted API object: the
		// add path currently mutates its copy's network annotation in place.
		events.OnAdd(pod.DeepCopy(), false)
		kube.On("SetAnnotationsOnPod", mock.Anything, mock.Anything).
			Return(func(_ *corev1.Pod, annotations map[string]string) error {
				if deleted {
					return kerrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod.Name)
				}
				if patchError != nil && !applyPatchThenError {
					return patchError
				}
				for key, value := range annotations {
					pod.Annotations[key] = value
				}
				return patchError
			})
	})

	It("does not program a deleted pod from an already captured add snapshot", func() {
		// GetCachedNAD is called after AddPeriodicUpdate snapshots its queue,
		// but before GUID allocation. Deliver deletion at this exact boundary.
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").
			Run(func(_ mock.Arguments) { deleted = true; events.OnDelete(pod.DeepCopy()) }).Return(nad, nil).Once()
		controller.AddPeriodicUpdate()
		addQueue, _ := events.GetResults()
		Expect(addQueue.Items).To(BeEmpty())
		Expect(backend.addGuidsCallCount).To(BeZero(),
			"deletion cancelled the queue entry, but the detached add snapshot still programmed UFM")
	})

	DescribeTable("retains a reservation until removal is observed after deletion during add",
		func(removalError error) {
			kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
			backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
			backend.holdRemove = true
			backend.removeGuidsFromPKeyError = removalError
			controller.AddPeriodicUpdate()
			Expect(backend.addGuids).To(HaveLen(1))
			addr := backend.addGuids[0].String()
			Expect(backend.membership).To(HaveKey(addr))
			Expect(backend.removeGuidsCallCount).To(BeNumerically(">", 0))
			pkey, err := controller.guidPool.Get(addr)
			Expect(err).NotTo(HaveOccurred())
			Expect(pkey).To(Equal("0x1234"),
				"annotation NotFound released a GUID that still belongs to the old owner in UFM")
		},
		Entry("when UFM removal fails", fmt.Errorf("UFM unavailable")),
		Entry("when UFM acknowledges removal but membership remains", nil),
	)

	It("retries orphan cleanup after UFM recovers even when delete had no GUID annotation", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		backend.removeGuidsFromPKeyError = fmt.Errorf("UFM unavailable")
		controller.AddPeriodicUpdate()
		Expect(backend.membership).To(HaveLen(1), "the first cleanup attempt failed")
		backend.beforeAdd = nil
		backend.removeGuidsFromPKeyError = nil
		// Recovery must not require another informer event for a deleted Pod.
		controller.DeletePeriodicUpdate()
		controller.AddPeriodicUpdate()
		controller.DeletePeriodicUpdate()
		Expect(backend.membership).To(BeEmpty(), "failed cleanup was forgotten when the add queue was cancelled")
	})
	It("retains live Pod retries after annotation failure and configures it after recovery", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		patchError = fmt.Errorf("API unavailable")
		controller.AddPeriodicUpdate()
		add, _ := events.GetResults()
		Expect(add.Items).NotTo(BeEmpty())
		patchError = nil
		controller.AddPeriodicUpdate()
		Expect(add.Items).To(BeEmpty())
		Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		Expect(backend.membership).To(HaveLen(1))
	})

	It("does not mutate the queued Pod while programming", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		add, _ := events.GetResults()
		queued, found := add.Get("default_ib-net")
		Expect(found).To(BeTrue())
		snapshot := queued.([]*corev1.Pod)[0]
		original := snapshot.DeepCopy()
		controller.AddPeriodicUpdate()
		Expect(snapshot).To(Equal(original))
	})

	It("accepts a canonical-equivalent uppercase requested GUID", func() {
		controller.guidPool, _ = guid.NewPool(&config.GUIDPoolConfig{
			RangeStart: "02:00:00:00:00:00:00:0a", RangeEnd: "02:00:00:00:00:00:00:0b",
		})
		pod.Annotations[netv1.NetworkAttachmentAnnot] = `[{"name":"ib-net","namespace":"default","interface":"net1","infiniband-guid":"02:00:00:00:00:00:00:0A"}]`
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		controller.AddPeriodicUpdate()
		Expect(backend.membership).To(HaveKey("02:00:00:00:00:00:00:0a"))
		Expect(controller.guidPodNetworkMap).To(HaveKey("02:00:00:00:00:00:00:0a"))
		Expect(backend.removeGuidsCallCount).To(BeZero())
	})

	It("cleans an accepted add that returned an error without recycling its uncertain GUID", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		backend.applyThenError = fmt.Errorf("response lost after acceptance")
		controller.AddPeriodicUpdate()
		Expect(backend.addGuids).To(HaveLen(1))
		addr := backend.addGuids[0].String()
		Expect(backend.membership).To(BeEmpty())
		pkey, err := controller.guidPool.Get(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkey).To(Equal("0x1234"))
		Expect(controller.allocations[addr].uncertain).To(BeTrue())
	})

	It("retains observed cleanup until absence and only then permits reuse", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		backend.holdRemove = true
		controller.AddPeriodicUpdate()
		addr := backend.addGuids[0].String()
		Expect(controller.allocations).To(HaveKey(addr))
		next, err := controller.guidPool.GenerateGUID()
		Expect(err).NotTo(HaveOccurred())
		Expect(next.String()).NotTo(Equal(addr))
		delete(backend.membership, addr) // backend finishes the previously accepted removal
		controller.DeletePeriodicUpdate()
		Expect(controller.allocations).NotTo(HaveKey(addr))
		pkey, err := controller.guidPool.Get(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkey).To(BeEmpty())
		Expect(backend.removeGuidsCallCount).To(Equal(1), "do not issue duplicate accepted removals")
	})

	It("preserves cleanup reservations across pool resync and observation failures", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		backend.listGuidsInUseError = fmt.Errorf("observation unavailable")
		controller.AddPeriodicUpdate()
		addr := backend.addGuids[0].String()
		Expect(backend.membership).To(BeEmpty())
		Expect(controller.allocations).To(HaveKey(addr))
		backend.listGuidsInUseError = nil
		Expect(controller.syncWithSubnetManager()).To(Succeed())
		pkey, err := controller.guidPool.Get(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkey).To(Equal("0x1234"))
	})

	It("cleans using the original PKey even after NAD deletion", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil).Once()
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		backend.removeGuidsFromPKeyError = fmt.Errorf("UFM unavailable")
		controller.AddPeriodicUpdate()
		backend.removeGuidsFromPKeyError = nil
		controller.DeletePeriodicUpdate()
		Expect(backend.membership).To(BeEmpty())
		Expect(backend.removeGuidsPKey).To(Equal(0x1234))
		kube.AssertNumberOfCalls(GinkgoT(), "GetNetworkAttachmentDefinition", 1)
	})

	It("patches multiple interfaces without overwriting unrelated annotations", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		pod.Annotations[netv1.NetworkAttachmentAnnot] = `[{"name":"ib-net","namespace":"default","interface":"net1"},{"name":"ib-net","namespace":"default","interface":"net2"}]`
		pod.Annotations["example.com/keep"] = "untouched"
		controller.AddPeriodicUpdate()
		var networks []netv1.NetworkSelectionElement
		Expect(json.Unmarshal([]byte(pod.Annotations[netv1.NetworkAttachmentAnnot]), &networks)).To(Succeed())
		Expect(networks).To(HaveLen(2))
		for i := range networks {
			Expect(utils.IsPodNetworkConfiguredWithInfiniBand(&networks[i])).To(BeTrue())
		}
		Expect(pod.Annotations["example.com/keep"]).To(Equal("untouched"))
		Expect(backend.membership).To(HaveLen(2))
	})

	It("retains membership when the annotation commits but its response is lost", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		patchError = fmt.Errorf("response lost")
		applyPatchThenError = true
		controller.AddPeriodicUpdate()
		Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		Expect(backend.membership).To(HaveLen(1))
		Expect(backend.removeGuidsCallCount).To(BeZero())
		controller.DeletePeriodicUpdate()
		Expect(backend.removeGuidsCallCount).To(BeZero())
		patchError = nil
		controller.AddPeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(Equal(1), "do not repeat accepted add while retrying the patch")
		Expect(backend.removeGuidsCallCount).To(BeZero())
		add, _ := events.GetResults()
		Expect(add.Items).To(BeEmpty())
	})

	It("does not program after leadership is canceled during the live Pod read", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		controller.stopErr = ctx.Err
		readHook = cancel
		controller.AddPeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(BeZero())
		kube.AssertNotCalled(GinkgoT(), "SetAnnotationsOnPod", mock.Anything, mock.Anything)
	})

	It("batches cleanup by PKey and observes the fabric once per tick", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		pod.Annotations[netv1.NetworkAttachmentAnnot] = `[{"name":"ib-net","namespace":"default","interface":"net1"},{"name":"ib-net","namespace":"default","interface":"net2"}]`
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		controller.AddPeriodicUpdate()
		Expect(backend.addGuids).To(HaveLen(2))
		Expect(backend.removeGuidsCallCount).To(Equal(1))
		Expect(backend.removedGuids).To(HaveLen(2))
		Expect(backend.listCalls).To(Equal(1))
		Expect(controller.allocations).To(BeEmpty())
	})

	It("does not program zero GUID when resync cannot free an exhausted pool", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		for _, addr := range []string{"02:00:00:00:00:00:00:01", "02:00:00:00:00:00:00:02"} {
			Expect(controller.guidPool.AllocateGUID(addr, "0x1234")).To(Succeed())
			controller.guidPodNetworkMap[addr] = "another-owner-" + addr
		}
		controller.AddPeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(BeZero())
		_, err := controller.guidPool.GenerateGUID()
		Expect(err).To(MatchError(guid.ErrGUIDPoolExhausted))
	})

	It("rejects a replacement UID before programming", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		pod.UID = "replacement-owner"
		controller.AddPeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(BeZero())
		Expect(controller.allocations).To(BeEmpty())
	})

	It("retries an ambiguous add for the same live owner after UFM recovery", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		backend.applyThenError = fmt.Errorf("accepted add response lost")
		controller.AddPeriodicUpdate()
		addr := backend.addGuids[0].String()
		Expect(controller.allocations[addr].cleanup).To(BeFalse())
		Expect(controller.allocations[addr].uncertain).To(BeTrue())
		Expect(backend.removeGuidsCallCount).To(BeZero())
		backend.applyThenError = nil
		controller.AddPeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(Equal(2))
		Expect(backend.addGuids[0].String()).To(Equal(addr))
		Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		Expect(backend.membership).To(HaveKey(addr))
		Expect(backend.removeGuidsCallCount).To(BeZero())
		add, _ := events.GetResults()
		Expect(add.Items).To(BeEmpty())
	})

	It("retries accepted removals that remain visible without risking later GUID reuse", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		backend.holdRemove = true
		controller.AddPeriodicUpdate()
		addr := backend.addGuids[0].String()
		Expect(backend.membership).To(HaveKey(addr))
		backend.holdRemove = false
		controller.DeletePeriodicUpdate()
		Expect(backend.removeGuidsCallCount).To(Equal(2))
		Expect(backend.membership).To(BeEmpty())
		Expect(controller.allocations[addr].uncertain).To(BeTrue())
		pkey, err := controller.guidPool.Get(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkey).To(Equal("0x1234"), "an earlier accepted remove may still arrive; do not give this GUID to a new owner")
	})

	It("dequeues a completed Pod while an unrelated owner cannot allocate", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		blocked := pod.DeepCopy()
		blocked.Name, blocked.UID = "blocked", "blocked-owner"
		blocked.Annotations[netv1.NetworkAttachmentAnnot] = `[{"name":"ib-net","namespace":"default","interface":"net1","infiniband-guid":"02:00:00:00:00:00:00:ff"}]`
		kube.On("GetPod", "default", "blocked").Return(blocked, nil)
		events.OnAdd(blocked, false)
		controller.AddPeriodicUpdate()
		add, _ := events.GetResults()
		queued, exists := add.Get("default_ib-net")
		Expect(exists).To(BeTrue())
		Expect(queued.([]*corev1.Pod)).To(HaveLen(1))
		Expect(queued.([]*corev1.Pod)[0].UID).To(Equal(blocked.UID))
		controller.AddPeriodicUpdate()
		kube.AssertNumberOfCalls(GinkgoT(), "SetAnnotationsOnPod", 1)
	})

	It("deduplicates repeated add snapshots for one UID", func() {
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Return(nad, nil)
		events.OnAdd(pod.DeepCopy(), false)
		controller.AddPeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(Equal(1))
		Expect(backend.addGuids).To(HaveLen(1))
		kube.AssertNumberOfCalls(GinkgoT(), "SetAnnotationsOnPod", 1)
		add, _ := events.GetResults()
		Expect(add.Items).To(BeEmpty())
	})

})
