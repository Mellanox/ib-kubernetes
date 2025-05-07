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

package errcode

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ErrCode", func() {
	Context("Error()", func() {
		It("Getting text", func() {
			text := "Some text describing error"
			err := &errCode{0, text}
			Expect(err.Error()).To(Equal(text))
		})
	})
	Context("GetCode()", func() {
		It("Passing 'error' type", func() {
			var err error
			Expect(GetCode(err)).To(Equal(NotErrCodeType))
		})
		It("Passing 'errCode' type", func() {
			err := &errCode{}
			Expect(GetCode(err)).To(Equal(0))
		})
	})
	Context("Errorf()", func() {
		It("Passing valid code & arguments list", func() {
			err := Errorf(10, "Some text '%s', int '%d'", "abcd", 123)
			Expect(GetCode(err)).To(Equal(10))
			Expect(err.Error()).To(Equal("Some text 'abcd', int '123'"))
		})
	})
})
