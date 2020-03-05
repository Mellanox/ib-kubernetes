package resouce_event_handler

import (
	"github.com/golang/glog"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type podEventHandler struct {
}

func NewPodEventHandler() ResourceEventHandler {
	return &podEventHandler{}
}

func (p *podEventHandler) GetResourceObject() runtime.Object {
	return &kapi.Pod{TypeMeta: metav1.TypeMeta{Kind: kapi.ResourcePods.String()}}
}

func (p *podEventHandler) OnAdd(obj interface{}) {
	glog.Infof("OnAdd(): pod %v", obj)
}

func (p *podEventHandler) OnUpdate(oldObj, newObj interface{}) {
	glog.Infof("OnUpdate(): oldPod %v, newPod %v", oldObj, newObj)

}

func (p *podEventHandler) OnDelete(obj interface{}) {
	glog.Infof("OnDelete(): pod %v", obj)
}
