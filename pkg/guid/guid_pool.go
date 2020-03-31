package guid

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	k8s_client "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"

	"github.com/golang/glog"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type GuidPool interface {
	// InitPool initializes the pool and marks the allocated guids from previous session.
	// It returns error in case of failure.
	InitPool() error

	// AllocateGUID allocate given guid if in range or
	// allocate the next free guid in the range if no given guid.
	// It returns the allocated guid or error if range is full.
	AllocateGUID(types.UID, string, string) error

	GenerateGUID() (net.HardwareAddr, error)

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

func NewGuidPool(conf *config.GuidPoolConfig, client k8s_client.Client) (GuidPool, error) {
	glog.Infof("NewGuidPool(): guidRangeStart %s, guidRangeEnd %s", conf.RangeStart, conf.RangeEnd)
	rangeStart, err := net.ParseMAC(conf.RangeStart)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	rangeEnd, err := net.ParseMAC(conf.RangeEnd)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	if !isValidGUID(conf.RangeStart) {
		err := fmt.Errorf("NewGuidPool(): invalid start guid range %s", conf.RangeStart)
		glog.Error(err)
		return nil, err
	}
	if !isValidGUID(conf.RangeEnd) {
		err := fmt.Errorf("NewGuidPool(): invalid start guid range %s", conf.RangeEnd)
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

	for index := range pods.Items {
		glog.V(3).Infof("InitPool(): checking pod for network annotations %v", pods.Items[index])
		pod := pods.Items[index]
		networks, err := netAttUtils.ParsePodNetworkAnnotation(&pod)
		if err != nil {
			continue
		}

		for _, network := range networks {
			if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
				continue
			}

			guid, err := utils.GetPodNetworkGuid(network)
			if err != nil {
				continue
			}

			if err = p.AllocateGUID(pod.UID, network.Name, guid); err != nil {
				err = fmt.Errorf("InitPool(): failed to allocate guid for running pod: %v", err)
				glog.Error(err)
				continue
			}
		}
	}

	return nil
}

// GenerateGUID generates a guid from the range
func (p *guidPool) GenerateGUID() (net.HardwareAddr, error) {
	glog.Info("GenerateGUID():")

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

				return freeGUID, nil
			}

			if p.currentGUID.String() == p.rangeEnd.String() {
				break
			}
			p.currentGUID = p.getNextGUID(p.currentGUID)
		}

		copy(p.currentGUID, p.rangeStart)
	}
	return nil, fmt.Errorf("AllocateGUID(): the range is full")
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

// AllocateGUID allocate guid for the pod
func (p *guidPool) AllocateGUID(podUid types.UID, networkId, guid string) error {
	glog.Infof("AllocateGUID(): podUid %v, networkId %s, guid %s", podUid, networkId, guid)

	if _, err := net.ParseMAC(guid); err != nil {
		return err
	}

	if !isValidGUID(guid) {
		return fmt.Errorf("AllocateGUID(): invalid guid %s", guid)
	}

	podNetworkId := string(podUid) + networkId
	if _, exist := p.guidPoolMap[guid]; exist {
		if podNetworkId != p.guidPodNetworkMap[guid] {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
				guid, p.guidPodNetworkMap[guid])
		}
		return nil
	}

	p.guidPoolMap[guid] = true
	p.guidPodNetworkMap[guid] = podNetworkId
	return nil
}

func (p *guidPool) getNextGUID(currentGUID net.HardwareAddr) net.HardwareAddr {
	for idx := 7; idx >= 0; idx-- {
		currentGUID[idx]++
		if currentGUID[idx] != 0 {
			break
		}
	}

	return currentGUID
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

// isValidGUID check if the guild is valid
func isValidGUID(guid string) bool {
	match, _ := regexp.MatchString("^([0-9a-fA-F]{2}:){7}[0-9a-fA-F]{2}$", guid)
	return match && guid != "00:00:00:00:00:00:00:00" && !strings.EqualFold(guid, "ff:ff:ff:ff:ff:ff:ff:ff")
}
