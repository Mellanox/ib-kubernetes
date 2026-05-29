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
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// removeProcessedPodsFromQueue reconciles a periodic-loop snapshot back
// into the live queue: it removes only the pod UIDs in 'processed' from
// the entry at networkID, leaving any pods the informer enqueued after
// the snapshot. If no pods remain, the entry is removed entirely.
//
// Use this instead of q.Remove(networkID) after processing a snapshot —
// the unconditional Remove would discard pods that were appended to the
// queue while the snapshot was being processed.
func removeProcessedPodsFromQueue(q *utils.SynchronizedMap, networkID string, processed []*kapi.Pod) {
	if len(processed) == 0 {
		return
	}
	processedUIDs := make(map[types.UID]struct{}, len(processed))
	for _, p := range processed {
		processedUIDs[p.UID] = struct{}{}
	}

	q.Lock()
	defer q.Unlock()

	current, ok := q.Items[networkID]
	if !ok {
		return
	}
	currentPods, ok := current.([]*kapi.Pod)
	if !ok {
		return
	}

	// Allocate a fresh slice instead of currentPods[:0] — the underlying
	// array may still be referenced outside this lock (e.g. by a snapshot
	// the periodic loop hasn't finished iterating), and an in-place
	// rewrite would corrupt those reads.
	remaining := make([]*kapi.Pod, 0, len(currentPods))
	for _, pod := range currentPods {
		if _, was := processedUIDs[pod.UID]; !was {
			remaining = append(remaining, pod)
		}
	}
	if len(remaining) == 0 {
		q.UnSafeRemove(networkID)
		return
	}
	q.UnSafeSet(networkID, remaining)
}
