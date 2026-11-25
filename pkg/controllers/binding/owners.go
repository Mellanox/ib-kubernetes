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

import (
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/Mellanox/ib-kubernetes/pkg/guid"
)

// Owners records which pod interface holds a GUID. The lock is taken before the pool lock.
type Owners struct {
	mu          sync.Mutex
	ownerByGUID map[string]string
}

// NewOwners returns an empty owner map.
func NewOwners() *Owners {
	return &Owners{ownerByGUID: map[string]string{}}
}

func (owners *Owners) setOwner(guid, owner string) error {
	owners.mu.Lock()
	defer owners.mu.Unlock()
	return owners.setOwnerLocked(guid, owner)
}

func (owners *Owners) setOwnerLocked(guid, owner string) error {
	if current, ok := owners.ownerByGUID[guid]; ok && current != owner {
		return fmt.Errorf("guid %s already allocated for %s", guid, current)
	}
	owners.ownerByGUID[guid] = owner
	return nil
}

func (owners *Owners) dropOwner(allocated string) {
	owners.mu.Lock()
	defer owners.mu.Unlock()
	delete(owners.ownerByGUID, allocated)
}

// releaseGUID drops the owner and the pool reservation. Lock order is owners, then pool.
func (owners *Owners) releaseGUID(pool guid.Pool, allocated string) {
	owners.dropOwner(allocated)
	if err := pool.ReleaseGUID(allocated); err != nil {
		log.Debug().Err(err).Str("guid", allocated).Msg("guid was not reserved in the pool")
	}
}

// replaceOwner records owner as the holder of guid, replacing whoever held it.
func (owners *Owners) replaceOwner(allocated, owner string) {
	owners.mu.Lock()
	defer owners.mu.Unlock()
	owners.ownerByGUID[canonicalGUID(allocated)] = owner
}

func (owners *Owners) ownerOf(allocated string) string {
	owners.mu.Lock()
	defer owners.mu.Unlock()
	return owners.ownerByGUID[canonicalGUID(allocated)]
}

func (owners *Owners) ownedGUIDs() map[string]struct{} {
	owners.mu.Lock()
	defer owners.mu.Unlock()
	out := make(map[string]struct{}, len(owners.ownerByGUID))
	for allocated := range owners.ownerByGUID {
		out[canonicalGUID(allocated)] = struct{}{}
	}
	return out
}

// ownersForNetwork returns the GUIDs owned by interfaces of the "namespace/name" network.
func (owners *Owners) ownersForNetwork(network string) map[string]string {
	owners.mu.Lock()
	defer owners.mu.Unlock()
	out := map[string]string{}
	for guid, owner := range owners.ownerByGUID {
		if ownerNetwork(owner) == network {
			out[guid] = owner
		}
	}
	return out
}
