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

package main

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("noop plugin", func() {
	Context("Initialize", func() {
		It("Initialize noop plugin", func() {
			plugin, err := Initialize()
			Expect(err).ToNot(HaveOccurred())
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.Name()).To(Equal("noop"))
			Expect(plugin.Spec()).To(Equal("1.0"))
			Expect(InvalidPlugin).ToNot(BeNil())

			err = plugin.Validate()
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToPKey(0, nil)
			Expect(err).ToNot(HaveOccurred())

			err = plugin.RemoveGuidsFromPKey(0, nil)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
