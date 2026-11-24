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
	"errors"
	"sync"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
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
			guid, err := pool.GenerateGUID("0x1234")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			pkey, err := pool.Get(guid.String())
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(Equal("0x1234"))
			Expect(pool.AllocateGUID(guid.String(), "0x1234")).To(HaveOccurred())
			guid, err = pool.GenerateGUID("0x1234")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate and release guid then re-allocate the newly released guids", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00",
				RangeEnd: "00:00:00:00:00:00:01:ff"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID("0x1234")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			err = pool.ReleaseGUID(guid.String())
			Expect(err).ToNot(HaveOccurred())

			for i := 0; i < 255; i++ {
				_, err = pool.GenerateGUID("0x1234")
				Expect(err).ToNot(HaveOccurred())
			}

			// After the last guid in the pool was reserved the pool wraps to the released one.
			guid, err = pool.GenerateGUID("0x1234")
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

			guid, err := p.GenerateGUID("0x1234")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
		})
		It("Generate guid when range is full", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:01:00",
				RangeEnd: "00:00:00:00:00:00:01:00"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())
			guid, err := pool.GenerateGUID("0x1234")
			Expect(err).ToNot(HaveOccurred())
			Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))
			_, err = pool.GenerateGUID("0x1234")
			Expect(err).To(HaveOccurred())
		})
		It("reserves each generated guid for concurrent callers", func() {
			poolConfig := &config.GUIDPoolConfig{RangeStart: "00:00:00:00:00:00:03:00",
				RangeEnd: "00:00:00:00:00:00:03:40"}
			pool, err := NewPool(poolConfig)
			Expect(err).ToNot(HaveOccurred())

			const n = 32
			var wg sync.WaitGroup
			got := make([]string, n)
			errs := make([]error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					g, genErr := pool.GenerateGUID("0x1234")
					errs[i] = genErr
					if genErr == nil {
						got[i] = g.String()
					}
				}(i)
			}
			wg.Wait()

			seen := map[string]struct{}{}
			for i := 0; i < n; i++ {
				Expect(errs[i]).ToNot(HaveOccurred())
				_, dup := seen[got[i]]
				Expect(dup).To(BeFalse())
				seen[got[i]] = struct{}{}
			}
		})
	})
	Context("Reset", func() {
		It("replaces the reservations and skips out of range guids", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			Expect(pool.AllocateGUID("02:00:00:00:00:00:00:01", "0x1")).To(Succeed())

			Expect(pool.Reset(map[string]string{
				"02:00:00:00:00:00:00:3e": "0x2",
				"00:00:00:00:00:00:00:01": "0x2",
			})).To(Succeed())

			Expect(pool.ReleaseGUID("02:00:00:00:00:00:00:01")).ToNot(Succeed())
			pkey, err := pool.Get("02:00:00:00:00:00:00:3e")
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(Equal("0x2"))
			pkey, err = pool.Get("00:00:00:00:00:00:00:01")
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(BeEmpty())
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
			Expect(errors.Is(err, ErrAlreadyAllocated)).To(BeTrue())
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
		It("Allocate guid and retrieve pkey", func() {
			pool, err := NewPool(conf)
			Expect(err).ToNot(HaveOccurred())
			guid := "02:00:00:00:00:00:00:00"
			pkey := "0x5678"
			err = pool.AllocateGUID(guid, pkey)
			Expect(err).ToNot(HaveOccurred())

			retrievedPkey, err := pool.Get(guid)
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedPkey).To(Equal(pkey))
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
	Context("Get", func() {
		It("get pkey for existing allocated guid", func() {
			guid := "00:00:00:00:00:00:00:01"
			expectedPkey := "0x1234"
			pool := &guidPool{guidPoolMap: map[GUID]string{1: expectedPkey}}

			pkey, err := pool.Get(guid)
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(Equal(expectedPkey))
		})
		It("get pkey for non-existing guid", func() {
			guid := "02:00:00:00:00:00:00:00"
			pool := &guidPool{guidPoolMap: map[GUID]string{}}

			pkey, err := pool.Get(guid)
			Expect(err).ToNot(HaveOccurred())
			Expect(pkey).To(Equal(""))
		})
		It("get pkey for invalid guid", func() {
			pool := &guidPool{guidPoolMap: map[GUID]string{}}

			_, err := pool.Get("invalid")
			Expect(err).To(HaveOccurred())
		})
	})
	Context("Lookup", func() {
		It("tells a keyless reservation from a missing guid", func() {
			pool := &guidPool{guidPoolMap: map[GUID]string{1: ""}}

			pkey, ok, err := pool.Lookup("00:00:00:00:00:00:00:01")
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(pkey).To(Equal(""))

			_, ok, err = pool.Lookup("02:00:00:00:00:00:00:00")
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())
		})
	})
})
