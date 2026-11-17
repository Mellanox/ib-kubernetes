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

// Package meta parses NetworkAttachmentDefinition config shared by both reconcilers.
package meta

import (
	"encoding/json"
	"fmt"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// NetworkID is the "namespace_name" string plugin APIs still expect.
func NetworkID(nad *netv1.NetworkAttachmentDefinition) string {
	return nad.Namespace + "_" + nad.Name
}

// IsIbSriov reports whether the NAD config selects the ib-sriov CNI.
func IsIbSriov(nad *netv1.NetworkAttachmentDefinition) bool {
	raw, err := parseNADConfig(nad)
	if err != nil {
		return false
	}
	return utils.IsIbSriovCniSpec(raw)
}

// PartitionEnabled reports ibKubernetesEnabled on an ib-sriov NAD.
func PartitionEnabled(nad *netv1.NetworkAttachmentDefinition) bool {
	raw, err := parseNADConfig(nad)
	if err != nil || !utils.IsIbSriovCniSpec(raw) {
		return false
	}
	enabled, ok := raw[utils.IBKubernetesEnabled].(bool)
	return ok && enabled
}

// Spec returns the ib-sriov plugin spec inside the NAD config.
func Spec(nad *netv1.NetworkAttachmentDefinition) (*utils.IbSriovCniSpec, error) {
	raw, err := parseNADConfig(nad)
	if err != nil {
		return nil, err
	}
	spec, err := utils.GetIbSriovCniFromNetwork(raw)
	if err != nil {
		return nil, fmt.Errorf("nad %s/%s: %w", nad.Namespace, nad.Name, err)
	}
	return spec, nil
}

func parseNADConfig(nad *netv1.NetworkAttachmentDefinition) (map[string]interface{}, error) {
	if nad.Spec.Config == "" {
		return nil, fmt.Errorf("nad config is empty")
	}
	raw := map[string]interface{}{}
	if err := json.Unmarshal([]byte(nad.Spec.Config), &raw); err != nil {
		return nil, fmt.Errorf("parse nad config: %w", err)
	}
	return raw, nil
}
