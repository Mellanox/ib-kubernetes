package resouce_event

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
)

type ResourceEvent interface {
	cache.ResourceEventHandler
	GetResourceObject() runtime.Object
}
