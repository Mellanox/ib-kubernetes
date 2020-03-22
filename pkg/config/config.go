package config

import (
	"fmt"

	"github.com/caarlos0/env"
	"github.com/golang/glog"
)

type DaemonConfig struct {
	PeriodicUpdate int `env:"DAEMON_PERIODIC_UPDATE" envDefault:"5"` // Interval between every check for the added and deleted pods
	GuidPool       GuidPoolConfig
	Plugin         string `env:"DAEMON_SM_PLUGIN"` // Subnet manager plugin name
}

type GuidPoolConfig struct {
	RangeStart string `env:"GUID_POOL_RANGE_START" envDefault:"02:00:00:00:00:00:00:00"` // First guid in the pool
	RangeEnd   string `env:"GUID_POOL_RANGE_END"   envDefault:"02:FF:FF:FF:FF:FF:FF:FF"` // Last guid in the pool
}

func (dc *DaemonConfig) ReadConfig() error {
	glog.Info("ReadConfig():")
	return env.Parse(dc)
}

func (dc *DaemonConfig) ValidateConfig() error {
	glog.Info("ValidateConfig():")
	if dc.PeriodicUpdate <= 0 {
		return fmt.Errorf("ValidateConfig(): invalid \"PeriodicUpdate\" value %v", dc.PeriodicUpdate)
	}

	if dc.Plugin == "" {
		return fmt.Errorf("ValidateConfig(): no plugin selected")
	}
	return nil
}
