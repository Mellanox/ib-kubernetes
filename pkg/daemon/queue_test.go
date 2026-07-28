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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

var _ = Describe("removeProcessedPodsFromQueue", func() {
	podWithUID := func(uid, name string) *kapi.Pod {
		return &kapi.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns1", UID: types.UID("uid-" + uid)},
		}
	}

	It("preserves pods the informer enqueued after the snapshot", func() {
		// Simulates the race: snapshot took pods [A, B]; while processing,
		// the informer appended C. Reconcile must keep C.
		q := utils.NewSynchronizedMap()
		podA := podWithUID("A", "pod-A")
		podB := podWithUID("B", "pod-B")
		podC := podWithUID("C", "pod-C")

		snapshot := []*kapi.Pod{podA, podB}
		q.Set("ns1_ib-net", []*kapi.Pod{podA, podB, podC})

		removeProcessedPodsFromQueue(q, "ns1_ib-net", snapshot)

		current, ok := q.Get("ns1_ib-net")
		Expect(ok).To(BeTrue(), "entry must remain because pod C is still pending")
		remaining := current.([]*kapi.Pod)
		Expect(remaining).To(HaveLen(1))
		Expect(remaining[0].UID).To(Equal(podC.UID))
	})

	It("removes the entry when every pod was processed", func() {
		q := utils.NewSynchronizedMap()
		podA := podWithUID("A", "pod-A")
		podB := podWithUID("B", "pod-B")

		snapshot := []*kapi.Pod{podA, podB}
		q.Set("ns1_ib-net", []*kapi.Pod{podA, podB})

		removeProcessedPodsFromQueue(q, "ns1_ib-net", snapshot)

		_, ok := q.Get("ns1_ib-net")
		Expect(ok).To(BeFalse(), "entry must be removed when nothing remains")
	})

	It("is a no-op for an empty processed slice", func() {
		q := utils.NewSynchronizedMap()
		podA := podWithUID("A", "pod-A")
		q.Set("ns1_ib-net", []*kapi.Pod{podA})

		removeProcessedPodsFromQueue(q, "ns1_ib-net", nil)

		current, ok := q.Get("ns1_ib-net")
		Expect(ok).To(BeTrue())
		Expect(current.([]*kapi.Pod)).To(HaveLen(1))
	})

	It("is a no-op when the networkID is no longer in the queue", func() {
		q := utils.NewSynchronizedMap()
		podA := podWithUID("A", "pod-A")

		// Should not panic or error.
		removeProcessedPodsFromQueue(q, "ns1_ib-net", []*kapi.Pod{podA})

		_, ok := q.Get("ns1_ib-net")
		Expect(ok).To(BeFalse())
	})
})
