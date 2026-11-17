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

package daemon

import (
	"context"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/binding"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/index"
	"github.com/Mellanox/ib-kubernetes/pkg/controllers/nad"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	"github.com/Mellanox/ib-kubernetes/pkg/scheme"
	"github.com/Mellanox/ib-kubernetes/pkg/sm"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
)

type Daemon interface {
	// Run blocks until SIGINT/SIGTERM, or exits the process if leadership is lost.
	Run()
}

// daemon loads config and the subnet-manager plugin, then runs a controller-runtime
// manager. Reconciliation lives in the NAD and binding reconcilers.
type daemon struct {
	config     config.DaemonConfig
	smClient   plugins.SubnetManagerClient
	fabric     plugins.FabricClient
	pool       guid.Pool
	rangeStart guid.GUID
	rangeEnd   guid.GUID
}

const leaseName = "ib-kubernetes-leader"

// Exponential backoff ~26 sec + 6 * <api call time>.
// NOTE: k8s client has built in exponential backoff, which ib-kubernetes don't use.
// NOTE: ufm client has default timeout on request operation for 30 seconds.
var backoffValues = wait.Backoff{Duration: 1 * time.Second, Factor: 1.6, Jitter: 0.1, Steps: 6}

// NewDaemon loads config, the subnet-manager plugin, and an empty GUID pool.
// The pool is filled from the manager cache only after this process becomes leader.
func NewDaemon() (Daemon, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	smClient, fabric, err := loadSubnetManager(cfg)
	if err != nil {
		return nil, err
	}
	pool, rangeStart, rangeEnd, err := openGUIDPool(cfg)
	if err != nil {
		return nil, err
	}
	return &daemon{
		config:     cfg,
		smClient:   smClient,
		fabric:     fabric,
		pool:       pool,
		rangeStart: rangeStart,
		rangeEnd:   rangeEnd,
	}, nil
}

func loadConfig() (config.DaemonConfig, error) {
	cfg := config.DaemonConfig{}
	if err := cfg.ReadConfig(); err != nil {
		return config.DaemonConfig{}, err
	}
	if err := cfg.ValidateConfig(); err != nil {
		return config.DaemonConfig{}, err
	}
	return cfg, nil
}

func loadSubnetManager(cfg config.DaemonConfig) (plugins.SubnetManagerClient, plugins.FabricClient, error) {
	loader := sm.NewPluginLoader()
	getClient, err := loader.LoadPlugin(
		path.Join(cfg.PluginPath, cfg.Plugin+".so"), sm.InitializePluginFunc)
	if err != nil {
		return nil, nil, err
	}
	smClient, err := getClient()
	if err != nil {
		return nil, nil, err
	}
	if err = validateSubnetManager(smClient); err != nil {
		return nil, nil, err
	}
	fabric, ok := smClient.(plugins.FabricClient)
	if ok {
		log.Info().Msgf("Plugin %s implements FabricClient interface", smClient.Name())
	}
	return smClient, fabric, nil
}

func validateSubnetManager(smClient plugins.SubnetManagerClient) error {
	var validateErr error
	err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		if err := smClient.Validate(); err != nil {
			log.Warn().Msgf("%v", err)
			validateErr = err
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return validateErr
	}
	return nil
}

func openGUIDPool(cfg config.DaemonConfig) (guid.Pool, guid.GUID, guid.GUID, error) {
	pool, err := guid.NewPool(&cfg.GUIDPool)
	if err != nil {
		return nil, 0, 0, err
	}
	rangeStart, err := guid.ParseGUID(cfg.GUIDPool.RangeStart)
	if err != nil {
		return nil, 0, 0, err
	}
	rangeEnd, err := guid.ParseGUID(cfg.GUIDPool.RangeEnd)
	if err != nil {
		return nil, 0, 0, err
	}
	return pool, rangeStart, rangeEnd, nil
}

func (ibDaemon *daemon) Run() {
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := ibDaemon.start(sigCtx)
	stop()
	if err != nil {
		log.Error().Err(err).Msg("ib-kubernetes stopped")
		os.Exit(1)
	}
}

func (ibDaemon *daemon) start(parent context.Context) error {
	mgr, err := ibDaemon.newManager()
	if err != nil {
		return err
	}
	if err := ibDaemon.register(parent, mgr); err != nil {
		return err
	}
	return runManager(parent, mgr)
}

func (ibDaemon *daemon) newManager() (ctrl.Manager, error) {
	ctrl.SetLogger(zap.New())
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	sch, err := scheme.New()
	if err != nil {
		return nil, err
	}
	lease, renew, retry := 60*time.Second, 30*time.Second, 20*time.Second
	return ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        sch,
		LeaderElection:                true,
		LeaderElectionID:              leaseName,
		LeaderElectionNamespace:       leaseNamespace(),
		LeaderElectionReleaseOnCancel: true,
		LeaseDuration:                 &lease,
		RenewDeadline:                 &renew,
		RetryPeriod:                   &retry,
		Metrics:                       metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:        "0",
	})
}

func (ibDaemon *daemon) register(ctx context.Context, mgr ctrl.Manager) error {
	if err := index.Register(ctx, mgr.GetFieldIndexer()); err != nil {
		return err
	}
	period := time.Duration(ibDaemon.config.PeriodicUpdate) * time.Second
	nadRec := &nad.Reconciler{Client: mgr.GetClient(), Fabric: ibDaemon.fabric, RetryInterval: period}
	if err := nadRec.SetupWithManager(mgr); err != nil {
		return err
	}
	bindingReconciler := &binding.Reconciler{
		Client:             mgr.GetClient(),
		SM:                 ibDaemon.smClient,
		Fabric:             ibDaemon.fabric,
		Pool:               ibDaemon.pool,
		Owners:             binding.NewOwners(),
		Nodes:              binding.NewNodes(),
		RetryInterval:      period,
		DriftCheckInterval: 10 * time.Minute,
		OrphanGC:           ibDaemon.config.OrphanGC,
		RangeStart:         ibDaemon.rangeStart,
		RangeEnd:           ibDaemon.rangeEnd,
	}
	return bindingReconciler.SetupWithManager(mgr)
}

// runManager exits 0 on SIGTERM. An unexpected lease loss is returned so Run exits 1.
// controller-runtime can report "leader election lost" while it is releasing the lease
// during our own cancel, so that error is ignored once shutdown has started.
func runManager(parent context.Context, mgr ctrl.Manager) error {
	var shuttingDown atomic.Bool
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	go func() {
		select {
		case <-parent.Done():
			shuttingDown.Store(true)
			cancel()
		case <-ctx.Done():
		}
	}()
	err := mgr.Start(ctx)
	if err == nil || (shuttingDown.Load() && leaseLost(err)) {
		return nil
	}
	return err
}

func leaseLost(err error) bool {
	return err != nil && strings.Contains(err.Error(), "leader election lost")
}

func leaseNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	log.Warn().Msg("POD_NAMESPACE environment variable not set, falling back to 'kube-system'")
	return "kube-system"
}
