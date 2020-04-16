package guid

import (
	"github.com/Mellanox/ib-kubernetes/pkg/config"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("GUID Pool", func() {
	conf := &config.GUIDPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
	Context("NewPool", func() {
		It("Create guid pool with valid  parameters", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())
		})
		It("Create guid pool with invalid start guid", func() {
			invalidConf := &config.GUIDPoolConfig{RangeStart: "invalid", RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewPool(invalidConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with invalid end guid", func() {
			invalidConf := &config.GUIDPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "invalid"}
			pool, err := NewPool(invalidConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with not allowed start guid", func() {
			invalidRangeStartConf := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:00:00", RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewPool(invalidRangeStartConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with not allowed end guid", func() {
			invalidRangeEndConf := &config.GUIDPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "FF:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewPool(invalidRangeEndConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with invalid range", func() {
			invalidRangeConf := &config.GUIDPoolConfig{RangeStart: "02:FF:FF:FF:FF:FF:FF:FF", RangeEnd: "02:00:00:00:00:00:00:00"}
			pool, err := NewPool(invalidRangeConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})

	})
	Context("GenerateGUID", func() {
		It("Generate guid when range is not full", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:01"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(pool.AllocateGUID(guid.String())).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			guid, err = pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate and release guid then re-allocate the newly released guids", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:ff"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			Expect(pool.AllocateGUID(guid.String())).ToNot(HaveOccurred())
			err = pool.ReleaseGUID(guid.String())
			Expect(err).ToNot(HaveOccurred())

			// Generate all the range
			Expect(err).ToNot(HaveOccurred())
			for i := 0; i < 255; i++ {
				guid, err = pool.GenerateGUID()
				Expect(err).ToNot(HaveOccurred())
				Expect(pool.AllocateGUID(guid.String())).ToNot(HaveOccurred())
			}

			// After the last guid in the pool was allocated then the pool check back from first guid
			guid, err = pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
		})
		It("Generate guid when current guid is allocated", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:01"}
			p, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			err = p.AllocateGUID("00:00:00:00:00:00:01:00")
			Expect(err).ToNot(HaveOccurred())

			guid, err := p.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate guid when range is full", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00", RangeEnd: "00:00:00:00:00:00:01:00"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			Expect(pool.AllocateGUID(guid.String())).ToNot(HaveOccurred())
			_, err = pool.GenerateGUID()
			Expect(err).To(HaveOccurred())
		})
	})
	Context("AllocateGUID", func() {
		It("Allocate guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
		})
		It("Allocate out of range guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("55:00:00:00:00:00:00:FF")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate an allocated guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("02:00:00:00:00:00:00:00")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate invalid guid from the pool", func() {
			pool := &guidPool{guidPoolMap: map[GUID]bool{}}
			err := pool.AllocateGUID("invalid")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate valid network address but invalid guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("00:00:00:00:00:00:00:00")
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
