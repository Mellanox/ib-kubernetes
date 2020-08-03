package errcode

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("ErrCode", func() {
	Context("Error()", func() {
		It("Getting text", func() {
			text := "Some text describing error"
			err := &errCode{ErrUnknown, text}
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
			Expect(GetCode(err)).To(Equal(ErrUnknown))
		})
	})
	Context("Errorf()", func() {
		It("Passing valid code & arguments list", func() {
			err := Errorf(ErrNotIBSriovNetwork, "Some text '%s', int '%d'", "abcd", 123)
			Expect(GetCode(err)).To(Equal(ErrNotIBSriovNetwork))
			Expect(err.Error()).To(Equal("Some text 'abcd', int '123'"))
		})
	})
})
