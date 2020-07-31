package errors

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Errors", func() {
	Context("Error()", func() {
		It("Getting text", func() {
			text := "Some text describing error"
			err := &codedError{UnknownError, text}
			Expect(err.Error()).To(Equal(text))
		})
	})
	Context("ErrorCode()", func() {
		It("Passing 'error' type", func() {
			var err error
			Expect(ErrorCode(err)).To(Equal(NotCodedErrorType))
		})
		It("Passing 'codedError' type", func() {
			err := &codedError{}
			Expect(ErrorCode(err)).To(Equal(UnknownError))
		})
	})
	Context("Errorf()", func() {
		It("Passing valid code & arguments list", func() {
			err := Errorf(NotValidError, "Some text '%s', int '%d'", "abcd", 123)
			Expect(ErrorCode(err)).To(Equal(NotValidError))
			Expect(err.Error()).To(Equal("Some text 'abcd', int '123'"))
		})
		It("Passing invalid 'code' value range: NotErrorCode", func() {
			err := Errorf(NotCodedErrorType, "")
			Expect(ErrorCode(err)).To(Equal(UnknownError))
		})
		It("Passing invalid 'code' value range: ErrorMax", func() {
			err := Errorf(ErrorMax, "")
			Expect(ErrorCode(err)).To(Equal(UnknownError))
		})
		It("Passing invalid 'code' value out of range range", func() {
			err := Errorf(-100, "")
			Expect(ErrorCode(err)).To(Equal(UnknownError))
		})
	})
})
