package guid

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/Mellanox/ib-kubernetes/pkg/k8s-client"

	"github.com/golang/glog"
	multus "github.com/intel/multus-cni/types"
	kapi "k8s.io/api/core/v1"
)

const (
	networksAnnotation = "k8s.v1.cni.cncf.io/networks"
)

type GuidPool interface {
	// InitPool initializes the pool and marks the allocated guids from previous session.
	// It returns error in case of failure.
	InitPool() error

	// AllocateGUID allocate the next free guid in the range.
	// It returns the allocated guid or error if range is full.
	AllocateGUID() (string, error)

	// ReleaseGUID release the reservation of the guid.
	// It returns error if the guid is not in the range.
	ReleaseGUID(string) error
}

type guidPool struct {
	kubeClient  k8s_client.Client // kubernetes client
	rangeStart  net.HardwareAddr  // first guid in range
	rangeEnd    net.HardwareAddr  // last guid in range
	currentGUID net.HardwareAddr  // last given guid
	guidPoolMap map[string]bool   // allocated guid map and status
}

func NewGuidPool(guidRangeStart, guidRangeEnd string, client k8s_client.Client) (GuidPool, error) {
	glog.Infof("NewGuidPool(): guidRangeStart %s, guidRangeEnd %s", guidRangeStart, guidRangeEnd)
	rangeStart, err := net.ParseMAC(guidRangeStart)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	rangeEnd, err := net.ParseMAC(guidRangeEnd)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	if !isAllowedGUID(guidRangeStart) {
		err := fmt.Errorf("NewGuidPool(): invalid start guid range %s", guidRangeStart)
		glog.Error(err)
		return nil, err
	}
	if !isAllowedGUID(guidRangeEnd) {
		err := fmt.Errorf("NewGuidPool(): invalid start guid range %s", guidRangeEnd)
		glog.Error(err)
		return nil, err
	}
	currentGUID := make(net.HardwareAddr, len(rangeStart))
	copy(currentGUID, rangeStart)
	if err := checkGuidRange(rangeStart, rangeEnd); err != nil {
		err = fmt.Errorf("NewGuidPool(): invalied range: %v", err)
		glog.Error(err)
		return nil, err
	}

	return &guidPool{rangeStart: rangeStart,
		rangeEnd:    rangeEnd,
		currentGUID: currentGUID,
		guidPoolMap: map[string]bool{},
		kubeClient:  client}, nil
}

//  InitPool check the guids that are already allocated by the running pods
func (p *guidPool) InitPool() error {
	glog.Info("InitPool():")
	pods, err := p.kubeClient.GetPods(kapi.NamespaceAll)
	if err != nil {
		err = fmt.Errorf("InitPool(): failed to get pods from kubernetes: %v", err)
		glog.Error(err)
		return err
	}

	for _, pod := range pods.Items {
		glog.V(3).Infof("InitPool(): checking pod for network annotations %v", pod)
		if pod.Annotations == nil {
			glog.V(3).Info("InitPool(): no annotations found in pod")
			continue
		}

		networkValue, ok := pod.Annotations[networksAnnotation]
		if !ok {
			glog.V(3).Infof(`InitPool(): no network "%s" annotations found in pod`, networksAnnotation)
			continue
		}

		networks, err := p.parsePodNetworkAnnotation(networkValue, pod.Namespace)
		if err != nil {
			continue
		}

		if len(networks) == 0 {
			continue
		}

		for _, network := range networks {
			if network.CNIArgs == nil {
				continue
			}
			cniArgs := *network.CNIArgs
			value, exist := cniArgs["guid"]
			guid := fmt.Sprintf("%v", value)
			if exist {
				if err = p.allocatePodRequestedGUID(guid); err != nil {
					err = fmt.Errorf("InitPool(): failed to allocate guid for running pod: %v", err)
					glog.Error(err)
					return err
				}
			}
		}
	}

	return nil
}

