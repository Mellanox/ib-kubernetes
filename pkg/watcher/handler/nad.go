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

package handler

import (
	"encoding/json"
	"fmt"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/rs/zerolog/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

type NADEventHandler struct {
	addedNADs   *utils.SynchronizedMap // Maps network ID to NAD for added NADs
	deletedNADs *utils.SynchronizedMap // Maps network ID to NAD for deleted NADs
}

// NewNADEventHandler creates a new NAD event handler
func NewNADEventHandler() ResourceEventHandler {
	return &NADEventHandler{
		addedNADs:   utils.NewSynchronizedMap(),
		deletedNADs: utils.NewSynchronizedMap(),
	}
}

// GetResourceObject returns the NAD object type for the watcher
func (n *NADEventHandler) GetResourceObject() runtime.Object {
	return &v1.NetworkAttachmentDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "NetworkAttachmentDefinition",
			APIVersion: "k8s.cni.cncf.io/v1",
		},
	}
}

// OnAdd handles NAD creation events
func (n *NADEventHandler) OnAdd(obj interface{}, _ bool) {
	nad := obj.(*v1.NetworkAttachmentDefinition)
	log.Info().Msgf("NAD add event: namespace %s name %s", nad.Namespace, nad.Name)

	// Only handle InfiniBand SR-IOV networks
	if !n.isInfiniBandNetwork(nad) {
		log.Debug().Msgf("NAD %s/%s is not an InfiniBand network", nad.Namespace, nad.Name)
		return
	}

	networkID := fmt.Sprintf("%s_%s", nad.Namespace, nad.Name)

	// Informer-replay case: daemon restarted while the NAD was already
	// Terminating. k8s delivers existing objects through OnAdd; route to
	// the deletion queue so handleNADDeletion runs the finalizer cleanup
	// instead of the add path treating it as new.
	if nad.DeletionTimestamp != nil {
		n.deletedNADs.Set(networkID, nad)
		log.Info().Msgf("NAD already terminating at add; routed to deletion queue: %s", networkID)
		return
	}

	n.addedNADs.Set(networkID, nad)
	log.Info().Msgf("Successfully processed NAD add event: %s", networkID)
}

// OnUpdate handles NAD updates, including detection of DeletionTimestamp (Terminating state)
func (n *NADEventHandler) OnUpdate(oldObj, newObj interface{}) {
	oldNAD := oldObj.(*v1.NetworkAttachmentDefinition)
	newNAD := newObj.(*v1.NetworkAttachmentDefinition)

	// Detect entry into Terminating: DeletionTimestamp just appeared.
	if newNAD.DeletionTimestamp != nil && (oldNAD.DeletionTimestamp == nil) {
		log.Info().Msgf("NAD entering terminating state: namespace %s name %s", newNAD.Namespace, newNAD.Name)

		if !n.isInfiniBandNetwork(newNAD) {
			log.Debug().Msgf("NAD %s/%s is not an InfiniBand network, skipping termination processing",
				newNAD.Namespace, newNAD.Name)
			return
		}

		networkID := fmt.Sprintf("%s_%s", newNAD.Namespace, newNAD.Name)

		// Add to deletion processing queue (treat as delete event).
		// Note: if already in queue, this updates with latest NAD object.
		n.deletedNADs.Set(networkID, newNAD)
		n.addedNADs.Remove(networkID)

		log.Info().Msgf("Added terminating NAD to deletion queue: %s", networkID)
	}
}

// OnDelete handles NAD deletion events (NAD fully removed from K8s).
// Partition cleanup is handled in OnUpdate when DeletionTimestamp is set;
// OnDelete fires only AFTER the finalizer is removed and the NAD is gone.
func (n *NADEventHandler) OnDelete(obj interface{}) {
	nad, ok := obj.(*v1.NetworkAttachmentDefinition)
	if !ok {
		log.Debug().Msg("NAD delete event: unexpected object type")
		return
	}

	networkID := fmt.Sprintf("%s_%s", nad.Namespace, nad.Name)
	log.Debug().Msgf("NAD delete event: %s", networkID)
	n.addedNADs.Remove(networkID)
}

// GetResults returns the added and deleted NADs maps for processing by the daemon
func (n *NADEventHandler) GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap) {
	return n.addedNADs, n.deletedNADs
}

// isInfiniBandNetwork checks if the NAD is for InfiniBand SR-IOV
func (n *NADEventHandler) isInfiniBandNetwork(nad *v1.NetworkAttachmentDefinition) bool {
	var networkConfig map[string]interface{}
	if err := json.Unmarshal([]byte(nad.Spec.Config), &networkConfig); err != nil {
		log.Error().Msgf("Failed to parse NAD config for %s/%s: %v", nad.Namespace, nad.Name, err)
		return false
	}

	if cniType, ok := networkConfig["type"]; ok {
		return cniType == utils.InfiniBandSriovCni
	}

	return false
}
