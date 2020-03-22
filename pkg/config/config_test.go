package config

import (
	"os"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Configuration", func() {
	Context("ReadConfig", func() {
		AfterEach(func() {
			Expect(os.Unsetenv("PERIODIC_UPDATE")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("RANGE_START")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("RANGE_END")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("PLUGIN")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("UFM_USERNAME")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("UFM_PASSWORD")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("UFM_ADDRESS")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("UFM_PORT")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("UFM_HTTP_SCHEMA")).ToNot(HaveOccurred())
			Expect(os.Unsetenv("UFM_CERTIFICATE")).ToNot(HaveOccurred())
		})
		It("Read configuration from environment variables", func() {
			dc := &DaemonConfig{}

			Expect(os.Setenv("PERIODIC_UPDATE", "10")).ToNot(HaveOccurred())
			Expect(os.Setenv("RANGE_START", "02:00:00:00:00:00:00:00")).ToNot(HaveOccurred())
			Expect(os.Setenv("RANGE_END", "02:00:00:00:00:00:00:FF")).ToNot(HaveOccurred())
			Expect(os.Setenv("PLUGIN", "ufm")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_USERNAME", "admin")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PASSWORD", "123456")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_ADDRESS", "1.1.1.1")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PORT", "80")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_HTTP_SCHEMA", "http")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_CERTIFICATE", "certificate data")).ToNot(HaveOccurred())

			err := dc.ReadConfig()
			Expect(err).ToNot(HaveOccurred())
			Expect(dc.PeriodicUpdate).To(Equal(10))
			Expect(dc.GuidPool.RangeStart).To(Equal("02:00:00:00:00:00:00:00"))
			Expect(dc.GuidPool.RangeEnd).To(Equal("02:00:00:00:00:00:00:FF"))
			Expect(dc.SubnetManager.Plugin).To(Equal("ufm"))
			Expect(dc.SubnetManager.Ufm.Username).To(Equal("admin"))
			Expect(dc.SubnetManager.Ufm.Password).To(Equal("123456"))
			Expect(dc.SubnetManager.Ufm.Address).To(Equal("1.1.1.1"))
			Expect(dc.SubnetManager.Ufm.Port).To(Equal(80))
			Expect(dc.SubnetManager.Ufm.HttpSchema).To(Equal("http"))
			Expect(dc.SubnetManager.Ufm.Certificate).To(Equal("certificate data"))
		})
		It("Read configuration with default values", func() {
			dc := &DaemonConfig{}

			Expect(os.Setenv("PLUGIN", "ufm")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_USERNAME", "admin")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PASSWORD", "123456")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_ADDRESS", "1.1.1.1")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PORT", "80")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_HTTP_SCHEMA", "http")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_CERTIFICATE", "certificate data")).ToNot(HaveOccurred())

			err := dc.ReadConfig()
			Expect(err).ToNot(HaveOccurred())
			Expect(dc.PeriodicUpdate).To(Equal(5))
			Expect(dc.GuidPool.RangeStart).To(Equal("02:00:00:00:00:00:00:00"))
			Expect(dc.GuidPool.RangeEnd).To(Equal("02:FF:FF:FF:FF:FF:FF:FF"))
			Expect(dc.SubnetManager.Plugin).To(Equal("ufm"))
			Expect(dc.SubnetManager.Ufm.Username).To(Equal("admin"))
			Expect(dc.SubnetManager.Ufm.Password).To(Equal("123456"))
			Expect(dc.SubnetManager.Ufm.Address).To(Equal("1.1.1.1"))
			Expect(dc.SubnetManager.Ufm.Port).To(Equal(80))
			Expect(dc.SubnetManager.Ufm.HttpSchema).To(Equal("http"))
			Expect(dc.SubnetManager.Ufm.Certificate).To(Equal("certificate data"))
		})
	})
	Context("ValidateConfig", func() {
		It("Validate valid configuration", func() {
			dc := &DaemonConfig{
				PeriodicUpdate: 10,
				GuidPool: GuidPoolConfig{
					RangeStart: "02:00:00:00:00:00:00:10",
					RangeEnd:   "02:00:00:00:00:00:00:FF"},
				SubnetManager: SubnetManagerPluginConfig{Plugin: "noop"}}

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
		It("Validate configuration with not supported plugin", func() {
			dc := &DaemonConfig{PeriodicUpdate: 10, SubnetManager: SubnetManagerPluginConfig{Plugin: "not-supported"}}
			err := dc.ValidateConfig()
			Expect(err).To(HaveOccurred())
		})
		It("Validate configuration with guid pool start not set", func() {
			dc := &DaemonConfig{PeriodicUpdate: 10, SubnetManager: SubnetManagerPluginConfig{Plugin: "ufm"}}
			err := dc.ValidateConfig()
			Expect(err).ToNot(HaveOccurred())
		})
		It("Validate configuration with guid pool end not set", func() {
			dc := &DaemonConfig{
				PeriodicUpdate: 10,
				GuidPool:       GuidPoolConfig{RangeStart: "02:00:00:00:00:00:00:00"},
				SubnetManager:  SubnetManagerPluginConfig{Plugin: "ufm"}}
			err := dc.ValidateConfig()
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
