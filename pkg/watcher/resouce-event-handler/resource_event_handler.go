package resouce_event_handler

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
)

type ResourceEventHandler interface {
	cache.ResourceEventHandler
	GetResourceObject() runtime.Object
}
