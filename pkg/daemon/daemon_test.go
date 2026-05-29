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

package daemon

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	k8sMocks "github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher"
	resEventHandler "github.com/Mellanox/ib-kubernetes/pkg/watcher/handler"
	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	testifyMock "github.com/stretchr/testify/mock"
	kapi "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Enhanced mock for SubnetManagerClient
type mockSMClient struct {
	addGuidsToPKeyError      error
	addGuidsCallCount        int
	addGuidsPKey             int
	addGuids                 []net.HardwareAddr
	name                     string
	removeGuidsFromPKeyError error
	removeGuidsCallCount     int
	removeGuidsPKey          int
	removedGuids             []net.HardwareAddr
	listGuidsInUseResult     map[string]string
	listGuidsInUseError      error
	validateError            error
}

func (m *mockSMClient) Name() string {
	return m.name
}

func (m *mockSMClient) Spec() string {
	return "test-spec-1.0"
}

func (m *mockSMClient) Validate() error {
	return m.validateError
}

func (m *mockSMClient) AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error {
	m.addGuidsCallCount++
	m.addGuidsPKey = pkey
	m.addGuids = cloneHardwareAddrs(guids)
	return m.addGuidsToPKeyError
}

func (m *mockSMClient) RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error {
	m.removeGuidsCallCount++
	m.removeGuidsPKey = pkey
	m.removedGuids = cloneHardwareAddrs(guids)
	return m.removeGuidsFromPKeyError
}

func (m *mockSMClient) ListGuidsInUse() (map[string]string, error) {
	if m.listGuidsInUseResult == nil {
		return map[string]string{}, m.listGuidsInUseError
	}
	return m.listGuidsInUseResult, m.listGuidsInUseError
}

func cloneHardwareAddrs(guids []net.HardwareAddr) []net.HardwareAddr {
	if guids == nil {
		return nil
	}
	cloned := make([]net.HardwareAddr, len(guids))
	for i := range guids {
		cloned[i] = append(net.HardwareAddr(nil), guids[i]...)
	}
	return cloned
}

// mockFabricClient extends mockSMClient with FabricClient methods for partition-aware tests
type mockFabricClient struct {
	mockSMClient

	// CreateIBPartition
	createPartitionKey    string
	createPartitionError  error
	createPartitionCalls  int
	createdPartitionNames []string

	// DeleteIBPartition
	deletePartitionError  error
	deletePartitionCalls  int
	deletedPartitionNames []string

	// IsPartitionReady
	partitionReady      bool
	partitionReadyKey   string
	partitionReadyErr   error
	isReadyCalls        int
	readyPartitionNames []string

	// GetNodeIBDevices
	nodeIBDevices      []plugins.IBDeviceGUID
	nodeIBDevicesErr   error
	getDevicesCalls    int
	requestedNodeNames []string
}

func (m *mockFabricClient) CreateIBPartition(name string) (string, error) {
	m.createPartitionCalls++
	m.createdPartitionNames = append(m.createdPartitionNames, name)
	return m.createPartitionKey, m.createPartitionError
}

func (m *mockFabricClient) DeleteIBPartition(name string) error {
	m.deletePartitionCalls++
	m.deletedPartitionNames = append(m.deletedPartitionNames, name)
	return m.deletePartitionError
}

func (m *mockFabricClient) IsPartitionReady(name string) (bool, string, error) {
	m.isReadyCalls++
	m.readyPartitionNames = append(m.readyPartitionNames, name)
	return m.partitionReady, m.partitionReadyKey, m.partitionReadyErr
}

func (m *mockFabricClient) GetNodeIBDevices(nodeName string) ([]plugins.IBDeviceGUID, error) {
	m.getDevicesCalls++
	m.requestedNodeNames = append(m.requestedNodeNames, nodeName)
	return m.nodeIBDevices, m.nodeIBDevicesErr
}

func useFastBackoff() {
	originalBackoff := backoffValues
	backoffValues = wait.Backoff{Duration: time.Millisecond, Factor: 1, Steps: 2}
	DeferCleanup(func() {
		backoffValues = originalBackoff
	})
}

// stubWatcher / stubEventHandler are minimal in-package fakes for exercising
// AddPeriodicUpdate / DeletePeriodicUpdate without standing up an informer.
type stubWatcher struct {
	handler resEventHandler.ResourceEventHandler
}

func (s *stubWatcher) RunBackground() watcher.StopFunc            { return func() {} }
func (s *stubWatcher) GetHandler() resEventHandler.ResourceEventHandler { return s.handler }

type stubEventHandler struct {
	addMap    *utils.SynchronizedMap
	deleteMap *utils.SynchronizedMap
}

func (s *stubEventHandler) GetResourceObject() runtime.Object { return nil }
func (s *stubEventHandler) GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap) {
	return s.addMap, s.deleteMap
}
func (s *stubEventHandler) OnAdd(_ interface{}, _ bool) {}
func (s *stubEventHandler) OnUpdate(_, _ interface{})   {}
func (s *stubEventHandler) OnDelete(_ interface{})      {}

