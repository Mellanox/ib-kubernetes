package main

import (
	"errors"
	"fmt"
	"net"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/ib-kubernetes/pkg/drivers/http/mocks"
)

var _ = Describe("Ufm Subnet Manager Client plugin", func() {
	Context("Initialize", func() {
		AfterEach(func() {
			os.Clearenv()
		})
		It("Initialize ufm plugin", func() {
			Expect(os.Setenv("UFM_USERNAME", "admin")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PASSWORD", "123456")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_ADDRESS", "1.1.1.1")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PORT", "80")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_HTTP_SCHEMA", "http")).ToNot(HaveOccurred())
			plugin, err := Initialize()
			Expect(err).ToNot(HaveOccurred())
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.Name()).To(Equal("ufm"))
			Expect(plugin.Spec()).To(Equal("1.0"))
		})
	})
	Context("newUfmPlugin", func() {
		AfterEach(func() {
			os.Clearenv()
		})
		It("newUfmPlugin ufm plugin", func() {
			Expect(os.Setenv("UFM_USERNAME", "admin")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PASSWORD", "123456")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_ADDRESS", "1.1.1.1")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_HTTP_SCHEMA", "http")).ToNot(HaveOccurred())
			plugin, err := newUfmPlugin()
			Expect(err).ToNot(HaveOccurred())
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.Name()).To(Equal("ufm"))
			Expect(plugin.Spec()).To(Equal("1.0"))
			Expect(plugin.conf.Port).To(Equal(80))
		})
		It("newUfmPlugin with missing address config", func() {
			Expect(os.Setenv("UFM_USERNAME", "admin")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PASSWORD", "123456")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_HTTP_SCHEMA", "http")).ToNot(HaveOccurred())
			plugin, err := newUfmPlugin()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal(`missing one or more required fileds for ufm ["username", "password", "address"]`))
			Expect(plugin).To(BeNil())
		})
	})
	Context("Validate", func() {
		It("Validate connection to ufm", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			err := plugin.Validate()
			Expect(err).ToNot(HaveOccurred())
		})
		It("Validate connection to ufm failed to connect", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return(nil, errors.New("failed"))

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			err := plugin.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("failed to connect to ufm subnet manager: failed"))
		})
	})
	Context("AddGuidsToPKey", func() {
		It("Add guid to valid pkey", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return([]byte(`{"pkey": "0x1234"}`), nil)
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).ToNot(HaveOccurred())
		})
		It("Add guid to invalid pkey", func() {
			plugin := &ufmPlugin{conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToPKey(0xFFFF, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("invalid pkey 0xFFFF, out of range 0x0001 - 0xFFFE"))
		})
		It("Add guid to pkey failed from ufm", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return([]byte(`{"pkey": "0x1234"}`), nil)
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("failed"))

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			guids := []net.HardwareAddr{guid}
			pKey := 0x1234
			err = plugin.AddGuidsToPKey(pKey, guids)
			Expect(err).To(HaveOccurred())
			errMessage := fmt.Sprintf("failed to add guids %v to PKey 0x%04X with error: failed", guids, pKey)
			Expect(err.Error()).To(Equal(errMessage))
		})
	})
	Context("RemoveGuidsFromPKey", func() {
		It("Remove guid from valid pkey", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.RemoveGuidsFromPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).ToNot(HaveOccurred())
		})
		It("Remove guid from invalid pkey", func() {
			plugin := &ufmPlugin{conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.RemoveGuidsFromPKey(0xFFFF, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("invalid pkey 0xFFFF, out of range 0x0001 - 0xFFFE"))
		})
		It("Remove guid from pkey failed from ufm", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("failed"))

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			guids := []net.HardwareAddr{guid}
			pKey := 0x1234
			err = plugin.RemoveGuidsFromPKey(pKey, guids)
			Expect(err).To(HaveOccurred())
			errMessage := fmt.Sprintf("failed to delete guids %v from PKey 0x%04X, with error: failed",
				guids, pKey)
			errMsg := err.Error()
			Expect(&errMsg).To(Equal(&errMessage))
		})
	})
	Context("AddGuidsToLimitedPKey", func() {
		It("Add guid to valid limited pkey that exists", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return([]byte(`{"pkey": "0x1234"}`), nil)
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToLimitedPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).ToNot(HaveOccurred())
		})
		It("Add guid to limited pkey that does not exist", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return(nil, errors.New("404"))

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToLimitedPKey(0x1234, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("limited pkey 0x1234 does not exist, will not create it"))
		})
		It("Add guid to invalid limited pkey", func() {
			plugin := &ufmPlugin{conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			err = plugin.AddGuidsToLimitedPKey(0xFFFF, []net.HardwareAddr{guid})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("invalid pkey 0xFFFF, out of range 0x0001 - 0xFFFE"))
		})
		It("Add guid to limited pkey failed from ufm", func() {
			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return([]byte(`{"pkey": "0x1234"}`), nil)
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("failed"))

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guid, err := net.ParseMAC("11:22:33:44:55:66:77:88")
			Expect(err).ToNot(HaveOccurred())

			guids := []net.HardwareAddr{guid}
			pKey := 0x1234
			err = plugin.AddGuidsToLimitedPKey(pKey, guids)
			Expect(err).To(HaveOccurred())
			errMessage := fmt.Sprintf("failed to add guids %v as limited members to PKey 0x%04X with error: failed", guids, pKey)
			Expect(err.Error()).To(Equal(errMessage))
		})
	})
	Context("createEmptyPKey with EnableIPOverIB", func() {
		It("Create pkey with IP over IB enabled", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: UFMConfig{EnableIPOverIB: true}}
			err := plugin.createEmptyPKey(0x1234)
			Expect(err).ToNot(HaveOccurred())

			// Verify the Post call was made with ip_over_ib: true
			client.AssertCalled(GinkgoT(), "Post", mock.Anything, mock.Anything, mock.MatchedBy(func(data []byte) bool {
				return string(data) == `{"pkey": "0x1234", "index0": true, "ip_over_ib": true, "mtu_limit": 4, "service_level": 0, "rate_limit": 300, "guids": [], "membership": "full"}`
			}))
		})
		It("Create pkey with IP over IB disabled", func() {
			client := &mocks.Client{}
			client.On("Post", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)

			plugin := &ufmPlugin{client: client, conf: UFMConfig{EnableIPOverIB: false}}
			err := plugin.createEmptyPKey(0x1234)
			Expect(err).ToNot(HaveOccurred())

			// Verify the Post call was made with ip_over_ib: false
			client.AssertCalled(GinkgoT(), "Post", mock.Anything, mock.Anything, mock.MatchedBy(func(data []byte) bool {
				return string(data) == `{"pkey": "0x1234", "index0": true, "ip_over_ib": false, "mtu_limit": 4, "service_level": 0, "rate_limit": 300, "guids": [], "membership": "full"}`
			}))
		})
	})
	Context("UFMConfig with new environment variables", func() {
		AfterEach(func() {
			os.Clearenv()
		})
		It("newUfmPlugin with EnableIPOverIB and DefaultLimitedPartition config", func() {
			Expect(os.Setenv("UFM_USERNAME", "admin")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PASSWORD", "123456")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_ADDRESS", "1.1.1.1")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_HTTP_SCHEMA", "http")).ToNot(HaveOccurred())
			Expect(os.Setenv("ENABLE_IP_OVER_IB", "true")).ToNot(HaveOccurred())
			Expect(os.Setenv("DEFAULT_LIMITED_PARTITION", "0x1")).ToNot(HaveOccurred())
			plugin, err := newUfmPlugin()
			Expect(err).ToNot(HaveOccurred())
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.conf.EnableIPOverIB).To(BeTrue())
			Expect(plugin.conf.DefaultLimitedPartition).To(Equal("0x1"))
		})
		It("newUfmPlugin with default EnableIPOverIB config", func() {
			Expect(os.Setenv("UFM_USERNAME", "admin")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_PASSWORD", "123456")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_ADDRESS", "1.1.1.1")).ToNot(HaveOccurred())
			Expect(os.Setenv("UFM_HTTP_SCHEMA", "http")).ToNot(HaveOccurred())
			plugin, err := newUfmPlugin()
			Expect(err).ToNot(HaveOccurred())
			Expect(plugin).ToNot(BeNil())
			Expect(plugin.conf.EnableIPOverIB).To(BeFalse()) // Default should be false
			Expect(plugin.conf.DefaultLimitedPartition).To(Equal("")) // Default should be empty
		})
	})
	Context("ListGuidsInUse", func() {
		It("Remove guid from valid pkey", func() {
			testResponse := `{
				"0x7fff": {
					"guids": []
				},
				"0x7aff": {
					"test": "val"
				},
				"0x5": {
					"guids": [
						{
							"guid": "020000000000003e"
						},
						{
							"guid": "02000FF000FF0009"
						}
					]
				},
				"0x6": {
					"guids": [
						{
							"guid": "0200000000000000"
						}
					]
				}
			}`

			client := &mocks.Client{}
			client.On("Get", mock.Anything, mock.Anything).Return([]byte(testResponse), nil)

			plugin := &ufmPlugin{client: client, conf: UFMConfig{}}
			guids, err := plugin.ListGuidsInUse()
			Expect(err).ToNot(HaveOccurred())

			expectedGuids := []string{"02:00:00:00:00:00:00:3e", "02:00:0F:F0:00:FF:00:09", "02:00:00:00:00:00:00:00"}
			Expect(guids).To(ConsistOf(expectedGuids))
		})
	})
})
