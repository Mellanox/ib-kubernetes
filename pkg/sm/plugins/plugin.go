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

package plugins

import (
	"errors"
	"net"
)

type SubnetManagerClient interface {
	// Name returns the name of the plugin
	Name() string

	// SpecVersion returns the version of the spec of the plugin
	Spec() string

	// Validate Check the client can reach the subnet manager and return error in case if it is not reachable.
	Validate() error

	// AddGuidsToPKey add pkey for the given guid.
	// It return error if failed.
	// For partition-aware plugins, returns ErrPending while backend is provisioning.
	// The daemon should retry on the next periodic cycle.
	AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error

	// RemoveGuidsFromPKey remove guids for given pkey.
	// It return error if failed.
	RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error

	// ListGuidsInUse returns a list of all GUIDS associated with PKeys
	ListGuidsInUse() (map[string]string, error)
}

// ErrPending is returned by AddGuidsToPKey when the backend is still provisioning.
// The daemon should keep the pod in queue and retry on the next periodic cycle.
var ErrPending = errors.New("operation pending: backend provisioning in progress")

// IBDeviceGUID represents a physical InfiniBand device with its hardware GUID.
type IBDeviceGUID struct {
	Device         string           // IB device name, e.g. "mlx5_0"
	DeviceInstance int              // device instance number, e.g. 0, 1, 2, 3
	GUID           net.HardwareAddr // hardware-assigned PF GUID
}

// FabricClient is an optional interface for plugins that manage IB partitions.
// The daemon type-asserts to this interface at startup. Plugins that do not manage
// partitions (e.g. UFM, noop) do not implement this interface.
//
// Partition lifecycle is driven by NAD events:
//   - NAD created with ibKubernetesEnabled → daemon calls CreateIBPartition
//   - NAD deleted → daemon calls DeleteIBPartition
//
// Pod binding still goes through SubnetManagerClient.AddGuidsToPKey using the
// partition key (PKey) returned by CreateIBPartition. The plugin translates
// PKey + GUID to backend-specific operations internally (e.g. partition ID,
// instance ID). The daemon never sees backend-specific identifiers.
type FabricClient interface {
	SubnetManagerClient

	// CreateIBPartition creates an IB partition with the given name (e.g. "namespace_nadName").
	// Returns the partition key (PKey) assigned by the backend.
	// Idempotent: if partition already exists, returns the existing PKey.
	// The plugin internally maps name → backend partition ID.
	CreateIBPartition(name string) (partitionKey string, err error)

	// DeleteIBPartition deletes an IB partition by name.
	// Idempotent: deleting a non-existent partition is not an error.
	// The plugin internally looks up the backend partition ID by name.
	DeleteIBPartition(name string) error

	// IsPartitionReady checks if a partition is ready for pod binding.
	// Returns ready status and the partition key (PKey).
	// PKey may be empty if partition is not yet ready (assigned by backend when ready).
	IsPartitionReady(name string) (ready bool, partitionKey string, err error)

	// GetNodeIBDevices returns the IB PF devices on a node with their hardware GUIDs.
	// Results are cached by the plugin; the daemon may call this on every cycle.
	GetNodeIBDevices(nodeName string) ([]IBDeviceGUID, error)
}
