package k8s_client

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	netapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/clientset/versioned/scheme"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

type Client interface {
	GetPods(namespace string) (*kapi.PodList, error)
	GetAnnotationsOnPod(namespace, name string) (map[string]string, error)
	SetAnnotationOnPod(pod *kapi.Pod, key, value string) error
	PatchPod(pod *kapi.Pod, patchType types.PatchType, patchData []byte) error
	GetSecret(namespace, name string) (*kapi.Secret, error)
	GetNetworkAttachmentDefinition(namespace, name string) (*netapi.NetworkAttachmentDefinition, error)
	GetRestClient() rest.Interface
}

type client struct {
	clientset kubernetes.Interface
}

// NewK8sClient returns a kubernetes client
func NewK8sClient() (Client, error) {
	glog.V(3).Info("NewK8sClient():")
	// Get a config to talk to the api server
	glog.Info("Setting up kubernetes client")
	conf, err := config.GetConfig()
	if err != nil {
		err = fmt.Errorf("unable to set up client config error %v", err)
		glog.Error(err)
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(conf)
	if err != nil {
		err = fmt.Errorf("unable to create a kubernetes client error %v", err)
		glog.Error(err)
		return nil, err
	}
	return &client{clientset}, nil
}

// GetPods obtains the Pods resources from kubernetes api server for given namespace
func (c *client) GetPods(namespace string) (*kapi.PodList, error) {
	glog.V(3).Infof("GetPods(): namespace %s", namespace)
	return c.clientset.CoreV1().Pods(namespace).List(metav1.ListOptions{})
}

// GetAnnotationsOnPod obtains the pod annotations from kubernetes api server for given namespace and name
func (c *client) GetAnnotationsOnPod(namespace, name string) (map[string]string, error) {
	glog.V(3).Infof("GetAnnotationsOnPod(): namespace %s, name %s", namespace, name)
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod %v", err)
	}

	return pod.Annotations, nil
}

// SetAnnotationOnPod takes the pod object and key/value string pair to set it as an annotation
func (c *client) SetAnnotationOnPod(pod *kapi.Pod, key, value string) error {
	glog.V(3).Infof("setAnnotationOnPod(): namespace: %s, podName: %s, key: %s, value: %s",
		pod.Namespace, pod.Name, key, value)
	// escape double quotes in the annotation value so it can be sent as a JSON patch
	value = strings.Replace(value, "\"", "\\\"", -1)
	patchData := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s"}}}`, key, value)
	return c.PatchPod(pod, types.MergePatchType, []byte(patchData))
}

// PatchPod applies the patch changes
func (c *client) PatchPod(pod *kapi.Pod, patchType types.PatchType, patchData []byte) error {
	glog.V(3).Infof("PatchPod(): namespace: %s, podName: %s", pod.Namespace, pod.Name)
	_, err := c.clientset.CoreV1().Pods(pod.Namespace).Patch(pod.Name, patchType, patchData)
	return err
}

// GetSecret returns the Secret resource from kubernetes api server for given namespace and name
func (c *client) GetSecret(namespace, name string) (*kapi.Secret, error) {
	glog.V(3).Infof("GetSecret(): namespace %s, name: %s", namespace, name)
	return c.clientset.CoreV1().Secrets(namespace).Get(name, metav1.GetOptions{})
}

// GetNetworkAttachmentDefinition returns the network crd from kubernetes api server for given namespace and name
func (c *client) GetNetworkAttachmentDefinition(namespace, name string) (*netapi.NetworkAttachmentDefinition, error) {
	glog.V(3).Infof("GetNetworkAttachmentDefinition(): namespace %s, name: %s", namespace, name)
	result := &netapi.NetworkAttachmentDefinition{}
	err := c.GetRestClient().Get().
		Namespace(namespace).
		Resource("network-attachment-definitions").
		Name(name).
		VersionedParams(&metav1.GetOptions{}, scheme.ParameterCodec).
		Do().
		Into(result)
	return result, err
}

// GetRestClient returns the client rest api for k8s
func (c *client) GetRestClient() rest.Interface {
	glog.V(3).Info("GetRestClient():")
	return c.clientset.CoreV1().RESTClient()
}
