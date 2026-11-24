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

package binding

import "sync"

// Nodes remembers PF GUIDs per node while this process is leader.
// After a restart or leader change the map is empty. A PF whose last pod left
// before the restart has no record, so sweepPartition only warns about it in
// the default log mode. DAEMON_ORPHAN_GC=enforce is what removes it.
// While this process is still leader, dropIdleNodes removes the PF when the
// node's last pod leaves, without enforce.
type Nodes struct {
	mu           sync.Mutex
	pfsByNADNode map[string]nodePFs
}

// nodePFs is the PF GUIDs of one node and the pkey they were added to.
type nodePFs struct {
	pkey  int
	guids []string
}

// NewNodes returns an empty node memory.
func NewNodes() *Nodes {
	return &Nodes{pfsByNADNode: map[string]nodePFs{}}
}

func nadNodeKey(namespace, name, node string) string {
	return namespace + "/" + name + "/" + node
}

func (nodes *Nodes) recordNodePFGUIDs(namespace, name, node string, pkey int, guids []string) {
	nodes.mu.Lock()
	defer nodes.mu.Unlock()
	nodes.pfsByNADNode[nadNodeKey(namespace, name, node)] = nodePFs{pkey: pkey, guids: append([]string(nil), guids...)}
}

func (nodes *Nodes) dropNodePFGUIDs(namespace, name, node string) {
	nodes.mu.Lock()
	defer nodes.mu.Unlock()
	delete(nodes.pfsByNADNode, nadNodeKey(namespace, name, node))
}

// keptByOther reports that a NAD other than namespace/name on node still records
// one of guids on pkey. Dropping or moving this node must leave those GUIDs in place.
func (nodes *Nodes) keptByOther(namespace, name, node string, pkey int, guids []string) bool {
	if nodes == nil || len(guids) == 0 {
		return false
	}
	except := nadNodeKey(namespace, name, node)
	want := map[string]struct{}{}
	for _, allocated := range guids {
		want[canonicalGUID(allocated)] = struct{}{}
	}
	nodes.mu.Lock()
	defer nodes.mu.Unlock()
	for key, pfs := range nodes.pfsByNADNode {
		if key == except || pfs.pkey != pkey {
			continue
		}
		for _, allocated := range pfs.guids {
			if _, ok := want[canonicalGUID(allocated)]; ok {
				return true
			}
		}
	}
	return false
}

// withoutShared returns the GUIDs no other NAD still records on pkey.
func (nodes *Nodes) withoutShared(namespace, name, node string, pkey int, guids []string) []string {
	if nodes == nil {
		return append([]string(nil), guids...)
	}
	mine := make([]string, 0, len(guids))
	for _, allocated := range guids {
		if nodes.keptByOther(namespace, name, node, pkey, []string{allocated}) {
			continue
		}
		mine = append(mine, allocated)
	}
	return mine
}

// guidsOnPKey is every recorded PF GUID whose node was added to pkey, for any NAD.
func (nodes *Nodes) guidsOnPKey(pkey int) map[string]struct{} {
	nodes.mu.Lock()
	defer nodes.mu.Unlock()
	out := map[string]struct{}{}
	for _, pfs := range nodes.pfsByNADNode {
		if pfs.pkey != pkey {
			continue
		}
		for _, allocated := range pfs.guids {
			out[allocated] = struct{}{}
		}
	}
	return out
}

func (nodes *Nodes) nodePFGUIDsForNAD(namespace, name string) map[string]nodePFs {
	nodes.mu.Lock()
	defer nodes.mu.Unlock()
	prefix := namespace + "/" + name + "/"
	out := map[string]nodePFs{}
	for key, pfs := range nodes.pfsByNADNode {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		out[key[len(prefix):]] = nodePFs{pkey: pfs.pkey, guids: append([]string(nil), pfs.guids...)}
	}
	return out
}
