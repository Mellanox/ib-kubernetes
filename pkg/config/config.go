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
	// Default partition key for limited membership
	DefaultLimitedPartition string `env:"DEFAULT_LIMITED_PARTITION"`
	// Enable IP over IB functionality
	EnableIPOverIB bool `env:"ENABLE_IP_OVER_IB" envDefault:"false"`
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

	// If IP over IB enabled - log at startup
	if dc.EnableIPOverIB {
		log.Warn().Msg("New partitions will be created with IP over IB enabled.")
	} else {
		log.Info().Msg("New partitions will be created with IP over IB disabled.")
	}

	// If default limited partition is set - log at startup
	if dc.DefaultLimitedPartition != "" {
		log.Info().Msgf("Default limited partition is set to %s. New GUIDs will be added as limited members to this partition.", dc.DefaultLimitedPartition)
	} else {
		log.Info().Msg("Default limited partition is not set.")
	}

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
