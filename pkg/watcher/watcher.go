package watcher

import (
	"time"

	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	resEvent "github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event"

	"github.com/golang/glog"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
)

type Watcher interface {
	Run()
}

type watcher struct {
	event     resEvent.ResourceEvent
	watchList cache.ListerWatcher
	stopChan  chan struct{}
}

func NewWatcher(event resEvent.ResourceEvent, client k8sClient.Client) Watcher {
	glog.Info("NewWatcher():")
	resource := event.GetResourceObject().GetObjectKind().GroupVersionKind().Kind
	watchList := cache.NewListWatchFromClient(client.GetRestClient(), resource, kapi.NamespaceAll, fields.Everything())
	return &watcher{event: event, watchList: watchList, stopChan: make(chan struct{})}
}

// Run for for infinite listening for k8s resource events
func (w *watcher) Run() {
	glog.Info("Run(): resource event")
	_, controller := cache.NewInformer(w.watchList, w.event.GetResourceObject(), time.Second*0, w.event)
	controller.Run(w.stopChan)
	close(w.stopChan)
}
