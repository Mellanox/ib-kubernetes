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

package utils

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	kapi "k8s.io/api/core/v1"
)

type IbSriovCniSpec struct {
	Type         string          `json:"type"`
	PKey         string          `json:"pkey"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
}

const (
	InfiniBandAnnotation    = "mellanox.infiniband.app"
	PkeyAnnotation          = "pkey"
	ConfiguredInfiniBandPod = "configured"
	InfiniBandSriovCni      = "ib-sriov"
)

// PodWantsNetwork check if pod needs cni
func PodWantsNetwork(pod *kapi.Pod) bool {
	return !pod.Spec.HostNetwork
}

// PodScheduled check if pod is assigned to node
func PodScheduled(pod *kapi.Pod) bool {
	return pod.Spec.NodeName != ""
}

// HasNetworkAttachmentAnnot check if pod has Network Attachment Annotation
func HasNetworkAttachmentAnnot(pod *kapi.Pod) bool {
	return len(pod.Annotations[v1.NetworkAttachmentAnnot]) > 0
}

// PodIsRunning check if pod is in "Running" state
func PodIsRunning(pod *kapi.Pod) bool {
	return pod.Status.Phase == kapi.PodRunning
}

// PodIsFinished check if pod is in finished
func PodIsFinished(pod *kapi.Pod) bool {
	return pod.Status.Phase == kapi.PodSucceeded || pod.Status.Phase == kapi.PodFailed
}

// IsPodNetworkConfiguredWithInfiniBand check if pod is already InfiniBand supported
func IsPodNetworkConfiguredWithInfiniBand(network *v1.NetworkSelectionElement) bool {
	if network == nil || network.CNIArgs == nil {
		return false
	}

	return (*network.CNIArgs)[InfiniBandAnnotation] == ConfiguredInfiniBandPod
}

// PodNetworkHasGUID check if network cni-args has guid field
func PodNetworkHasGUID(network *v1.NetworkSelectionElement) bool {
	_, err := GetPodNetworkGUID(network)
	return err == nil
}

// GetPodNetworkPkey return network cni-args pkey field
func GetPodNetworkPkey(network *v1.NetworkSelectionElement) (string, error) {
	if network == nil {
		return "", fmt.Errorf("network element is nil")
	}

	if network.CNIArgs == nil {
		return "", fmt.Errorf(
			"network \"cni-arg\" is missing from network %+v", network)
	}

	cniArgs := *network.CNIArgs
	pkey, exist := cniArgs["pkey"]
	if !exist {
		return "", fmt.Errorf(
			"no \"pkey\" field in \"cni-arg\" in network %+v", network)
	}

	return pkey.(string), nil
}

// GetPodNetworkGUID return network cni-args guid field
func GetPodNetworkGUID(network *v1.NetworkSelectionElement) (string, error) {
	if network == nil {
		return "", fmt.Errorf("network element is nil")
	}

	if network.InfinibandGUIDRequest != "" {
		return network.InfinibandGUIDRequest, nil
	}

	if network.CNIArgs == nil {
		return "", fmt.Errorf(
			"network \"cni-arg\" or \"infinibandGUID\" runtime config is missing from network %+v", network)
	}

	cniArgs := *network.CNIArgs
	guid, exist := cniArgs["guid"]
	if !exist {
		return "", fmt.Errorf(
			"no \"guid\" field in \"cni-arg\" or \"infinibandGUID\" runtime config in network %+v", network)
	}

	return fmt.Sprintf("%s", guid), nil
}

// SetPodNetworkGUID set network cni-args guid
func SetPodNetworkGUID(network *v1.NetworkSelectionElement, guid string, setAsRuntimeConfig bool) error {
	if network == nil {
		return fmt.Errorf("invalid network value: nil")
	}

	if setAsRuntimeConfig {
		network.InfinibandGUIDRequest = guid
		return nil
	}

	if network.CNIArgs == nil {
		network.CNIArgs = &map[string]interface{}{}
	}

	(*network.CNIArgs)["guid"] = guid
	return nil
}

// GetIbSriovCniFromNetwork check if network uses IB-SR-IOV-CNi
func GetIbSriovCniFromNetwork(networkSpec map[string]interface{}) (*IbSriovCniSpec, error) {
	if networkSpec == nil {
		return nil, fmt.Errorf("empty network spec")
	}

	if networkSpec["type"] == InfiniBandSriovCni {
		var ibSpec IbSriovCniSpec
		data, err := json.Marshal(networkSpec)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &ibSpec); err != nil {
			return nil, err
		}
		return &ibSpec, nil
	}

	pluginsValue, ok := networkSpec["plugins"]
	if !ok {
		return nil, fmt.Errorf(
			"network spec type \"%s\" is not supported and \"plugins\" field not found, "+
				"supported type \"ib-sriov\"",
			networkSpec["type"])
	}

	pluginsData, err := json.Marshal(pluginsValue)
	if err != nil {
		return nil, err
	}

	var plugins []*IbSriovCniSpec
	if err := json.Unmarshal(pluginsData, &plugins); err != nil {
		return nil, err
	}

	for _, plugin := range plugins {
		if plugin.Type == InfiniBandSriovCni {
			return plugin, nil
		}
	}

	return nil, fmt.Errorf("cni plugin ib-sriov not found")
}

func GetPodNetwork(networks []*v1.NetworkSelectionElement, networkName string) (*v1.NetworkSelectionElement, error) {
	for _, network := range networks {
		if network.Name == networkName {
			return network, nil
		}
	}

	return nil, fmt.Errorf("network %s not found", networkName)
}

// ParsePKey returns parsed PKey from string
func ParsePKey(pKey string) (int, error) {
	match := regexp.MustCompile(`0[xX]\d+`)
	if !match.MatchString(pKey) {
		return 0, fmt.Errorf("invalid pkey %s, should be leading by 0x ", pKey)
	}

	i, err := strconv.ParseUint(pKey[2:], 16, 32)
	if err != nil {
		return 0, err
	}

	return int(i), nil
}

// ParseNetworkID returns the network name and network namespace
func ParseNetworkID(networkID string) (string, string, error) {
	const expectedLen = 2
	idArray := strings.Split(networkID, "_")
	if len(idArray) != expectedLen {
		return "", "", fmt.Errorf("invalid networkID %s, should be <networkNamespace>_<networkName>", networkID)
	}
	return idArray[0], idArray[1], nil
}

// GenerateNetworkID returns the network name and network namespace with . separation
func GenerateNetworkID(network *v1.NetworkSelectionElement) string {
	return fmt.Sprintf("%s_%s", network.Namespace, network.Name)
}

func GeneratePodNetworkID(pod *kapi.Pod, networkID string) string {
	return string(pod.UID) + "_" + networkID
}
