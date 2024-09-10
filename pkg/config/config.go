package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
	"github.com/rs/zerolog/log"
)

type DaemonConfig struct {
	// Interval between every check for the added and deleted pods
	PeriodicUpdate int `env:"DAEMON_PERIODIC_UPDATE" envDefault:"5"`
	GUIDPool       GUIDPoolConfig
	// Subnet manager plugin name
	Plugin string `env:"DAEMON_SM_PLUGIN"`
	// Subnet manager plugins path
	PluginPath string `env:"DAEMON_SM_PLUGIN_PATH" envDefault:"/plugins"`
}

type GUIDPoolConfig struct {
	// First guid in the pool
	RangeStart string `env:"GUID_POOL_RANGE_START" envDefault:"02:00:00:00:00:00:00:00"`
	// Last guid in the pool
	RangeEnd string `env:"GUID_POOL_RANGE_END" envDefault:"02:FF:FF:FF:FF:FF:FF:FF"`
}

func (dc *DaemonConfig) ReadConfig() error {
	log.Debug().Msg("Reading configuration environment variables")
	err := env.Parse(dc)

	return err
}

func (dc *DaemonConfig) ValidateConfig() error {
	log.Debug().Msgf("Validating configurations %+v", dc)
	if dc.PeriodicUpdate <= 0 {
		return fmt.Errorf("invalid \"PeriodicUpdate\" value %d", dc.PeriodicUpdate)
	}

	if dc.Plugin == "" {
		return fmt.Errorf("no plugin selected")
	}
	return nil
}
