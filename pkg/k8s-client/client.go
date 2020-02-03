package k8s_client

import (
	"time"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
)

type Client interface {
	GetPods(namespace string) (*kapi.PodList, error)
	GetAnnotationsOnPod(pod *kapi.Pod) (map[string]string, error)
	SetAnnotationOnPod(pod *kapi.Pod, key, value string) error
	SetAnnotationsOnPod(pod *kapi.Pod, annotations map[string]string) error
	UpdatePod(pod *kapi.Pod) error
	GetSecret(namespace, name string) (*kapi.Secret, error)
	NewListWatch(c cache.Getter, resource string, namespace string, fieldSelector fields.Selector) *cache.ListWatch
	NewInformer(lw cache.ListerWatcher, objType runtime.Object, resyncPeriod time.Duration, h cache.ResourceEventHandler) (cache.Store, cache.Controller)
}
