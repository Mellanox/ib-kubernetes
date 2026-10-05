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
	"fmt"
	"os"
	"os/signal"
	"path"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/sm"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher"
	resEvenHandler "github.com/Mellanox/ib-kubernetes/pkg/watcher/handler"
)

type Daemon interface {
	// Execute Daemon loop, returns when os.Interrupt signal is received
	Run()
}

// daemon is the top-level wiring: config + plugin load + leader election +
// the two controllers. All reconciliation lives in partitionController and
// podController; daemon only orchestrates start/stop.
type daemon struct {
	config        config.DaemonConfig
	kubeClient    k8sClient.Client
	podCtrl       *podController
	partitionCtrl *partitionController
}

// Exponential backoff ~26 sec + 6 * <api call time>
// NOTE: k8s client has built in exponential backoff, which ib-kubernetes don't use.
// In case client's backoff was configured time may dramatically increase.
// NOTE: ufm client has default timeout on request operation for 30 seconds.
var backoffValues = wait.Backoff{Duration: 1 * time.Second, Factor: 1.6, Jitter: 0.1, Steps: 6}

// NewDaemon initializes the daemon: config, k8s client, SM plugin, GUID pool,
// pod + NAD watchers, and the two controllers. Construction is two-step so
// the controllers can satisfy each other's dependencies before either
// goroutine starts.
func NewDaemon() (Daemon, error) {
	daemonConfig := config.DaemonConfig{}
	if err := daemonConfig.ReadConfig(); err != nil {
		return nil, err
	}

	if err := daemonConfig.ValidateConfig(); err != nil {
		return nil, err
	}

	podEventHandler := resEvenHandler.NewPodEventHandler()
	nadEventHandler := resEvenHandler.NewNADEventHandler()
	client, err := k8sClient.NewK8sClient()
	if err != nil {
		return nil, err
	}

	pluginLoader := sm.NewPluginLoader()
	getSmClientFunc, err := pluginLoader.LoadPlugin(path.Join(
		daemonConfig.PluginPath, daemonConfig.Plugin+".so"), sm.InitializePluginFunc)
	if err != nil {
		return nil, err
	}

	smClient, err := getSmClientFunc()
	if err != nil {
		return nil, err
	}

	// Plugins that implement FabricClient drive partition-aware paths;
	// others (UFM, noop) stay on the legacy GUID pool flow.
	var fabricClient plugins.FabricClient
	if pc, ok := smClient.(plugins.FabricClient); ok {
		fabricClient = pc
		log.Info().Msgf("Plugin %s implements FabricClient interface", smClient.Name())
	}

	// Try to validate if subnet manager is reachable in backoff loop
	var validateErr error
	if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		if err := smClient.Validate(); err != nil {
			log.Warn().Msgf("%v", err)
			validateErr = err
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, validateErr
	}

	guidPool, err := guid.NewPool(&daemonConfig.GUIDPool)
	if err != nil {
		return nil, err
	}

	podWatcher := watcher.NewWatcher(podEventHandler, client)
	nadWatcher := watcher.NewWatcher(nadEventHandler, client)

	// Two-step wiring resolves the mutual dependency between controllers:
	//   - partitionController needs podController.updatePodNetworkAnnotation
	//     (passed by value as a method reference)
	//   - podController needs partitionController as its PartitionReader
	// Both controllers are constructed before any goroutine starts, so the
	// post-construction pointer assignment is safe.
	partitionCtrl := newPartitionController(fabricClient, smClient, client, nadWatcher)
	podCtrl := newPodController(client, smClient, guidPool, podWatcher, partitionCtrl)
	partitionCtrl.updatePodAnnotation = podCtrl.updatePodNetworkAnnotation

	return &daemon{
		config:        daemonConfig,
		kubeClient:    client,
		podCtrl:       podCtrl,
		partitionCtrl: partitionCtrl,
	}, nil
}

