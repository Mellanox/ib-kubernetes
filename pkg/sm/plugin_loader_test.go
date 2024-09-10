package sm

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Subnet Manager Plugin", func() {
	Context("NewPluginLoader", func() {
		It("Create new plugin loader", func() {
			pl := NewPluginLoader()
			Expect(pl).ToNot(BeNil())
		})
	})
	Context("LoadPlugin", func() {
		var testPlugin string
		BeforeEach(func() {
			curDir, err := os.Getwd()
			Expect(err).ToNot(HaveOccurred())
			testPlugin = filepath.Join(curDir, "../../build/plugins/noop.so")
		})
		It("Load valid subnet manager client plugin", func() {
			pl := NewPluginLoader()
			smClient, err := pl.LoadPlugin(testPlugin, InitializePluginFunc)
			Expect(err).ToNot(HaveOccurred())
			Expect(smClient).ToNot(BeNil())
		})
		It("Load non existing plugin", func() {
			pl := NewPluginLoader()
			plugin, err := pl.LoadPlugin("not existing", InitializePluginFunc)
			Expect(err).To(HaveOccurred())
			Expect(plugin).To(BeNil())
			isTextInError := strings.Contains(err.Error(), "failed to load plugin")
			Expect(isTextInError).To(BeTrue())
		})
		It("Load plugin with no Plugin object", func() {
			pl := NewPluginLoader()
			plugin, err := pl.LoadPlugin(testPlugin, "NotExits")
			Expect(err).To(HaveOccurred())
			Expect(plugin).To(BeNil())
			isTextInError := strings.Contains(err.Error(),
				`failed to find "NotExits" object in the plugin file`)
			Expect(isTextInError).To(BeTrue())
		})
		It("Load plugin with not valid Plugin object", func() {
			pl := NewPluginLoader()
			plugin, err := pl.LoadPlugin(testPlugin, "InvalidPlugin")
			Expect(err).To(HaveOccurred())
			Expect(plugin).To(BeNil())
			isTextInError := strings.Contains(err.Error(), `"InvalidPlugin" object is not of type function`)
			Expect(isTextInError).To(BeTrue())
		})
	})
})
