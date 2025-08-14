// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

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

	var watchList cache.ListerWatcher
	if resource == "NetworkAttachmentDefinition" {
		// Use NAD-specific client for NetworkAttachmentDefinition resources
		watchList = cache.NewListWatchFromClient(
			client.GetNetClient().RESTClient(),
			"network-attachment-definitions",
			kapi.NamespaceAll,
			fields.Everything())
	} else {
		// Use standard client for other resources (Pods, etc.)
		watchList = cache.NewListWatchFromClient(
			client.GetRestClient(),
			resource,
			kapi.NamespaceAll,
			fields.Everything())
	}

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
