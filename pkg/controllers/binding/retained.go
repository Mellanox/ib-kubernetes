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
	"context"
	"fmt"
	"sort"
	"strings"

	netv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

// recordPKeys stores on the NAD every pkey its pods are configured on, before any pod is marked
// configured on it. After a restart this is the trusted record of the pkeys a configured pod may
// keep, since the pod annotation is user-writable. Anyone who can edit the NAD can already change
// its pkey, so trusting the NAD grants no extra access.
func (reconciler *Reconciler) recordPKeys(
	ctx context.Context, nad *netv1.NetworkAttachmentDefinition, groups []pkeyGroup,
) error {
	seen := map[string]struct{}{}
	var pkeys []string
	for _, group := range groups {
		if len(group.ifaces) == 0 {
			continue
		}
		parsed, err := utils.ParsePKey(group.pkey)
		if err != nil {
			continue
		}
		text := fmt.Sprintf("%#x", parsed)
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		pkeys = append(pkeys, text)
	}
	sort.Strings(pkeys)
	value := strings.Join(pkeys, ",")
	if nad.Annotations[utils.ConfiguredPKeysAnnotation] == value {
		return nil
	}
	base := nad.DeepCopy()
	if value == "" {
		delete(nad.Annotations, utils.ConfiguredPKeysAnnotation)
	} else {
		if nad.Annotations == nil {
			nad.Annotations = map[string]string{}
		}
		nad.Annotations[utils.ConfiguredPKeysAnnotation] = value
	}
	// A plain merge of this one key does not need the optimistic lock. The
	// partition reconciler writes the same NAD, and the lock made the two
	// patches fail each other.
	patch := client.MergeFrom(base)
	if err := reconciler.Patch(ctx, nad, patch); err != nil {
		return fmt.Errorf("record configured pkeys: %w", err)
	}
	return nil
}

// trustedPKeys is the NAD pkey and the pkeys recorded on the NAD.
func trustedPKeys(nad *netv1.NetworkAttachmentDefinition, spec *utils.IbSriovCniSpec) []string {
	out := []string{spec.PKey}
	if recorded := nad.Annotations[utils.ConfiguredPKeysAnnotation]; recorded != "" {
		out = append(out, strings.Split(recorded, ",")...)
	}
	return out
}

func isTrustedPKey(pkey string, pkeys []string) bool {
	for _, candidate := range pkeys {
		if samePKey(pkey, candidate) {
			return true
		}
	}
	return false
}
