package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/caarlos0/env"
	"github.com/golang/glog"

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
)

type UFMConfig struct {
	Username    string `env:"UFM_USERNAME"`    // Username of ufm
	Password    string `env:"UFM_PASSWORD"`    // Password of ufm
	Address     string `env:"UFM_ADDRESS"`     // IP address or hostname of ufm server
	Port        int    `env:"UFM_PORT"`        // REST API port of ufm
	HttpSchema  string `env:"UFM_HTTP_SCHEMA"` // http or https
	Certificate string `env:"UFM_CERTIFICATE"` // Certificate of ufm
}

func newUfmPlugin() (*ufmPlugin, error) {
	glog.V(3).Info("newUfmPlugin():")
	ufmConf := UFMConfig{}
	if err := env.Parse(&ufmConf); err != nil {
		return nil, err
	}

	if ufmConf.Username == "" || ufmConf.Password == "" || ufmConf.Address == "" {
		return nil, fmt.Errorf(`missing one or more required fileds for ufm ["username", "password", "address"]`)
	}

	// set httpSchema and port to ufm default if missing
	ufmConf.HttpSchema = strings.ToLower(ufmConf.HttpSchema)
	if ufmConf.HttpSchema == "" {
		ufmConf.HttpSchema = "https"
	}
	if ufmConf.Port == 0 {
		if ufmConf.HttpSchema == "https" {
			ufmConf.Port = 443
		} else {
			ufmConf.Port = 80
		}
	}

	isSecure := strings.EqualFold(ufmConf.HttpSchema, "https")
	auth := &httpDriver.BasicAuth{Username: ufmConf.Username, Password: ufmConf.Password}
	client, err := httpDriver.NewClient(isSecure, auth, ufmConf.Certificate)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client err: %v", err)
	}
	return &ufmPlugin{PluginName: pluginName,
		SpecVersion: specVersion,
		conf:        ufmConf,
		client:      client}, nil
}

func (u *ufmPlugin) Name() string {
	return u.PluginName
}

func (u *ufmPlugin) Spec() string {
	return u.SpecVersion
}

func (u *ufmPlugin) Validate() error {
	glog.V(3).Info("Validate():")
	_, err := u.client.Get(u.buildUrl("/ufmRest/app/ufm_version"), http.StatusOK)

	if err != nil {
		err = fmt.Errorf("validate(): failed to connect to fum subnet manger: %v", err)
		glog.Error(err)
		return err
	}

	return nil
}

func (u *ufmPlugin) AddGuidsToPKey(pKey int, guids []net.HardwareAddr) error {
	glog.V(3).Infof("AddGuidsToPKey(): pkey 0x%04X, guids %v", pKey, guids)

	if !ibUtils.IsPKeyValid(pKey) {
		err := fmt.Errorf("AddGuidsToPKey(): Invalid pkey 0x%04X, out of range 0x0001 - 0xFFFE", pKey)
		glog.Error(err)
		return err
	}

	var guidsString []string
	for _, guid := range guids {
		guidAddr := ibUtils.GuidToString(guid)
		guidsString = append(guidsString, fmt.Sprintf("%q", guidAddr))
	}
	data := []byte(fmt.Sprintf(`{"pkey": "0x%04X", "index0": true, "ip_over_ib": true, "membership": "full", "guids": [%v]}`,
		pKey, strings.Join(guidsString, ",")))

	if _, err := u.client.Post(u.buildUrl("/ufmRest/resources/pkeys"), http.StatusOK, data); err != nil {
		err = fmt.Errorf("AddGuidsToPKey(): failed to add guids %v to PKey 0x%04X "+
			"with error: %v", guids, pKey, err)
		glog.Error(err)
		return err
	}

	return nil
}

func (u *ufmPlugin) RemoveGuidsFromPKey(pKey int, guids []net.HardwareAddr) error {
	glog.V(3).Infof("RemoveGuidsFromPKey(): pkey 0x%04X, guids %v", pKey, guids)

	if !ibUtils.IsPKeyValid(pKey) {
		err := fmt.Errorf("RemoveGuidsFromPKey(): Invalid pkey 0x%04X, out of range 0x0001 - 0xFFFE", pKey)
		glog.Error(err)
		return err
	}

	var guidsString []string
	for _, guid := range guids {
		guidAddr := ibUtils.GuidToString(guid)
		guidsString = append(guidsString, fmt.Sprintf("%q", guidAddr))
	}
	data := []byte(fmt.Sprintf(`{"pkey": "0x%04X", "guids": [%v]}`, pKey, strings.Join(guidsString, ",")))

	if _, err := u.client.Post(u.buildUrl("/ufmRest/actions/remove_guids_from_pkey"), http.StatusOK, data); err != nil {
		err = fmt.Errorf("RemoveGuidsFromPKey(): failed to delete guids %v from PKey 0x%04X, "+
			"with error: %v", guids, pKey, err)
		glog.Error(err)
		return err
	}

	return nil
}

func (u *ufmPlugin) buildUrl(path string) string {
	return fmt.Sprintf("%s://%s:%d%s", u.conf.HttpSchema, u.conf.Address, u.conf.Port, path)
}

// Initialize applies configs to plugin and return a subnet manager client
func Initialize() (plugins.SubnetManagerClient, error) {
	glog.Info("Initialize(): ufm plugin")
	return newUfmPlugin()
}
