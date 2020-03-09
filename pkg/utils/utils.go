package utils

import (
	"encoding/json"
	"fmt"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	kapi "k8s.io/api/core/v1"
)

const (
	InfiniBandAnnotation    = "mellanox.infiniband.app"
	ConfiguredInfiniBandPod = "configured"
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
	if network == nil || network.CNIArgs == nil {
		return false
	}

	cniArgs := *network.CNIArgs
	_, exist := cniArgs["guid"]
	return exist
}
