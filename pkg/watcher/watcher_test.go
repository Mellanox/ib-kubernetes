package watcher

import (
	"time"

	resEventHandler "github.com/Mellanox/ib-kubernetes/pkg/watcher/resource-event-handler"

	"github.com/stretchr/testify/mock"

	k8sClientMock "github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher/resource-event-handler/mocks"

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
