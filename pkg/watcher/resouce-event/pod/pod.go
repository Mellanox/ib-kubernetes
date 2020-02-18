package pod

import (
	resEvent "github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event"

	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type podEvent struct {
}

func NewPodEvent() resEvent.ResourceEvent {
	return &podEvent{}
}

func (p *podEvent) GetResourceObject() runtime.Object {
	return &kapi.Pod{TypeMeta: metav1.TypeMeta{Kind: kapi.ResourcePods.String()}}
}

func (p *podEvent) OnAdd(obj interface{}) {

}
func (p *podEvent) OnUpdate(oldObj, newObj interface{}) {

}
func (p *podEvent) OnDelete(obj interface{}) {

}
