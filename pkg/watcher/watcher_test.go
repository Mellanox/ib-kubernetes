package watcher

import (
	"github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event/pod"
	"time"

	k8sClientMock "github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher/resouce-event/mocks"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	cacheTesting "k8s.io/client-go/tools/cache/testing"
)

var _ = Describe("Kubernetes Watcher", func() {
	Context("NewWatcher", func() {
		It("Create new watcher", func() {
			fakeClient := fake.NewSimpleClientset()
			client := &k8sClientMock.Client{}
			event := pod.NewPodEvent()

			client.On("GetRestClient").Return(fakeClient.CoreV1().RESTClient())
			obj := NewWatcher(event, client)
			watcher := obj.(*watcher)
			Expect(watcher.watchList).ToNot(BeNil())
		})
	})
	Context("Run", func() {
		It("Run watcher listening for events", func() {
			event := &mocks.ResourceEvent{}
			wl := cacheTesting.NewFakeControllerSource()
			pod := &kapi.Pod{TypeMeta: metav1.TypeMeta{Kind: kapi.ResourcePods.String()},
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test",
					Annotations: map[string]string{"event": "none"}}}

			watcher := &watcher{event: event, watchList: wl, stopChan: make(chan struct{})}

			event.On("GetResource").Return(kapi.ResourcePods.String())
			event.On("GetResourceObject").Return(&kapi.Pod{})
			event.On("OnAdd", mock.Anything).Run(func(args mock.Arguments) {
				addedPod := args[0].(*kapi.Pod)
				annotations := addedPod.Annotations
				value, ok := annotations["event"]
				Expect(ok).To(BeTrue())
				Expect(value).To(Equal("none"))

				addedPod.Annotations["event"] = "add"
				pod = addedPod
			})
			event.On("OnUpdate", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				updatedPod := args[1].(*kapi.Pod)
				annotations := updatedPod.Annotations
				value, ok := annotations["event"]

				Expect(ok).To(BeTrue())
				Expect(value).To(Equal("add"))
			})
			event.On("OnDelete", mock.Anything).Run(func(args mock.Arguments) {
				deletedPod := args[0].(*kapi.Pod)
				Expect(deletedPod.Name).To(Equal(pod.Name))
			})

			go func() {
				// wait until the watcher start listening
				time.Sleep(1 * time.Second)

				wl.Add(pod)
				wl.Modify(pod)
				wl.Delete(pod)

				// needed to stop all the running goroutines
				watcher.stopChan <- struct{}{}
				watcher.stopChan <- struct{}{}
				watcher.stopChan <- struct{}{}
				watcher.stopChan <- struct{}{}
				watcher.stopChan <- struct{}{}
			}()

			watcher.Run()

			// Check the channel is closed
			_, ok := <-watcher.stopChan
			Expect(ok).To(BeFalse())
		})
	})
})
