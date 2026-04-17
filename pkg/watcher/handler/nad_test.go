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
	"time"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("NAD Event Handler", func() {
	var nadHandler ResourceEventHandler

	BeforeEach(func() {
		nadHandler = NewNADEventHandler()
	})

	Describe("GetResourceObject", func() {
		It("should return NetworkAttachmentDefinition resource object", func() {
			resource := nadHandler.GetResourceObject()
			Expect(resource).ToNot(BeNil())

			nad, ok := resource.(*v1.NetworkAttachmentDefinition)
			Expect(ok).To(BeTrue())
			Expect(nad.Kind).To(Equal("NetworkAttachmentDefinition"))
			Expect(nad.APIVersion).To(Equal("k8s.cni.cncf.io/v1"))
		})
	})

	Describe("OnAdd", func() {
		Context("with InfiniBand SR-IOV NAD", func() {
			It("should process the NAD add event", func() {
				nad := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-ib-network",
						Namespace: "default",
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
					},
				}

				nadHandler.OnAdd(nad, false)

				addedNADs, _ := nadHandler.GetResults()
				networkID := "default_test-ib-network"

				result, exists := addedNADs.Get(networkID)
				Expect(exists).To(BeTrue())
				Expect(result).To(Equal(nad))
			})
		})

		Context("with non-InfiniBand NAD", func() {
			It("should ignore the NAD add event", func() {
				nad := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-other-network",
						Namespace: "default",
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "sriov"}`,
					},
				}

				nadHandler.OnAdd(nad, false)

				addedNADs, _ := nadHandler.GetResults()
				networkID := "default_test-other-network"

				_, exists := addedNADs.Get(networkID)
				Expect(exists).To(BeFalse())
			})
		})
	})

	Describe("OnUpdate", func() {
		Context("when NAD enters terminating state (DeletionTimestamp set)", func() {
			It("should add InfiniBand NAD to deletedNADs queue", func() {
				oldNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-ib-network",
						Namespace: "default",
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
					},
				}

				now := metav1.NewTime(time.Now())
				newNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-ib-network",
						Namespace:         "default",
						DeletionTimestamp: &now,
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
					},
				}

				nadHandler.OnUpdate(oldNAD, newNAD)

				_, deletedNADs := nadHandler.GetResults()
				networkID := "default_test-ib-network"

				result, exists := deletedNADs.Get(networkID)
				Expect(exists).To(BeTrue())
				Expect(result).To(Equal(newNAD))
			})

			It("should not add non-InfiniBand NAD to deletedNADs queue", func() {
				oldNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-other-network",
						Namespace: "default",
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "sriov"}`,
					},
				}

				now := metav1.NewTime(time.Now())
				newNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-other-network",
						Namespace:         "default",
						DeletionTimestamp: &now,
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "sriov"}`,
					},
				}

				nadHandler.OnUpdate(oldNAD, newNAD)

				_, deletedNADs := nadHandler.GetResults()
				networkID := "default_test-other-network"

				_, exists := deletedNADs.Get(networkID)
				Expect(exists).To(BeFalse())
			})
		})

		Context("when NAD update is not a termination", func() {
			It("should ignore updates where DeletionTimestamp is not set", func() {
				oldNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-ib-network",
						Namespace: "default",
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
					},
				}

				newNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-ib-network",
						Namespace: "default",
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "ib-sriov", "pkey": "0x8fff"}`,
					},
				}

				nadHandler.OnUpdate(oldNAD, newNAD)

				_, deletedNADs := nadHandler.GetResults()
				networkID := "default_test-ib-network"

				_, exists := deletedNADs.Get(networkID)
				Expect(exists).To(BeFalse())
			})

			It("should ignore updates where DeletionTimestamp was already set", func() {
				now := metav1.NewTime(time.Now())
				oldNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-ib-network",
						Namespace:         "default",
						DeletionTimestamp: &now,
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
					},
				}

				newNAD := &v1.NetworkAttachmentDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-ib-network",
						Namespace:         "default",
						DeletionTimestamp: &now,
					},
					Spec: v1.NetworkAttachmentDefinitionSpec{
						Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
					},
				}

				nadHandler.OnUpdate(oldNAD, newNAD)

				_, deletedNADs := nadHandler.GetResults()
				networkID := "default_test-ib-network"

				_, exists := deletedNADs.Get(networkID)
				Expect(exists).To(BeFalse())
			})
		})
	})

	Describe("OnDelete", func() {
		It("should remove NAD from cache", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ib-network",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
				},
			}

			// Add the NAD first so it's in the cache
			nadHandler.OnAdd(nad, false)

			nadHandlerImpl := nadHandler.(*NADEventHandler)
			cachedNAD, exists := nadHandlerImpl.GetNADFromCache("default_test-ib-network")
			Expect(exists).To(BeTrue())
			Expect(cachedNAD).To(Equal(nad))

			// Delete should clean up the cache entry
			nadHandler.OnDelete(nad)

			_, exists = nadHandlerImpl.GetNADFromCache("default_test-ib-network")
			Expect(exists).To(BeFalse())
		})

		It("should handle unexpected object type gracefully", func() {
			// Passing a non-NAD object should not panic
			nadHandler.OnDelete("not-a-nad-object")
		})
	})

	Describe("GetResults", func() {
		It("should return both addedNADs and deletedNADs maps", func() {
			addedNADs, deletedNADs := nadHandler.GetResults()
			Expect(addedNADs).ToNot(BeNil())
			Expect(deletedNADs).ToNot(BeNil())
		})

		It("should contain added NADs in the first map and deleted NADs in the second", func() {
			ibNAD := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ib-network",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
				},
			}

			// Trigger an add
			nadHandler.OnAdd(ibNAD, false)

			// Trigger a termination via OnUpdate
			now := metav1.NewTime(time.Now())
			terminatingNAD := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-ib-delete",
					Namespace:         "kube-system",
					DeletionTimestamp: &now,
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type": "ib-sriov", "pkey": "0x1234"}`,
				},
			}
			oldNAD := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ib-delete",
					Namespace: "kube-system",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type": "ib-sriov", "pkey": "0x1234"}`,
				},
			}
			nadHandler.OnUpdate(oldNAD, terminatingNAD)

			addedNADs, deletedNADs := nadHandler.GetResults()

			_, addedExists := addedNADs.Get("default_test-ib-network")
			Expect(addedExists).To(BeTrue())

			_, deletedExists := deletedNADs.Get("kube-system_test-ib-delete")
			Expect(deletedExists).To(BeTrue())
		})
	})

	Describe("GetNADFromCache", func() {
		It("should retrieve cached NAD", func() {
			nad := &v1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ib-network",
					Namespace: "default",
				},
				Spec: v1.NetworkAttachmentDefinitionSpec{
					Config: `{"type": "ib-sriov", "pkey": "0x7fff"}`,
				},
			}

			// First add the NAD
			nadHandler.OnAdd(nad, false)

			// Then retrieve from cache
			nadHandlerImpl := nadHandler.(*NADEventHandler)
			cachedNAD, exists := nadHandlerImpl.GetNADFromCache("default_test-ib-network")

			Expect(exists).To(BeTrue())
			Expect(cachedNAD).To(Equal(nad))
		})

		It("should return false for non-existent NAD", func() {
			nadHandlerImpl := nadHandler.(*NADEventHandler)
			_, exists := nadHandlerImpl.GetNADFromCache("nonexistent/network")

			Expect(exists).To(BeFalse())
		})
	})
})
