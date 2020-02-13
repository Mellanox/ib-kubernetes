package pod

import (
	resEvent "github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event"

	"github.com/golang/glog"
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
	glog.Infof("OnAdd(): pod %v", obj)
}

func (p *podEvent) OnUpdate(oldObj, newObj interface{}) {
	glog.Infof("OnUpdate(): oldPod %v, newPod %v", oldObj, newObj)

}

func (p *podEvent) OnDelete(obj interface{}) {
	glog.Infof("OnDelete(): pod %v", obj)
}
