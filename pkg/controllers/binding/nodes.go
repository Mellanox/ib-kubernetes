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
// After a restart the map is empty, so those GUIDs are log-only until enforce.
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
