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

// Package fake is the shared subnet-manager and fabric double for reconciler tests.
package fake

import (
	"fmt"
	"net"
	"sync"

	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
)

// Fabric implements SubnetManagerClient and FabricClient.
type Fabric struct {
	mu         sync.Mutex
	guids      map[string]string
	adds       int
	lists      int
	lastAdd    int
	pending    bool
	pendingFor map[string]struct{}
	partitions map[string]string
	created    []string
	deleted    []string
	removed    []string
	removeErr  error
	devices    map[string][]plugins.IBDeviceGUID
	deviceErr  map[string]error
}

// New returns an empty fabric. Partitions become ready as soon as they are created.
func New() *Fabric {
	return &Fabric{
		guids:      map[string]string{},
		pendingFor: map[string]struct{}{},
		partitions: map[string]string{},
		devices:    map[string][]plugins.IBDeviceGUID{},
		deviceErr:  map[string]error{},
	}
}

// Name implements plugins.SubnetManagerClient.
func (fabric *Fabric) Name() string { return "fake" }

// Spec implements plugins.SubnetManagerClient.
func (fabric *Fabric) Spec() string { return "test" }

// Validate implements plugins.SubnetManagerClient.
func (fabric *Fabric) Validate() error { return nil }

// AddGuidsToPKey records membership. ErrPending is returned while Pending is set or the
// call includes a GUID marked pending.
func (fabric *Fabric) AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.adds++
	fabric.lastAdd = len(guids)
	if fabric.pending {
		return plugins.ErrPending
	}
	for _, guid := range guids {
		if _, ok := fabric.pendingFor[guid.String()]; ok {
			return plugins.ErrPending
		}
	}
	pkeyText := fmt.Sprintf("0x%x", pkey)
	for _, guid := range guids {
		fabric.guids[guid.String()] = pkeyText
	}
	return nil
}

// RemoveGuidsFromPKey drops membership and records each "pkey guid" removal request. It returns
// the error set by SetRemoveError instead, when there is one.
func (fabric *Fabric) RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	if fabric.removeErr != nil {
		return fabric.removeErr
	}
	for _, guid := range guids {
		fabric.removed = append(fabric.removed, fmt.Sprintf("0x%x %s", pkey, guid))
		delete(fabric.guids, guid.String())
	}
	return nil
}

// ListGuidsInUse returns a copy of guid to pkey.
func (fabric *Fabric) ListGuidsInUse() (map[string]string, error) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.lists++
	out := make(map[string]string, len(fabric.guids))
	for guid, pkey := range fabric.guids {
		out[guid] = pkey
	}
	return out, nil
}

// CreateIBPartition is idempotent and returns the stored pkey, or 0x5 on first create.
func (fabric *Fabric) CreateIBPartition(name string) (string, error) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.created = append(fabric.created, name)
	if _, ok := fabric.partitions[name]; !ok {
		fabric.partitions[name] = "0x5"
	}
	return fabric.partitions[name], nil
}

// DeleteIBPartition records the name and forgets the partition.
func (fabric *Fabric) DeleteIBPartition(name string) error {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.deleted = append(fabric.deleted, name)
	delete(fabric.partitions, name)
	return nil
}

// IsPartitionReady reports readiness and the pkey assigned at create time.
func (fabric *Fabric) IsPartitionReady(name string) (bool, string, error) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	return true, fabric.partitions[name], nil
}

// GetNodeIBDevices returns the PF GUIDs configured for the node.
func (fabric *Fabric) GetNodeIBDevices(node string) ([]plugins.IBDeviceGUID, error) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	if err := fabric.deviceErr[node]; err != nil {
		return nil, err
	}
	return append([]plugins.IBDeviceGUID(nil), fabric.devices[node]...), nil
}

// SetDeviceError makes GetNodeIBDevices fail for node. A nil error clears it.
func (fabric *Fabric) SetDeviceError(node string, err error) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	if err == nil {
		delete(fabric.deviceErr, node)
		return
	}
	fabric.deviceErr[node] = err
}

// SetPending makes the next AddGuidsToPKey calls return ErrPending.
func (fabric *Fabric) SetPending(pending bool) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.pending = pending
}

// SetPendingGUID makes every AddGuidsToPKey call that includes guid return ErrPending.
func (fabric *Fabric) SetPendingGUID(guid string) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.pendingFor[guid] = struct{}{}
}

// SetRemoveError makes RemoveGuidsFromPKey return err; nil restores normal removal.
func (fabric *Fabric) SetRemoveError(err error) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.removeErr = err
}

// SetDevices installs the PF GUIDs returned for a node.
func (fabric *Fabric) SetDevices(node string, devices []plugins.IBDeviceGUID) {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.devices[node] = devices
}

// AddCount is how many times AddGuidsToPKey was called.
func (fabric *Fabric) AddCount() int {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	return fabric.adds
}

// LastAddLen is the number of GUIDs in the most recent add call.
func (fabric *Fabric) LastAddLen() int {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	return fabric.lastAdd
}

// ResetCounts zeroes the add counter without clearing membership.
func (fabric *Fabric) ResetCounts() {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	fabric.adds = 0
	fabric.lists = 0
	fabric.lastAdd = 0
}

// ListCount is how many times ListGuidsInUse was called.
func (fabric *Fabric) ListCount() int {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	return fabric.lists
}

// Has reports whether guid is a member.
func (fabric *Fabric) Has(guid string) bool {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	_, ok := fabric.guids[guid]
	return ok
}

// Created is the partition names passed to CreateIBPartition.
func (fabric *Fabric) Created() []string {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	return append([]string(nil), fabric.created...)
}

// Removed is the "pkey guid" pairs passed to RemoveGuidsFromPKey.
func (fabric *Fabric) Removed() []string {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	return append([]string(nil), fabric.removed...)
}

// Deleted is the partition names passed to DeleteIBPartition.
func (fabric *Fabric) Deleted() []string {
	fabric.mu.Lock()
	defer fabric.mu.Unlock()
	return append([]string(nil), fabric.deleted...)
}
