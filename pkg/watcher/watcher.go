package watcher

import (
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"

	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	resEventHandler "github.com/Mellanox/ib-kubernetes/pkg/watcher/handler"
)

type StopFunc func()

type Watcher interface {
	// Run Watcher in the background, listening for k8s resource events, until StopFunc is called
	RunBackground() StopFunc
	// Get ResourceEventHandler
	GetHandler() resEventHandler.ResourceEventHandler
}

type watcher struct {
	eventHandler resEventHandler.ResourceEventHandler
	watchList    cache.ListerWatcher
}

func NewWatcher(eventHandler resEventHandler.ResourceEventHandler, client k8sClient.Client) Watcher {
	resource := eventHandler.GetResourceObject().GetObjectKind().GroupVersionKind().Kind
	watchList := cache.NewListWatchFromClient(client.GetRestClient(), resource, kapi.NamespaceAll, fields.Everything())
	return &watcher{eventHandler: eventHandler, watchList: watchList}
}

// Run Watcher in the background, listening for k8s resource events, until StopFunc is called
func (w *watcher) RunBackground() StopFunc {
	stopChan := make(chan struct{})
	_, controller := cache.NewInformerWithOptions(cache.InformerOptions{
		ListerWatcher: w.watchList,
		ObjectType:    w.eventHandler.GetResourceObject(),
		ResyncPeriod:  0,
		Handler:       w.eventHandler,
	})
	go controller.Run(stopChan)
	return func() {
		stopChan <- struct{}{}
		close(stopChan)
	}
}

func (w *watcher) GetHandler() resEventHandler.ResourceEventHandler {
	return w.eventHandler
}
