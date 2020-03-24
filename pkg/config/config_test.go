package config

import (
	"os"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Configuration", func() {
	Context("ReadConfig", func() {
		AfterEach(func() {
			os.Clearenv()
		})
		It("Read configuration from environment variables", func() {
			dc := &DaemonConfig{}

			Expect(os.Setenv("DAEMON_PERIODIC_UPDATE", "10")).ToNot(HaveOccurred())
			Expect(os.Setenv("GUID_POOL_RANGE_START", "02:00:00:00:00:00:00:00")).ToNot(HaveOccurred())
			Expect(os.Setenv("GUID_POOL_RANGE_END", "02:00:00:00:00:00:00:FF")).ToNot(HaveOccurred())
			Expect(os.Setenv("DAEMON_SM_PLUGIN", "ufm")).ToNot(HaveOccurred())

			err := dc.ReadConfig()
			Expect(err).ToNot(HaveOccurred())
			Expect(dc.PeriodicUpdate).To(Equal(10))
			Expect(dc.GuidPool.RangeStart).To(Equal("02:00:00:00:00:00:00:00"))
			Expect(dc.GuidPool.RangeEnd).To(Equal("02:00:00:00:00:00:00:FF"))
			Expect(dc.Plugin).To(Equal("ufm"))
		})
		It("Read configuration with default values", func() {
			dc := &DaemonConfig{}
			Expect(os.Setenv("DAEMON_SM_PLUGIN", "ufm")).ToNot(HaveOccurred())

			err := dc.ReadConfig()
			Expect(err).ToNot(HaveOccurred())
			Expect(dc.PeriodicUpdate).To(Equal(5))
			Expect(dc.GuidPool.RangeStart).To(Equal("02:00:00:00:00:00:00:00"))
			Expect(dc.GuidPool.RangeEnd).To(Equal("02:FF:FF:FF:FF:FF:FF:FF"))
			Expect(dc.Plugin).To(Equal("ufm"))
		})
	})
	Context("ValidateConfig", func() {
		It("Validate valid configuration", func() {
			dc := &DaemonConfig{
				PeriodicUpdate: 10,
				GuidPool: GuidPoolConfig{
					RangeStart: "02:00:00:00:00:00:00:10",
					RangeEnd:   "02:00:00:00:00:00:00:FF"},
				Plugin: "noop"}

			err := dc.ValidateConfig()
			Expect(err).ToNot(HaveOccurred())
		})
		It("Validate configuration with invalid periodic update", func() {
			dc := &DaemonConfig{PeriodicUpdate: -10}
			err := dc.ValidateConfig()
			Expect(err).To(HaveOccurred())
		})
		It("Validate configuration with not selected plugin", func() {
			dc := &DaemonConfig{PeriodicUpdate: 10}
			err := dc.ValidateConfig()
			Expect(err).To(HaveOccurred())
		})
		It("Validate configuration with guid pool start not set", func() {
			dc := &DaemonConfig{PeriodicUpdate: 10, Plugin: "ufm"}
			err := dc.ValidateConfig()
			Expect(err).ToNot(HaveOccurred())
		})
		It("Validate configuration with guid pool end not set", func() {
			dc := &DaemonConfig{
				PeriodicUpdate: 10,
				GuidPool:       GuidPoolConfig{RangeStart: "02:00:00:00:00:00:00:00"},
				Plugin:         "ufm"}
			err := dc.ValidateConfig()
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
