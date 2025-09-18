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

package utils

import (
	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Utils", func() {
	Context("PodWantsNetwork", func() {
		It("Check pod if pod needs a cni network", func() {
			pod := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: false}}
			Expect(PodWantsNetwork(pod)).To(BeTrue())
		})
	})
	Context("PodScheduled", func() {
		It("Check pod if pod is scheduled", func() {
			pod := &kapi.Pod{Spec: kapi.PodSpec{NodeName: "test"}}
			Expect(PodScheduled(pod)).To(BeTrue())
		})
	})
	Context("HasNetworkAttachmentAnnot", func() {
		It("Check pod if pod is has network attachment annotation", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{v1.NetworkAttachmentAnnot: `[{"name":"test"}]`}}}
			Expect(HasNetworkAttachmentAnnot(pod)).To(BeTrue())
		})
	})
	Context("PodIsRunning", func() {
		It("Check pod if pod is is in running phase", func() {
			pod := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodRunning}}
			Expect(PodIsRunning(pod)).To(BeTrue())
		})
	})
	Context("PodIsFinished", func() {
		It("Check pod if pod is in succeeded phase", func() {
			pod := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodSucceeded}}
			Expect(PodIsFinished(pod)).To(BeTrue())
		})
		It("Check pod if pod is in failed phase", func() {
			pod := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodFailed}}
			Expect(PodIsFinished(pod)).To(BeTrue())
		})
		It("Check pod if pod is not in finished phase", func() {
			pod := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodRunning}}
			Expect(PodIsFinished(pod)).To(BeFalse())
		})
	})
	Context("IsPodNetworkConfiguredWithInfiniBand", func() {
		It("Pod network is InfiniBand configured", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{
				InfiniBandAnnotation: ConfiguredInfiniBandPod}}
			Expect(IsPodNetworkConfiguredWithInfiniBand(network)).To(BeTrue())
		})
		It("Pod network is not InfiniBand configured", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{InfiniBandAnnotation: ""}}
			Expect(IsPodNetworkConfiguredWithInfiniBand(network)).To(BeFalse())
		})
		It("Nil network", func() {
			Expect(IsPodNetworkConfiguredWithInfiniBand(nil)).To(BeFalse())
		})
	})
	Context("GetPodNetworkGUID", func() {
		It("Pod network has guid in CNI args", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{
				"guid": "02:00:00:00:00:00:00:00"}}
			guid, err := GetPodNetworkGUID(network)
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal((*network.CNIArgs)["guid"]))
		})
		It("Pod network has guid in runtime config", func() {
			network := &v1.NetworkSelectionElement{
				CNIArgs: &map[string]interface{}{}, InfinibandGUIDRequest: "02:00:00:00:00:00:00:00"}
			guid, err := GetPodNetworkGUID(network)
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal(network.InfinibandGUIDRequest))
		})
		It("Pod network has guid in runtime config and nil CNI args", func() {
			network := &v1.NetworkSelectionElement{InfinibandGUIDRequest: "02:00:00:00:00:00:00:00"}
			guid, err := GetPodNetworkGUID(network)
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal(network.InfinibandGUIDRequest))
		})
		It("Pod network doesn't have guid", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{}}
			_, err := GetPodNetworkGUID(network)
			Expect(err).To(HaveOccurred())
		})
		It("Pod network doesn't have CNI_ARGS and runtime config guid", func() {
			network := &v1.NetworkSelectionElement{}
			_, err := GetPodNetworkGUID(network)
			Expect(err).To(HaveOccurred())
		})
		It("Pod network is nil", func() {
			_, err := GetPodNetworkGUID(nil)
			Expect(err).To(HaveOccurred())
		})
		It("Pod network has guid as runtime config and CNI args guid", func() {
			network := &v1.NetworkSelectionElement{
				CNIArgs:               &map[string]interface{}{"guid": "02:00:00:00:00:00:00:00"},
				InfinibandGUIDRequest: "02:13:14:15:16:17:18:19"}
			guid, err := GetPodNetworkGUID(network)
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal(network.InfinibandGUIDRequest))
		})
	})
	Context("PodNetworkHasGUID", func() {
		It("Pod network has guid", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{
				"guid": "02:00:00:00:00:00:00:00"}}
			hasGUID := PodNetworkHasGUID(network)
			Expect(hasGUID).To(BeTrue())
		})
		It("Pod network doesn't have guid", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{}}
			hasGUID := PodNetworkHasGUID(network)
			Expect(hasGUID).To(BeFalse())
		})
		It("Pod network doesn't have \"cni-args\"", func() {
			network := &v1.NetworkSelectionElement{}
			hasGUID := PodNetworkHasGUID(network)
			Expect(hasGUID).To(BeFalse())
		})
		It("Pod network has guid as runtime config", func() {
			network := &v1.NetworkSelectionElement{
				CNIArgs: &map[string]interface{}{}, InfinibandGUIDRequest: "02:00:00:00:00:00:00:00"}
			hasGUID := PodNetworkHasGUID(network)
			Expect(hasGUID).To(BeTrue())
		})
		It("Pod network has guid as runtime config, No CNI args", func() {
			network := &v1.NetworkSelectionElement{
				CNIArgs: nil, InfinibandGUIDRequest: "02:00:00:00:00:00:00:00"}
			hasGUID := PodNetworkHasGUID(network)
			Expect(hasGUID).To(BeTrue())
		})
	})
	Context("GetPodNetworkPkey", func() {
		It("Pod network has pkey in CNI args", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{
				"pkey": "0x1234"}}
			pkey, err := GetPodNetworkPkey(network)
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(Equal("0x1234"))
		})
		It("Pod network doesn't have pkey", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{}}
			_, err := GetPodNetworkPkey(network)
			Expect(err).To(HaveOccurred())
		})
		It("Pod network doesn't have CNI_ARGS", func() {
			network := &v1.NetworkSelectionElement{}
			_, err := GetPodNetworkPkey(network)
			Expect(err).To(HaveOccurred())
		})
		It("Pod network is nil", func() {
			_, err := GetPodNetworkPkey(nil)
			Expect(err).To(HaveOccurred())
		})
	})
	Context("SetPodNetworkGUID", func() {
		It("Set guid for network", func() {
			network := &v1.NetworkSelectionElement{}
			guid := "02:00:00:00:00:00:00:00"
			err := SetPodNetworkGUID(network, "02:00:00:00:00:00:00:00", false)
			Expect(err).ToNot(HaveOccurred())
			Expect((*network.CNIArgs)["guid"]).To(Equal(guid))
			Expect(network.InfinibandGUIDRequest).To(BeEmpty())
		})
		It("Set guid for network as runtime config", func() {
			network := &v1.NetworkSelectionElement{}
			guid := "02:00:00:00:00:00:00:00"
			err := SetPodNetworkGUID(network, "02:00:00:00:00:00:00:00", true)
			Expect(err).ToNot(HaveOccurred())
			Expect(network.InfinibandGUIDRequest).To(Equal(guid))
			Expect(network.CNIArgs).To(BeNil())
		})
		It("Set guid for invalid network", func() {
			err := SetPodNetworkGUID(nil, "02:00:00:00:00:00:00:00", true)
			Expect(err).To(HaveOccurred())
			err = SetPodNetworkGUID(nil, "02:00:00:00:00:00:00:00", false)
			Expect(err).To(HaveOccurred())
		})
	})
	Context("GetIbSriovCniFromNetwork", func() {
		It("Get Ib SR-IOV Spec from \"type\" field", func() {
			spec := map[string]interface{}{"type": InfiniBandSriovCni}
			ibSpec, err := GetIbSriovCniFromNetwork(spec)
			Expect(err).ToNot(HaveOccurred())
			Expect(ibSpec.Type).To(Equal(InfiniBandSriovCni))
		})
		It("Get Ib SR-IOV Spec from \"plugins\" field", func() {
			plugins := []*IbSriovCniSpec{{Type: InfiniBandSriovCni}}
			spec := map[string]interface{}{"plugins": plugins}
			ibSpec, err := GetIbSriovCniFromNetwork(spec)
			Expect(err).ToNot(HaveOccurred())
			Expect(ibSpec.Type).To(Equal(InfiniBandSriovCni))
		})
		It("Get Ib SR-IOV Spec from invalid network spec", func() {
			ibSpec, err := GetIbSriovCniFromNetwork(nil)
			Expect(err).To(HaveOccurred())
			Expect(ibSpec).To(BeNil())
		})
		It("Get Ib SR-IOV Spec where \"type\" and \"plugins\" fields not exist", func() {
			spec := map[string]interface{}{}
			ibSpec, err := GetIbSriovCniFromNetwork(spec)
			Expect(err).To(HaveOccurred())
			Expect(ibSpec).To(BeNil())
		})
		It("Get Ib SR-IOV Spec with invalid \"plugins\" field", func() {
			spec := map[string]interface{}{"plugins": "invalid"}
			ibSpec, err := GetIbSriovCniFromNetwork(spec)
			Expect(err).To(HaveOccurred())
			Expect(ibSpec).To(BeNil())
		})
		It("Get Ib SR-IOV Spec where \"ib-sriov-cni\" not in \"plugins\"", func() {
			plugins := []*IbSriovCniSpec{{Type: "test"}}
			spec := map[string]interface{}{"plugins": plugins}
			ibSpec, err := GetIbSriovCniFromNetwork(spec)
			Expect(err).To(HaveOccurred())
			Expect(ibSpec).To(BeNil())
		})
	})
	Context("GetAllPodNetworks", func() {
		It("should return all networks with matching name", func() {
			networks := []*v1.NetworkSelectionElement{
				{Name: "ib-vf-network", InterfaceRequest: "net1"},
				{Name: "ib-vf-network", InterfaceRequest: "net2"},
				{Name: "ib-vf-network-1", InterfaceRequest: "net3"},
			}

			matchingNetworks, err := GetAllPodNetworks(networks, "ib-vf-network")

			Expect(err).ToNot(HaveOccurred())
			Expect(matchingNetworks).To(HaveLen(2))
			Expect(matchingNetworks[0].Name).To(Equal("ib-vf-network"))
			Expect(matchingNetworks[0].InterfaceRequest).To(Equal("net1"))
			Expect(matchingNetworks[1].Name).To(Equal("ib-vf-network"))
			Expect(matchingNetworks[1].InterfaceRequest).To(Equal("net2"))
		})

		It("should return single network when only one matches", func() {
			networks := []*v1.NetworkSelectionElement{
				{Name: "ib-vf-network", InterfaceRequest: "net1"},
				{Name: "ib-vf-network-1", InterfaceRequest: "net2"},
			}

			matchingNetworks, err := GetAllPodNetworks(networks, "ib-vf-network")

			Expect(err).ToNot(HaveOccurred())
			Expect(matchingNetworks).To(HaveLen(1))
			Expect(matchingNetworks[0].Name).To(Equal("ib-vf-network"))
			Expect(matchingNetworks[0].InterfaceRequest).To(Equal("net1"))
		})

		It("should return error when no matching networks found", func() {
			networks := []*v1.NetworkSelectionElement{
				{Name: "ib-vf-network-1", InterfaceRequest: "net1"},
				{Name: "ib-vf-network-2", InterfaceRequest: "net2"},
			}

			matchingNetworks, err := GetAllPodNetworks(networks, "ib-vf-network")

			Expect(err).To(HaveOccurred())
			Expect(matchingNetworks).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("network ib-vf-network not found"))
		})

		It("should handle empty networks list", func() {
			networks := []*v1.NetworkSelectionElement{}

			matchingNetworks, err := GetAllPodNetworks(networks, "ib-vf-network")

			Expect(err).To(HaveOccurred())
			Expect(matchingNetworks).To(BeNil())
		})

		It("should handle nil networks list", func() {
			matchingNetworks, err := GetAllPodNetworks(nil, "ib-vf-network")

			Expect(err).To(HaveOccurred())
			Expect(matchingNetworks).To(BeNil())
		})

		It("should handle multiple networks with different names", func() {
			networks := []*v1.NetworkSelectionElement{
				{Name: "ib-vf-network-1", InterfaceRequest: "net1"},
				{Name: "ib-vf-network-2", InterfaceRequest: "net2"},
				{Name: "ib-vf-network-1", InterfaceRequest: "net3"}, // Same name as first
				{Name: "ib-vf-network-3", InterfaceRequest: "net4"},
			}

			// Test first network name
			matchingNetworks1, err := GetAllPodNetworks(networks, "ib-vf-network-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(matchingNetworks1).To(HaveLen(2))
			Expect(matchingNetworks1[0].InterfaceRequest).To(Equal("net1"))
			Expect(matchingNetworks1[1].InterfaceRequest).To(Equal("net3"))

			// Test second network name
			matchingNetworks2, err := GetAllPodNetworks(networks, "ib-vf-network-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(matchingNetworks2).To(HaveLen(1))
			Expect(matchingNetworks2[0].InterfaceRequest).To(Equal("net2"))

			// Test third network name
			matchingNetworks3, err := GetAllPodNetworks(networks, "ib-vf-network-3")
			Expect(err).ToNot(HaveOccurred())
			Expect(matchingNetworks3).To(HaveLen(1))
			Expect(matchingNetworks3[0].InterfaceRequest).To(Equal("net4"))
		})
	})
	Context("GeneratePodNetworkInterfaceID", func() {
		It("should generate unique ID with interface name", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("a1b2c3d4-e5f6-7890-abcd-ef1234567890"),
					Name: "test-pod",
				},
			}
			networkID := "default_ib-vf-network"
			interfaceName := "net1"

			id := GeneratePodNetworkInterfaceID(pod, networkID, interfaceName)

			expectedID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890_default_ib-vf-network_net1"
			Expect(id).To(Equal(expectedID))
		})

		It("should generate unique ID with different interface names", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("b2c3d4e5-f6g7-8901-bcde-f23456789012"),
					Name: "test-pod",
				},
			}
			networkID := "default_ib-vf-network"

			id1 := GeneratePodNetworkInterfaceID(pod, networkID, "net1")
			id2 := GeneratePodNetworkInterfaceID(pod, networkID, "net2")

			Expect(id1).ToNot(Equal(id2))
			Expect(id1).To(Equal("b2c3d4e5-f6g7-8901-bcde-f23456789012_default_ib-vf-network_net1"))
			Expect(id2).To(Equal("b2c3d4e5-f6g7-8901-bcde-f23456789012_default_ib-vf-network_net2"))
		})

		It("should generate unique ID with same interface name but different pods", func() {
			pod1 := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("c3d4e5f6-g7h8-9012-cdef-345678901234"),
					Name: "pod1",
				},
			}
			pod2 := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("d4e5f6g7-h8i9-0123-def0-456789012345"),
					Name: "pod2",
				},
			}
			networkID := "default_ib-vf-network"
			interfaceName := "net1"

			id1 := GeneratePodNetworkInterfaceID(pod1, networkID, interfaceName)
			id2 := GeneratePodNetworkInterfaceID(pod2, networkID, interfaceName)

			Expect(id1).ToNot(Equal(id2))
			Expect(id1).To(Equal("c3d4e5f6-g7h8-9012-cdef-345678901234_default_ib-vf-network_net1"))
			Expect(id2).To(Equal("d4e5f6g7-h8i9-0123-def0-456789012345_default_ib-vf-network_net1"))
		})

		It("should handle special characters in interface name", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("f6g7h8i9-j0k1-2345-f012-678901234567"),
					Name: "test-pod",
				},
			}
			networkID := "default_ib-vf-network"
			interfaceName := "net-1_interface"

			id := GeneratePodNetworkInterfaceID(pod, networkID, interfaceName)

			expectedID := "f6g7h8i9-j0k1-2345-f012-678901234567_default_ib-vf-network_net-1_interface"
			Expect(id).To(Equal(expectedID))
		})

		It("should handle interface index names generated by daemon", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("g7h8i9j0-k1l2-3456-0123-789012345678"),
					Name: "test-pod",
				},
			}
			networkID := "default_ib-vf-network"
			interfaceName := "idx_0" // Generated by daemon when InterfaceRequest is empty

			id := GeneratePodNetworkInterfaceID(pod, networkID, interfaceName)

			// The daemon generates idx_%d when InterfaceRequest is empty
			// This utility function receives the generated name
			expectedID := "g7h8i9j0-k1l2-3456-0123-789012345678_default_ib-vf-network_idx_0"
			Expect(id).To(Equal(expectedID))
		})

		It("should generate different IDs for different interface indices", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("h8i9j0k1-l2m3-4567-1234-890123456789"),
					Name: "test-pod",
				},
			}
			networkID := "default_ib-vf-network"

			// Test multiple interfaces with same network but different indices
			id0 := GeneratePodNetworkInterfaceID(pod, networkID, "idx_0")
			id1 := GeneratePodNetworkInterfaceID(pod, networkID, "idx_1")
			id2 := GeneratePodNetworkInterfaceID(pod, networkID, "idx_2")

			Expect(id0).To(Equal("h8i9j0k1-l2m3-4567-1234-890123456789_default_ib-vf-network_idx_0"))
			Expect(id1).To(Equal("h8i9j0k1-l2m3-4567-1234-890123456789_default_ib-vf-network_idx_1"))
			Expect(id2).To(Equal("h8i9j0k1-l2m3-4567-1234-890123456789_default_ib-vf-network_idx_2"))

			// All should be different
			Expect(id0).ToNot(Equal(id1))
			Expect(id1).ToNot(Equal(id2))
			Expect(id0).ToNot(Equal(id2))
		})

		It("should handle mixed interface names and indices", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("i9j0k1l2-m3n4-5678-2345-901234567890"),
					Name: "test-pod",
				},
			}
			networkID := "default_ib-vf-network"

			// Mix of named interfaces and index-based interfaces
			// Note: idx_0, idx_1, etc. are generated by the daemon processing loop
			// The utility function receives the generated names, not empty strings
			idNamed := GeneratePodNetworkInterfaceID(pod, networkID, "net1")
			idIndex0 := GeneratePodNetworkInterfaceID(pod, networkID, "idx_0")
			idIndex1 := GeneratePodNetworkInterfaceID(pod, networkID, "idx_1")

			Expect(idNamed).To(Equal("i9j0k1l2-m3n4-5678-2345-901234567890_default_ib-vf-network_net1"))
			Expect(idIndex0).To(Equal("i9j0k1l2-m3n4-5678-2345-901234567890_default_ib-vf-network_idx_0"))
			Expect(idIndex1).To(Equal("i9j0k1l2-m3n4-5678-2345-901234567890_default_ib-vf-network_idx_1"))

			// All should be different
			Expect(idNamed).ToNot(Equal(idIndex0))
			Expect(idIndex0).ToNot(Equal(idIndex1))
		})

		It("should demonstrate the correct workflow for multiple interfaces with same network name", func() {
			pod := &kapi.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:  types.UID("j0k1l2m3-n4o5-6789-3456-012345678901"),
					Name: "test-pod",
				},
			}
			networkID := "default_ib-vf-network"

			// This simulates what happens in the daemon processing loop:
			// 1. Initial networks have empty InterfaceRequest
			// 2. Daemon generates idx_0, idx_1, etc. for empty names
			// 3. GeneratePodNetworkInterfaceID is called with the generated names

			// Simulate the daemon's idx_%d generation
			interfaceNames := []string{"idx_0", "idx_1", "idx_2"}
			generatedIDs := make([]string, len(interfaceNames))

			for i, interfaceName := range interfaceNames {
				generatedIDs[i] = GeneratePodNetworkInterfaceID(pod, networkID, interfaceName)
			}

			// Verify all IDs are unique
			Expect(generatedIDs[0]).To(Equal("j0k1l2m3-n4o5-6789-3456-012345678901_default_ib-vf-network_idx_0"))
			Expect(generatedIDs[1]).To(Equal("j0k1l2m3-n4o5-6789-3456-012345678901_default_ib-vf-network_idx_1"))
			Expect(generatedIDs[2]).To(Equal("j0k1l2m3-n4o5-6789-3456-012345678901_default_ib-vf-network_idx_2"))

			// All should be different
			Expect(generatedIDs[0]).ToNot(Equal(generatedIDs[1]))
			Expect(generatedIDs[1]).ToNot(Equal(generatedIDs[2]))
			Expect(generatedIDs[0]).ToNot(Equal(generatedIDs[2]))
		})
	})
})
