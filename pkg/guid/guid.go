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

package guid

import (
	"fmt"
	"net"
)

// GUID address is an uint64 encapsulation for network hardware address
type GUID uint64

const (
	guidLength = 8
	byteBitLen = 8
	byteMask   = 0xff
)

// ParseGUID parses string only as GUID 64 bit
func ParseGUID(s string) (GUID, error) {
	ha, err := net.ParseMAC(s)
	if err != nil {
		return 0, err
	}
	if len(ha) != guidLength {
		return 0, fmt.Errorf("invalid GUID address %s", s)
	}
	var guid uint64
	for idx, octet := range ha {
		guid |= uint64(octet) << uint(byteBitLen*(guidLength-1-idx)) // #nosec G115 -- octet is byte, no overflow possible
	}
	return GUID(guid), nil
}

// String returns the string representation of GUID
func (g GUID) String() string {
	return g.HardWareAddress().String()
}

func (g GUID) HardWareAddress() net.HardwareAddr {
	value := uint64(g)
	ha := make(net.HardwareAddr, guidLength)
	for idx := guidLength - 1; idx >= 0; idx-- {
		ha[idx] = byte(value & byteMask)
		value >>= byteBitLen
	}

	return ha
}
