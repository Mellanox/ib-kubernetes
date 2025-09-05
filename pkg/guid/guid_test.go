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

package guid

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
)

var _ = Describe("GUID Pool", func() {
	conf := &config.GUIDPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
	Context("ResetPool", func() {
		It("Reset pool clears previous values", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())

			err = pool.AllocateGUID("02:00:00:00:00:00:00:00", "0x1234")
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("02:00:00:00:FF:00:00:00", "0x1234")
			Expect(err).ToNot(HaveOccurred())

			pool.Reset(nil)

			err = pool.ReleaseGUID("02:00:00:00:00:00:00:00")
			Expect(err).To(HaveOccurred())
			err = pool.ReleaseGUID("02:00:00:00:FF:00:00:00")
			Expect(err).To(HaveOccurred())
		})
		It("Reset pool stores new values", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())

			expectedGuids := map[string]string{
				"02:00:00:00:00:00:00:3e": "0x1234",
				"02:00:0F:F0:00:FF:00:09": "0x1234", 
				"02:00:00:00:00:00:00:00": "0x1234",
			}

			pool.Reset(expectedGuids)

			for expectedGuid := range expectedGuids {
				err = pool.ReleaseGUID(expectedGuid)
				Expect(err).ToNot(HaveOccurred())
			}
		})
		It("Exhausted pool throws error and doesn't after reset", func() {
			conf := &config.GUIDPoolConfig{RangeStart: "02:00:00:00:00:00:00:00", RangeEnd: "02:00:00:00:00:00:00:00"}
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool).ToNot(BeNil())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID(guid.String(), "0x1234")
			Expect(err).ToNot(HaveOccurred())
			guid, err = pool.GenerateGUID()
			Expect(err).To(Equal(ErrGUIDPoolExhausted))

			err = pool.Reset(nil)
			Expect(err).ToNot(HaveOccurred())

			guid, err = pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
		})
	})
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
			invalidRangeStartConf := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:00:00",
				RangeEnd: "02:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewPool(invalidRangeStartConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with not allowed end guid", func() {
			invalidRangeEndConf := &config.GUIDPoolConfig{RangeStart: "02:00:00:00:00:00:00:00",
				RangeEnd: "FF:FF:FF:FF:FF:FF:FF:FF"}
			pool, err := NewPool(invalidRangeEndConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})
		It("Create guid pool with invalid range", func() {
			invalidRangeConf := &config.GUIDPoolConfig{RangeStart: "02:FF:FF:FF:FF:FF:FF:FF",
				RangeEnd: "02:00:00:00:00:00:00:00"}
			pool, err := NewPool(invalidRangeConf)
			Expect(err).To(HaveOccurred())
			Expect(pool).To(BeNil())
		})

	})
	Context("GenerateGUID", func() {
		It("Generate guid when range is not full", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00",
				RangeEnd: "00:00:00:00:00:00:01:01"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(pool.AllocateGUID(guid.String(), "0x1234")).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			guid, err = pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate and release guid then re-allocate the newly released guids", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00",
				RangeEnd: "00:00:00:00:00:00:01:ff"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			Expect(pool.AllocateGUID(guid.String(), "0x1234")).ToNot(HaveOccurred())
			err = pool.ReleaseGUID(guid.String())
			Expect(err).ToNot(HaveOccurred())

			// Generate all the range
			Expect(err).ToNot(HaveOccurred())
			for i := 0; i < 255; i++ {
				guid, err = pool.GenerateGUID()
				Expect(err).ToNot(HaveOccurred())
				Expect(pool.AllocateGUID(guid.String(), "0x1234")).ToNot(HaveOccurred())
			}

			// After the last guid in the pool was allocated then the pool check back from first guid
			guid, err = pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
		})
		It("Generate guid when current guid is allocated", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00",
				RangeEnd: "00:00:00:00:00:00:01:01"}
			p, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			err = p.AllocateGUID("00:00:00:00:00:00:01:00", "0x1234")
			Expect(err).ToNot(HaveOccurred())

			guid, err := p.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate guid when range is full", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00",
				RangeEnd: "00:00:00:00:00:00:01:00"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID()
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			Expect(pool.AllocateGUID(guid.String(), "0x1234")).ToNot(HaveOccurred())
			_, err = pool.GenerateGUID()
			Expect(err).To(HaveOccurred())
		})
	})
	Context("AllocateGUID", func() {
		It("Allocate guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("02:00:00:00:00:00:00:00", "0x1234")
			Expect(err).ToNot(HaveOccurred())
		})
		It("Allocate out of range guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("55:00:00:00:00:00:00:FF", "0x1234")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate an allocated guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("02:00:00:00:00:00:00:00", "0x1234")
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("02:00:00:00:00:00:00:00", "0x1234")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate invalid guid from the pool", func() {
			pool := &guidPool{guidPoolMap: map[GUID]string{}}
			err := pool.AllocateGUID("invalid", "0x1234")
			Expect(err).To(HaveOccurred())
		})
		It("Allocate valid network address but invalid guid from the pool", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			err = pool.AllocateGUID("00:00:00:00:00:00:00:00", "0x1234")
			Expect(err).To(HaveOccurred())
		})
	})
	Context("ReleaseGUID", func() {
		It("release existing allocated guid", func() {
			guid := "00:00:00:00:00:00:00:01"
			pool := &guidPool{guidPoolMap: map[GUID]string{1: "0x1234"}}

			err := pool.ReleaseGUID(guid)
			Expect(err).ToNot(HaveOccurred())
		})
		It("release non existing allocated guid", func() {
			guid := "02:00:00:00:00:00:00:00"
			pool := &guidPool{guidPoolMap: map[GUID]string{}}

			err := pool.ReleaseGUID(guid)
			Expect(err).To(HaveOccurred())
		})
	})
})
