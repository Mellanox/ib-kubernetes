package main

import (
	"net"

	"github.com/rs/zerolog/log"

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

func (p *plugin) Name() string {
	return p.PluginName
}

func (p *plugin) Spec() string {
	return p.SpecVersion
}

func (p *plugin) Validate() error {
	log.Info().Msg("noop Plugin Validate()")
	return nil
}

func (p *plugin) AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error {
	log.Info().Msg("noop Plugin AddPkey()")
	return nil
}

func (p *plugin) RemoveGuidsFromPKey(pkey int, guids []net.HardwareAddr) error {
	log.Info().Msg("noop Plugin RemovePKey()")
	return nil
}

// Initialize applies configs to plugin and return a subnet manager client
func Initialize() (plugins.SubnetManagerClient, error) {
	log.Info().Msg("Initializing noop plugin")
	return newNoopPlugin()
}
