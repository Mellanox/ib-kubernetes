package guid

import (
	"errors"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"

	"github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("GUID Pool", func() {
	Context("NewGuidPool", func() {
		It("Create guid pool with valid  parameters", func() {
			pool, err := NewGuidPool("02:00:00:00:00:00:00:00",
				"02:FF:FF:FF:FF:FF:FF:FF", nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())
		})
		It("Create guid pool with incorrect start guid", func() {
			pool, err := NewGuidPool("incorrect",
				"02:FF:FF:FF:FF:FF:FF:FF", nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with incorrect end guid", func() {
			pool, err := NewGuidPool("02:00:00:00:00:00:00:00",
				"incorrect", nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with not allowed start guid", func() {
			pool, err := NewGuidPool("00:00:00:00:00:00:00:00",
				"02:FF:FF:FF:FF:FF:FF:FF", nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with not allowed end guid", func() {
			pool, err := NewGuidPool("02:00:00:00:00:00:00:00",
				"FF:FF:FF:FF:FF:FF:FF:FF", nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with invalid  range", func() {
			pool, err := NewGuidPool("02:FF:FF:FF:FF:FF:FF:FF",
				"02:00:00:00:00:00:00:00", nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})

	})
	Context("InitPool", func() {
		It("Init pool with pods running pods allocated guid", func() {
			client := &mocks.Client{}
			pods := &kapi.PodList{Items: []kapi.Pod{}}
			pod1 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default", Annotations: map[string]string{
				utils.InfiniBandAnnotation: `{"test3":"configured"}`,
				v1.NetworkAttachmentAnnot:  `[{"name":"test3","cni-args":{"guid":"02:00:00:00:00:00:00:03"}}]`}}}
			pod2 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "default", Annotations: map[string]string{
				utils.InfiniBandAnnotation: `{"test":"configured", "test2":"configured"}`,
				v1.NetworkAttachmentAnnot: `[{"name":"test","cni-args":{"guid":"02:00:00:00:00:00:00:04"}},
                {"name":"test2","cni-args":{"guid":"02:00:00:00:00:00:00:05"}}]`}}}
			pod3 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p3", Namespace: "default", Annotations: map[string]string{
				utils.InfiniBandAnnotation: `{"test":"configured"}`,
				v1.NetworkAttachmentAnnot:  `[{"name":"test","cni-args":{"guid":"02:00:00:00:00:00:00:03"}}]`}}}
			pod4 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p4", Namespace: "foo", Annotations: map[string]string{
				utils.InfiniBandAnnotation: `{"test":"configured"}`,
				v1.NetworkAttachmentAnnot:  `[{"name":"test","namespace":"foo"}]`}}}
			pod5 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p5", Namespace: "foo", Annotations: map[string]string{
				utils.InfiniBandAnnotation: `{}`,
				v1.NetworkAttachmentAnnot:  `[{"name":"test","namespace":"foo"}]`}}}
			pod6 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p6", Namespace: "foo", Annotations: map[string]string{
				utils.InfiniBandAnnotation: `invalid`,
				v1.NetworkAttachmentAnnot:  `[{"name":"test","namespace":"foo"}]`}}}
			pod7 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p7", Namespace: "foo"}}

			pods.Items = append(pods.Items, pod1)
			pods.Items = append(pods.Items, pod2)
			pods.Items = append(pods.Items, pod3)
			pods.Items = append(pods.Items, pod4)
			pods.Items = append(pods.Items, pod5)
			pods.Items = append(pods.Items, pod6)
			pods.Items = append(pods.Items, pod7)

			client.On("GetPods", "").Return(pods, nil)
			pool, err := NewGuidPool("02:00:00:00:00:00:00:00",
				"02:FF:FF:FF:FF:FF:FF:FF", client)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())

			err = pool.InitPool()
			Expect(err).ToNot(HaveOccurred())
		})
		It("Init pool failed to get pods", func() {
			client := &mocks.Client{}

			client.On("GetPods", "").Return(nil, errors.New("err"))
			pool, err := NewGuidPool("02:00:00:00:00:00:00:00",
				"02:FF:FF:FF:FF:FF:FF:FF", client)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())

			err = pool.InitPool()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("InitPool(): failed to get pods from kubernetes: err"))
		})
	})
	Context("GenerateGUID", func() {
		It("Generate guid when range is not full", func() {
			pool, err := NewGuidPool("00:00:00:00:00:00:01:00", "00:00:00:00:00:00:01:01", nil)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID("")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal("00:00:00:00:00:00:01:00"))
			guid, err = pool.GenerateGUID("")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate and release guid then re-allocate the newly released guids", func() {
			pool, err := NewGuidPool("00:00:00:00:00:00:01:00", "00:00:00:00:00:00:01:ff",
				nil)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID("")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal("00:00:00:00:00:00:01:00"))
			err = pool.ReleaseGUID(guid)
			Expect(err).ToNot(HaveOccurred())

			// Generate all the range
			Expect(err).ToNot(HaveOccurred())
			for i := 0; i < 255; i++ {
				_, err = pool.GenerateGUID("pod" + string(i))
				Expect(err).ToNot(HaveOccurred())
			}

			// After the last guid in the pool was allocated then the pool check back from first guid
			guid, err = pool.GenerateGUID("")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal("00:00:00:00:00:00:01:00"))
		})
		It("Generate guid when current guid is allocated", func() {
			p, err := NewGuidPool("00:00:00:00:00:00:01:00", "00:00:00:00:00:00:01:01", nil)
			Expect(err).ToNot(HaveOccurred())
			err = p.AllocateGUID("", "00:00:00:00:00:00:01:00")
			Expect(err).ToNot(HaveOccurred())

			guid, err := p.GenerateGUID("")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate guid when range is full", func() {
			pool, err := NewGuidPool("00:00:00:00:00:00:01:00", "00:00:00:00:00:00:01:00", nil)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID("")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid).To(Equal("00:00:00:00:00:00:01:00"))
			guid, err = pool.GenerateGUID("")
			Expect(err).To(HaveOccurred())
			Expect(guid).To(Equal(""))
		})
	})
	Context("AllocateGUID", func() {
		It("Allocate guid from the pool", func() {
			pool := &guidPool{guidPoolMap: map[string]bool{}, guidPodNetworkMap: map[string]string{}}
			err := pool.AllocateGUID("", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
		})
		It("Allocate an allocated guid from the pool for the same pod", func() {
			pool := &guidPool{guidPoolMap: map[string]bool{}, guidPodNetworkMap: map[string]string{}}
			err := pool.AllocateGUID("pod1", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("pod1", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
		})
		It("Allocate an allocated guid from the pool", func() {
			pool := &guidPool{guidPoolMap: map[string]bool{}, guidPodNetworkMap: map[string]string{}}
			err := pool.AllocateGUID("pod1", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("pod2", "02:00:00:00:00:00:00:00")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate invalid guid from the pool", func() {
			pool := &guidPool{guidPoolMap: map[string]bool{}}
			err := pool.AllocateGUID("", "invalid")
			Expect(err).To(HaveOccurred())
		})
	})
	Context("ReleaseGUID", func() {
		It("release existing allocated guid", func() {
			guid := "02:00:00:00:00:00:00:00"
			pool := &guidPool{guidPoolMap: map[string]bool{guid: true}}

			err := pool.ReleaseGUID(guid)
			Expect(err).ToNot(HaveOccurred())
		})
		It("release non existing allocated guid", func() {
			guid := "02:00:00:00:00:00:00:00"
			pool := &guidPool{guidPoolMap: map[string]bool{}}

			err := pool.ReleaseGUID(guid)
			Expect(err).To(HaveOccurred())
		})
	})
})
