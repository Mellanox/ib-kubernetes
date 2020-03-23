package main

import (
	. "github.com/onsi/ginkgo"
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
