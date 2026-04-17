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

package main

import (
	"context"
	"net"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/NVIDIA/ncx-infra-controller-rest/sdk/simple"
	"github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

// ── Mock implementation ──

type mockNicoClient struct {
	// Partition operations
	createPartitionResult   *simple.InfinibandPartition
	createPartitionError    *simple.ApiError
	getPartitionsResult     []simple.InfinibandPartition
	getPartitionsPagination *standard.PaginationResponse
	getPartitionsError      *simple.ApiError
	getPartitionResult      *simple.InfinibandPartition
	getPartitionError       *simple.ApiError
	deletePartitionError    *simple.ApiError

	// Instance operations
	getInstancesResult  []standard.Instance
	getInstancesError   *simple.ApiError
	getInstanceResult   *standard.Instance
	getInstanceError    *simple.ApiError
	updateInstanceResult *standard.Instance
	updateInstanceError  *simple.ApiError

	// Call tracking
	createPartitionCalls int
	deletePartitionCalls int
	updateInstanceCalls  int
}

func (m *mockNicoClient) GetInfinibandPartitions(_ context.Context, _ *simple.PaginationFilter) ([]simple.InfinibandPartition, *standard.PaginationResponse, *simple.ApiError) {
	return m.getPartitionsResult, m.getPartitionsPagination, m.getPartitionsError
}

func (m *mockNicoClient) GetInfinibandPartition(_ context.Context, _ string) (*simple.InfinibandPartition, *simple.ApiError) {
	return m.getPartitionResult, m.getPartitionError
}

func (m *mockNicoClient) CreateInfinibandPartition(_ context.Context, _ simple.InfinibandPartitionCreateRequest) (*simple.InfinibandPartition, *simple.ApiError) {
	m.createPartitionCalls++
	return m.createPartitionResult, m.createPartitionError
}

func (m *mockNicoClient) DeleteInfinibandPartition(_ context.Context, _ string) *simple.ApiError {
	m.deletePartitionCalls++
	return m.deletePartitionError
}

func (m *mockNicoClient) GetInstances(_ context.Context, _ *simple.InstanceFilter, _ *simple.PaginationFilter) ([]standard.Instance, *standard.PaginationResponse, *simple.ApiError) {
	return m.getInstancesResult, nil, m.getInstancesError
}

func (m *mockNicoClient) GetInstance(_ context.Context, _ string) (*standard.Instance, *simple.ApiError) {
	return m.getInstanceResult, m.getInstanceError
}

func (m *mockNicoClient) UpdateInstance(_ context.Context, _ string, _ simple.InstanceUpdateRequest) (*standard.Instance, *simple.ApiError) {
	m.updateInstanceCalls++
	return m.updateInstanceResult, m.updateInstanceError
}

// ── Helpers ──

func newTestPlugin(client nicoClientAPI) *nicoPlugin {
	return &nicoPlugin{
		PluginName:         "nico",
		SpecVersion:        "1.0",
		client:             client,
		defaultPartitionID: "default-partition-id",
		nodeLocks:          make(map[string]*sync.Mutex),
	}
}

func ptrString(s string) *string { return &s }

func makeIBInterface(guid, device string, deviceInstance int32, isPhysical bool, partitionID string, status standard.InfiniBandInterfaceStatus) standard.InfiniBandInterface {
	iface := standard.InfiniBandInterface{}
	iface.SetDevice(device)
	iface.SetDeviceInstance(deviceInstance)
	iface.SetIsPhysical(isPhysical)
	iface.SetGuid(guid)
	iface.SetPartitionId(partitionID)
	iface.SetStatus(status)
	return iface
}

func makeInstance(id, name string, ibInterfaces []standard.InfiniBandInterface) standard.Instance {
	inst := standard.Instance{}
	inst.SetId(id)
	inst.SetName(name)
	if ibInterfaces != nil {
		inst.SetInfinibandInterfaces(ibInterfaces)
	}
	return inst
}

// ── Tests ──

var _ = Describe("Nico Plugin", func() {

	Context("Name and Spec", func() {
		It("returns the correct plugin name", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			Expect(plugin.Name()).To(Equal("nico"))
		})

		It("returns the correct spec version", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			Expect(plugin.Spec()).To(Equal("1.0"))
		})
	})

	Context("Validate", func() {
		It("succeeds when GetInfinibandPartitions returns ok", func() {
			mockClient := &mockNicoClient{
				getPartitionsResult: []simple.InfinibandPartition{},
			}
			plugin := newTestPlugin(mockClient)
			err := plugin.Validate()
			Expect(err).ToNot(HaveOccurred())
		})

		It("fails when client is nil", func() {
			plugin := &nicoPlugin{
				PluginName:  "nico",
				SpecVersion: "1.0",
				client:      nil,
				nodeLocks:   make(map[string]*sync.Mutex),
			}
			err := plugin.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("nico client not initialized"))
		})

		It("fails when API returns error", func() {
			mockClient := &mockNicoClient{
				getPartitionsError: &simple.ApiError{Code: 500, Message: "server error"},
			}
			plugin := newTestPlugin(mockClient)
			err := plugin.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("code 500"))
		})
	})

	Context("ListGuidsInUse", func() {
		It("returns empty map (PF GUIDs not pool-managed)", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			guids, err := plugin.ListGuidsInUse()
			Expect(err).ToNot(HaveOccurred())
			Expect(guids).To(Equal(map[string]string{}))
		})
	})

	Context("CreateIBPartition", func() {
		It("creates partition and returns PKey", func() {
			pkey := "0x1234"
			mockClient := &mockNicoClient{
				createPartitionResult: &simple.InfinibandPartition{
					ID:           "part-id-1",
					Name:         "test-partition",
					PartitionKey: &pkey,
				},
			}
			plugin := newTestPlugin(mockClient)
			result, err := plugin.CreateIBPartition("test-partition")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("0x1234"))
			Expect(mockClient.createPartitionCalls).To(Equal(1))
		})

		It("handles 409 conflict by looking up existing", func() {
			pkey := "0x5678"
			mockClient := &mockNicoClient{
				createPartitionError: &simple.ApiError{Code: 409, Message: "conflict"},
				getPartitionsResult: []simple.InfinibandPartition{
					{
						ID:           "existing-id",
						Name:         "test-partition",
						PartitionKey: &pkey,
						Status:       "Ready",
					},
				},
			}
			plugin := newTestPlugin(mockClient)
			result, err := plugin.CreateIBPartition("test-partition")
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal("0x5678"))
		})

		It("rejects empty partition name", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			_, err := plugin.CreateIBPartition("")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("partition name cannot be empty"))
		})

		It("returns error on API failure", func() {
			mockClient := &mockNicoClient{
				createPartitionError: &simple.ApiError{Code: 500, Message: "internal error"},
			}
			plugin := newTestPlugin(mockClient)
			_, err := plugin.CreateIBPartition("test-partition")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("code 500"))
		})
	})

	Context("DeleteIBPartition", func() {
		It("deletes partition by name (cached ID)", func() {
			mockClient := &mockNicoClient{}
			plugin := newTestPlugin(mockClient)
			plugin.nameToPartitionCache.Store("my-partition", "part-id-1")

			err := plugin.DeleteIBPartition("my-partition")
			Expect(err).ToNot(HaveOccurred())
			Expect(mockClient.deletePartitionCalls).To(Equal(1))

			// Cache entry should be removed
			_, ok := plugin.nameToPartitionCache.Load("my-partition")
			Expect(ok).To(BeFalse())
		})

		It("handles 404 (already deleted) silently", func() {
			mockClient := &mockNicoClient{
				deletePartitionError: &simple.ApiError{Code: 404, Message: "not found"},
			}
			plugin := newTestPlugin(mockClient)
			plugin.nameToPartitionCache.Store("my-partition", "part-id-1")

			err := plugin.DeleteIBPartition("my-partition")
			Expect(err).ToNot(HaveOccurred())
		})

		It("idempotent when partition not found", func() {
			mockClient := &mockNicoClient{
				getPartitionsResult: []simple.InfinibandPartition{},
			}
			plugin := newTestPlugin(mockClient)

			err := plugin.DeleteIBPartition("nonexistent")
			Expect(err).ToNot(HaveOccurred())
			Expect(mockClient.deletePartitionCalls).To(Equal(0))
		})

		It("rejects empty name", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			err := plugin.DeleteIBPartition("")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("partition name cannot be empty"))
		})
	})

	Context("IsPartitionReady", func() {
		It("returns true + PKey when partition status is Ready", func() {
			pkey := "0xAAAA"
			mockClient := &mockNicoClient{
				getPartitionResult: &simple.InfinibandPartition{
					ID:           "part-id-1",
					Name:         "test-partition",
					PartitionKey: &pkey,
					Status:       "Ready",
				},
			}
			plugin := newTestPlugin(mockClient)
			plugin.nameToPartitionCache.Store("test-partition", "part-id-1")

			ready, resultPKey, err := plugin.IsPartitionReady("test-partition")
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(BeTrue())
			Expect(resultPKey).To(Equal("0xAAAA"))
		})

		It("returns false when partition status is not Ready", func() {
			mockClient := &mockNicoClient{
				getPartitionResult: &simple.InfinibandPartition{
					ID:     "part-id-1",
					Name:   "test-partition",
					Status: "Provisioning",
				},
			}
			plugin := newTestPlugin(mockClient)
			plugin.nameToPartitionCache.Store("test-partition", "part-id-1")

			ready, _, err := plugin.IsPartitionReady("test-partition")
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(BeFalse())
		})

		It("returns error when partition not found", func() {
			mockClient := &mockNicoClient{
				getPartitionsResult: []simple.InfinibandPartition{},
			}
			plugin := newTestPlugin(mockClient)

			_, _, err := plugin.IsPartitionReady("missing-partition")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("GetNodeIBDevices", func() {
		It("returns PF devices with hardware GUIDs", func() {
			ibInterfaces := []standard.InfiniBandInterface{
				makeIBInterface("11:22:33:44:55:66:77:88", "mlx5_0", 0, true, "part-1", standard.INFINIBANDINTERFACESTATUS_READY),
				makeIBInterface("aa:bb:cc:dd:ee:ff:00:11", "mlx5_1", 1, true, "part-1", standard.INFINIBANDINTERFACESTATUS_READY),
			}
			inst := makeInstance("inst-1", "node-1", ibInterfaces)

			mockClient := &mockNicoClient{
				getInstancesResult: []standard.Instance{inst},
			}
			plugin := newTestPlugin(mockClient)

			devices, err := plugin.GetNodeIBDevices("node-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(devices).To(HaveLen(2))
			Expect(devices[0].Device).To(Equal("mlx5_0"))
			Expect(devices[0].DeviceInstance).To(Equal(0))
			Expect(devices[1].Device).To(Equal("mlx5_1"))
			Expect(devices[1].DeviceInstance).To(Equal(1))
		})

		It("returns cached result on second call", func() {
			ibInterfaces := []standard.InfiniBandInterface{
				makeIBInterface("11:22:33:44:55:66:77:88", "mlx5_0", 0, true, "part-1", standard.INFINIBANDINTERFACESTATUS_READY),
			}
			inst := makeInstance("inst-1", "node-1", ibInterfaces)

			mockClient := &mockNicoClient{
				getInstancesResult: []standard.Instance{inst},
			}
			plugin := newTestPlugin(mockClient)

			devices1, err1 := plugin.GetNodeIBDevices("node-1")
			Expect(err1).ToNot(HaveOccurred())

			// Clear mock results to prove cache is used
			mockClient.getInstancesResult = nil
			mockClient.getInstancesError = &simple.ApiError{Code: 500, Message: "should not be called"}

			devices2, err2 := plugin.GetNodeIBDevices("node-1")
			Expect(err2).ToNot(HaveOccurred())
			Expect(devices2).To(Equal(devices1))
		})

		It("returns error when node has no IB interfaces", func() {
			inst := makeInstance("inst-1", "node-1", []standard.InfiniBandInterface{})

			mockClient := &mockNicoClient{
				getInstancesResult: []standard.Instance{inst},
			}
			plugin := newTestPlugin(mockClient)

			_, err := plugin.GetNodeIBDevices("node-1")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no InfiniBand interfaces"))
		})

		It("populates guidToNodeCache for GUID-to-node resolution", func() {
			ibInterfaces := []standard.InfiniBandInterface{
				makeIBInterface("11:22:33:44:55:66:77:88", "mlx5_0", 0, true, "part-1", standard.INFINIBANDINTERFACESTATUS_READY),
			}
			inst := makeInstance("inst-1", "node-1", ibInterfaces)

			mockClient := &mockNicoClient{
				getInstancesResult: []standard.Instance{inst},
			}
			plugin := newTestPlugin(mockClient)

			_, err := plugin.GetNodeIBDevices("node-1")
			Expect(err).ToNot(HaveOccurred())

			nodeVal, found := plugin.guidToNodeCache.Load(strings.ToLower("11:22:33:44:55:66:77:88"))
			Expect(found).To(BeTrue())
			Expect(nodeVal.(string)).To(Equal("node-1"))
		})
	})

	Context("AddGuidsToPKey", func() {
		It("returns nil for empty guids", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			err := plugin.AddGuidsToPKey(0x1234, []net.HardwareAddr{})
			Expect(err).ToNot(HaveOccurred())
		})

		It("returns error when pkey cache is cold AND backend has no matching partition", func() {
			// With the backend-fallback resolver, a cold cache is no longer
			// a hard error — we only fail if the backend also has nothing.
			plugin := newTestPlugin(&mockNicoClient{})
			guid, _ := net.ParseMAC("11:22:33:44:55:66:77:88")

			err := plugin.AddGuidsToPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no partition found for pkey"))
		})

		It("resolves pkey via backend fallback when cache is cold", func() {
			// Simulate daemon restart: pkeyToPartitionCache is empty but a
			// matching partition exists on the backend. Resolver should
			// populate the cache and proceed without returning an error.
			pkeyStr := "0x1234"
			partitionID := "part-id-1"
			partitionName := "test-partition"
			mockClient := &mockNicoClient{
				getPartitionsResult: []simple.InfinibandPartition{
					{
						ID:           partitionID,
						Name:         partitionName,
						PartitionKey: &pkeyStr,
					},
				},
			}
			plugin := newTestPlugin(mockClient)
			guid, _ := net.ParseMAC("11:22:33:44:55:66:77:88")

			// guidToNodeCache is empty — call fails at guid lookup, not at
			// pkey lookup. That proves the resolver ran.
			err := plugin.AddGuidsToPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("none of the"))

			// Cache should now be populated.
			val, ok := plugin.pkeyToPartitionCache.Load(pkeyStr)
			Expect(ok).To(BeTrue())
			Expect(val.(string)).To(Equal(partitionID))
		})

		It("returns error when GUIDs not in guidToNodeCache", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			plugin.pkeyToPartitionCache.Store("0x1234", "part-id-1")

			guid, _ := net.ParseMAC("11:22:33:44:55:66:77:88")
			err := plugin.AddGuidsToPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("none of the"))
		})

		It("spawns pending op and returns ErrPending on first call", func() {
			ibInterfaces := []standard.InfiniBandInterface{
				makeIBInterface("11:22:33:44:55:66:77:88", "mlx5_0", 0, true, "part-1", standard.INFINIBANDINTERFACESTATUS_READY),
			}
			inst := makeInstance("inst-1", "node-1", ibInterfaces)

			mockClient := &mockNicoClient{
				getInstancesResult:   []standard.Instance{inst},
				updateInstanceResult: &inst,
				getInstanceResult:    &inst,
			}
			plugin := newTestPlugin(mockClient)
			plugin.pkeyToPartitionCache.Store("0x1234", "part-id-1")
			plugin.guidToNodeCache.Store("11:22:33:44:55:66:77:88", "node-1")

			guid, _ := net.ParseMAC("11:22:33:44:55:66:77:88")
			err := plugin.AddGuidsToPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).To(Equal(plugins.ErrPending))
		})
	})

	Context("RemoveGuidsFromPKey", func() {
		It("returns nil for empty guids", func() {
			plugin := newTestPlugin(&mockNicoClient{})
			err := plugin.RemoveGuidsFromPKey(0x1234, []net.HardwareAddr{})
			Expect(err).ToNot(HaveOccurred())
		})

		It("clears pendingByNode for affected nodes", func() {
			ibInterfaces := []standard.InfiniBandInterface{
				makeIBInterface("11:22:33:44:55:66:77:88", "mlx5_0", 0, true, "default-partition-id", standard.INFINIBANDINTERFACESTATUS_READY),
			}
			inst := makeInstance("inst-1", "node-1", ibInterfaces)

			mockClient := &mockNicoClient{
				getInstancesResult:   []standard.Instance{inst},
				updateInstanceResult: &inst,
			}
			plugin := newTestPlugin(mockClient)
			plugin.guidToNodeCache.Store("11:22:33:44:55:66:77:88", "node-1")
			plugin.pendingByNode.Store("node-1", &pendingOp{partitionID: "part-1", started: true})

			guid, _ := net.ParseMAC("11:22:33:44:55:66:77:88")
			err := plugin.RemoveGuidsFromPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).ToNot(HaveOccurred())

			_, found := plugin.pendingByNode.Load("node-1")
			Expect(found).To(BeFalse())
		})
	})
})
