package sm

import (
	"fmt"
	"plugin"

	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"

	"github.com/golang/glog"
)

const DefaultPluginSymbolName = "Plugin"

type PluginLoader interface {
	// LoadPlugin loads go plugin from given path with given symbolName which is the variable needed to be extracted.
	LoadPlugin(path, symbolName string) (plugins.SubnetManagerClient, error)
}

type pluginLoader struct {
}

func NewPluginLoader() PluginLoader {
	return &pluginLoader{}
}

func (p *pluginLoader) LoadPlugin(path, symbolName string) (plugins.SubnetManagerClient, error) {
	glog.V(3).Infof("LoadPlugin(): path %s, symbolName %s", path, symbolName)
	smPlugin, err := plugin.Open(path)
	if err != nil {
		err = fmt.Errorf("LoadPlugin(): failed to load plugin: %v", err)
		glog.Error(err)
		return nil, err
	}

	symbol, err := smPlugin.Lookup(symbolName)
	if err != nil {
		err = fmt.Errorf("LoadPlugin(): failed to find \"%s\" object in the plugin file: %v", symbolName, err)
		glog.Error(err)
		return nil, err
	}

	smClient, ok := symbol.(plugins.SubnetManagerClient)
	if !ok {
		err = fmt.Errorf("LoadPlugin(): \"%s\" object is not of type SubnetManagerClient: %v", symbolName, smClient)
		glog.Error(err)
		return nil, err
	}

	return smClient, nil
}
