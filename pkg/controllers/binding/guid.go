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
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

func canonicalGUID(value string) string {
	parsed, err := guid.ParseGUID(value)
	if err != nil {
		return strings.ToLower(value)
	}
	return parsed.String()
}

func guidToHardwareAddr(value string) (net.HardwareAddr, error) {
	parsed, err := guid.ParseGUID(value)
	if err != nil {
		return nil, fmt.Errorf("parse guid %s: %w", value, err)
	}
	return parsed.HardWareAddress(), nil
}

func parseOptionalPKey(pkey string) (int, bool, error) {
	if pkey == "" {
		return 0, false, nil
	}
	parsed, err := utils.ParsePKey(pkey)
	if err != nil {
		return 0, false, fmt.Errorf("parse pkey %s: %w", pkey, err)
	}
	return parsed, true, nil
}

func alreadyAllocated(err error) bool {
	return errors.Is(err, guid.ErrAlreadyAllocated)
}
