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
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	cacheTesting "k8s.io/client-go/tools/cache/testing"

	k8sClientMock "github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"
	resEventHandler "github.com/Mellanox/ib-kubernetes/pkg/watcher/handler"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher/handler/mocks"
)

var _ = Describe("Kubernetes Watcher", func() {
	Context("NewWatcher", func() {
		It("Create new watcher", func() {
			fakeClient := fake.NewSimpleClientset()
			client := &k8sClientMock.Client{}
			eventHandler := resEventHandler.NewPodEventHandler()

			client.On("GetRestClient").Return(fakeClient.CoreV1().RESTClient())
			watcher := NewWatcher(eventHandler, client)
			Expect(watcher.GetHandler()).To(Equal(eventHandler))
		})
	})
	Context("RunBackground", func() {
		It("Run watcher listening for events", func() {
			eventHandler := &mocks.ResourceEventHandler{}
			wl := cacheTesting.NewFakeControllerSource()
			pod := &kapi.Pod{TypeMeta: metav1.TypeMeta{Kind: kapi.ResourcePods.String()},
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test",
					Annotations: map[string]string{"event": "none"}}}

			watcher := &watcher{eventHandler: eventHandler, watchList: wl}

			eventHandler.On("GetResource").Return(kapi.ResourcePods.String())
			eventHandler.On("GetResourceObject").Return(&kapi.Pod{})
			eventHandler.On("OnAdd", mock.Anything).Run(func(args mock.Arguments) {
				addedPod := args[0].(*kapi.Pod)
				annotations := addedPod.Annotations
				value, ok := annotations["event"]
				Expect(ok).To(BeTrue())
				Expect(value).To(Equal("none"))

				addedPod.Annotations["event"] = "add"
				pod = addedPod
			})
			eventHandler.On("OnUpdate", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				updatedPod := args[1].(*kapi.Pod)
				annotations := updatedPod.Annotations
				value, ok := annotations["event"]

				Expect(ok).To(BeTrue())
				Expect(value).To(Equal("add"))
			})
			eventHandler.On("OnDelete", mock.Anything).Run(func(args mock.Arguments) {
				deletedPod := args[0].(*kapi.Pod)
				Expect(deletedPod.Name).To(Equal(pod.Name))
			})

			stopFunc := watcher.RunBackground()
			// wait until the watcher start listening
			time.Sleep(1 * time.Second)
			wl.Add(pod)
			wl.Modify(pod)
			wl.Delete(pod)
			stopFunc()
		})
	})
})
