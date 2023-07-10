package handler

import (
	"fmt"
	"sync"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

type podEventHandler struct {
	retryPods   sync.Map
	addedPods   *utils.SynchronizedMap
	deletedPods *utils.SynchronizedMap
}

func NewPodEventHandler() ResourceEventHandler {
	eventHandler := &podEventHandler{
		retryPods:   sync.Map{},
		addedPods:   utils.NewSynchronizedMap(),
		deletedPods: utils.NewSynchronizedMap(),
	}

	return eventHandler
}

func (p *podEventHandler) GetResourceObject() runtime.Object {
	return &kapi.Pod{TypeMeta: metav1.TypeMeta{Kind: kapi.ResourcePods.String()}}
}

func (p *podEventHandler) OnAdd(obj interface{}, _ bool) {
	log.Debug().Msgf("pod add Event: pod %v", obj)
	pod := obj.(*kapi.Pod)
	log.Info().Msgf("pod add Event: namespace %s name %s", pod.Namespace, pod.Name)

	if !utils.PodWantsNetwork(pod) {
		log.Debug().Msg("pod doesn't require network")
		return
	}

	if utils.PodIsRunning(pod) {
		log.Debug().Msg("pod is already in running state")
		return
	}

	if !utils.HasNetworkAttachmentAnnot(pod) {
		log.Debug().Msgf("pod doesn't have network annotation \"%v\"", v1.NetworkAttachmentAnnot)
		return
	}

	if !utils.PodScheduled(pod) {
		p.retryPods.Store(pod.UID, true)
		return
	}

	if err := p.addNetworksFromPod(pod); err != nil {
		log.Error().Msgf("%v", err)
		return
	}

	log.Info().Msgf("pod add event: successfully added namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) OnUpdate(oldObj, newObj interface{}) {
	log.Debug().Msgf("pod update event: oldPod %v, newPod %v", oldObj, newObj)
	pod := newObj.(*kapi.Pod)
	log.Info().Msgf("pod update event: namespace %s name %s", pod.Namespace, pod.Name)

	if !utils.PodWantsNetwork(pod) {
		log.Debug().Msg("pod doesn't require network")
		return
	}

	if utils.PodIsRunning(pod) {
		log.Debug().Msg("pod is already in running state")
		p.retryPods.Delete(pod.UID)
		return
	}

	if !utils.HasNetworkAttachmentAnnot(pod) {
		log.Debug().Msgf("pod doesn't have network annotation \"%v\"", v1.NetworkAttachmentAnnot)
		return
	}

	_, retry := p.retryPods.Load(pod.UID)
	if !retry || !utils.PodScheduled(pod) {
		return
	}

	if err := p.addNetworksFromPod(pod); err != nil {
		log.Error().Msgf("%v", err)
		return
	}

	p.retryPods.Delete(pod.UID)
	log.Info().Msgf("pod update event: successfully updated namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) OnDelete(obj interface{}) {
	log.Debug().Msgf("pod delete event: pod %v", obj)
	pod := obj.(*kapi.Pod)
	log.Info().Msgf("pod delete event: namespace %s name %s", pod.Namespace, pod.Name)

	// make sure this pod won't be in the retry pods
	p.retryPods.Delete(pod.UID)

	if !utils.PodWantsNetwork(pod) {
		log.Debug().Msg("pod doesn't require network")
		return
	}

	if !utils.HasNetworkAttachmentAnnot(pod) {
		log.Debug().Msgf("pod doesn't have network annotation \"%v\"", v1.NetworkAttachmentAnnot)
		return
	}

	networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		log.Error().Msgf("failed to parse network annotations with error: %v", err)
		return
	}

	for _, network := range networks {
		if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
			continue
		}

		// check if pod network has guid
		if !utils.PodNetworkHasGUID(network) {
			log.Error().Msgf("pod %s has network %s marked as configured with InfiniBand without having guid",
				pod.Name, network.Name)
			continue
		}

		networkID := utils.GenerateNetworkID(network)
		pods, ok := p.deletedPods.Get(networkID)
		if !ok {
			pods = []*kapi.Pod{pod}
		} else {
			pods = append(pods.([]*kapi.Pod), pod)
		}
		p.deletedPods.Set(networkID, pods)
	}

	log.Info().Msgf("successfully deleted namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap) {
	return p.addedPods, p.deletedPods
}

func (p *podEventHandler) addNetworksFromPod(pod *kapi.Pod) error {
	networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		p.retryPods.Store(pod.UID, true)
		return fmt.Errorf("failed to parse network annotations with error: %v", err)
	}

	for _, network := range networks {
		// check if pod network is configured
		if utils.IsPodNetworkConfiguredWithInfiniBand(network) {
			continue
		}

		networkID := utils.GenerateNetworkID(network)
		pods, ok := p.addedPods.Get(networkID)
		if !ok {
			pods = []*kapi.Pod{pod}
		} else {
			pods = append(pods.([]*kapi.Pod), pod)
		}

		p.addedPods.Set(networkID, pods)
	}

	return nil
}
