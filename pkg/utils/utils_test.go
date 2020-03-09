package utils

import (
	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Utils", func() {
	Context("PodWantsNetwork", func() {
		It("Check pod if pod needs a cni network", func() {
			pod := &kapi.Pod{Spec: kapi.PodSpec{HostNetwork: false}}
			Expect(PodWantsNetwork(pod)).To(BeTrue())
		})
	})
	Context("PodScheduled", func() {
		It("Check pod if pod is scheduled", func() {
			pod := &kapi.Pod{Spec: kapi.PodSpec{NodeName: "test"}}
			Expect(PodScheduled(pod)).To(BeTrue())
		})
	})
	Context("HasNetworkAttachment", func() {
		It("Check pod if pod is has network attachment annotation", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{v1.NetworkAttachmentAnnot: `[{"name":"test"}]`}}}
			Expect(HasNetworkAttachment(pod)).To(BeTrue())
		})
	})
	Context("PodIsRunning", func() {
		It("Check pod if pod is is in running phase", func() {
			pod := &kapi.Pod{Status: kapi.PodStatus{Phase: kapi.PodRunning}}
			Expect(PodIsRunning(pod)).To(BeTrue())
		})
	})
	Context("ParseInfiniBandAnnotation", func() {
		It("Parse InfiniBand annotations", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{InfiniBandAnnotation: `{"testNetwork":"configured"}`}}}
			ibAnnotation, err := ParseInfiniBandAnnotation(pod)
			Expect(err).ToNot(HaveOccurred())
			Expect(ibAnnotation["testNetwork"]).To(Equal(ConfiguredInfiniBandPod))
		})
		It("Non existing InfiniBand annotations", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{}}}
			ibAnnotation, err := ParseInfiniBandAnnotation(pod)
			Expect(err).To(HaveOccurred())
			Expect(ibAnnotation).To(BeNil())
		})
		It("Parse invalid InfiniBand annotations", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{InfiniBandAnnotation: "invalid"}}}
			ibAnnotation, err := ParseInfiniBandAnnotation(pod)
			Expect(err).To(HaveOccurred())
			Expect(ibAnnotation).To(BeNil())
		})
	})
	Context("IsPodNetworkConfiguredWithInfiniBand", func() {
		It("Pod has InfiniBand annotation", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{InfiniBandAnnotation: `{"testNetwork":"configured"}`}}}
			ibAnnotation, err := ParseInfiniBandAnnotation(pod)
			Expect(err).ToNot(HaveOccurred())
			isConfigured := IsPodNetworkConfiguredWithInfiniBand(ibAnnotation, "testNetwork")
			Expect(isConfigured).To(BeTrue())
		})
		It("Nil InfiniBand annotations", func() {
			isConfigured := IsPodNetworkConfiguredWithInfiniBand(nil, "testNetwork")
			Expect(isConfigured).To(BeFalse())
		})
		It("Not configured network", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{InfiniBandAnnotation: `{"testNetwork":""}`}}}
			ibAnnotation, err := ParseInfiniBandAnnotation(pod)
			Expect(err).ToNot(HaveOccurred())
			isConfigured := IsPodNetworkConfiguredWithInfiniBand(ibAnnotation, "testNetwork")
			Expect(isConfigured).To(BeFalse())
		})
		It("Non existing network ", func() {
			pod := &kapi.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{InfiniBandAnnotation: `{"testNetwork":"configured"}`}}}
			ibAnnotation, err := ParseInfiniBandAnnotation(pod)
			Expect(err).ToNot(HaveOccurred())
			isConfigured := IsPodNetworkConfiguredWithInfiniBand(ibAnnotation, "non-exist")
			Expect(isConfigured).To(BeFalse())
		})
	})
	Context("PodNetworkHasGuid", func() {
		It("Pod network has guid", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{
				"guid": "02:00:00:00:00:00:00:00"}}
			hasGuid := PodNetworkHasGuid(network)
			Expect(hasGuid).To(BeTrue())
		})
		It("Pod network doesn't have guid", func() {
			network := &v1.NetworkSelectionElement{CNIArgs: &map[string]interface{}{}}
			hasGuid := PodNetworkHasGuid(network)
			Expect(hasGuid).To(BeFalse())
		})
		It("Pod network doesn't have \"cni-args\"", func() {
			network := &v1.NetworkSelectionElement{}
			hasGuid := PodNetworkHasGuid(network)
			Expect(hasGuid).To(BeFalse())
		})
	})
})
