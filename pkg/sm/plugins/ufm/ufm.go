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
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	env "github.com/caarlos0/env/v11"
	"github.com/rs/zerolog/log"

	httpDriver "github.com/Mellanox/ib-kubernetes/pkg/drivers/http"
	ibUtils "github.com/Mellanox/ib-kubernetes/pkg/ib-utils"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
)

type ufmPlugin struct {
	PluginName  string
	SpecVersion string
	conf        UFMConfig
	client      httpDriver.Client
}

const (
	pluginName  = "ufm"
	specVersion = "1.0"
	httpsProto  = "https"
)

type UFMConfig struct {
	Username    string `env:"UFM_USERNAME"`    // Username of ufm
	Password    string `env:"UFM_PASSWORD"`    // Password of ufm
	Address     string `env:"UFM_ADDRESS"`     // IP address or hostname of ufm server
	Port        int    `env:"UFM_PORT"`        // REST API port of ufm
	HTTPSchema  string `env:"UFM_HTTP_SCHEMA"` // http or https
	Certificate string `env:"UFM_CERTIFICATE"` // Certificate of ufm
}

func newUfmPlugin() (*ufmPlugin, error) {
	ufmConf := UFMConfig{}
	if err := env.Parse(&ufmConf); err != nil {
		return nil, err
	}

	if ufmConf.Username == "" || ufmConf.Password == "" || ufmConf.Address == "" {
		return nil, fmt.Errorf("missing one or more required fileds for ufm [\"username\", \"password\", \"address\"]")
	}

	// set httpSchema and port to ufm default if missing
	ufmConf.HTTPSchema = strings.ToLower(ufmConf.HTTPSchema)
	if ufmConf.HTTPSchema == "" {
		ufmConf.HTTPSchema = httpsProto
	}
	if ufmConf.Port == 0 {
		if ufmConf.HTTPSchema == httpsProto {
			ufmConf.Port = 443
		} else {
			ufmConf.Port = 80
		}
	}

	isSecure := strings.EqualFold(ufmConf.HTTPSchema, httpsProto)
	auth := &httpDriver.BasicAuth{Username: ufmConf.Username, Password: ufmConf.Password}
	client, err := httpDriver.NewClient(isSecure, auth, ufmConf.Certificate)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client err: %v", err)
	}
	return &ufmPlugin{
		PluginName:  pluginName,
		SpecVersion: specVersion,
		conf:        ufmConf,
		client:      client,
	}, nil
}

func (u *ufmPlugin) Name() string {
	return u.PluginName
}

func (u *ufmPlugin) Spec() string {
	return u.SpecVersion
}

func (u *ufmPlugin) Validate() error {
	_, err := u.client.Get(u.buildURL("/ufmRest/app/ufm_version"), http.StatusOK)
	if err != nil {
		return fmt.Errorf("failed to connect to ufm subnet manager: %v", err)
	}

	return nil
}

func (u *ufmPlugin) AddGuidsToPKey(pKey int, guids []net.HardwareAddr) error {
	log.Debug().Msgf("adding guids %v to pKey 0x%04X", guids, pKey)

	if !ibUtils.IsPKeyValid(pKey) {
		return fmt.Errorf("invalid pkey 0x%04X, out of range 0x0001 - 0xFFFE", pKey)
	}

	guidsString := make([]string, 0, len(guids))
	for _, guid := range guids {
		guidAddr := ibUtils.GUIDToString(guid)
		guidsString = append(guidsString, fmt.Sprintf("%q", guidAddr))
	}
	data := []byte(fmt.Sprintf(
		`{"pkey": "0x%04X", "index0": true, "ip_over_ib": true, "membership": "full", "guids": [%v]}`,
		pKey, strings.Join(guidsString, ",")))

	if _, err := u.client.Post(u.buildURL("/ufmRest/resources/pkeys"), http.StatusOK, data); err != nil {
		return fmt.Errorf("failed to add guids %v to PKey 0x%04X with error: %v", guids, pKey, err)
	}

	return nil
}

func (u *ufmPlugin) RemoveGuidsFromPKey(pKey int, guids []net.HardwareAddr) error {
	log.Debug().Msgf("removing guids %v pkey 0x%04X", guids, pKey)

	if !ibUtils.IsPKeyValid(pKey) {
		return fmt.Errorf("invalid pkey 0x%04X, out of range 0x0001 - 0xFFFE", pKey)
	}

	guidsString := make([]string, 0, len(guids))
	for _, guid := range guids {
		guidAddr := ibUtils.GUIDToString(guid)
		guidsString = append(guidsString, fmt.Sprintf("%q", guidAddr))
	}
	data := []byte(fmt.Sprintf(`{"pkey": "0x%04X", "guids": [%v]}`, pKey, strings.Join(guidsString, ",")))

	if _, err := u.client.Post(u.buildURL("/ufmRest/actions/remove_guids_from_pkey"), http.StatusOK, data); err != nil {
		return fmt.Errorf("failed to delete guids %v from PKey 0x%04X, with error: %v", guids, pKey, err)
	}

	return nil
}

// convertToMacAddr adds semicolons each 2 characters to convert to MAC format
// UFM returns GUIDS without any delimiters, so expected format is as follows:
// FF00FF00FF00FF00
func convertToMacAddr(guid string) string {
	for i := 2; i < len(guid); i += 3 {
		guid = guid[:i] + ":" + guid[i:]
	}
	return guid
}

type GUID struct {
	GUIDValue string `json:"guid"`
}

type PKey struct {
	Guids []GUID `json:"guids"`
}

// ListGuidsInUse returns all guids currently in use by pKeys
func (u *ufmPlugin) ListGuidsInUse() (map[string]string, error) {
	response, err := u.client.Get(u.buildURL("/ufmRest/resources/pkeys/?guids_data=true"), http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("failed to get the list of guids: %v", err)
	}

	var pKeys map[string]PKey

	if err := json.Unmarshal(response, &pKeys); err != nil {
		return nil, fmt.Errorf("failed to get the list of guids: %v", err)
	}

	guids := make(map[string]string)

	for pkey := range pKeys {
		pkeyData := pKeys[pkey]
		for _, guidData := range pkeyData.Guids {
			guids[convertToMacAddr(guidData.GUIDValue)] = pkey
		}
	}
	return guids, nil
}

func (u *ufmPlugin) buildURL(path string) string {
	return fmt.Sprintf("%s://%s:%d%s", u.conf.HTTPSchema, u.conf.Address, u.conf.Port, path)
}

// Initialize applies configs to plugin and return a subnet manager client
func Initialize() (plugins.SubnetManagerClient, error) {
	log.Info().Msg("Initializing ufm plugin")
	return newUfmPlugin()
}
