package sm

import (
	"fmt"
	"plugin"

	"github.com/rs/zerolog/log"

	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
)

const InitializePluginFunc = "Initialize"

// PluginInitialize is function type to Initizalize the sm plugin. It returns sm plugin instance.
type PluginInitialize func() (plugins.SubnetManagerClient, error)

type PluginLoader interface {
	// LoadPlugin loads go plugin from given path with given symbolName which is the variable needed to be extracted.
	LoadPlugin(path, symbolName string) (PluginInitialize, error)
}

type pluginLoader struct {
}

func NewPluginLoader() PluginLoader {
	return &pluginLoader{}
}

func (p *pluginLoader) LoadPlugin(path, symbolName string) (PluginInitialize, error) {
	log.Info().Msgf("loading plugin from path %s, symbolName %s", path, symbolName)
	smPlugin, err := plugin.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load plugin: %v", err)
	}

	symbol, err := smPlugin.Lookup(symbolName)
	if err != nil {
		return nil, fmt.Errorf("failed to find \"%s\" object in the plugin file: %v", symbolName, err)
	}

	pluginInitializer, ok := symbol.(func() (plugins.SubnetManagerClient, error))
	if !ok {
		return nil, fmt.Errorf("\"%s\" object is not of type function", symbolName)
	}
	return pluginInitializer, nil
}
