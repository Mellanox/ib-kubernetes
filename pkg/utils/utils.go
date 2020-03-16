package utils

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	kapi "k8s.io/api/core/v1"
)

type IbSriovCniSpec struct {
	Type string `json:"type"`
	PKey string `json:"pkey"`
}

const (
	InfiniBandAnnotation    = "mellanox.infiniband.app"
	ConfiguredInfiniBandPod = "configured"
	InfiniBandSriovCni      = "ib-sriov-cni"
)

// PodWantsNetwork check if pod needs cni
func PodWantsNetwork(pod *kapi.Pod) bool {
	return !pod.Spec.HostNetwork
}

// PodScheduled check if pod is assigned to node
func PodScheduled(pod *kapi.Pod) bool {
	return pod.Spec.NodeName != ""
}

// HasNetworkAttachment check if pod has Network Attachment
func HasNetworkAttachment(pod *kapi.Pod) bool {
	return len(pod.Annotations[v1.NetworkAttachmentAnnot]) > 0
}

// PodIsRunning check if pod is in "Running" state
func PodIsRunning(pod *kapi.Pod) bool {
	return pod.Status.Phase == kapi.PodRunning
}

// ParseInfiniBandAnnotation return a map of InfiniBand annotation
func ParseInfiniBandAnnotation(pod *kapi.Pod) (map[string]string, error) {
	value, ok := pod.Annotations[InfiniBandAnnotation]
	if !ok {
		return nil, fmt.Errorf("no InfiniBand annotations found")
	}

	ibAnnotation := map[string]string{}
	if err := json.Unmarshal([]byte(value), &ibAnnotation); err != nil {
		return nil, err
	}

	return ibAnnotation, nil
}

// PodIsConfiguredWithInfiniBand check if pod is already infiniband supported
func IsPodNetworkConfiguredWithInfiniBand(ibAnnotation map[string]string, network string) bool {
	return ibAnnotation != nil && ibAnnotation[network] == ConfiguredInfiniBandPod
}

// PodNetworkHasGuid check if network cni-args has guid field
func PodNetworkHasGuid(network *v1.NetworkSelectionElement) bool {
	_, err := GetPodNetworkGuid(network)
	return err == nil
}

// GetPodNetworkGuid return network cni-args guid field
func GetPodNetworkGuid(network *v1.NetworkSelectionElement) (string, error) {
	if network == nil || network.CNIArgs == nil {
		return "", fmt.Errorf("network or network \"cni-arg\" is missing, network %v", network)
	}

	cniArgs := *network.CNIArgs
	guid, exist := cniArgs["guid"]
	if !exist {
		return "", fmt.Errorf("no \"guid\" field in network %v", network)
	}

	return fmt.Sprintf("%v", guid), nil
}

// SetPodNetworkGuid set network cni-args guid
func SetPodNetworkGuid(network *v1.NetworkSelectionElement, guid string) error {
	if network == nil {
		return fmt.Errorf("invalid network nil Noetwork")
	}

	if network.CNIArgs == nil {
		network.CNIArgs = &map[string]interface{}{}
	}

	(*network.CNIArgs)["guid"] = guid
	return nil
}

// IsIbSriovCniInNetwork check if network uses IB-SR-IOV-CNi
func IsIbSriovCniInNetwork(networkSpec map[string]interface{}) (*IbSriovCniSpec, error) {
	if networkSpec == nil {
		return nil, fmt.Errorf("empty network spec")
	}

	if networkSpec["type"] == InfiniBandSriovCni {
		ibSpec := &IbSriovCniSpec{Type: InfiniBandSriovCni}
		pkey, ok := networkSpec["pkey"]
		if ok {
			ibSpec.PKey = fmt.Sprintf("%v", pkey)
		}

		return ibSpec, nil
	}

	pluginsValue, ok := networkSpec["plugins"]
	if !ok {
		return nil, fmt.Errorf("netwprk spec type \"%s\" is not supported and \"plugins\" field not found, supported type \"ib-sriov-cni\"",
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

	return nil, fmt.Errorf("cni plugin ib-sriov-cni not found")
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
