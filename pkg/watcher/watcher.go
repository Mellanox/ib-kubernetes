package watcher

import (
	"time"

	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	resEventHandler "github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event-handler"

	"github.com/golang/glog"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
)

type Watcher interface {
	Run()
}

type watcher struct {
	eventHandler resEventHandler.ResourceEventHandler
	watchList    cache.ListerWatcher
	stopChan     chan struct{}
}

func NewWatcher(eventHandler resEventHandler.ResourceEventHandler, client k8sClient.Client) Watcher {
	glog.Info("NewWatcher():")
	resource := eventHandler.GetResourceObject().GetObjectKind().GroupVersionKind().Kind
	watchList := cache.NewListWatchFromClient(client.GetRestClient(), resource, kapi.NamespaceAll, fields.Everything())
	return &watcher{eventHandler: eventHandler, watchList: watchList, stopChan: make(chan struct{})}
}

// Run for for infinite listening for k8s resource events
func (w *watcher) Run() {
	_, controller := cache.NewInformer(w.watchList, w.eventHandler.GetResourceObject(), time.Second*0, w.eventHandler)
	controller.Run(w.stopChan)
	close(w.stopChan)
}