func (d *daemon) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Use node name + Pod UID for stable and unique leader identity
	nodeName := os.Getenv("K8S_NODE")
	if nodeName == "" {
		log.Warn().Msg("K8S_NODE environment variable not set, falling back to hostname")
		var err error
		nodeName, err = os.Hostname()
		if err != nil {
			log.Error().Msgf("Failed to get hostname: %v", err)
			return
		}
	}

	podUID := os.Getenv("POD_UID")
	var identity string
	if podUID == "" {
		log.Warn().Msg("POD_UID environment variable not set, falling back to node name only")
		identity = nodeName
	} else {
		identity = nodeName + "_" + podUID
	}

	// Get the namespace where this pod is running
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		log.Warn().Msg("POD_NAMESPACE environment variable not set, falling back to 'kube-system'")
		namespace = "kube-system"
	}

	log.Info().Msgf("Starting leader election in namespace: %s with identity: %s", namespace, identity)

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      "ib-kubernetes-leader",
			Namespace: namespace,
		},
		Client: d.kubeClient.GetCoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}

	work := &leaderWork{}
	leaderElectionConfig := leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   60 * time.Second, // Standard Kubernetes components duration
		RenewDeadline:   30 * time.Second, // Standard Kubernetes components deadline
		RetryPeriod:     20 * time.Second, // Standard Kubernetes components retry
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				work.run(leaderCtx, func(workCtx context.Context) {
					log.Info().Msgf("Started leading with identity: %s", identity)
					if err := d.becomeLeader(workCtx); err != nil {
						log.Error().Msgf("Failed to become leader: %v", err)
						cancel()
					}
				})
			},
			OnStoppedLeading: func() {
				if ctx.Err() != nil {
					return
				}
				log.Error().Msgf("Lost leadership unexpectedly, identity: %s", identity)
				// Plugin calls do not support cancellation. Exit immediately on
				// involuntary loss rather than letting old work outlive leadership.
				os.Exit(1)
			},
			OnNewLeader: func(leaderIdentity string) {
				if leaderIdentity == identity {
					log.Info().Msgf("We are the new leader: %s", leaderIdentity)
				} else {
					log.Info().Msgf("New leader elected: %s", leaderIdentity)
				}
			},
		},
	}

	leaderElectionDone := make(chan struct{})
	go func() {
		defer close(leaderElectionDone)
		leaderelection.RunOrDie(ctx, leaderElectionConfig)
	}()

	select {
	case sig := <-sigChan:
		log.Info().Msgf("Received signal %s. Terminating...", sig)
		// Keep renewing the lease while the in-flight step drains. Only then
		// release it, so a successor cannot overlap our backend mutations.
		work.stopAndWait()
		cancel()
		<-leaderElectionDone
	case <-leaderElectionDone:
		log.Info().Msg("Leader election completed")
	}
}

// becomeLeader runs once when this instance acquires leadership. It rebuilds
// the GUID pool state from the cluster and then enters the reconciliation
// loop in runLeaderLogic.
func (d *daemon) becomeLeader(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	log.Info().Msg("Becoming leader, initializing daemon logic")
	d.podCtrl.stopErr = ctx.Err

	if err := d.podCtrl.initGUIDPool(); err != nil {
		log.Error().Msgf("initGUIDPool(): Leader could not init the guid pool: %v", err)
		return fmt.Errorf("failed to initialize GUID pool as leader: %v", err)
	}

	if ctx.Err() == nil {
		d.runLeaderLogic(ctx)
	}
	return nil
}

// runLeaderLogic runs all lifecycle mutations on one worker. Informers only
// enqueue work and remain responsive while a backend request is in flight.
func (d *daemon) runLeaderLogic(ctx context.Context) {
	log.Info().Msg("Starting leader daemon logic")
	podWatcherStopFunc := d.podCtrl.podWatcher.RunBackground()
	nadWatcherStopFunc := d.partitionCtrl.nadWatcher.RunBackground()
	defer podWatcherStopFunc()
	defer nadWatcherStopFunc()

	runPeriodicSteps(ctx, time.Duration(d.config.PeriodicUpdate)*time.Second,
		d.podCtrl.DeletePeriodicUpdate,
		d.partitionCtrl.ProcessNADChanges,
		d.podCtrl.AddPeriodicUpdate)
}

// runPeriodicSteps checks cancellation between each serialized step. Returning
// means the active step has finished; cancellation never starts another step.
func runPeriodicSteps(ctx context.Context, interval time.Duration, steps ...func()) {
	for {
		for _, step := range steps {
			if ctx.Err() != nil {
				return
			}
			step()
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// leaderWork gates callback startup against voluntary shutdown. The election
// may invoke OnStartedLeading asynchronously after shutdown has already begun.
type leaderWork struct {
	mu       sync.Mutex
	stopping bool
	cancel   context.CancelFunc
	done     chan struct{}
}

func (w *leaderWork) run(ctx context.Context, run func(context.Context)) {
	w.mu.Lock()
	if w.stopping {
		w.mu.Unlock()
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()
	defer close(done)
	defer w.cancel()
	run(ctx)
}

func (w *leaderWork) stopAndWait() {
	w.mu.Lock()
	w.stopping = true
	if w.cancel != nil {
		w.cancel()
	}
	done := w.done
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}
