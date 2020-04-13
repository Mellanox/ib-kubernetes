package guid

import (
	"fmt"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	k8s_client "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"

	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
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

	GenerateGUID() (GUID, error)

	// ReleaseGUID release the reservation of the guid.
	// It returns error if the guid is not in the range.
	ReleaseGUID(string) error
}

type guidPool struct {
	kubeClient        k8s_client.Client // kubernetes client
	rangeStart        GUID              // first guid in range
	rangeEnd          GUID              // last guid in range
	currentGUID       GUID              // last given guid
	guidPoolMap       map[GUID]bool     // allocated guid map and status
	guidPodNetworkMap map[string]string // allocated guid mapped to the pod and network
}

func NewGuidPool(conf *config.GuidPoolConfig, client k8s_client.Client) (GuidPool, error) {
	log.Info().Msgf("creating guid pool, guidRangeStart %s, guidRangeEnd %s", conf.RangeStart, conf.RangeEnd)
	rangeStart, err := ParseGUID(conf.RangeStart)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	rangeEnd, err := ParseGUID(conf.RangeEnd)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	if !isValidRange(rangeStart, rangeEnd) {
		return nil, fmt.Errorf("invalid guid range. rangeStart: %v rangeEnd: %v", rangeStart, rangeEnd)
	}

	return &guidPool{
		rangeStart:        rangeStart,
		rangeEnd:          rangeEnd,
		currentGUID:       rangeStart,
		guidPoolMap:       map[GUID]bool{},
		guidPodNetworkMap: map[string]string{},
		kubeClient:        client}, nil
}

//  InitPool check the guids that are already allocated by the running pods
func (p *guidPool) InitPool() error {
	log.Info().Msg("Initializing Pool")
	pods, err := p.kubeClient.GetPods(kapi.NamespaceAll)
	if err != nil {
		return fmt.Errorf("failed to get pods from kubernetes: %v", err)
	}

	for index := range pods.Items {
		log.Debug().Msgf("checking pod for network annotations %v", pods.Items[index])
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
				log.Warn().Msgf("failed to allocate guid for running pod %s, namespace %s: %v",
					pod.Name, pod.Namespace, err)
				continue
			}
		}
	}

	return nil
}

// GenerateGUID generates a guid from the range
func (p *guidPool) GenerateGUID() (GUID, error) {
	// this look will ensure that we check all the range
	// first iteration from current guid to last guid in the range
	// second iteration from first guid in the range to the latest one
	if guid := p.getFreeGUID(p.currentGUID, p.rangeEnd); guid != 0 {
		return guid, nil
	}

	if guid := p.getFreeGUID(p.rangeStart, p.rangeEnd); guid != 0 {
		return guid, nil
	}
	return 0, fmt.Errorf("guid pool range is full")
}

// ReleaseGUID release allocated guid
func (p *guidPool) ReleaseGUID(guid string) error {
	log.Debug().Msgf("releasing guid %s", guid)
	guidAddr, err := ParseGUID(guid)
	if err != nil {
		return err
	}

	if _, ok := p.guidPoolMap[guidAddr]; !ok {
		return fmt.Errorf("failed to release guid %s, not allocated ", guid)
	}
	delete(p.guidPoolMap, guidAddr)
	delete(p.guidPodNetworkMap, guid)
	return nil
}

// AllocateGUID allocate guid for the pod
func (p *guidPool) AllocateGUID(podUid types.UID, networkId, guid string) error {
	log.Debug().Msgf("allocating guid %s, for podUid %v, networkId %s", guid, podUid, networkId)

	guidAddr, err := ParseGUID(guid)
	if err != nil {
		return err
	}

	if guidAddr < p.rangeStart || guidAddr > p.rangeEnd {
		return fmt.Errorf("out of range guid %s, pool range %v - %v", guid, p.rangeStart, p.rangeEnd)
	}

	podNetworkId := string(podUid) + networkId
	if _, exist := p.guidPoolMap[guidAddr]; exist {
		if podNetworkId != p.guidPodNetworkMap[guid] {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
				guid, p.guidPodNetworkMap[guid])
		}
		return nil
	}

	p.guidPoolMap[guidAddr] = true
	p.guidPodNetworkMap[guid] = podNetworkId
	return nil
}

func isValidRange(rangeStart, rangeEnd GUID) bool {
	return rangeStart <= rangeEnd && rangeStart != 0 && rangeEnd != 0xFFFFFFFFFFFFFFFF
}

// getFreeGUID return free guid in given range
func (p *guidPool) getFreeGUID(start, end GUID) GUID {
	for guid := start; guid <= end; guid++ {
		if _, ok := p.guidPoolMap[guid]; !ok {
			p.currentGUID++
			return guid
		}
	}

	return 0
}