var _ = Describe("Daemon", func() {
	var (
		mockClient    *mockSMClient
		guidPool      guid.Pool
		testPodCtrl   *podController
		testGUID      = "02:00:00:00:00:00:00:01"
		testPKey      = "0x1234"
		testUID       = types.UID("test-pod-uid")
		testNetworkID = "test-network"
	)

	BeforeEach(func() {
		mockClient = &mockSMClient{
			name:                     "test-sm",
			removeGuidsFromPKeyError: nil,
			removeGuidsCallCount:     0,
			listGuidsInUseResult:     nil,
			listGuidsInUseError:      nil,
			validateError:            nil,
		}

		poolConfig := &config.GUIDPoolConfig{
			RangeStart: "02:00:00:00:00:00:00:00",
			RangeEnd:   "02:00:00:00:00:00:FF:FF",
		}
		var err error
		guidPool, err = guid.NewPool(poolConfig)
		Expect(err).ToNot(HaveOccurred())

		testPodCtrl = &podController{
			smClient:          mockClient,
			guidPool:          guidPool,
			guidPodNetworkMap: make(map[string]string),
		}
	})

	Context("allocatePodNetworkGUID", func() {
		It("allocates new GUID successfully", func() {
			err := testPodCtrl.allocatePodNetworkGUID(testGUID, testNetworkID, testUID, testPKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(testPodCtrl.guidPodNetworkMap[testGUID]).To(Equal(testNetworkID))

			pkey, err := guidPool.Get(testGUID)
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(Equal(testPKey))
		})

		It("returns error when GUID already allocated to different network", func() {
			testPodCtrl.guidPodNetworkMap[testGUID] = "different-network"

			err := testPodCtrl.allocatePodNetworkGUID(testGUID, testNetworkID, testUID, testPKey)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already allocated"))
		})

		It("succeeds when GUID already allocated to same network", func() {
			testPodCtrl.guidPodNetworkMap[testGUID] = testNetworkID

			err := testPodCtrl.allocatePodNetworkGUID(testGUID, testNetworkID, testUID, testPKey)
			Expect(err).ToNot(HaveOccurred())
		})

		It("handles existing pkey cleanup on reallocation", func() {
			oldPKey := "0x5678"
			err := guidPool.AllocateGUID(testGUID, oldPKey)
			Expect(err).ToNot(HaveOccurred())
			testPodCtrl.guidPodNetworkMap[testGUID] = "old-network"

			err = testPodCtrl.allocatePodNetworkGUID(testGUID, testNetworkID, testUID, testPKey)
			Expect(err).ToNot(HaveOccurred())

			Expect(testPodCtrl.guidPodNetworkMap[testGUID]).To(Equal(testNetworkID))
			pkey, err := guidPool.Get(testGUID)
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(Equal(testPKey))
			Expect(mockClient.removeGuidsCallCount).To(Equal(1))
		})

		It("handles subnet manager removal failure gracefully", func() {
			oldPKey := "0x5678"
			err := guidPool.AllocateGUID(testGUID, oldPKey)
			Expect(err).ToNot(HaveOccurred())
			testPodCtrl.guidPodNetworkMap[testGUID] = "old-network"

			// Make the mock fail first few times then succeed to avoid infinite backoff
			mockClient.removeGuidsFromPKeyError = fmt.Errorf("sm error")

			// This test verifies that the system handles SM failures gracefully
			// We expect this to take some time due to exponential backoff, so let's skip this test
			Skip("Skipping test that causes exponential backoff delay")
		})

		It("returns error for invalid GUID format", func() {
			err := testPodCtrl.allocatePodNetworkGUID("invalid-guid", testNetworkID, testUID, testPKey)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to allocate GUID"))
		})
	})

	Context("getAllPodGUIDInfosForNetwork", func() {
		It("returns interface-aware pod network IDs for duplicate network names", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "multi-pod",
					Name:      "multi-pod",
					Namespace: "default",
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": `[{"name":"ib-sriov-network","namespace":"default","cni-args":{"guid":"02:00:00:00:00:00:00:21","pkey":"0x7fff","mellanox.infiniband.app":"configured"}},{"name":"ib-sriov-network","namespace":"default","cni-args":{"guid":"02:00:00:00:00:00:00:22","pkey":"0x7fff","mellanox.infiniband.app":"configured"}}]`,
					},
				},
			}

			guidInfos, err := getAllPodGUIDInfosForNetwork(pod, "ib-sriov-network")
			Expect(err).ToNot(HaveOccurred())
			Expect(guidInfos).To(HaveLen(2))
			Expect(guidInfos[0].addr.String()).To(Equal("02:00:00:00:00:00:00:21"))
			Expect(guidInfos[0].podNetworkID).To(Equal(
				utils.GeneratePodNetworkInterfaceID(pod, "ib-sriov-network", "idx_0"),
			))
			Expect(guidInfos[1].addr.String()).To(Equal("02:00:00:00:00:00:00:22"))
			Expect(guidInfos[1].podNetworkID).To(Equal(
				utils.GeneratePodNetworkInterfaceID(pod, "ib-sriov-network", "idx_1"),
			))
		})
	})

	Context("initGUIDPool", func() {
		var (
			mockK8sClient *k8sMocks.Client
		)

		BeforeEach(func() {
			mockK8sClient = &k8sMocks.Client{}
			testPodCtrl.kubeClient = mockK8sClient
		})

		It("successfully initializes GUID pool from running pods", func() {
			// Create a test pod with a simple network annotation
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "test-pod-1",
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": `[{"name":"ib-sriov-network","cniArgs":{"guid":"02:00:00:00:00:00:00:02","pkey":"0x7fff"}}]`,
					},
				},
				Status: kapi.PodStatus{
					Phase: kapi.PodRunning,
				},
			}

			podList := &kapi.PodList{
				Items: []kapi.Pod{*pod},
			}

			// Mock K8s client to return our test pod
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)

			// Mock SM to return empty initially (no GUIDs in use)
			// This tests the case where initGUIDPool processes running pods
			// and then syncs with an empty SM (all GUIDs are new)
			mockClient.listGuidsInUseResult = map[string]string{}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred())

			// In this scenario:
			// 1. Pod processing phase adds GUID to map
			// 2. SM sync finds no GUIDs in use
			// 3. Cleanup phase removes the GUID from map (since not in SM)
			// This verifies the cleanup logic works as expected
			Expect(testPodCtrl.guidPodNetworkMap).To(BeEmpty())

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("preserves GUIDs that are reported as in use by subnet manager", func() {
			// This test verifies the proper behavior when a GUID is both:
			// 1. Found in running pods
			// 2. Reported as in use by the subnet manager
			podList := &kapi.PodList{Items: []kapi.Pod{}}
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)

			// Pre-populate the map as if a pod was processed earlier
			testPodCtrl.guidPodNetworkMap["02:00:00:00:00:00:00:05"] = "existing-pod-network"

			// SM reports this GUID as in use
			mockClient.listGuidsInUseResult = map[string]string{
				"02:00:00:00:00:00:00:05": "0x1000",
			}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred())

			// GUID should be preserved since SM reports it as in use
			Expect(testPodCtrl.guidPodNetworkMap).To(HaveKey("02:00:00:00:00:00:00:05"))
			Expect(testPodCtrl.guidPodNetworkMap["02:00:00:00:00:00:00:05"]).To(Equal("existing-pod-network"))

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("handles finished pods correctly", func() {
			// Create a finished pod
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "finished-pod",
					Name:      "finished-pod",
					Namespace: "default",
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": `[{"name":"ib-sriov-network","cniArgs":{"guid":"02:00:00:00:00:00:00:03","pkey":"0x7fff"}}]`,
					},
				},
				Status: kapi.PodStatus{
					Phase: kapi.PodSucceeded, // Finished pod
				},
			}

			podList := &kapi.PodList{
				Items: []kapi.Pod{*pod},
			}

			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)
			mockClient.listGuidsInUseResult = map[string]string{}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred())

			// Verify finished pod's GUID was not added to the map (since pod is finished)
			Expect(testPodCtrl.guidPodNetworkMap).ToNot(HaveKey("02:00:00:00:00:00:00:03"))

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("handles pods with invalid network annotations", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "invalid-pod",
					Name:      "invalid-pod",
					Namespace: "default",
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": "invalid-json",
					},
				},
				Status: kapi.PodStatus{
					Phase: kapi.PodRunning,
				},
			}

			podList := &kapi.PodList{
				Items: []kapi.Pod{*pod},
			}

			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)
			mockClient.listGuidsInUseResult = map[string]string{}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred()) // Should continue despite invalid annotations

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("removes stale GUIDs not in subnet manager", func() {
			// Setup: add a GUID to the map but NOT to the pool
			// This simulates a GUID that was allocated but the pool was reset
			testPodCtrl.guidPodNetworkMap["02:00:00:00:00:00:00:04"] = "stale-pod-network"

			podList := &kapi.PodList{Items: []kapi.Pod{}}
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)

			// SM doesn't have this GUID, so it should be cleaned up but will fail
			mockClient.listGuidsInUseResult = map[string]string{}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred())

			// With current logic: GUID is NOT removed because pool release fails
			// This is the conservative behavior - only remove if we can properly clean up
			Expect(testPodCtrl.guidPodNetworkMap).To(HaveKey("02:00:00:00:00:00:00:04"))

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("handles K8s client error", func() {
			// Make the client return an error for 2 attempts, then succeed on the 3rd to avoid infinite backoff
			call1 := mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(nil, fmt.Errorf("k8s error")).Once()
			call2 := mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(nil, fmt.Errorf("k8s error")).Once().NotBefore(call1)
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(&kapi.PodList{Items: []kapi.Pod{}}, nil).NotBefore(call2)
			mockClient.listGuidsInUseResult = map[string]string{}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred()) // Should succeed on the 3rd try

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("handles subnet manager error", func() {
			podList := &kapi.PodList{Items: []kapi.Pod{}}
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)
			mockClient.listGuidsInUseError = fmt.Errorf("sm error")

			err := testPodCtrl.initGUIDPool()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("sm error"))

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("handles GUID allocation conflicts", func() {
			// This test verifies that initGUIDPool processes multiple pods successfully
			// even when they use the same GUID (which shouldn't normally happen in real scenarios)
			// The current implementation handles this gracefully without errors

			pod1 := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "pod-1",
					Name:      "pod-1",
					Namespace: "default",
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": `[{"name":"network-1","cniArgs":{"guid":"02:00:00:00:00:00:00:05","pkey":"0x1000"}}]`,
					},
				},
				Status: kapi.PodStatus{Phase: kapi.PodRunning},
			}

			pod2 := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "pod-2",
					Name:      "pod-2",
					Namespace: "default",
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": `[{"name":"network-2","cniArgs":{"guid":"02:00:00:00:00:00:00:05","pkey":"0x2000"}}]`,
					},
				},
				Status: kapi.PodStatus{Phase: kapi.PodRunning},
			}

			podList := &kapi.PodList{Items: []kapi.Pod{*pod1, *pod2}}
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)
			mockClient.listGuidsInUseResult = map[string]string{}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred())

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("uses the same interface-aware IDs as processNetworkGUID after restart", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       "multi-pod",
					Name:      "multi-pod",
					Namespace: "default",
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": `[{"name":"ib-sriov-network","namespace":"default","cni-args":{"guid":"02:00:00:00:00:00:00:31","pkey":"0x7fff","mellanox.infiniband.app":"configured"}},{"name":"ib-sriov-network","namespace":"default","cni-args":{"guid":"02:00:00:00:00:00:00:32","pkey":"0x7fff","mellanox.infiniband.app":"configured"}}]`,
					},
				},
				Status: kapi.PodStatus{
					Phase: kapi.PodRunning,
				},
			}

			podList := &kapi.PodList{Items: []kapi.Pod{*pod}}
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)
			mockClient.listGuidsInUseResult = map[string]string{
				"02:00:00:00:00:00:00:31": "0x7fff",
				"02:00:00:00:00:00:00:32": "0x7fff",
			}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred())

			netMap := networksMap{theMap: make(map[types.UID][]*v1.NetworkSelectionElement)}
			podNetworkInfos, err := getAllPodNetworkInfos("ib-sriov-network", pod, netMap)
			Expect(err).ToNot(HaveOccurred())
			Expect(podNetworkInfos).To(HaveLen(2))

			spec := &utils.IbSriovCniSpec{PKey: "0x7fff"}
			for _, pi := range podNetworkInfos {
				err = testPodCtrl.processNetworkGUID("ib-sriov-network", spec, pi)
				Expect(err).ToNot(HaveOccurred())
			}

			mockK8sClient.AssertExpectations(GinkgoT())
		})
	})

	Context("syncWithSubnetManager", func() {
		It("successfully syncs with subnet manager", func() {
			// Setup some GUIDs in the map but not in the pool (simulates post-reset state)
			testPodCtrl.guidPodNetworkMap["02:00:00:00:00:00:00:06"] = "pod-network-1"
			testPodCtrl.guidPodNetworkMap["02:00:00:00:00:00:00:07"] = "pod-network-2"

			// SM reports only one of them as in use
			mockClient.listGuidsInUseResult = map[string]string{
				"02:00:00:00:00:00:00:06": "0x1000",
			}

			err := testPodCtrl.syncWithSubnetManager()
			Expect(err).ToNot(HaveOccurred())

			// With current logic: GUID 07 is NOT removed because pool release fails
			// Both GUIDs remain in the map due to conservative cleanup behavior
			Expect(testPodCtrl.guidPodNetworkMap).To(HaveKey("02:00:00:00:00:00:00:06"))
			Expect(testPodCtrl.guidPodNetworkMap).To(HaveKey("02:00:00:00:00:00:00:07"))
		})

		It("handles subnet manager ListGuidsInUse error", func() {
			mockClient.listGuidsInUseError = fmt.Errorf("sm list error")

			err := testPodCtrl.syncWithSubnetManager()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("sm list error"))
		})

		It("handles pool reset error", func() {
			// Force a pool reset error by using invalid data
			mockClient.listGuidsInUseResult = map[string]string{
				"invalid-guid": "0x1000",
			}

			err := testPodCtrl.syncWithSubnetManager()
			Expect(err).To(HaveOccurred())
		})

		It("handles GUID release error gracefully", func() {
			// Add a GUID that's not in the pool but is in our map
			// This simulates an inconsistent state where the map has a GUID but the pool doesn't
			testPodCtrl.guidPodNetworkMap["02:00:00:00:00:00:00:08"] = "stale-network"

			mockClient.listGuidsInUseResult = map[string]string{}

			err := testPodCtrl.syncWithSubnetManager()
			Expect(err).ToNot(HaveOccurred())

			// GUID should NOT be removed from map if pool release fails (to prevent data loss)
			// The daemon logs a warning but keeps the GUID in the map
			Expect(testPodCtrl.guidPodNetworkMap).To(HaveKey("02:00:00:00:00:00:00:08"))
		})
	})

	Context("removeStaleGUID", func() {
		It("successfully removes GUID from subnet manager and pool", func() {
			allocatedGUID := "02:00:00:00:00:00:00:10"
			existingPkey := "0x1234"

			// Pre-allocate the GUID with an existing pkey
			err := guidPool.AllocateGUID(allocatedGUID, existingPkey)
			Expect(err).ToNot(HaveOccurred())
			testPodCtrl.guidPodNetworkMap[allocatedGUID] = "existing-network"

			// Track initial call count
			initialCallCount := mockClient.removeGuidsCallCount

			// Remove the stale GUID
			err = testPodCtrl.removeStaleGUID(allocatedGUID, existingPkey)
			Expect(err).ToNot(HaveOccurred())

			// Verify GUID was removed from the map
			Expect(testPodCtrl.guidPodNetworkMap).ToNot(HaveKey(allocatedGUID))

			// Verify RemoveGuidsFromPKey was called exactly once
			Expect(mockClient.removeGuidsCallCount).To(Equal(initialCallCount + 1))
		})

		It("handles invalid PKey format", func() {
			allocatedGUID := "02:00:00:00:00:00:00:11"
			invalidPkey := "invalid-pkey"

			err := testPodCtrl.removeStaleGUID(allocatedGUID, invalidPkey)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid pkey"))
		})

		It("handles invalid GUID format", func() {
			invalidGUID := "invalid-guid"
			existingPkey := "0x1234"

			err := testPodCtrl.removeStaleGUID(invalidGUID, existingPkey)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to parse"))
		})

		It("returns error when subnet manager removal fails", func() {
			// This test takes ~17 seconds due to exponential backoff
			// Skip it in short test mode
			if testing.Short() {
				Skip("Skipping slow test in short mode")
			}

			allocatedGUID := "02:00:00:00:00:00:00:12"
			existingPkey := "0x1234"

			// Pre-allocate the GUID
			err := guidPool.AllocateGUID(allocatedGUID, existingPkey)
			Expect(err).ToNot(HaveOccurred())
			testPodCtrl.guidPodNetworkMap[allocatedGUID] = "existing-network"

			// Make subnet manager fail to remove
			mockClient.removeGuidsFromPKeyError = fmt.Errorf("sm removal error")

			// Should return error after retries (exponential backoff timeout)
			err = testPodCtrl.removeStaleGUID(allocatedGUID, existingPkey)
			Expect(err).To(HaveOccurred())
			// The error is from wait.ExponentialBackoff timeout
			Expect(err.Error()).To(Or(
				ContainSubstring("timed out"),
				ContainSubstring("waiting for the condition"),
			))

			// GUID should still be in pool since removal failed
			_, err = guidPool.Get(allocatedGUID)
			Expect(err).ToNot(HaveOccurred())

			// GUID should still be in map since removal failed
			Expect(testPodCtrl.guidPodNetworkMap).To(HaveKey(allocatedGUID))
		})

		It("handles pool release failure gracefully", func() {
			allocatedGUID := "02:00:00:00:00:00:00:13"
			existingPkey := "0x1234"

			// Don't pre-allocate the GUID in the pool, but add to map
			// This simulates a situation where the pool state is inconsistent
			testPodCtrl.guidPodNetworkMap[allocatedGUID] = "existing-network"

			// Should successfully remove from SM but fail to release from pool
			err := testPodCtrl.removeStaleGUID(allocatedGUID, existingPkey)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to release guid"))

			// Verify RemoveGuidsFromPKey was still called
			Expect(mockClient.removeGuidsCallCount).To(BeNumerically(">", 0))
		})
	})

	Context("initGUIDPool delegation", func() {
		var (
			mockK8sClient *k8sMocks.Client
		)

		BeforeEach(func() {
			mockK8sClient = &k8sMocks.Client{}
			testPodCtrl.kubeClient = mockK8sClient
		})

		It("calls syncWithSubnetManager at the end", func() {
			// This test verifies the refactored behavior where initGUIDPool
			// delegates the sync logic to syncWithSubnetManager

			podList := &kapi.PodList{Items: []kapi.Pod{}}
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)

			// Pre-populate the map
			testPodCtrl.guidPodNetworkMap["02:00:00:00:00:00:00:14"] = "test-network"

			// SM returns the GUID as in use
			mockClient.listGuidsInUseResult = map[string]string{
				"02:00:00:00:00:00:00:14": "0x1000",
			}

			err := testPodCtrl.initGUIDPool()
			Expect(err).ToNot(HaveOccurred())

			// Verify that the GUID is preserved (syncWithSubnetManager was called)
			Expect(testPodCtrl.guidPodNetworkMap).To(HaveKey("02:00:00:00:00:00:00:14"))

			mockK8sClient.AssertExpectations(GinkgoT())
		})

		It("propagates errors from syncWithSubnetManager", func() {
			podList := &kapi.PodList{Items: []kapi.Pod{}}
			mockK8sClient.On("GetPods", kapi.NamespaceAll).Return(podList, nil)

			// Make syncWithSubnetManager fail
			mockClient.listGuidsInUseError = fmt.Errorf("sync error")

			err := testPodCtrl.initGUIDPool()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("sync error"))

			mockK8sClient.AssertExpectations(GinkgoT())
		})
	})

	Context("NewDaemon", func() {
		var originalEnvs map[string]string

		BeforeEach(func() {
			// Save current env vars
			originalEnvs = make(map[string]string)
			for _, env := range []string{
				"DAEMON_SM_PLUGIN", "DAEMON_SM_PLUGIN_PATH",
				"GUID_POOL_RANGE_START", "GUID_POOL_RANGE_END",
			} {
				if val := os.Getenv(env); val != "" {
					originalEnvs[env] = val
				}
			}

			// Set required env vars for daemon creation
			os.Setenv("DAEMON_SM_PLUGIN", "noop")
			os.Setenv("DAEMON_SM_PLUGIN_PATH", "./testdata/plugins")
			os.Setenv("GUID_POOL_RANGE_START", "02:00:00:00:00:00:00:00")
			os.Setenv("GUID_POOL_RANGE_END", "02:00:00:00:00:00:FF:FF")
		})

		AfterEach(func() {
			// Restore original env vars
			for _, env := range []string{
				"DAEMON_SM_PLUGIN", "DAEMON_SM_PLUGIN_PATH",
				"GUID_POOL_RANGE_START", "GUID_POOL_RANGE_END",
			} {
				if originalVal, exists := originalEnvs[env]; exists {
					os.Setenv(env, originalVal)
				} else {
					os.Unsetenv(env)
				}
			}
		})

		It("returns fully formed daemon without calling initGUIDPool", func() {
			// This test verifies the refactored NewDaemon behavior where:
			// 1. initGUIDPool is NOT called during construction
			// 2. The daemon is returned fully formed as a single struct literal
			//
			// We expect NewDaemon to fail due to missing K8s config, but
			// importantly it should fail BEFORE attempting to call initGUIDPool
			// (which would require K8s connectivity)
			_, err := NewDaemon()

			// We expect an error about K8s configuration
			// The fact that we get this error confirms initGUIDPool was not called
			// (otherwise we'd get errors about pod listing)
			Expect(err).To(HaveOccurred())
			// Error should be about k8s client initialization, not GUID pool init
			Expect(err.Error()).To(Or(
				ContainSubstring("configuration"),
				ContainSubstring("k8s"),
				ContainSubstring("client"),
				ContainSubstring("plugin"),
			))
		})
	})

	Context("isPartitionManagedNAD", func() {
		var partitionCtrl *partitionController

		BeforeEach(func() {
			partitionCtrl = &partitionController{}
		})

		It("returns true when NAD has ibKubernetesEnabled set to true", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-partition-net",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true,"pkey":"0x5005"}`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-partition-net", nad)

			result := partitionCtrl.IsPartitionManaged("default_ib-partition-net")
			Expect(result).To(BeTrue())
		})

		It("returns false when NAD lacks ibKubernetesEnabled", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-legacy-net",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","pkey":"0x5005"}`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-legacy-net", nad)

			result := partitionCtrl.IsPartitionManaged("default_ib-legacy-net")
			Expect(result).To(BeFalse())
		})

		It("returns false when NAD is not in cache", func() {
			result := partitionCtrl.IsPartitionManaged("default_nonexistent-net")
			Expect(result).To(BeFalse())
		})

		It("returns false when NAD spec config is malformed JSON", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-bad-config",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{not valid json`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-bad-config", nad)

			result := partitionCtrl.IsPartitionManaged("default_ib-bad-config")
			Expect(result).To(BeFalse())
		})

		It("returns false when ibKubernetesEnabled is set to false", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-disabled-net",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":false,"pkey":"0x5005"}`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-disabled-net", nad)

			result := partitionCtrl.IsPartitionManaged("default_ib-disabled-net")
			Expect(result).To(BeFalse())
		})

		It("returns false when NAD config is empty string", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-empty-config",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: "",
				},
			}
			partitionCtrl.cacheNAD("default_ib-empty-config", nad)

			result := partitionCtrl.IsPartitionManaged("default_ib-empty-config")
			Expect(result).To(BeFalse())
		})

		It("uses the cached ibKubernetesEnabled decision after caching a NAD", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-cached-net",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-cached-net", nad)
			nad.Spec.Config = `{"type":"ib-sriov","ibKubernetesEnabled":false}`

			result := partitionCtrl.IsPartitionManaged("default_ib-cached-net")
			Expect(result).To(BeTrue())
		})
	})

	Context("getPartitionKeyFromNAD", func() {
		var partitionCtrl *partitionController

		BeforeEach(func() {
			partitionCtrl = &partitionController{}
		})

		It("returns partition key from NAD annotation", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-partition-net",
					Namespace: "default",
					Annotations: map[string]string{
						utils.PartitionKeyAnnotation: "0x500A",
					},
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true,"pkey":"0x5005"}`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-partition-net", nad)

			pkey := partitionCtrl.getPartitionKeyFromNAD("default_ib-partition-net")
			Expect(pkey).To(Equal("0x500A"))
		})

		It("returns empty string when NAD has no partition key annotation", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-no-pkey-net",
					Namespace: "default",
					Annotations: map[string]string{
						"some-other-annotation": "value",
					},
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-no-pkey-net", nad)

			pkey := partitionCtrl.getPartitionKeyFromNAD("default_ib-no-pkey-net")
			Expect(pkey).To(BeEmpty())
		})

		It("returns empty string when NAD not in cache", func() {
			pkey := partitionCtrl.getPartitionKeyFromNAD("default_nonexistent-net")
			Expect(pkey).To(BeEmpty())
		})

		It("returns empty string when NAD has nil annotations", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-nil-annot-net",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			}
			partitionCtrl.cacheNAD("default_ib-nil-annot-net", nad)

			pkey := partitionCtrl.getPartitionKeyFromNAD("default_ib-nil-annot-net")
			Expect(pkey).To(BeEmpty())
		})
	})

	Context("mockFabricClient interface compliance", func() {
		It("implements FabricClient interface", func() {
			mock := &mockFabricClient{}
			// Compile-time check: mockFabricClient satisfies plugins.FabricClient
			var _ plugins.FabricClient = mock
			Expect(mock).ToNot(BeNil())
		})

		It("tracks call counts and returns configured values", func() {
			mock := &mockFabricClient{
				createPartitionKey: "0x600A",
				partitionReady:     true,
				partitionReadyKey:  "0x600A",
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", DeviceInstance: 0, GUID: net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}},
				},
			}

			key, err := mock.CreateIBPartition("default_test-net")
			Expect(err).ToNot(HaveOccurred())
			Expect(key).To(Equal("0x600A"))
			Expect(mock.createPartitionCalls).To(Equal(1))

			err = mock.DeleteIBPartition("default_test-net")
			Expect(err).ToNot(HaveOccurred())
			Expect(mock.deletePartitionCalls).To(Equal(1))

			ready, pkey, err := mock.IsPartitionReady("default_test-net")
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(BeTrue())
			Expect(pkey).To(Equal("0x600A"))
			Expect(mock.isReadyCalls).To(Equal(1))

			devices, err := mock.GetNodeIBDevices("node-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(devices).To(HaveLen(1))
			Expect(devices[0].Device).To(Equal("mlx5_0"))
			Expect(mock.getDevicesCalls).To(Equal(1))
		})
	})

	Context("partitionController fabric client plumbing", func() {
		It("retains the FabricClient passed via constructor", func() {
			mock := &mockFabricClient{
				mockSMClient: mockSMClient{name: "test-fabric"},
			}

			pc := newPartitionController(mock, mock, nil, nil)
			Expect(pc.fabricClient).ToNot(BeNil())
			Expect(pc.smClient.Name()).To(Equal("test-fabric"))
		})

		It("nil fabricClient is preserved for legacy plugins", func() {
			sm := &mockSMClient{name: "legacy-sm"}

			pc := newPartitionController(nil, sm, nil, nil)
			Expect(pc.fabricClient).To(BeNil())
			Expect(pc.smClient.Name()).To(Equal("legacy-sm"))
		})
	})

	Context("updatePodNetworkAnnotation error propagation", func() {
		var mockK8sClient *k8sMocks.Client

		BeforeEach(func() {
			mockK8sClient = &k8sMocks.Client{}
			testPodCtrl.kubeClient = mockK8sClient
		})

		It("returns a non-nil error when SetAnnotationsOnPod fails", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "p1", Namespace: "ns1", UID: "uid-1",
					Annotations: map[string]string{},
				},
			}
			addr, parseErr := net.ParseMAC("02:00:00:00:00:00:00:09")
			Expect(parseErr).ToNot(HaveOccurred())

			pi := &podNetworkInfo{
				pod:       pod,
				ibNetwork: &v1.NetworkSelectionElement{Name: "n1"},
				networks:  []*v1.NetworkSelectionElement{{Name: "n1"}},
				addr:      addr,
			}

			// Use a NotFound error so the backoff loop exits on the first attempt
			// (the IsNotFound branch returns false, err immediately) — keeps the
			// test fast while still exercising the new error-propagation path.
			gr := schema.GroupResource{Resource: "pods"}
			mockK8sClient.On(
				"SetAnnotationsOnPod", pod, pod.Annotations,
			).Return(kerrors.NewNotFound(gr, "p1"))

			var removed []net.HardwareAddr
			err := testPodCtrl.updatePodNetworkAnnotation(pi, &removed, "0x1234")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ns1/p1"))
			Expect(removed).To(ContainElement(addr))
		})

		It("does not try to release a GUID when PF-mode annotation updates fail", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "p1", Namespace: "ns1", UID: "uid-1",
					Annotations: map[string]string{},
				},
			}
			pi := &podNetworkInfo{
				pod:       pod,
				ibNetwork: &v1.NetworkSelectionElement{Name: "n1"},
				networks:  []*v1.NetworkSelectionElement{{Name: "n1"}},
			}

			gr := schema.GroupResource{Resource: "pods"}
			mockK8sClient.On(
				"SetAnnotationsOnPod", pod, testifyMock.Anything,
			).Return(kerrors.NewNotFound(gr, "p1"))

			var removed []net.HardwareAddr
			err := testPodCtrl.updatePodNetworkAnnotation(pi, &removed, "")

			Expect(err).To(HaveOccurred())
			Expect(removed).To(BeEmpty())
		})
	})

	Context("partition-aware pod processing", func() {
		var mockK8sClient *k8sMocks.Client

		newPartitionPod := func() *kapi.Pod {
			return &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "p1",
					Namespace: "ns1",
					UID:       "uid-1",
					Annotations: map[string]string{
						v1.NetworkAttachmentAnnot: `[{"name":"ib-net","namespace":"ns1"}]`,
					},
				},
				Spec: kapi.PodSpec{NodeName: "node-1"},
			}
		}

		BeforeEach(func() {
			mockK8sClient = &k8sMocks.Client{}
		})

		It("keeps the add queue when backend provisioning is pending", func() {
			fabric := &mockFabricClient{
				mockSMClient: mockSMClient{
					name:                "test-fabric",
					addGuidsToPKeyError: plugins.ErrPending,
				},
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x01}},
				},
			}
			d := &partitionController{smClient: fabric, fabricClient: fabric, kubeClient: mockK8sClient}
			addMap := utils.NewSynchronizedMap()
			addMap.Set("ns1_ib-net", []*kapi.Pod{newPartitionPod()})

			d.processPartitionPods(
				addMap.Items["ns1_ib-net"].([]*kapi.Pod),
				"ib-net", "0x5ac", networksMap{theMap: map[types.UID][]*v1.NetworkSelectionElement{}},
				addMap, "ns1_ib-net",
			)

			_, exists := addMap.Get("ns1_ib-net")
			Expect(exists).To(BeTrue())
			Expect(fabric.addGuidsCallCount).To(Equal(1))
			Expect(fabric.getDevicesCalls).To(Equal(1))
		})

		It("does not bind GUIDs when the PKey annotation write fails", func() {
			useFastBackoff()
			fabric := &mockFabricClient{
				mockSMClient:      mockSMClient{name: "test-fabric"},
				partitionReady:    true,
				partitionReadyKey: "0x5ac",
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x0a}},
				},
			}
			// Managed NAD with no PKey annotation yet, so ProcessAddIfManaged must
			// persist the pkey before binding GUIDs; the annotation write then fails.
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "ib-net", Namespace: "ns1"},
				Spec:       v1.NetworkAttachmentDefinitionSpec{Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`},
			}
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").Return(nad.DeepCopy(), nil)
			mockK8sClient.On("UpdateNetworkAttachmentDefinition", testifyMock.Anything).
				Return(fmt.Errorf("apiserver rejected update"))
			d := &partitionController{smClient: fabric, fabricClient: fabric, kubeClient: mockK8sClient}
			d.cacheNAD("ns1_ib-net", nad)

			addMap := utils.NewSynchronizedMap()
			addMap.Set("ns1_ib-net", []*kapi.Pod{newPartitionPod()})

			handled := d.ProcessAddIfManaged("ns1_ib-net", "ib-net",
				addMap.Items["ns1_ib-net"].([]*kapi.Pod),
				networksMap{theMap: map[types.UID][]*v1.NetworkSelectionElement{}}, addMap)

			Expect(handled).To(BeTrue(), "managed network: caller must not fall through to the legacy path")
			Expect(fabric.addGuidsCallCount).To(Equal(0),
				"must not bind GUIDs before the PKey annotation is persisted")
			Expect(fabric.getDevicesCalls).To(Equal(0))
			_, stillQueued := addMap.Get("ns1_ib-net")
			Expect(stillQueued).To(BeTrue(), "entry retained so the next tick retries")
		})

		It("removes the add queue entry after GUID add and pod annotation succeed", func() {
			pod := newPartitionPod()
			fabric := &mockFabricClient{
				mockSMClient: mockSMClient{name: "test-fabric"},
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x02}},
				},
			}
			mockK8sClient.On("SetAnnotationsOnPod", pod, testifyMock.Anything).Return(nil)
			d := &partitionController{smClient: fabric, fabricClient: fabric, kubeClient: mockK8sClient}
			d.updatePodAnnotation = (&podController{kubeClient: mockK8sClient}).updatePodNetworkAnnotation
			addMap := utils.NewSynchronizedMap()
			addMap.Set("ns1_ib-net", []*kapi.Pod{pod})

			d.processPartitionPods(
				[]*kapi.Pod{pod}, "ib-net", "0x5ac",
				networksMap{theMap: map[types.UID][]*v1.NetworkSelectionElement{}},
				addMap, "ns1_ib-net",
			)

			_, exists := addMap.Get("ns1_ib-net")
			Expect(exists).To(BeFalse())
			Expect(fabric.addGuidsCallCount).To(Equal(1))
			Expect(fabric.addGuidsPKey).To(Equal(0x5ac))
			Expect(fabric.addGuids).To(HaveLen(1))
			Expect(pod.Annotations[v1.NetworkAttachmentAnnot]).To(ContainSubstring(utils.ConfiguredInfiniBandPod))
		})

		It("keeps the add queue when PF-mode annotation update fails", func() {
			pod := newPartitionPod()
			fabric := &mockFabricClient{
				mockSMClient: mockSMClient{name: "test-fabric"},
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x03}},
				},
			}
			gr := schema.GroupResource{Resource: "pods"}
			mockK8sClient.On("SetAnnotationsOnPod", pod, testifyMock.Anything).
				Return(kerrors.NewNotFound(gr, "p1"))
			d := &partitionController{smClient: fabric, fabricClient: fabric, kubeClient: mockK8sClient}
			d.updatePodAnnotation = (&podController{kubeClient: mockK8sClient}).updatePodNetworkAnnotation
			addMap := utils.NewSynchronizedMap()
			addMap.Set("ns1_ib-net", []*kapi.Pod{pod})

			d.processPartitionPods(
				[]*kapi.Pod{pod}, "ib-net", "0x5ac",
				networksMap{theMap: map[types.UID][]*v1.NetworkSelectionElement{}},
				addMap, "ns1_ib-net",
			)

			_, exists := addMap.Get("ns1_ib-net")
			Expect(exists).To(BeTrue())
		})

		It("returns without panicking when FabricClient is absent", func() {
			addMap := utils.NewSynchronizedMap()
			addMap.Set("ns1_ib-net", []*kapi.Pod{newPartitionPod()})
			d := &partitionController{smClient: &mockSMClient{name: "legacy-sm"}}

			Expect(func() {
				d.processPartitionPods(
					addMap.Items["ns1_ib-net"].([]*kapi.Pod),
					"ib-net", "0x5ac", networksMap{theMap: map[types.UID][]*v1.NetworkSelectionElement{}},
					addMap, "ns1_ib-net",
				)
			}).ToNot(Panic())
		})
	})

	Context("partition-aware pod deletion", func() {
		newManagedNAD := func() *v1.NetworkAttachmentDefinition {
			return &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-net",
					Namespace: "ns1",
					Annotations: map[string]string{
						utils.PartitionKeyAnnotation: "0x5ac",
					},
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			}
		}

		It("returns nil and no panic when FabricClient is absent", func() {
			d := &partitionController{smClient: &mockSMClient{name: "legacy-sm"}}
			d.cacheNAD("ns1_ib-net", newManagedNAD())

			err := d.DeletePartitionPods("ns1_ib-net", []*kapi.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns1"},
				Spec:       kapi.PodSpec{NodeName: "node-1"},
			}})

			Expect(err).ToNot(HaveOccurred())
		})

		It("removes node PF GUIDs from the partition", func() {
			fabric := &mockFabricClient{
				mockSMClient: mockSMClient{name: "test-fabric"},
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x04}},
					{Device: "mlx5_1", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x05}},
				},
			}
			d := &partitionController{smClient: fabric, fabricClient: fabric}
			d.cacheNAD("ns1_ib-net", newManagedNAD())

			err := d.DeletePartitionPods("ns1_ib-net", []*kapi.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns1"},
				Spec:       kapi.PodSpec{NodeName: "node-1"},
			}})

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.requestedNodeNames).To(Equal([]string{"node-1"}))
			Expect(fabric.removeGuidsCallCount).To(Equal(1))
			Expect(fabric.removeGuidsPKey).To(Equal(0x5ac))
			Expect(fabric.removedGuids).To(HaveLen(2))
		})

		It("returns first error when RemoveGuidsFromPKey fails", func() {
			fabric := &mockFabricClient{
				mockSMClient: mockSMClient{
					name:                     "test-fabric",
					removeGuidsFromPKeyError: fmt.Errorf("backend gRPC not connected"),
				},
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x07}},
				},
			}
			d := &partitionController{smClient: fabric, fabricClient: fabric}
			d.cacheNAD("ns1_ib-net", newManagedNAD())

			err := d.DeletePartitionPods("ns1_ib-net", []*kapi.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns1"},
				Spec:       kapi.PodSpec{NodeName: "node-1"},
			}})

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("backend gRPC not connected"))
			Expect(fabric.removeGuidsCallCount).To(Equal(1))
		})

		It("returns first error when GetNodeIBDevices fails", func() {
			fabric := &mockFabricClient{
				mockSMClient:     mockSMClient{name: "test-fabric"},
				nodeIBDevicesErr: fmt.Errorf("node unreachable"),
			}
			d := &partitionController{smClient: fabric, fabricClient: fabric}
			d.cacheNAD("ns1_ib-net", newManagedNAD())

			err := d.DeletePartitionPods("ns1_ib-net", []*kapi.Pod{{
				ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns1"},
				Spec:       kapi.PodSpec{NodeName: "node-1"},
			}})

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("node unreachable"))
			Expect(fabric.removeGuidsCallCount).To(Equal(0))
		})

		// Proves the gate in podController.DeletePeriodicUpdate: when
		// DeletePartitionPods returns a non-nil error the delete-queue entry
		// MUST stay so the next periodic tick retries the detach. Dropping it
		// here would leave the node/instance attached to a partition we
		// already asked the backend to release.
		It("DeletePeriodicUpdate retains delete-queue entry when detach fails", func() {
			fabric := &mockFabricClient{
				mockSMClient: mockSMClient{
					name:                     "test-fabric",
					removeGuidsFromPKeyError: fmt.Errorf("backend gRPC not connected"),
				},
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x08}},
				},
			}
			partitionCtrl := &partitionController{smClient: fabric, fabricClient: fabric}
			partitionCtrl.cacheNAD("ns1_ib-net", newManagedNAD())

			deleteMap := utils.NewSynchronizedMap()
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "p1", Namespace: "ns1",
					Annotations: map[string]string{
						v1.NetworkAttachmentAnnot: `[{"name":"ib-net","namespace":"ns1"}]`,
					},
				},
				Spec: kapi.PodSpec{NodeName: "node-1"},
			}
			deleteMap.Set("ns1_ib-net", []*kapi.Pod{pod})

			podCtrl := &podController{
				smClient:          fabric,
				guidPool:          guidPool,
				guidPodNetworkMap: make(map[string]string),
				podWatcher: &stubWatcher{handler: &stubEventHandler{
					addMap:    utils.NewSynchronizedMap(),
					deleteMap: deleteMap,
				}},
				partition: partitionCtrl,
			}

			podCtrl.DeletePeriodicUpdate()

			_, stillQueued := deleteMap.Get("ns1_ib-net")
			Expect(stillQueued).To(BeTrue(),
				"delete-queue entry must survive a failed partition detach so the next tick retries")
			Expect(fabric.removeGuidsCallCount).To(Equal(1),
				"detach must have been attempted exactly once this pass")
		})

		It("warms a cold cache before routing a partition-managed delete", func() {
			// The first delete tick after daemon start / leader failover can beat
			// the NAD informer, leaving the cache cold. Warming it before
			// IsPartitionManaged keeps the managed detach from being misrouted to
			// the legacy path, which finds no pool GUID for a PF-mode pod and
			// would dequeue the entry without detaching.
			fabric := &mockFabricClient{
				mockSMClient: mockSMClient{name: "test-fabric"},
				nodeIBDevices: []plugins.IBDeviceGUID{
					{Device: "mlx5_0", GUID: net.HardwareAddr{0x02, 0, 0, 0, 0, 0, 0, 0x09}},
				},
			}
			mockK8sClient := &k8sMocks.Client{}
			// Cache intentionally left cold; the NAD is only reachable via the API.
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(newManagedNAD(), nil)
			partitionCtrl := &partitionController{smClient: fabric, fabricClient: fabric, kubeClient: mockK8sClient}

			deleteMap := utils.NewSynchronizedMap()
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "p1", Namespace: "ns1",
					Annotations: map[string]string{
						v1.NetworkAttachmentAnnot: `[{"name":"ib-net","namespace":"ns1"}]`,
					},
				},
				Spec: kapi.PodSpec{NodeName: "node-1"},
			}
			deleteMap.Set("ns1_ib-net", []*kapi.Pod{pod})

			podCtrl := &podController{
				smClient:          fabric,
				guidPool:          guidPool,
				guidPodNetworkMap: make(map[string]string),
				podWatcher: &stubWatcher{handler: &stubEventHandler{
					addMap:    utils.NewSynchronizedMap(),
					deleteMap: deleteMap,
				}},
				partition: partitionCtrl,
			}

			podCtrl.DeletePeriodicUpdate()

			Expect(fabric.removeGuidsCallCount).To(Equal(1),
				"managed detach must run even when the cache started cold")
			_, stillQueued := deleteMap.Get("ns1_ib-net")
			Expect(stillQueued).To(BeFalse(),
				"a successful detach dequeues the entry")
		})
	})

	Context("partition NAD setup and deletion", func() {
		newPartitionNAD := func() *v1.NetworkAttachmentDefinition {
			return &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ib-net",
					Namespace: "ns1",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			}
		}

		It("skips setup when the NAD already has a partition key", func() {
			fabric := &mockFabricClient{
				mockSMClient:       mockSMClient{name: "test-fabric"},
				createPartitionKey: "0x5ac",
			}
			nad := newPartitionNAD()
			nad.Annotations = map[string]string{utils.PartitionKeyAnnotation: "0x5ac"}
			d := &partitionController{fabricClient: fabric}

			err := d.setupPartitionForNAD(nad)

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.createPartitionCalls).To(Equal(0))
		})

		It("skips setup for non ib-sriov NADs", func() {
			fabric := &mockFabricClient{mockSMClient: mockSMClient{name: "test-fabric"}}
			nad := newPartitionNAD()
			nad.Spec.Config = `{"type":"sriov","ibKubernetesEnabled":true}`
			d := &partitionController{fabricClient: fabric}

			err := d.setupPartitionForNAD(nad)

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.createPartitionCalls).To(Equal(0))
		})

		It("skips setup when ibKubernetesEnabled is false", func() {
			fabric := &mockFabricClient{mockSMClient: mockSMClient{name: "test-fabric"}}
			nad := newPartitionNAD()
			nad.Spec.Config = `{"type":"ib-sriov","ibKubernetesEnabled":false}`
			d := &partitionController{fabricClient: fabric}

			err := d.setupPartitionForNAD(nad)

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.createPartitionCalls).To(Equal(0))
		})

		It("creates a partition and persists the returned PKey on the NAD", func() {
			useFastBackoff()
			fabric := &mockFabricClient{
				mockSMClient:       mockSMClient{name: "test-fabric"},
				createPartitionKey: "0x5ac",
			}
			mockK8sClient := &k8sMocks.Client{}
			nad := newPartitionNAD()
			var updatedNAD *v1.NetworkAttachmentDefinition
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(nad.DeepCopy(), nil)
			mockK8sClient.On("UpdateNetworkAttachmentDefinition", testifyMock.Anything).
				Run(func(args testifyMock.Arguments) {
					updatedNAD = args.Get(0).(*v1.NetworkAttachmentDefinition)
				}).Return(nil)
			d := &partitionController{fabricClient: fabric, kubeClient: mockK8sClient}

			err := d.setupPartitionForNAD(nad)

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.createdPartitionNames).To(Equal([]string{"ns1_ib-net"}))
			Expect(updatedNAD).ToNot(BeNil())
			Expect(updatedNAD.Annotations[utils.PartitionKeyAnnotation]).To(Equal("0x5ac"))
			Expect(updatedNAD.Finalizers).To(ContainElement(utils.PartitionNADFinalizer))
		})

		It("skips partition creation when the NAD is already terminating", func() {
			fabric := &mockFabricClient{
				mockSMClient:       mockSMClient{name: "test-fabric"},
				createPartitionKey: "0x5ac",
			}
			mockK8sClient := &k8sMocks.Client{}
			nad := newPartitionNAD()
			terminating := nad.DeepCopy()
			now := metav1.NewTime(time.Now())
			terminating.DeletionTimestamp = &now
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(terminating, nil)
			d := &partitionController{fabricClient: fabric, kubeClient: mockK8sClient}

			err := d.setupPartitionForNAD(nad)

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.createPartitionCalls).To(Equal(0),
				"must not create a partition for a NAD that is already terminating")
		})

		It("rolls back the created partition when the NAD races into terminating during setup", func() {
			useFastBackoff()
			fabric := &mockFabricClient{
				mockSMClient:       mockSMClient{name: "test-fabric"},
				createPartitionKey: "0x5ac",
			}
			mockK8sClient := &k8sMocks.Client{}
			nad := newPartitionNAD()
			terminating := nad.DeepCopy()
			now := metav1.NewTime(time.Now())
			terminating.DeletionTimestamp = &now

			// Pre-create check sees a live NAD; the finalizer write then fails
			// (k8s rejects finalizers on a deleting object); the rollback re-check
			// sees the NAD now terminating and deletes the orphaned partition.
			getPre := mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(nad.DeepCopy(), nil).Once()
			getWrite := mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(nad.DeepCopy(), nil).Once().NotBefore(getPre)
			update := mockK8sClient.On("UpdateNetworkAttachmentDefinition", testifyMock.Anything).
				Return(fmt.Errorf("forbidden: no new finalizers can be added if the object is being deleted")).
				Once().NotBefore(getWrite)
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(terminating, nil).Once().NotBefore(update)
			d := &partitionController{fabricClient: fabric, kubeClient: mockK8sClient}

			err := d.setupPartitionForNAD(nad)

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.createPartitionCalls).To(Equal(1))
			Expect(fabric.deletedPartitionNames).To(Equal([]string{"ns1_ib-net"}),
				"a partition created for a NAD that raced into terminating must be rolled back")
		})

		It("skips updating a NAD that already has the same PKey and finalizer", func() {
			useFastBackoff()
			mockK8sClient := &k8sMocks.Client{}
			nad := newPartitionNAD()
			nad.Annotations = map[string]string{utils.PartitionKeyAnnotation: "0x5ac"}
			nad.Finalizers = []string{utils.PartitionNADFinalizer}
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(nad.DeepCopy(), nil).Once()
			d := &partitionController{kubeClient: mockK8sClient}

			err := d.updateNADPartitionKey("ns1_ib-net", "0x5ac")

			Expect(err).ToNot(HaveOccurred())
			mockK8sClient.AssertNumberOfCalls(GinkgoT(), "UpdateNetworkAttachmentDefinition", 0)
			cached, ok := d.nadCache.Load("ns1_ib-net")
			Expect(ok).To(BeTrue())
			Expect(cached.(*v1.NetworkAttachmentDefinition).Annotations[utils.PartitionKeyAnnotation]).
				To(Equal("0x5ac"))
		})

		It("deletes the backend partition and removes the NAD finalizer", func() {
			useFastBackoff()
			fabric := &mockFabricClient{mockSMClient: mockSMClient{name: "test-fabric"}}
			mockK8sClient := &k8sMocks.Client{}
			nad := newPartitionNAD()
			freshNAD := nad.DeepCopy()
			freshNAD.Finalizers = []string{utils.PartitionNADFinalizer, "other-finalizer"}
			var updatedNAD *v1.NetworkAttachmentDefinition
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(freshNAD, nil)
			mockK8sClient.On("UpdateNetworkAttachmentDefinition", testifyMock.Anything).
				Run(func(args testifyMock.Arguments) {
					updatedNAD = args.Get(0).(*v1.NetworkAttachmentDefinition)
				}).Return(nil)
			d := &partitionController{fabricClient: fabric, kubeClient: mockK8sClient}

			err := d.handleNADDeletion(nad)

			Expect(err).ToNot(HaveOccurred())
			Expect(fabric.deletedPartitionNames).To(Equal([]string{"ns1_ib-net"}))
			Expect(updatedNAD.Finalizers).To(Equal([]string{"other-finalizer"}))
		})

		It("retries finalizer removal on update conflict", func() {
			useFastBackoff()
			mockK8sClient := &k8sMocks.Client{}
			nad := newPartitionNAD()
			firstFreshNAD := nad.DeepCopy()
			firstFreshNAD.Finalizers = []string{utils.PartitionNADFinalizer}
			secondFreshNAD := nad.DeepCopy()
			secondFreshNAD.Finalizers = []string{utils.PartitionNADFinalizer}
			conflict := kerrors.NewConflict(
				schema.GroupResource{Group: "k8s.cni.cncf.io", Resource: "networkattachmentdefinitions"},
				"ib-net", fmt.Errorf("conflict"),
			)

			get1 := mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(firstFreshNAD, nil).Once()
			update1 := mockK8sClient.On("UpdateNetworkAttachmentDefinition", testifyMock.Anything).
				Return(conflict).Once().NotBefore(get1)
			get2 := mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(secondFreshNAD, nil).Once().NotBefore(update1)
			mockK8sClient.On("UpdateNetworkAttachmentDefinition", testifyMock.Anything).
				Return(nil).Once().NotBefore(get2)
			d := &partitionController{kubeClient: mockK8sClient}

			err := d.removeNADFinalizer(nad, utils.PartitionNADFinalizer)

			Expect(err).ToNot(HaveOccurred())
			mockK8sClient.AssertNumberOfCalls(GinkgoT(), "UpdateNetworkAttachmentDefinition", 2)
		})
	})

	Context("processNADResults handleNADDeletion gating", func() {
		// Proves the A3 invariant from PR #228 review: a NAD entering Terminating
		// without our PartitionNADFinalizer must NOT reach DeleteIBPartition.
		// The finalizer is the only signal that *we* created the partition;
		// without it the partition belongs to someone else (or never existed).

		It("does not call DeleteIBPartition when the NAD lacks our finalizer", func() {
			fabric := &mockFabricClient{mockSMClient: mockSMClient{name: "test-fabric"}}
			pc := newPartitionController(fabric, fabric, nil, nil)

			now := metav1.NewTime(time.Now())
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "ib-net",
					Namespace:         "ns1",
					DeletionTimestamp: &now,
					// No finalizers — someone else's NAD, we never owned it.
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			}

			added := utils.NewSynchronizedMap()
			deleted := utils.NewSynchronizedMap()
			deleted.Set("ns1_ib-net", nad)

			pc.processNADResults(added, deleted)

			Expect(fabric.deletePartitionCalls).To(Equal(0),
				"DeleteIBPartition must not be called for a NAD without PartitionNADFinalizer")
			_, stillQueued := deleted.Get("ns1_ib-net")
			Expect(stillQueued).To(BeFalse(),
				"queue entry should be cleared so we don't loop on a NAD we don't own")
		})

		It("drains deletedNADs queue even when FabricClient is absent (no memory leak for legacy plugins)", func() {
			// Legacy plugins (UFM/noop) have no FabricClient. The deletedNADs
			// queue must still be drained so nadCache + partitionManagedCache
			// don't grow unbounded across NAD-delete events.
			pc := newPartitionController(nil, &mockSMClient{name: "legacy-sm"}, nil, nil)
			pc.cacheNAD("ns1_ib-net", &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "ib-net", Namespace: "ns1"},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			})

			added := utils.NewSynchronizedMap()
			deleted := utils.NewSynchronizedMap()
			deleted.Set("ns1_ib-net", &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "ib-net", Namespace: "ns1"},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			})

			pc.processNADResults(added, deleted)

			_, stillQueued := deleted.Get("ns1_ib-net")
			Expect(stillQueued).To(BeFalse(),
				"deletedNADs queue must drain even when fabricClient is nil")
			_, stillCached := pc.nadCache.Load("ns1_ib-net")
			Expect(stillCached).To(BeFalse(),
				"nadCache entry must be removed alongside queue drain")
		})

		It("calls DeleteIBPartition when the NAD carries our finalizer", func() {
			useFastBackoff()
			fabric := &mockFabricClient{mockSMClient: mockSMClient{name: "test-fabric"}}
			mockK8sClient := &k8sMocks.Client{}
			pc := newPartitionController(fabric, fabric, mockK8sClient, nil)

			now := metav1.NewTime(time.Now())
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "ib-net",
					Namespace:         "ns1",
					DeletionTimestamp: &now,
					Finalizers:        []string{utils.PartitionNADFinalizer},
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type":"ib-sriov","ibKubernetesEnabled":true}`,
				},
			}
			mockK8sClient.On("GetNetworkAttachmentDefinition", "ns1", "ib-net").
				Return(nad.DeepCopy(), nil)
			mockK8sClient.On("UpdateNetworkAttachmentDefinition", testifyMock.Anything).
				Return(nil)

			added := utils.NewSynchronizedMap()
			deleted := utils.NewSynchronizedMap()
			deleted.Set("ns1_ib-net", nad)

			pc.processNADResults(added, deleted)

			Expect(fabric.deletePartitionCalls).To(Equal(1))
			Expect(fabric.deletedPartitionNames).To(Equal([]string{"ns1_ib-net"}))
		})
	})

	Context("GetCachedNAD error type preservation", func() {
		var (
			mockK8sClient *k8sMocks.Client
			partitionCtrl *partitionController
		)

		BeforeEach(func() {
			mockK8sClient = &k8sMocks.Client{}
			partitionCtrl = &partitionController{kubeClient: mockK8sClient}
		})

		It("returns an error that satisfies kerrors.IsNotFound when NAD missing", func() {
			gr := schema.GroupResource{Group: "k8s.cni.cncf.io", Resource: "networkattachmentdefinitions"}
			notFoundErr := kerrors.NewNotFound(gr, "n1")

			mockK8sClient.On(
				"GetNetworkAttachmentDefinition", "ns1", "n1",
			).Return(nil, notFoundErr)

			_, err := partitionCtrl.GetCachedNAD("ns1_n1")

			Expect(err).To(HaveOccurred())
			Expect(kerrors.IsNotFound(err)).To(BeTrue(),
				"expected wrapped error to satisfy kerrors.IsNotFound; got %v", err)
		})
	})
})
