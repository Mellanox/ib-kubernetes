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
	"fmt"
	"net"
	"strconv"

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
	"k8s.io/client-go/tools/cache"
)

// Keep backend membership independent of the pool. An add may take effect even
// when its response fails, while a failed remove leaves membership untouched.
type lifecycleSM struct {
	mockSMClient
	beforeAdd  func()
	holdRemove bool
}

func (s *lifecycleSM) AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error {
	if s.beforeAdd != nil {
		s.beforeAdd()
	}
	err := s.mockSMClient.AddGuidsToPKey(pkey, guids)
	for _, addr := range guids {
		s.listGuidsInUseResult[addr.String()] = fmt.Sprintf("0x%04x", pkey)
	}
	return err
}

func (s *lifecycleSM) RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error {
	if err := s.mockSMClient.RemoveGuidsFromPKey(pkey, guids); err != nil {
		return err
	}
	if !s.holdRemove {
		for _, addr := range guids {
			if s.listGuidsInUseResult[addr.String()] == fmt.Sprintf("0x%04x", pkey) {
				delete(s.listGuidsInUseResult, addr.String())
			}
		}
	}
	return nil
}

var _ = Describe("Redmine 5230162 pod lifecycle", func() {
	var (
		nad                 *netv1.NetworkAttachmentDefinition
		nadError            error
		apiPods             map[string]*corev1.Pod
		applyPatchThenError bool
		pod                 *corev1.Pod
		kube                *k8sMocks.Client
		events              handler.ResourceEventHandler
		backend             *lifecycleSM
		controller          *podController
		deleted             bool
		patchError          error
		failedPatchUID      string
		beforePatch         func()
		beforeNAD           func()
	)
	BeforeEach(func() {
		useFastBackoff()
		deleted, patchError, beforeNAD, failedPatchUID = false, nil, nil, ""
		nadError, applyPatchThenError, beforePatch = nil, false, nil
		pod = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "short-lived", Namespace: "default", UID: "original-owner", ResourceVersion: "1",
				Annotations: map[string]string{netv1.NetworkAttachmentAnnot: `[{"name":"ib-net","namespace":"default","interface":"net1"}]`}},
			Spec:   corev1.PodSpec{NodeName: "worker", HostNetwork: false},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		}
		apiPods = map[string]*corev1.Pod{pod.Name: pod}
		pool, err := guid.NewPool(&config.GUIDPoolConfig{
			RangeStart: "02:00:00:00:00:00:00:01", RangeEnd: "02:00:00:00:00:00:00:02",
		})
		Expect(err).NotTo(HaveOccurred())
		kube = &k8sMocks.Client{}
		kube.On("GetPod", pod.Namespace, pod.Name).Return(func(_, _ string) (*corev1.Pod, error) {
			if deleted {
				return nil, kerrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod.Name)
			}
			return pod.DeepCopy(), nil
		})
		nad = &netv1.NetworkAttachmentDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "ib-net", Namespace: "default"},
			Spec:       netv1.NetworkAttachmentDefinitionSpec{Config: `{"type":"ib-sriov","pkey":"0x1234","capabilities":{"infinibandGUID":true}}`},
		}
		kube.On("GetNetworkAttachmentDefinition", "default", "ib-net").Run(func(_ mock.Arguments) {
			if beforeNAD != nil {
				beforeNAD()
			}
		}).Return(func(_, _ string) (*netv1.NetworkAttachmentDefinition, error) { return nad, nadError })
		kube.On("SetAnnotationsOnPod", mock.Anything, mock.Anything).Return(func(current *corev1.Pod, annotations map[string]string) error {
			if beforePatch != nil {
				hook := beforePatch
				beforePatch = nil
				hook()
			}
			target := apiPods[current.Name]
			if target == nil || (deleted && current.Name == pod.Name) {
				return kerrors.NewNotFound(schema.GroupResource{Resource: "pods"}, current.Name)
			}
			if current.UID != target.UID || current.ResourceVersion != target.ResourceVersion {
				return kerrors.NewConflict(schema.GroupResource{Resource: "pods"}, current.Name, fmt.Errorf("stale identity/version"))
			}
			writeError := patchError
			if failedPatchUID != "" && string(current.UID) != failedPatchUID {
				writeError = nil
			}
			if writeError != nil && !applyPatchThenError {
				return writeError
			}
			for key, value := range annotations {
				target.Annotations[key] = value
			}
			version, err := strconv.Atoi(target.ResourceVersion)
			Expect(err).NotTo(HaveOccurred())
			target.ResourceVersion = strconv.Itoa(version + 1)
			return writeError
		})
		events = handler.NewPodEventHandler()
		backend = &lifecycleSM{mockSMClient: mockSMClient{
			name: "lifecycle-sm", addGuidsToPKeyError: nil, addGuidsCallCount: 0, addGuidsPKey: 0, addGuids: nil,
			removeGuidsFromPKeyError: nil, removeGuidsCallCount: 0, removeGuidsPKey: 0, removedGuids: nil,
			listGuidsInUseResult: map[string]string{}, listGuidsInUseError: nil, validateError: nil,
		}, beforeAdd: nil, holdRemove: false}
		controller = newPodController(kube, backend, pool, &stubWatcher{handler: events}, newPartitionController(nil, backend, kube, nil))
		events.OnAdd(pod.DeepCopy(), false)
	})

	It("releases GUID membership after a missed deletion tombstone", func() {
		controller.AddPeriodicUpdate()
		Expect(backend.addGuids).To(HaveLen(1))
		addr := backend.addGuids[0].String()
		Expect(backend.listGuidsInUseResult).To(HaveKey(addr))

		deleted = true
		events.OnDelete(cache.DeletedFinalStateUnknown{Key: "default/short-lived", Obj: pod.DeepCopy()})
		controller.DeletePeriodicUpdate()

		Expect(backend.removeGuidsPKey).To(Equal(0x1234))
		Expect(backend.listGuidsInUseResult).NotTo(HaveKey(addr))
		Expect(controller.guidPodNetworkMap).NotTo(HaveKey(addr))
		pkey, err := controller.guidPool.Get(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkey).To(BeEmpty())
		_, deletes := events.GetResults()
		Expect(deletes.Items).To(BeEmpty())
	})

	DescribeTable("rejects stale add snapshots", func(replaced bool) {
		// NAD lookup happens after the add queue snapshot but before allocation.
		beforeNAD = func() {
			if replaced {
				pod.UID = "replacement-owner"
			} else {
				deleted = true
				events.OnDelete(pod.DeepCopy())
			}
		}
		controller.AddPeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(BeZero())
		Expect(controller.guidPodNetworkMap).To(BeEmpty())
	}, Entry("deleted Pod", false), Entry("same-name replacement", true))

	DescribeTable("retains and retries cleanup after deletion during add", func(addError error, held bool) {
		backend.beforeAdd = func() { deleted = true; events.OnDelete(pod.DeepCopy()) }
		backend.addGuidsToPKeyError = addError
		backend.holdRemove = held
		if !held {
			backend.removeGuidsFromPKeyError = fmt.Errorf("UFM unavailable")
		}
		controller.AddPeriodicUpdate()
		Expect(backend.addGuids).To(HaveLen(1))
		addr := backend.addGuids[0].String()
		Expect(backend.listGuidsInUseResult).To(HaveKey(addr))
		pkey, err := controller.guidPool.Get(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkey).To(Equal("0x1234"), "a failed remove must not release ownership")
		Expect(controller.guidPodNetworkMap).To(HaveKey(addr))

		// No further Pod events: cleanup must own its original PKey independently.
		backend.beforeAdd, backend.addGuidsToPKeyError, backend.removeGuidsFromPKeyError = nil, nil, nil
		backend.holdRemove = false
		adds := backend.addGuidsCallCount
		controller.DeletePeriodicUpdate()
		controller.AddPeriodicUpdate()
		controller.DeletePeriodicUpdate()
		Expect(backend.addGuidsCallCount).To(Equal(adds), "must not re-add a deleted owner")
		Expect(backend.listGuidsInUseResult).To(BeEmpty())
		Expect(backend.removeGuidsPKey).To(Equal(0x1234))
		Expect(controller.guidPodNetworkMap).NotTo(HaveKey(addr))
		pkey, err = controller.guidPool.Get(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(pkey).To(BeEmpty())
	}, Entry("successful add then annotation NotFound", nil, false),
		Entry("accepted add with failed response", fmt.Errorf("response lost"), false),
		Entry("successful removal awaiting membership disappearance", nil, true))

	It("cleans a failed add when its initially live owner is deleted later", func() {
		backend.addGuidsToPKeyError = fmt.Errorf("response lost")
		controller.AddPeriodicUpdate()
		Expect(backend.listGuidsInUseResult).To(HaveLen(1))
		deleted = true
		events.OnDelete(pod.DeepCopy())
		backend.addGuidsToPKeyError = nil
		controller.DeletePeriodicUpdate()
		Expect(backend.listGuidsInUseResult).To(BeEmpty())
		Expect(controller.guidPodNetworkMap).To(BeEmpty())
	})

	DescribeTable("retries a live Pod with the same GUID after recovery", func(addFailure bool) {
		if addFailure {
			backend.addGuidsToPKeyError = fmt.Errorf("UFM unavailable")
		} else {
			patchError = fmt.Errorf("API unavailable")
		}
		controller.AddPeriodicUpdate()
		add, _ := events.GetResults()
		Expect(add.Items).NotTo(BeEmpty())
		Expect(backend.addGuids).To(HaveLen(1))
		originalGUID := backend.addGuids[0].String()
		patchError, backend.addGuidsToPKeyError = nil, nil
		controller.DeletePeriodicUpdate()
		controller.AddPeriodicUpdate()
		Expect(add.Items).To(BeEmpty())
		Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		Expect(backend.listGuidsInUseResult).To(HaveKey(originalGUID))
		Expect(backend.listGuidsInUseResult).To(HaveLen(1))
	}, Entry("add fails", true), Entry("annotation fails", false))
	DescribeTable("dequeues a configured Pod independently of a blocked neighbor", func(allocationFails bool) {
		neighbor := pod.DeepCopy()
		neighbor.Name, neighbor.UID = "blocked", "blocked-owner"
		apiPods[neighbor.Name] = neighbor
		if allocationFails {
			addr := "02:00:00:00:00:00:00:02"
			neighbor.Annotations[netv1.NetworkAttachmentAnnot] = `[{"name":"ib-net","namespace":"default","interface":"net1","infiniband-guid":"` + addr + `"}]`
			Expect(controller.guidPool.AllocateGUID(addr, "0x1234")).To(Succeed())
			controller.guidPodNetworkMap[addr] = "another-owner"
		} else {
			patchError, failedPatchUID = fmt.Errorf("API unavailable"), string(neighbor.UID)
		}
		kube.On("GetPod", neighbor.Namespace, neighbor.Name).Return(neighbor.DeepCopy(), nil)
		events.OnAdd(neighbor.DeepCopy(), false)
		controller.AddPeriodicUpdate()
		Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		add, _ := events.GetResults()
		queued, exists := add.Get("default_ib-net")
		Expect(exists).To(BeTrue())
		remaining := queued.([]*corev1.Pod)
		Expect(remaining).To(HaveLen(1))
		Expect(remaining[0].UID).To(Equal(neighbor.UID))
	}, Entry("allocation fails", true), Entry("annotation fails", false))

	DescribeTable("preserves concurrent network intent", func(withdraw, atPatch bool) {
		mutate := func() {
			pod.ResourceVersion = "2"
			pod.Annotations[netv1.NetworkAttachmentAnnot] = `[{"name":"ib-net","namespace":"default","interface":"net1"},{"name":"other","interface":"net2"}]`
			if withdraw {
				pod.Annotations[netv1.NetworkAttachmentAnnot] = `[{"name":"other","interface":"net2"}]`
			}
		}
		if atPatch {
			beforePatch = mutate
		} else {
			backend.beforeAdd = mutate
		}
		controller.AddPeriodicUpdate()
		backend.beforeAdd = nil
		controller.DeletePeriodicUpdate()
		Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(`"name":"other"`))
		if withdraw {
			Expect(backend.listGuidsInUseResult).To(BeEmpty())
			Expect(controller.guidPodNetworkMap).To(BeEmpty())
		} else {
			Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		}
	}, Entry("another interface was appended", false, false), Entry("original interface was withdrawn", true, false),
		Entry("another interface was appended between GET and PATCH", false, true))

	DescribeTable("cleans a completed allocation using its original PKey", func(nadDeleted bool, observedPKey string) {
		controller.AddPeriodicUpdate()
		Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		if observedPKey != "skip" {
			addr := backend.addGuids[0].String()
			backend.listGuidsInUseResult = map[string]string{}
			if observedPKey != "" {
				backend.listGuidsInUseResult[addr] = observedPKey
			}
			Expect(controller.syncWithSubnetManager()).To(Succeed())
			pkey, err := controller.guidPool.Get(addr)
			Expect(err).NotTo(HaveOccurred())
			Expect(pkey).To(Equal("0x1234"))
			Expect(controller.guidPodNetworkMap).To(HaveKey(addr))
			backend.listGuidsInUseResult[addr] = "0x1234"
		}
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x5678","capabilities":{"infinibandGUID":true}}`
		if nadDeleted {
			nadError = kerrors.NewNotFound(schema.GroupResource{Resource: "network-attachment-definitions"}, nad.Name)
			controller.partition.(*partitionController).deleteCachedNAD("default_ib-net")
		}
		deleted = true
		events.OnDelete(pod.DeepCopy())
		controller.DeletePeriodicUpdate()
		Expect(backend.removeGuidsPKey).To(Equal(0x1234))
		Expect(backend.listGuidsInUseResult).To(BeEmpty())
		Expect(controller.guidPodNetworkMap).To(BeEmpty())
	}, Entry("NAD changed", false, "skip"), Entry("NAD deleted", true, "skip"),
		Entry("resync snapshot omits the GUID", false, ""), Entry("resync snapshot reports another PKey", false, "0x5678"))

	DescribeTable("resolves a changed NAD while an operation is pending", func(patchCommitted bool) {
		if patchCommitted {
			patchError, applyPatchThenError = fmt.Errorf("response lost"), true
		} else {
			backend.addGuidsToPKeyError = fmt.Errorf("response lost")
		}
		controller.AddPeriodicUpdate()
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x5678","capabilities":{"infinibandGUID":true}}`
		patchError, backend.addGuidsToPKeyError = nil, nil
		for i := 0; i < 3; i++ {
			controller.DeletePeriodicUpdate()
			controller.AddPeriodicUpdate()
		}
		add, _ := events.GetResults()
		Expect(add.Items).To(BeEmpty())
		Expect(backend.listGuidsInUseResult).To(HaveLen(1))
		if patchCommitted {
			Expect(backend.removeGuidsCallCount).To(BeZero(), "a committed annotation may already be consumed by CNI")
			Expect(backend.listGuidsInUseResult).To(HaveEach("0x1234"))
			Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring("0x1234"))
		} else {
			Expect(backend.removeGuidsPKey).To(Equal(0x1234))
			Expect(backend.listGuidsInUseResult).To(HaveEach("0x5678"))
			Expect(pod.Annotations[netv1.NetworkAttachmentAnnot]).To(ContainSubstring("0x5678"))
		}
	}, Entry("ADD response failed before annotation", false), Entry("PATCH committed but its response failed", true))

	DescribeTable("restores a completed interface before adopting it after the NAD changes", func(mode string) {
		controller.AddPeriodicUpdate()
		adds := backend.addGuidsCallCount
		addr := backend.addGuids[0].String()
		if mode != "present" {
			backend.listGuidsInUseResult = map[string]string{}
			adds++
		}
		if mode == "wrong-pkey" {
			backend.listGuidsInUseResult[addr] = "0x5678"
		}
		if mode == "list-error" {
			backend.listGuidsInUseError = fmt.Errorf("observation unavailable")
		}
		if mode == "add-error" {
			backend.addGuidsToPKeyError = fmt.Errorf("restore response lost")
		}
		if mode == "withdrawn" {
			backend.beforeAdd = func() {
				pod.ResourceVersion = "3"
				pod.Annotations[netv1.NetworkAttachmentAnnot] = `[]`
			}
		}
		add, _ := events.GetResults()
		add.Set("default_ib-net", []*corev1.Pod{pod.DeepCopy()})
		nad.Spec.Config = `{"type":"ib-sriov","pkey":"0x5678","capabilities":{"infinibandGUID":true}}`
		controller.AddPeriodicUpdate()
		if mode == "withdrawn" {
			controller.DeletePeriodicUpdate()
			Expect(backend.removeGuidsPKey).To(Equal(0x1234))
			Expect(backend.listGuidsInUseResult).To(BeEmpty())
			Expect(controller.guidPodNetworkMap).To(BeEmpty())
			return
		}
		if mode == "wrong-pkey" {
			Expect(backend.addGuidsCallCount).To(Equal(adds - 1))
			Expect(backend.removeGuidsCallCount).To(BeZero())
			Expect(backend.listGuidsInUseResult).To(HaveKeyWithValue(addr, "0x5678"))
			backend.listGuidsInUseResult = map[string]string{} // External correction, not daemon removal.
		}
		if mode == "list-error" || mode == "add-error" || mode == "wrong-pkey" {
			Expect(add.Items).NotTo(BeEmpty(), "failed observation/restore must retain retry")
			Expect(controller.guidPodNetworkMap).To(HaveKey(addr))
			Expect(controller.pendingGUIDs).To(HaveKey(addr))
			pkey, err := controller.guidPool.Get(addr)
			Expect(err).NotTo(HaveOccurred())
			Expect(pkey).To(Equal("0x1234"))
			backend.listGuidsInUseError, backend.addGuidsToPKeyError = nil, nil
			controller.AddPeriodicUpdate()
		}
		Expect(add.Items).To(BeEmpty())
		Expect(backend.addGuidsCallCount).To(Equal(adds))
		Expect(backend.removeGuidsCallCount).To(BeZero())
		Expect(backend.listGuidsInUseResult).To(HaveKeyWithValue(addr, "0x1234"))
		Expect(backend.listGuidsInUseResult).To(HaveLen(1))
	}, Entry("membership is present", "present"), Entry("membership is absent", "absent"),
		Entry("observation fails", "list-error"), Entry("restore fails", "add-error"),
		Entry("network withdrawn during restore", "withdrawn"), Entry("membership has an unexpected PKey", "wrong-pkey"))

})
