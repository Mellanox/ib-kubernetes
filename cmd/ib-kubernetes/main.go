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
	"flag"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/Mellanox/ib-kubernetes/pkg/daemon"
)

const exitError = 1

var (
	version = "master@git"
	commit  = "unknown commit"
	date    = "unknown date"
)

func setupLogging(debug bool) {
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: zerolog.TimeFieldFormat,
		NoColor:    true,
	})
}

func printVersionString() string {
	return fmt.Sprintf("ib-kubernetes version:%s, commit:%s, date:%s", version, commit, date)
}

func main() {
	// Init command line flags to clear vendor packages' flags, especially in init()
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	var debug bool
	var versionOpt bool
	flag.BoolVar(&versionOpt, "version", false, "Show application version")
	flag.BoolVar(&versionOpt, "v", false, "Show application version")
	flag.BoolVar(&debug, "debug", false, "Debug level logging")

	flag.Parse()
	if versionOpt {
		fmt.Printf("%s\n", printVersionString())
		return
	}

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
