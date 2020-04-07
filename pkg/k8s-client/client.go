package k8s_client

import (
	"encoding/json"
	"fmt"

	"github.com/golang/glog"
	netapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netclient "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/clientset/versioned/typed/k8s.cni.cncf.io/v1"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

type Client interface {
	GetPods(namespace string) (*kapi.PodList, error)
	SetAnnotationsOnPod(pod *kapi.Pod, annotations map[string]string) error
	PatchPod(pod *kapi.Pod, patchType types.PatchType, patchData []byte) error
	GetNetworkAttachmentDefinition(namespace, name string) (*netapi.NetworkAttachmentDefinition, error)
	GetRestClient() rest.Interface
}

type client struct {
	clientset kubernetes.Interface
	netClient netclient.K8sCniCncfIoV1Interface
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

	netClient, err := netclient.NewForConfig(conf)
	if err != nil {
		err = fmt.Errorf("unable to create a network attachment client: %v", err)
		glog.Error(err)
		return nil, err
	}

	return &client{clientset: clientset, netClient: netClient}, nil
}

// GetPods obtains the Pods resources from kubernetes api server for given namespace
func (c *client) GetPods(namespace string) (*kapi.PodList, error) {
	glog.V(3).Infof("GetPods(): namespace %s", namespace)
	return c.clientset.CoreV1().Pods(namespace).List(metav1.ListOptions{})
}

// SetAnnotationsOnPod takes the pod object and map of key/value string pairs to set as annotations
func (c *client) SetAnnotationsOnPod(pod *kapi.Pod, annotations map[string]string) error {
	glog.V(3).Infof("setAnnotationOnPod(): namespace: %s, podName: %s, annotations: %v",
		pod.Namespace, pod.Name, annotations)
	var err error
	var patchData []byte
	patch := struct {
		Metadata map[string]interface{} `json:"metadata"`
	}{
		Metadata: map[string]interface{}{
			"annotations": annotations,
		},
	}

	podDesc := pod.Namespace + "/" + pod.Name
	glog.Infof("Setting annotations %v on pod %s", annotations, podDesc)
	patchData, err = json.Marshal(&patch)
	if err != nil {
		glog.Errorf("Error in setting annotations on pod %s: %v", podDesc, err)
		return err
	}
	return c.PatchPod(pod, types.MergePatchType, patchData)
}

// PatchPod applies the patch changes
func (c *client) PatchPod(pod *kapi.Pod, patchType types.PatchType, patchData []byte) error {
	glog.V(3).Infof("PatchPod(): namespace: %s, podName: %s", pod.Namespace, pod.Name)
	_, err := c.clientset.CoreV1().Pods(pod.Namespace).Patch(pod.Name, patchType, patchData)
	return err
}

// GetNetworkAttachmentDefinition returns the network crd from kubernetes api server for given namespace and name
func (c *client) GetNetworkAttachmentDefinition(namespace, name string) (*netapi.NetworkAttachmentDefinition, error) {
	glog.V(3).Infof("GetNetworkAttachmentDefinition(): namespace %s, name: %s", namespace, name)
	return c.netClient.NetworkAttachmentDefinitions(namespace).Get(name, metav1.GetOptions{})
}

// GetRestClient returns the client rest api for k8s
func (c *client) GetRestClient() rest.Interface {
	glog.V(3).Info("GetRestClient():")
	return c.clientset.CoreV1().RESTClient()
}
