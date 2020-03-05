package k8s_client

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Kubernetes Client", func() {
	var fakeClient kubernetes.Interface
	var kubeClient Client
	BeforeSuite(func() {
		// Create k8s fake objects
		var objects []runtime.Object

		// add pods
		objects = append(objects, &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod1", Namespace: "default"}})
		objects = append(objects, &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod2", Namespace: "default",
			Annotations: map[string]string{"test": "approved"}}})
		objects = append(objects, &kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod3", Namespace: "kube-system"}})

		// add secret
		objects = append(objects, &kapi.Secret{ObjectMeta: metav1.ObjectMeta{Name: "password", Namespace: "kube-system"},
			Data: map[string][]byte{"password": []byte("test")}})

		// Create kubernetes fake client
		fakeClient = fake.NewSimpleClientset(objects...)
		kubeClient = &client{clientset: fakeClient}
	})

	Context("GetPods", func() {
		It("Get All pods in all namespaces", func() {

			podList, err := kubeClient.GetPods(kapi.NamespaceAll)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(podList.Items)).To(Equal(3))
		})
		It("GetPods in specific namespace", func() {

			podList, err := kubeClient.GetPods("kube-system")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(podList.Items)).To(Equal(1))
			Expect(podList.Items[0].Name).To(Equal("test-pod3"))
		})
		It("GetPods in non existing namespace", func() {

			podList, err := kubeClient.GetPods("foo")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(podList.Items)).To(Equal(0))
		})
	})
	Context("GetAnnotationOnPod", func() {
		It("Get annotations of existing pod", func() {

			annotations, err := kubeClient.GetAnnotationsOnPod("default", "test-pod2")
			Expect(err).ToNot(HaveOccurred())
			value, exist := annotations["test"]
			Expect(exist).To(BeTrue())
			Expect(value).To(Equal("approved"))
		})
		It("Get Annotations of non existing pod", func() {

			annotations, err := kubeClient.GetAnnotationsOnPod("foo", "foo")
			Expect(err).To(HaveOccurred())
			Expect(annotations).To(BeNil())
		})
	})
	Context("SetAnnotationOnPod", func() {
		It("Set Annotations on pod", func() {
			pods, err := kubeClient.GetPods("kube-system")
			Expect(err).ToNot(HaveOccurred())

			pod := &pods.Items[0]
			err = kubeClient.SetAnnotationOnPod(pod, "add", "new")
			Expect(err).ToNot(HaveOccurred())

			annotations, err := kubeClient.GetAnnotationsOnPod("kube-system", "test-pod3")
			Expect(err).ToNot(HaveOccurred())
			value, exist := annotations["add"]
			Expect(exist).To(BeTrue())
			Expect(value).To(Equal("new"))
		})
		It("Set Annotations on non existing pod", func() {
			pods, err := kubeClient.GetPods("kube-system")
			Expect(err).ToNot(HaveOccurred())

			pod := &pods.Items[0]
			pod.SetNamespace("foo")
			err = kubeClient.SetAnnotationOnPod(pod, "add", "new")
			Expect(err).To(HaveOccurred())
		})
	})
	Context("GetSecret", func() {
		It("Get Secret", func() {
			secret, err := kubeClient.GetSecret("kube-system", "password")
			Expect(err).ToNot(HaveOccurred())

			password := string(secret.Data["password"])

			Expect(password).To(Equal("test"))
		})
	})

})
