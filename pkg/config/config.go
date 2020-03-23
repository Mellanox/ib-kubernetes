package config

import (
	"fmt"

	"github.com/caarlos0/env"
	"github.com/golang/glog"
)

type DaemonConfig struct {
	PeriodicUpdate int `env:"PERIODIC_UPDATE" envDefault:"5"` // Interval between every check for the added and deleted pods
	GuidPool       GuidPoolConfig
	Plugin         string `env:"SM_PLUGIN"` // Subnet manager plugin name
}

type GuidPoolConfig struct {
	RangeStart string `env:"RANGE_START" envDefault:"02:00:00:00:00:00:00:00"` // First guid in the pool
	RangeEnd   string `env:"RANGE_END"   envDefault:"02:FF:FF:FF:FF:FF:FF:FF"` // Last guid in the pool
}

func (dc *DaemonConfig) ReadConfig() error {
	glog.Info("ReadConfig():")
	err := env.Parse(dc)

	return err
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
