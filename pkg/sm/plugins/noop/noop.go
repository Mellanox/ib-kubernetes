// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

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

//nolint:unparam
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

func (p *plugin) ListGuidsInUse() (map[string]string, error) {
	log.Info().Msg("noop Plugin ListGuidsInUse()")
	return make(map[string]string), nil
}

// Initialize applies configs to plugin and return a subnet manager client
func Initialize() (plugins.SubnetManagerClient, error) {
	log.Info().Msg("Initializing noop plugin")
	return newNoopPlugin()
}