/// AllocateGUID allocate guid for the pod
func (p *guidPool) AllocateGUID() (string, error) {
	glog.Info("AllocateGUID():")

	// this look will ensure that we check all the range
	// first iteration from current guid to last guid in the range
	// second iteration from first guid in the range to the latest one
	for idx := 0; idx <= 1; idx++ {

		// This loop runs from the current guid to the last one in the range
		for {
			if _, ok := p.guidPoolMap[p.currentGUID.String()]; !ok {
				freeGUID := make(net.HardwareAddr, len(p.currentGUID))
				copy(freeGUID, p.currentGUID)

				// move to the next guid after we found a free one
				if p.currentGUID.String() == p.rangeEnd.String() {
					copy(p.currentGUID, p.rangeStart)
				} else {
					p.currentGUID = p.getNextGUID(p.currentGUID)
				}

				guid := freeGUID.String()
				if err := p.allocatePodRequestedGUID(guid); err != nil {
					err = fmt.Errorf("AllocateGUID(): failed to allocate guid %s: %v", guid, err)
					glog.Error(err)
					return "", err
				}
				return guid, nil
			}

			if p.currentGUID.String() == p.rangeEnd.String() {
				break
			}
			p.currentGUID = p.getNextGUID(p.currentGUID)
		}

		copy(p.currentGUID, p.rangeStart)
	}
	return "", fmt.Errorf("AllocateGUID(): the range is full")
}

// ReleaseGUID release allocated guid
func (p *guidPool) ReleaseGUID(guid string) error {
	glog.Infof("ReleaseGUID(): guid %s", guid)
	if _, ok := p.guidPoolMap[guid]; !ok {
		err := fmt.Errorf("ReleaseGUID(): failed to release guid %s, not allocated ", guid)
		glog.Error(err)
		return err
	}
	delete(p.guidPoolMap, guid)
	return nil
}

func (p *guidPool) parsePodNetworkAnnotation(podNetworks, defaultNamespace string) ([]*multus.NetworkSelectionElement, error) {
	glog.Infof("parsePodNetworkAnnotation(): podNetworks %s, defaultNamespace %s", podNetworks, defaultNamespace)
	var networks []*multus.NetworkSelectionElement

	if podNetworks == "" {
		err := fmt.Errorf("parsePodNetworkAnnotation(): pod annotation not having \"network\" as key, refer Multus README.md for the usage guide")
		glog.Error(err)
		return nil, err
	}

	if !strings.ContainsAny(podNetworks, "[{\"") {
		glog.Info("parsePodNetworkAnnotation(): Only JSON List Format for networks is allowed to be parsed")
		return networks, nil
	}

	if err := json.Unmarshal([]byte(podNetworks), &networks); err != nil {
		return nil, fmt.Errorf("parsePodNetworkAnnotation(): failed to parse pod Network Attachment Selection Annotation JSON format: %v", err)
	}

	for _, network := range networks {
		if network.Namespace == "" {
			network.Namespace = defaultNamespace
		}
	}

	return networks, nil
}

func (p *guidPool) allocatePodRequestedGUID(guid string) error {
	glog.Infof("allocatePodRequestedGUID(): guid %s", guid)
	if _, err := net.ParseMAC(guid); err != nil {
		return err
	}

	if _, exist := p.guidPoolMap[guid]; exist {
		return fmt.Errorf("failed to allocate requested guid, already allocated %s", guid)
	}

	p.guidPoolMap[guid] = true

	return nil
}

func (p *guidPool) getNextGUID(currentGUID net.HardwareAddr) net.HardwareAddr {
	for idx := 7; idx >= 0; idx-- {
		currentGUID[idx] += 1
		if currentGUID[idx] != 0 {
			break
		}
	}

	return currentGUID
}

func isAllowedGUID(guid string) bool {
	return guid != "00:00:00:00:00:00:00:00" && strings.ToLower(guid) != "ff:ff:ff:ff:ff:ff:ff:ff"
}

func checkGuidRange(startGUID, endGUID net.HardwareAddr) error {
	glog.V(3).Infof("checkGuidRange(): startGUID %v, endGUID %s", startGUID.String(), endGUID.String())
	for idx := 0; idx <= 7; idx++ {
		if startGUID[idx] < endGUID[idx] {
			return nil
		}
		if startGUID[idx] > endGUID[idx] {
			return fmt.Errorf("invalid guid range. rangeStart: %s rangeEnd: %s", startGUID.String(), endGUID.String())
		}
	}

	return nil
}
