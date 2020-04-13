package guid

import (
	"errors"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/k8s-client/mocks"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("GUID Pool", func() {
	conf := &config.GuidPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
	Context("NewGuidPool", func() {
		It("Create guid pool with valid  parameters", func() {
			pool, err := NewGuidPool(conf, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())
		})
		It("Create guid pool with invalid start guid", func() {
			invalidConf := &config.GuidPoolConfig{RangeStart: "invalid", RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewGuidPool(invalidConf, nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with invalid end guid", func() {
			invalidConf := &config.GuidPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "invalid"}
			pool, err := NewGuidPool(invalidConf, nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with not allowed start guid", func() {
			invalidRangeStartConf := &config.GuidPoolConfig{RangeStart: "00:00:00:00:00:00:00:00", RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewGuidPool(invalidRangeStartConf, nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with not allowed end guid", func() {
			invalidRangeEndConf := &config.GuidPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "FF:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewGuidPool(invalidRangeEndConf, nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with invalid range", func() {
			invalidRangeConf := &config.GuidPoolConfig{RangeStart: "02:FF:FF:FF:FF:FF:FF:FF", RangeEnd: "02:00:00:00:00:00:00:00"}
			pool, err := NewGuidPool(invalidRangeConf, nil)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})

	})
	Context("InitPool", func() {
		It("Init pool with pods running pods allocated guid", func() {
			client := &mocks.Client{}
			pods := &kapi.PodList{Items: []kapi.Pod{}}
			pod1 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default", Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test3","cni-args":{"guid":"02:00:00:00:00:00:00:03", "mellanox.infiniband.app":"configured"}}]`}}}
			pod2 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "default", Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test","cni-args":{"guid":"02:00:00:00:00:00:00:04", "mellanox.infiniband.app":"configured"}},
                {"name":"test2","cni-args":{"guid":"02:00:00:00:00:00:00:05", "mellanox.infiniband.app":"configured"}}]`}}}
			pod3 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p3", Namespace: "default", Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test","cni-args":{"guid":"02:00:00:00:00:00:00:03", "mellanox.infiniband.app":"configured"}}]`}}}
			pod4 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p4", Namespace: "foo", Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test","namespace":"foo", "cni-args":{"mellanox.infiniband.app":"configured"}}]`}}}
			pod5 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p5", Namespace: "foo", Annotations: map[string]string{
				v1.NetworkAttachmentAnnot: `[{"name":"test","namespace":"foo"}]`}}}
			pod6 := kapi.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p6", Namespace: "foo"}}

			pods.Items = append(pods.Items, pod1)
			pods.Items = append(pods.Items, pod2)
			pods.Items = append(pods.Items, pod3)
			pods.Items = append(pods.Items, pod4)
			pods.Items = append(pods.Items, pod5)
			pods.Items = append(pods.Items, pod6)

			client.On("GetPods", "").Return(pods, nil)
			pool, err := NewGuidPool(conf, client)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())

			err = pool.InitPool()
			Expect(err).ToNot(HaveOccurred())
		})
		It("Init pool failed to get pods", func() {
			client := &mocks.Client{}

			client.On("GetPods", "").Return(nil, errors.New("err"))
			pool, err := NewGuidPool(conf, client)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())

			err = pool.InitPool()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("failed to get pods from kubernetes: err"))
		})
	})
	Context("GenerateGUID", func() {
		It("Generate guid when range is not full", func() {
			poolConfig := &config.GuidPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:01"}
			pool, err := NewGuidPool(poolConfig, nil)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(pool.AllocateGUID("", "", guid.String())).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			guid, err = pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate and release guid then re-allocate the newly released guids", func() {
			poolConfig := &config.GuidPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:ff"}
			pool, err := NewGuidPool(poolConfig, nil)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			Expect(pool.AllocateGUID("", "", guid.String())).ToNot(HaveOccurred())
			err = pool.ReleaseGUID(guid.String())
			Expect(err).ToNot(HaveOccurred())

			// Generate all the range
			Expect(err).ToNot(HaveOccurred())
			for i := 0; i < 255; i++ {
				guid, err = pool.GenerateGUID()
				Expect(err).ToNot(HaveOccurred())
				Expect(pool.AllocateGUID("", "", guid.String())).ToNot(HaveOccurred())
			}

			// After the last guid in the pool was allocated then the pool check back from first guid
			guid, err = pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
		})
		It("Generate guid when current guid is allocated", func() {
			poolConfig := &config.GuidPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:01"}
			p, err := NewGuidPool(poolConfig, nil)
			Expect(err).ToNot(HaveOccurred())
			err = p.AllocateGUID("", "", "00:00:00:00:00:00:01:00")
			Expect(err).ToNot(HaveOccurred())

			guid, err := p.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate guid when range is full", func() {
			poolConfig := &config.GuidPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:00"}
			pool, err := NewGuidPool(poolConfig, nil)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			Expect(pool.AllocateGUID("pod1", "test", guid.String())).ToNot(HaveOccurred())
			_, err = pool.GenerateGUID()
			Expect(err).To(HaveOccurred())
		})
	})
	Context("AllocateGUID", func() {
		It("Allocate guid from the pool", func() {
			pool, err := NewGuidPool(conf, nil)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("", "", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
		})
		It("Allocate out of range guid from the pool", func() {
			pool, err := NewGuidPool(conf, nil)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("pod1", "test", "55:00:00:00:00:00:00:FF")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate an allocated guid from the pool for the same pod", func() {
			pool, err := NewGuidPool(conf, nil)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("pod1", "test", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("pod1", "test", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
		})
		It("Allocate an allocated guid from the pool", func() {
			pool, err := NewGuidPool(conf, nil)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("pod1", "test", "02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("pod2", "test", "02:00:00:00:00:00:00:00")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate invalid guid from the pool", func() {
			pool := &guidPool{guidPoolMap: map[GUID]bool{}}
			err := pool.AllocateGUID("", "", "invalid")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate valid network address but invalid guid from the pool", func() {
			pool, err := NewGuidPool(conf, nil)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("", "", "00:00:00:00:00:00:00:00")
			Expect(err).To(HaveOccurred())
		})
	})
	Context("ReleaseGUID", func() {
		It("release existing allocated guid", func() {
			guid := "00:00:00:00:00:00:00:01"
			pool := &guidPool{guidPoolMap: map[GUID]bool{1: true}}

			err := pool.ReleaseGUID(guid)
			Expect(err).ToNot(HaveOccurred())
		})
		It("release non existing allocated guid", func() {
			guid := "02:00:00:00:00:00:00:00"
			pool := &guidPool{guidPoolMap: map[GUID]bool{}}

			err := pool.ReleaseGUID(guid)
			Expect(err).To(HaveOccurred())
		})
	})
})
