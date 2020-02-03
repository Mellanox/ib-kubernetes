package pod

import (
	resEvent "github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event"
)

type podEvent struct {
}

func NewPodEvent() resEvent.ResourceEvent {
	return &podEvent{}
}

func (p *podEvent) GetResource() string {
	return "pod"
}

func (p *podEvent) OnAdd(obj interface{}) {

}
func (p *podEvent) OnUpdate(oldObj, newObj interface{}) {

}
func (p *podEvent) OnDelete(obj interface{}) {

}
