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

// nico_standard_helpers.go isolates all pointer-dereference access for the
// auto-generated standard SDK types (standard.Instance, standard.InfiniBandInterface).
// The simple SDK returns standard.Instance from instance operations; its fields
// are all optional pointers. These helpers keep the main plugin code clean.

package main

import (
	"time"

	"github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

// ── Instance helpers ──

func instanceGetID(inst *standard.Instance) string {
	if inst == nil {
		return ""
	}
	return inst.GetId()
}

func instanceGetName(inst *standard.Instance) string {
	if inst == nil {
		return ""
	}
	return inst.GetName()
}

func instanceGetIBInterfaces(inst *standard.Instance) []standard.InfiniBandInterface {
	if inst == nil {
		return nil
	}
	return inst.GetInfinibandInterfaces()
}

// ── InfiniBandInterface helpers ──

func ibIfaceGetDevice(iface *standard.InfiniBandInterface) string {
	if iface == nil {
		return ""
	}
	return iface.GetDevice()
}

func ibIfaceGetDeviceInstance(iface *standard.InfiniBandInterface) int {
	if iface == nil {
		return 0
	}
	return int(iface.GetDeviceInstance())
}

func ibIfaceGetGUID(iface *standard.InfiniBandInterface) string {
	if iface == nil {
		return ""
	}
	return iface.GetGuid()
}

func ibIfaceGetIsPhysical(iface *standard.InfiniBandInterface) bool {
	if iface == nil {
		return false
	}
	return iface.GetIsPhysical()
}

func ibIfaceGetVirtualFunctionID(iface *standard.InfiniBandInterface) *int {
	if iface == nil {
		return nil
	}
	vfID, ok := iface.GetVirtualFunctionIdOk()
	if !ok || vfID == nil {
		return nil
	}
	v := int(*vfID)
	return &v
}

func ibIfaceGetPartitionID(iface *standard.InfiniBandInterface) string {
	if iface == nil {
		return ""
	}
	return iface.GetPartitionId()
}

func ibIfaceGetStatus(iface *standard.InfiniBandInterface) string {
	if iface == nil {
		return ""
	}
	return string(iface.GetStatus())
}

func ibIfaceGetUpdated(iface *standard.InfiniBandInterface) time.Time {
	if iface == nil {
		return time.Time{}
	}
	return iface.GetUpdated()
}
