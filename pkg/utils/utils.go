package utils

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/golang/glog"
	multus "github.com/intel/multus-cni/types"
	kapi "k8s.io/api/core/v1"
)

const NetworksAnnotation = "k8s.v1.cni.cncf.io/networks"

func ParsePodNetworkAnnotation(podNetworks, defaultNamespace string) ([]*multus.NetworkSelectionElement, error) {
	glog.V(3).Infof("parsePodNetworkAnnotation(): podNetworks %s, defaultNamespace %s", podNetworks, defaultNamespace)
	var networks []*multus.NetworkSelectionElement

	if podNetworks == "" {
		err := fmt.Errorf("parsePodNetworkAnnotation(): pod annotation not having \"network\" as key, refer Multus README.md for the usage guide")
		glog.Error(err)
		return nil, err
	}

	if !strings.ContainsAny(podNetworks, "[{\"") {
		glog.Warning("parsePodNetworkAnnotation(): Only JSON List Format for networks is allowed to be parsed")
		return networks, nil
	}

	if err := json.Unmarshal([]byte(podNetworks), &networks); err != nil {
		return nil, fmt.Errorf("parsePodNetworkAnnotation(): failed to parse pod Network Attachment Annotation: %v", err)
	}

	for _, network := range networks {
		if network.Namespace == "" {
			network.Namespace = defaultNamespace
		}
	}

	return networks, nil
}

// PodWantsNetwork check if pod needs cni
func PodWantsNetwork(pod *kapi.Pod) bool {
	return !pod.Spec.HostNetwork
}

// PodScheduled check if pod is assigned to node
func PodScheduled(pod *kapi.Pod) bool {
	return pod.Spec.NodeName != ""
}
