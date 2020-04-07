package config

import (
	"fmt"

	"github.com/caarlos0/env/v6"
	"github.com/rs/zerolog/log"
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
