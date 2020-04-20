package handler

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/Mellanox/ib-kubernetes/pkg/utils"
)

type ResourceEventHandler interface {
	cache.ResourceEventHandler
	GetResourceObject() runtime.Object
	GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap)
}
