package main

import (
	"net"

	"github.com/golang/glog"

	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
)

const (
	pluginName  = "noop"
	specVersion = "1.0"
)

var InvalidPlugin bool

type plugin struct {
	PluginName  string
	SpecVersion string
}

func newNoopPlugin() (*plugin, error) {
	return &plugin{PluginName: pluginName, SpecVersion: specVersion}, nil
}

func (u *plugin) Name() string {
	return u.PluginName
}

func (u *plugin) Spec() string {
	return u.SpecVersion
}

func (p *plugin) Validate() error {
	glog.V(3).Info("noop Plugin Validate()")
	return nil
}

func (p *plugin) AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error {
	glog.V(3).Info("noop Plugin AddPkey()")
	return nil
}

func (p *plugin) RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error {
	glog.V(3).Info("noop Plugin RemovePKey()")
	return nil
}

// Initialize applies configs to plugin and return a subnet manager client
func Initialize(configuration []byte) (plugins.SubnetManagerClient, error) {
	glog.Info("Initialize(): noop plugin")
	return newNoopPlugin()
}
