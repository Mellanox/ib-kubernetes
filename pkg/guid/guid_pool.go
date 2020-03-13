package guid

import (
	"fmt"
	"net"
	"strings"

	"github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"

	"github.com/golang/glog"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	kapi "k8s.io/api/core/v1"
)

type GuidPool interface {
	// InitPool initializes the pool and marks the allocated guids from previous session.
	// It returns error in case of failure.
	InitPool() error

	// AllocateGUID allocate given guid if in range or
	// allocate the next free guid in the range if no given guid.
	// It returns the allocated guid or error if range is full.
	AllocateGUID(string, string) error

	GenerateGUID(string) (string, error)

	// ReleaseGUID release the reservation of the guid.
	// It returns error if the guid is not in the range.
	ReleaseGUID(string) error
}

type guidPool struct {
	kubeClient        k8s_client.Client // kubernetes client
	rangeStart        net.HardwareAddr  // first guid in range
	rangeEnd          net.HardwareAddr  // last guid in range
	currentGUID       net.HardwareAddr  // last given guid
	guidPoolMap       map[string]bool   // allocated guid map and status
	guidPodNetworkMap map[string]string // allocated guid mapped to the pod and network
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
		rangeEnd:          rangeEnd,
		currentGUID:       currentGUID,
		guidPoolMap:       map[string]bool{},
		guidPodNetworkMap: map[string]string{},
		kubeClient:        client}, nil
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

		networks, err := netAttUtils.ParsePodNetworkAnnotation(&pod)
		if err != nil {
			continue
		}

		ibAnnotation, err := utils.ParseInfiniBandAnnotation(&pod)
		if err != nil {
			continue
		}

		for _, network := range networks {
			if !utils.IsPodNetworkConfiguredWithInfiniBand(ibAnnotation, network.Name) {
				continue
			}

			guid, err := utils.GetPodNetworkGuid(network)
			if err != nil {
				continue
			}

			if err = p.AllocateGUID(string(pod.UID)+network.Name, guid); err != nil {
				err = fmt.Errorf("InitPool(): failed to allocate guid for running pod: %v", err)
				glog.Error(err)
				continue
			}
		}
	}

	return nil
}

/// GenerateGUID generates a guid from the range
func (p *guidPool) GenerateGUID(podNetworkId string) (string, error) {
	glog.Infof("GenerateGUID(): podNetworkId %s", podNetworkId)

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
				if err := p.AllocateGUID(podNetworkId, guid); err != nil {
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
	delete(p.guidPodNetworkMap, guid)
	return nil
}

/// AllocateGUID allocate guid for the pod
func (p *guidPool) AllocateGUID(podNetworkId, guid string) error {
	glog.Infof("AllocateGUID(): podNetworkId, %s guid %s", podNetworkId, guid)

	if _, err := net.ParseMAC(guid); err != nil {
		return err
	}

	if _, exist := p.guidPoolMap[guid]; exist {
		if podNetworkId != p.guidPodNetworkMap[guid] {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
				guid, p.guidPodNetworkMap[guid])
		} else {
			return nil
		}
	}

	p.guidPoolMap[guid] = true
	p.guidPodNetworkMap[guid] = podNetworkId
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
