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

package meta_test

import (
	"testing"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Mellanox/ib-kubernetes/pkg/controllers/meta"
)

func nadWithConfig(config string) *netv1.NetworkAttachmentDefinition {
	return &netv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "ib", Namespace: "team"},
		Spec:       netv1.NetworkAttachmentDefinitionSpec{Config: config},
	}
}

func TestNetworkID(t *testing.T) {
	require.Equal(t, "team_ib", meta.NetworkID(nadWithConfig("")))
}

func TestIsIbSriov(t *testing.T) {
	require.True(t, meta.IsIbSriov(nadWithConfig(`{"type":"ib-sriov"}`)))
	require.False(t, meta.IsIbSriov(nadWithConfig(`{"type":"macvlan"}`)))
	require.False(t, meta.IsIbSriov(nadWithConfig("")))
	require.False(t, meta.IsIbSriov(nadWithConfig(`{"type":`)))
}

func TestPartitionEnabled(t *testing.T) {
	require.True(t, meta.PartitionEnabled(nadWithConfig(`{"type":"ib-sriov","ibKubernetesEnabled":true}`)))
	require.False(t, meta.PartitionEnabled(nadWithConfig(`{"type":"ib-sriov","ibKubernetesEnabled":false}`)))
	require.False(t, meta.PartitionEnabled(nadWithConfig(`{"type":"ib-sriov"}`)))
	require.False(t, meta.PartitionEnabled(nadWithConfig(`{"type":"ib-sriov","ibKubernetesEnabled":"true"}`)),
		"only a JSON boolean enables partitions")
	require.False(t, meta.PartitionEnabled(nadWithConfig(`{"type":"macvlan","ibKubernetesEnabled":true}`)))
}

func TestSpec(t *testing.T) {
	spec, err := meta.Spec(nadWithConfig(`{"type":"ib-sriov","pkey":"0x5","capabilities":{"infinibandGUID":true}}`))
	require.NoError(t, err)
	require.Equal(t, "0x5", spec.PKey)
	require.True(t, spec.Capabilities["infinibandGUID"])

	_, err = meta.Spec(nadWithConfig(`{"type":"macvlan"}`))
	require.ErrorContains(t, err, "nad team/ib")

	_, err = meta.Spec(nadWithConfig(""))
	require.Error(t, err)
}
