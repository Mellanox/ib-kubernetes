package main

import (
	"flag"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/Mellanox/ib-kubernetes/pkg/daemon"
)

const exitError = 1

func setupLogging(debug bool) {
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: zerolog.TimeFieldFormat,
		NoColor:    true})
}

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "Debug level logging")
	flag.Parse()

	setupLogging(debug)

	log.Info().Msg("Starting InfiniBand Daemon")
	ibDaemon, err := daemon.NewDaemon()
	if err != nil {
		log.Error().Msgf("failed to create daemon: %v", err)
		os.Exit(exitError)
	}

	log.Info().Msg("Running InfiniBand Daemon")
	ibDaemon.Run()
}
