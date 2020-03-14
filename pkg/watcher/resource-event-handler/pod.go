package resource_event_handler

import (
	"fmt"
	"sync"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"

	"github.com/golang/glog"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

func (p *podEventHandler) OnAdd(obj interface{}) {
	glog.V(4).Infof("OnAdd(): pod %v", obj)
	pod := obj.(*kapi.Pod)
	glog.Infof("OnAdd(): namespace %s name %s", pod.Namespace, pod.Name)

	if !utils.PodWantsNetwork(pod) {
		return
	}

	if utils.PodIsRunning(pod) {
		return
	}

	if !utils.HasNetworkAttachment(pod) {
		return
	}

	if !utils.PodScheduled(pod) {
		p.retryPods.Store(pod.UID, true)
		return
	}

	if err := p.addNetworksFromPod(pod); err != nil {
		err = fmt.Errorf("OnAdd(): %v", err)
		glog.Error(err)
		return
	}

	glog.V(3).Infof("OnAdd(): successfully added namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) OnUpdate(oldObj, newObj interface{}) {
	glog.V(4).Infof("OnUpdate(): oldPod %v, newPod %v", oldObj, newObj)
	pod := newObj.(*kapi.Pod)
	glog.Infof("OnUpdate(): namespace %s name %s", pod.Namespace, pod.Name)

	if !utils.PodWantsNetwork(pod) {
		return
	}

	if utils.PodIsRunning(pod) {
		p.retryPods.Delete(pod.UID)
		return
	}

	if !utils.HasNetworkAttachment(pod) {
		return
	}

	_, retry := p.retryPods.Load(pod.UID)
	if !retry || !utils.PodScheduled(pod) {
		return
	}

	if err := p.addNetworksFromPod(pod); err != nil {
		err = fmt.Errorf("OnUpdate(): %v", err)
		glog.Error(err)
		return
	}

	p.retryPods.Delete(pod.UID)
	glog.V(3).Infof("OnUpdate(): successfully updated namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) OnDelete(obj interface{}) {
	glog.V(4).Infof("OnDelete(): pod %v", obj)
	pod := obj.(*kapi.Pod)
	glog.Infof("OnDelete(): namespace %s name %s", pod.Namespace, pod.Name)

	// make sure this pod won't be in the retry pods
	p.retryPods.Delete(pod.UID)

	if !utils.PodWantsNetwork(pod) {
		return
	}

	if !utils.HasNetworkAttachment(pod) {
		return
	}

	networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		err = fmt.Errorf("OnDelete(): failed to parse network annotations with error: %v", err)
		glog.Error(err)
		return
	}

	ibAnnotation, err := utils.ParseInfiniBandAnnotation(pod)
	if err != nil {
		err = fmt.Errorf("OnDelete(): %v", err)
		glog.Error(err)
		return
	}

	for _, network := range networks {
		if !utils.IsPodNetworkConfiguredWithInfiniBand(ibAnnotation, network.Name) {
			continue
		}

		//check if pod network has guid
		if !utils.PodNetworkHasGuid(network) {
			glog.Errorf("OnDelete(): pod %s has network %s marked as configured with InfiniBand without having guid",
				pod.Name, network.Name)
			continue
		}

		pods, ok := p.deletedPods.Get(network.Name)
		if !ok {
			pods = []*kapi.Pod{pod}
		} else {
			pods = append(pods.([]*kapi.Pod), pod)
		}
		p.deletedPods.Set(network.Name, pods)
	}

	glog.V(3).Infof("OnDelete(): successfully deleted namespace %s name %s", pod.Namespace, pod.Name)
}

func (p *podEventHandler) GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap) {
	return p.addedPods, p.deletedPods
}

func (p *podEventHandler) addNetworksFromPod(pod *kapi.Pod) error {
	glog.V(3).Info("addNetworksFromPod():")
	networks, err := netAttUtils.ParsePodNetworkAnnotation(pod)
	if err != nil {
		p.retryPods.Store(pod.UID, true)
		return fmt.Errorf("failed to parse network annotations with error: %v", err)
	}

	ibAnnotation, _ := utils.ParseInfiniBandAnnotation(pod)

	for _, network := range networks {
		// check if pod network is configured
		if utils.IsPodNetworkConfiguredWithInfiniBand(ibAnnotation, network.Name) {
			continue
		}

		pods, ok := p.addedPods.Get(network.Name)
		if !ok {
			pods = []*kapi.Pod{pod}
		} else {
			pods = append(pods.([]*kapi.Pod), pod)
		}

		p.addedPods.Set(network.Name, pods)
	}

	return nil
}
