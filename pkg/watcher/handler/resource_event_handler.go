package handler

import (
	"github.com/Mellanox/ib-kubernetes/pkg/utils"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
)

type ResourceEventHandler interface {
	cache.ResourceEventHandler
	GetResourceObject() runtime.Object
	GetResults() (*utils.SynchronizedMap, *utils.SynchronizedMap)
}
