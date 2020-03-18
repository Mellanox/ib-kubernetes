package resource_event_handler

import (
	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Pod Event Handler", func() {
	Context("Create new Pod Event Handler", func() {
		It("Create new Pod Event Handler", func() {
			podEventHandler := NewPodEventHandler()
			Expect(podEventHandler.GetResourceObject().GetObjectKind().GroupVersionKind().Kind).To(Equal("pods"))
		})
	})
	Context("OnAdd", func() {
		It("On add pod event", func() {
			pod1 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test"},{"name":"test2", "cni-args":{"mellanox.infiniband.app":"configured"}}]`}},
				Spec: kapi.PodSpec{NodeName: "test"}}
			pod2 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test"}]`}},
				Spec: kapi.PodSpec{NodeName: "test"}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnAdd(pod1)
			podEventHandler.OnAdd(pod2)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(1))
			pods := addMap.Items["test"].([]*kapi.Pod)
			Expect(len(pods)).To(Equal(2))
		})
		It("On add pod invalid cases", func() {
			// No network needed
			pod1 := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: true}}
			// Running pod
			pod2 := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodRunning}}
			// No network attachment annotation
			pod3 := &kapi.Pod{}
			// Not scheduled
			pod4 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test"}]`}}}
			// Invalid network attachment annotation
			pod5 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[invalid]`}},
				Spec: kapi.PodSpec{NodeName: "test"}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnAdd(pod1)
			podEventHandler.OnAdd(pod2)
			podEventHandler.OnAdd(pod3)
			podEventHandler.OnAdd(pod4)
			podEventHandler.OnAdd(pod5)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(0))
		})
	})
	Context("OnUpdate", func() {
		It("On update pod event", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test"},{"name":"test2"}]`}}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnAdd(pod)
			pod.Spec = kapi.PodSpec{NodeName: "test"}
			podEventHandler.OnUpdate(nil, pod)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(2))
			Expect(len(addMap.Items["test"].([]*kapi.Pod))).To(Equal(1))
			Expect(len(addMap.Items["test2"].([]*kapi.Pod))).To(Equal(1))
		})
		It("On update pod invalid cases", func() {
			// No network needed
			pod1 := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: true}}
			// Running pod
			pod2 := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodRunning}}
			// No network attachment annotation
			pod3 := &kapi.Pod{}
			// Not scheduled
			pod4 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test"}]`}}}
			// Invalid network attachment annotation
			pod5 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[invalid]`}},
				Spec: kapi.PodSpec{}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnUpdate(nil, pod1)
			podEventHandler.OnUpdate(nil, pod2)
			podEventHandler.OnUpdate(nil, pod3)
			podEventHandler.OnUpdate(nil, pod4)
			podEventHandler.OnAdd(pod5)
			pod5.Spec.NodeName = "test"
			podEventHandler.OnUpdate(nil, pod5)

			addMap, _ := podEventHandler.GetResults()
			Expect(len(addMap.Items)).To(Equal(0))
		})
	})
	Context("OnDelete", func() {
		It("On delete pod event", func() {
			pod1 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test","cni-args":{"guid":"02:00:00:00:02:00:00:00", "mellanox.infiniband.app":"configured"}},
				{"name":"test2", "mellanox.infiniband.app":"configured"}, {"name":"test3"}]`}}}
			pod2 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test","cni-args":{"guid":"02:00:00:00:02:00:00:01", "mellanox.infiniband.app":"configured"}}]`}}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnDelete(pod1)
			podEventHandler.OnDelete(pod2)

			_, delMap := podEventHandler.GetResults()
			Expect(len(delMap.Items)).To(Equal(1))
			Expect(len(delMap.Items["test"].([]*kapi.Pod))).To(Equal(2))
		})
		It("On delete pod invalid cases", func() {
			// No network needed
			pod1 := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: true}}
			// No network attachment annotation
			pod2 := &kapi.Pod{}
			// Invalid network attachment annotation
			pod3 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[invalid]`}},
				Spec: kapi.PodSpec{}}
			// InfiniBand configured without guid
			pod4 := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test", "cni-args":{"mellanox.infiniband.app":"configured"}}]`}},
				Spec: kapi.PodSpec{}}

			podEventHandler := NewPodEventHandler()
			podEventHandler.OnDelete(pod1)
			podEventHandler.OnDelete(pod2)
			podEventHandler.OnDelete(pod3)
			podEventHandler.OnDelete(pod4)

			_, delMap := podEventHandler.GetResults()
			Expect(len(delMap.Items)).To(Equal(0))
		})
	})
})
