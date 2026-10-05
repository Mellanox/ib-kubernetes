// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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

package k8sclient

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestClient(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kubernetes Client Suite")
}

var _ = Describe("Conditional Pod annotations", func() {
	DescribeTable("rejects missing identity before sending any request", func(pod *corev1.Pod) {
		kube := &client{clientset: nil, netClient: nil}
		Expect(kube.SetAnnotationsOnPod(pod, map[string]string{"owned": "new"})).To(MatchError(ContainSubstring("UID and resourceVersion are required")))
	},
		Entry("nil Pod", (*corev1.Pod)(nil)),
		Entry("missing UID", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "1"}}),
		Entry("missing resourceVersion", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "owner"}}),
	)
})

var _ = Describe("Conditional Pod annotations against the API server", Ordered, func() {
	var environment *envtest.Environment
	var kube *client
	var pod *corev1.Pod

	BeforeAll(func() {
		// make test-coverage provisions these assets. Keep ordinary unit-test runs
		// usable on hosts without a local API server, but never substitute a fake
		// for the server-side UID/resourceVersion validation exercised here.
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Skip("set KUBEBUILDER_ASSETS to run API-server precondition tests")
		}
		environment = &envtest.Environment{}
		cfg, err := environment.Start()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(environment.Stop()).To(Succeed()) })
		clientset, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		kube = &client{clientset: clientset, netClient: nil}
	})

	BeforeEach(func() {
		created, err := kube.clientset.CoreV1().Pods("default").Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "conditional-", Namespace: "default", Annotations: map[string]string{"unrelated": "preserve", "owned": "old"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "example.invalid/test"}}},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		pod = created
	})

	It("reads current identity and patches only supplied annotations", func() {
		live, err := kube.GetPod(pod.Namespace, pod.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(live.UID).To(Equal(pod.UID))
		Expect(live.ResourceVersion).NotTo(BeEmpty())
		Expect(kube.SetAnnotationsOnPod(live, map[string]string{"owned": "new"})).To(Succeed())
		updated, err := kube.GetPod(pod.Namespace, pod.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Annotations).To(Equal(map[string]string{"unrelated": "preserve", "owned": "new"}))
	})

	It("leaves annotations unchanged for an empty patch", func() {
		Expect(kube.SetAnnotationsOnPod(pod, nil)).To(Succeed())
		live, err := kube.GetPod(pod.Namespace, pod.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(live.Annotations).To(Equal(pod.Annotations))
	})

	It("rejects a stale resourceVersion without overwriting concurrent changes", func() {
		Expect(kube.SetAnnotationsOnPod(pod, map[string]string{"owned": "concurrent"})).To(Succeed())
		err := kube.SetAnnotationsOnPod(pod, map[string]string{"owned": "stale"})
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "expected resourceVersion conflict, got %v", err)
		live, err := kube.GetPod(pod.Namespace, pod.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(live.Annotations["owned"]).To(Equal("concurrent"))
	})

	It("rejects another UID even with the current resourceVersion", func() {
		wrongOwner := pod.DeepCopy()
		wrongOwner.UID = "different-owner"
		Expect(kube.SetAnnotationsOnPod(wrongOwner, map[string]string{"owned": "wrong"})).NotTo(Succeed())
		live, err := kube.GetPod(pod.Namespace, pod.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(live.Annotations["owned"]).To(Equal("old"))
	})

	It("does not annotate a same-name replacement", func() {
		zero := int64(0)
		Expect(kube.clientset.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})).To(Succeed())
		Eventually(func() bool {
			_, err := kube.GetPod(pod.Namespace, pod.Name)
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
		replacement, err := kube.clientset.CoreV1().Pods(pod.Namespace).Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, Annotations: map[string]string{"owned": "replacement"}},
			Spec:       pod.Spec,
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(replacement.UID).NotTo(Equal(pod.UID))
		Expect(kube.SetAnnotationsOnPod(pod, map[string]string{"owned": "stale"})).NotTo(Succeed())
		live, err := kube.GetPod(pod.Namespace, pod.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(live.Annotations).To(Equal(map[string]string{"owned": "replacement"}))
	})
})
