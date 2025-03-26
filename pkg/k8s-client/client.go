package k8sclient

import (
	"context"
	"encoding/json"
	"fmt"

	netapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netclient "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/clientset/versioned/typed/k8s.cni.cncf.io/v1"
	"github.com/rs/zerolog/log"
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
	AddFinalizerToNetworkAttachmentDefinition(namespace, name, finalizer string) error
	RemoveFinalizerFromNetworkAttachmentDefinition(namespace, name, finalizer string) error
}

type client struct {
	clientset kubernetes.Interface
	netClient netclient.K8sCniCncfIoV1Interface
}

// NewK8sClient returns a kubernetes client
func NewK8sClient() (Client, error) {
	// Get a config to talk to the api server
	log.Debug().Msg("Setting up kubernetes client")
	conf, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("unable to set up client config error %v", err)
	}

	clientset, err := kubernetes.NewForConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("unable to create a kubernetes client error %v", err)
	}

	netClient, err := netclient.NewForConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("unable to create a network attachment client: %v", err)
	}

	return &client{clientset: clientset, netClient: netClient}, nil
}

// GetPods obtains the Pods resources from kubernetes api server for given namespace
func (c *client) GetPods(namespace string) (*kapi.PodList, error) {
	log.Debug().Msgf("getting pods in namespace %s", namespace)
	return c.clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
}

// SetAnnotationsOnPod takes the pod object and map of key/value string pairs to set as annotations
func (c *client) SetAnnotationsOnPod(pod *kapi.Pod, annotations map[string]string) error {
	log.Info().Msgf("Setting annotation on pod, namespace: %s, podName: %s, annotations: %v",
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
	patchData, err = json.Marshal(&patch)
	if err != nil {
		return fmt.Errorf("failed to set annotations on pod %s: %v", podDesc, err)
	}
	return c.PatchPod(pod, types.MergePatchType, patchData)
}

// PatchPod applies the patch changes
func (c *client) PatchPod(pod *kapi.Pod, patchType types.PatchType, patchData []byte) error {
	log.Debug().Msgf("patch pod, namespace: %s, podName: %s", pod.Namespace, pod.Name)
	_, err := c.clientset.CoreV1().Pods(pod.Namespace).Patch(
		context.TODO(), pod.Name, patchType, patchData, metav1.PatchOptions{})
	return err
}

// GetNetworkAttachmentDefinition returns the network crd from kubernetes api server for given namespace and name
func (c *client) GetNetworkAttachmentDefinition(namespace, name string) (*netapi.NetworkAttachmentDefinition, error) {
	log.Debug().Msgf("getting NetworkAttachmentDefinition namespace %s, name: %s", namespace, name)
	return c.netClient.NetworkAttachmentDefinitions(namespace).Get(context.TODO(), name, metav1.GetOptions{})
}

// GetRestClient returns the client rest api for k8s
func (c *client) GetRestClient() rest.Interface {
	return c.clientset.CoreV1().RESTClient()
}

// AddFinalizerToNetworkAttachmentDefinition adds a finalizer to a NetworkAttachmentDefinition
func (c *client) AddFinalizerToNetworkAttachmentDefinition(namespace, name, finalizer string) error {
	netAttDef, err := c.GetNetworkAttachmentDefinition(namespace, name)
	if err != nil {
		return err
	}

	// Check if finalizer already exists
	for _, existingFinalizer := range netAttDef.Finalizers {
		if existingFinalizer == finalizer {
			return nil // Finalizer already exists, nothing to do
		}
	}

	// Add the finalizer
	netAttDef.Finalizers = append(netAttDef.Finalizers, finalizer)

	// Update the NetworkAttachmentDefinition
	_, err = c.netClient.NetworkAttachmentDefinitions(namespace).Update(
		context.Background(), netAttDef, metav1.UpdateOptions{})
	return err
}

// RemoveFinalizerFromNetworkAttachmentDefinition removes a finalizer from a NetworkAttachmentDefinition
func (c *client) RemoveFinalizerFromNetworkAttachmentDefinition(namespace, name, finalizer string) error {
	netAttDef, err := c.GetNetworkAttachmentDefinition(namespace, name)
	if err != nil {
		return err
	}

	// Check if finalizer exists
	var found bool
	var updatedFinalizers []string
	for _, existingFinalizer := range netAttDef.Finalizers {
		if existingFinalizer != finalizer {
			updatedFinalizers = append(updatedFinalizers, existingFinalizer)
		} else {
			found = true
		}
	}

	// If finalizer wasn't found, nothing to do
	if !found {
		return nil
	}

	// Update finalizers
	netAttDef.Finalizers = updatedFinalizers

	// Update the NetworkAttachmentDefinition
	_, err = c.netClient.NetworkAttachmentDefinitions(namespace).Update(
		context.Background(), netAttDef, metav1.UpdateOptions{})
	return err
}
