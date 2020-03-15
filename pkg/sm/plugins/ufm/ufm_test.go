package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/drivers/http/mocks"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("Ufm Subnet Manager Client plugin", func() {
	Context("Initialize", func() {
		It("Initialize ufm plugin", func() {
			conf := []byte(`{"ufm_username":"admin", "ufm_password":"admin", "ufm_address":"192.168.1.10"}`)
			plugin, err := Initialize(conf)
			Expect(err).ToNot(HaveOccurred())
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.Name()).To(Equal("ufm"))
			Expect(plugin.Spec()).To(Equal("1.0"))
		})
	})
	Context("newUfmPlugin", func() {
		It("newUfmPlugin ufm plugin", func() {
			conf := []byte(`{"ufm_username":"admin", "ufm_password":"admin", "ufm_address":"192.168.1.10", "ufm_httpSchema":"http"}`)
			plugin, err := newUfmPlugin(conf)
			Expect(err).ToNot(HaveOccurred())
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.Name()).To(Equal("ufm"))
			Expect(plugin.Spec()).To(Equal("1.0"))
			Expect(plugin.conf.Port).To(Equal(80))
		})
		It("newUfmPlugin with invalid configuration", func() {
			conf := []byte("invalid")
			plugin, err := newUfmPlugin(conf)
			Expect(err).To(HaveOccurred())
			Expect(plugin).To(BeNil())
		})
		It("newUfmPlugin with missing address config", func() {
			conf := []byte(`{"username":"admin", "password":"admin"}`)
			plugin, err := newUfmPlugin(conf)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal(`missing one or more required fileds for ufm ["username", "password", "address"]`))
			Expect(plugin).To(BeNil())
		})
	})
	Context("Validate", func() {
		It("Validate connection to ufm", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: &config.UFMConfig{}}
			err := plugin.Validate()
			Expect(err).ToNot(HaveOccurred())
		})
		It("Validate connection to ufm failed to connect", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return(nil, errors.New("failed"))

			plugin := &ufmPlugin{client: client, conf: &config.UFMConfig{}}
			err := plugin.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("validate(): failed to connect to fum subnet manger: failed"))
		})
	})
	Context("AddGuidsToPKey", func() {
		It("Add guid to valid pkey", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: &config.UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).ToNot(HaveOccurred())
		})
		It("Add guid to invalid pkey", func() {
			plugin := &ufmPlugin{conf: &config.UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToPKey(0xFFFF, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("AddGuidsToPKey(): Invalid pkey 0xFFFF, out of range 0x0001 - 0xFFFE"))
		})
		It("Add guid to pkey failed from ufm", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("failed"))

			plugin := &ufmPlugin{client: client, conf: &config.UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			guids := []net.HardwareAddr{guid}
			pKey := 0x1234
			err = plugin.AddGuidsToPKey(pKey, guids)
			Expect(err).To(HaveOccurred())
			errMessage := fmt.Sprintf("AddGuidsToPKey(): failed to add guids %v to PKey 0x%04X with error: failed",
				guids, pKey)
			Expect(err.Error()).To(Equal(errMessage))
		})
	})
	Context("RemoveGuidsFromPKey", func() {
		It("Remove guid from valid pkey", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: &config.UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.RemoveGuidsFromPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).ToNot(HaveOccurred())
		})
		It("Remove guid from invalid pkey", func() {
			plugin := &ufmPlugin{conf: &config.UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.RemoveGuidsFromPKey(0xFFFF, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("RemoveGuidsFromPKey(): Invalid pkey 0xFFFF, out of range 0x0001 - 0xFFFE"))
		})
		It("Remove guid from pkey failed from ufm", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("failed"))

			plugin := &ufmPlugin{client: client, conf: &config.UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			guids := []net.HardwareAddr{guid}
			pKey := 0x1234
			err = plugin.RemoveGuidsFromPKey(pKey, guids)
			Expect(err).To(HaveOccurred())
			errMessage := fmt.Sprintf("RemoveGuidsFromPKey(): failed to delete guids %v from PKey 0x%04X, with error: failed",
				guids, pKey)
			Expect(err.Error()).To(Equal(errMessage))
		})
	})
})
