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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
	kapi "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
	"github.com/Mellanox/ib-kubernetes/pkg/guid"
	k8sClient "github.com/Mellanox/ib-kubernetes/pkg/k8s-client"
	"github.com/Mellanox/ib-kubernetes/pkg/sm"
	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"
	"github.com/Mellanox/ib-kubernetes/pkg/utils"
	"github.com/Mellanox/ib-kubernetes/pkg/watcher"
	resEvenHandler "github.com/Mellanox/ib-kubernetes/pkg/watcher/handler"
)

type Daemon interface {
	// Execute Daemon loop, returns when os.Interrupt signal is received
	Run()
}

type daemon struct {
	config            config.DaemonConfig
	podWatcher        watcher.Watcher
	nadWatcher        watcher.Watcher // NAD watcher for network definition changes
	kubeClient        k8sClient.Client
	guidPool          guid.Pool
	smClient          plugins.SubnetManagerClient
	guidPodNetworkMap map[string]string // allocated guid mapped to the pod and network

	// NAD add-only cache
	nadCache map[string]*v1.NetworkAttachmentDefinition // network ID -> NAD
}

// Temporary struct used to proceed pods' networks
type podNetworkInfo struct {
	pod       *kapi.Pod
	ibNetwork *v1.NetworkSelectionElement
	networks  []*v1.NetworkSelectionElement
	addr      net.HardwareAddr // GUID allocated for ibNetwork and saved as net.HardwareAddr
}

type networksMap struct {
	theMap map[types.UID][]*v1.NetworkSelectionElement
}

// Exponential backoff ~26 sec + 6 * <api call time>
// NOTE: k8s client has built in exponential backoff, which ib-kubernetes don't use.
// In case client's backoff was configured time may dramatically increase.
// NOTE: ufm client has default timeout on request operation for 30 seconds.
var backoffValues = wait.Backoff{Duration: 1 * time.Second, Factor: 1.6, Jitter: 0.1, Steps: 6}

// Return networks mapped to the pod. If mapping not exist it is created
func (n *networksMap) getPodNetworks(pod *kapi.Pod) ([]*v1.NetworkSelectionElement, error) {
	var err error
	networks, ok := n.theMap[pod.UID]
	if !ok {
		networks, err = netAttUtils.ParsePodNetworkAnnotation(pod)
		if err != nil {
			return nil, fmt.Errorf("failed to read pod networkName annotations pod namespace %s name %s, with error: %v",
				pod.Namespace, pod.Name, err)
		}

		n.theMap[pod.UID] = networks
	}
	return networks, nil
}

// NewDaemon initializes the need components including k8s client, subnet manager client plugins, and guid pool.
// It returns error in case of failure.
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

	// Return daemon fully formed
	return &daemon{
		config:            daemonConfig,
		kubeClient:        client,
		guidPool:          guidPool,
		smClient:          smClient,
		guidPodNetworkMap: make(map[string]string),
		podWatcher:        podWatcher,
		nadWatcher:        nadWatcher,
		nadCache:          make(map[string]*v1.NetworkAttachmentDefinition),
	}, nil
}

func (d *daemon) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

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

	// Create leader election configuration
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

	leaderElectionConfig := leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   60 * time.Second, // Standard Kubernetes components duration
		RenewDeadline:   30 * time.Second, // Standard Kubernetes components deadline
		RetryPeriod:     20 * time.Second, // Standard Kubernetes components retry
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				log.Info().Msgf("Started leading with identity: %s", identity)
				if err := d.becomeLeader(); err != nil {
					log.Error().Msgf("Failed to become leader: %v", err)
					// Cancel context to gracefully release lease and exit
					cancel()
					return
				}
			},
			OnStoppedLeading: func() {
				log.Error().Msgf("Lost leadership unexpectedly, identity: %s", identity)
				// Leadership lost unexpectedly - force immediate restart for clean state
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

	// Start leader election in background
	leaderElectionDone := make(chan struct{})
	go func() {
		defer close(leaderElectionDone)
		leaderelection.RunOrDie(ctx, leaderElectionConfig)
	}()

	// Wait for termination signal or leader election completion
	select {
	case sig := <-sigChan:
		log.Info().Msgf("Received signal %s. Terminating...", sig)
		cancel() // This triggers ReleaseOnCancel
		// Wait for graceful lease release
		select {
		case <-leaderElectionDone:
		case <-time.After(5 * time.Second):
			log.Warn().Msg("Graceful shutdown timeout exceeded")
		}
	case <-leaderElectionDone:
		log.Info().Msg("Leader election completed")
	}
}

// becomeLeader is called when this instance becomes the leader
func (d *daemon) becomeLeader() error {
	log.Info().Msg("Becoming leader, initializing daemon logic")

	// Initialize the GUID pool (rebuild state from existing pods)
	if err := d.initGUIDPool(); err != nil {
		log.Error().Msgf("initGUIDPool(): Leader could not init the guid pool: %v", err)
		return fmt.Errorf("failed to initialize GUID pool as leader: %v", err)
	}

	// Start the actual daemon logic
	d.runLeaderLogic()
	return nil
}

// runLeaderLogic runs the actual daemon operations, only called by the leader
func (d *daemon) runLeaderLogic() {
	log.Info().Msg("Starting leader daemon logic")

	// Run periodic tasks (only leader should do this)
	stopPeriodicsChan := make(chan struct{})

	go wait.Until(d.AddPeriodicUpdate, time.Duration(d.config.PeriodicUpdate)*time.Second, stopPeriodicsChan)
	go wait.Until(d.DeletePeriodicUpdate, time.Duration(d.config.PeriodicUpdate)*time.Second, stopPeriodicsChan)
	go wait.Until(d.ProcessNADChanges, time.Duration(d.config.PeriodicUpdate)*time.Second, stopPeriodicsChan)
	defer close(stopPeriodicsChan)

	// Run both watchers in background
	podWatcherStopFunc := d.podWatcher.RunBackground()
	nadWatcherStopFunc := d.nadWatcher.RunBackground()
	defer podWatcherStopFunc()
	defer nadWatcherStopFunc()

	// Run until interrupted by os signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	log.Info().Msgf("Received signal %s. Terminating...", sig)
}

// If network identified by networkID is IbSriov return network name and spec
func (d *daemon) getIbSriovNetwork(networkID string) (string, *utils.IbSriovCniSpec, error) {
	_, networkName, err := utils.ParseNetworkID(networkID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse network id %s with error: %v", networkID, err)
	}

	// Try to get net-attach-def from cache first, then fallback to API
	netAttInfo, err := d.getCachedNAD(networkID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get network attachment %s: %v", networkName, err)
	}
	log.Debug().Msgf("networkName attachment %v", netAttInfo)

	networkSpec := make(map[string]interface{})
	err = json.Unmarshal([]byte(netAttInfo.Spec.Config), &networkSpec)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse networkName attachment %s with error: %v", networkName, err)
	}
	log.Debug().Msgf("networkName attachment spec %+v", networkSpec)

	ibCniSpec, err := utils.GetIbSriovCniFromNetwork(networkSpec)
	if err != nil {
		return "", nil, fmt.Errorf(
			"failed to get InfiniBand SR-IOV CNI spec from network attachment %+v, with error %v",
			networkSpec, err)
	}

	log.Debug().Msgf("ib-sriov CNI spec %+v", ibCniSpec)
	return networkName, ibCniSpec, nil
}

// Return pod network info
// Return all pod network infos for multiple interfaces with same network name
func getAllPodNetworkInfos(netName string, pod *kapi.Pod, netMap networksMap) ([]*podNetworkInfo, error) {
	networks, err := netMap.getPodNetworks(pod)
	if err != nil {
		return nil, err
	}

	var matchingNetworks []*v1.NetworkSelectionElement
	matchingNetworks, err = utils.GetAllPodNetworks(networks, netName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod network specs for network %s with error: %v", netName, err)
	}

	podNetworkInfos := make([]*podNetworkInfo, 0, len(matchingNetworks))
	for _, network := range matchingNetworks {
		podNetworkInfos = append(podNetworkInfos, &podNetworkInfo{
			pod:       pod,
			networks:  networks,
			ibNetwork: network,
		})
	}

	return podNetworkInfos, nil
}

// processPodsForNetwork processes all pods and their interfaces for a given network
func (d *daemon) processPodsForNetwork(
	pods []*kapi.Pod, networkName string, ibCniSpec *utils.IbSriovCniSpec, netMap networksMap,
) ([]net.HardwareAddr, []*podNetworkInfo) {
	var guidList []net.HardwareAddr
	var passedPods []*podNetworkInfo

	for _, pod := range pods {
		log.Debug().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)

		// Get all network interfaces with the same network name
		podNetworkInfos, err := getAllPodNetworkInfos(networkName, pod, netMap)
		if err != nil {
			log.Error().Msgf("%v", err)
			continue
		}

		// Process each network interface separately
		for i, pi := range podNetworkInfos {
			interfaceName := pi.ibNetwork.InterfaceRequest
			if interfaceName == "" {
				interfaceName = fmt.Sprintf("idx_%d", i)
			}
			log.Debug().Msgf("processing interface %s for network %s on pod %s", interfaceName, networkName, pod.Name)

			if err = d.processNetworkGUID(networkName, ibCniSpec, pi, i); err != nil {
				log.Error().Msgf("failed to process network GUID for interface %s: %v", interfaceName, err)
				continue
			}

			guidList = append(guidList, pi.addr)
			passedPods = append(passedPods, pi)
		}
	}

	return guidList, passedPods
}

// Verify if GUID already exist for given network ID and allocates new one if not
func (d *daemon) allocatePodNetworkGUID(allocatedGUID, podNetworkID string, podUID types.UID, targetPkey string) error {
	existingPkey, _ := d.guidPool.Get(allocatedGUID)
	if existingPkey != "" {
		// This happens when a GUID is being reallocated to a different PKey
		// (e.g., pod was rescheduled or network configuration changed)
		if err := d.removeStaleGUID(allocatedGUID, existingPkey); err != nil {
			log.Warn().Msgf("failed to remove stale GUID %s from pkey %s: %v", allocatedGUID, existingPkey, err)
		}
	}
	if mappedID, exist := d.guidPodNetworkMap[allocatedGUID]; exist {
		if podNetworkID != mappedID {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
				allocatedGUID, mappedID)
		}
	} else if err := d.guidPool.AllocateGUID(allocatedGUID, targetPkey); err != nil {
		return fmt.Errorf("failed to allocate GUID for pod ID %s, with error: %v", podUID, err)
	} else {
		d.guidPodNetworkMap[allocatedGUID] = podNetworkID
	}

	return nil
}

// Allocate network GUID, update Pod's networks annotation and add GUID to the podNetworkInfo instance
func (d *daemon) processNetworkGUID(
	networkID string, spec *utils.IbSriovCniSpec, pi *podNetworkInfo, interfaceIndex int,
) error {
	var guidAddr guid.GUID
	allocatedGUID, err := utils.GetPodNetworkGUID(pi.ibNetwork)

	// Generate unique ID per interface to handle multiple interfaces with same network name
	var interfaceName string
	if pi.ibNetwork.InterfaceRequest != "" {
		interfaceName = pi.ibNetwork.InterfaceRequest
	} else {
		// Use interface index if interface name is not available
		interfaceName = fmt.Sprintf("idx_%d", interfaceIndex)
	}
	podNetworkID := utils.GeneratePodNetworkInterfaceID(pi.pod, networkID, interfaceName)
	if err == nil {
		// User allocated guid manually or Pod's network was rescheduled
		guidAddr, err = guid.ParseGUID(allocatedGUID)
		if err != nil {
			return fmt.Errorf("failed to parse user allocated guid %s with error: %v", allocatedGUID, err)
		}

		err = d.allocatePodNetworkGUID(allocatedGUID, podNetworkID, pi.pod.UID, spec.PKey)
		if err != nil {
			return err
		}
	} else {
		guidAddr, err = d.guidPool.GenerateGUID()
		if err != nil {
			switch err {
			// If the guid pool is exhausted, need to sync with SM in case there are unsynced changes
			case guid.ErrGUIDPoolExhausted:
				err = d.syncWithSubnetManager()
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("failed to generate GUID for pod ID %s, with error: %v", pi.pod.UID, err)
			}
		}

		allocatedGUID = guidAddr.String()
		err = d.allocatePodNetworkGUID(allocatedGUID, podNetworkID, pi.pod.UID, spec.PKey)
		if err != nil {
			return err
		}

		err = utils.SetPodNetworkGUID(pi.ibNetwork, allocatedGUID, spec.Capabilities["infinibandGUID"])
		if err != nil {
			return fmt.Errorf("failed to set pod network guid with error: %v ", err)
		}

		// Update Pod's network annotation here, so if network will be rescheduled we wouldn't allocate it again
		netAnnotations, err := json.Marshal(pi.networks)
		if err != nil {
			return fmt.Errorf("failed to dump networks %+v of pod into json with error: %v", pi.networks, err)
		}

		pi.pod.Annotations[v1.NetworkAttachmentAnnot] = string(netAnnotations)
	}

	// used GUID as net.HardwareAddress to use it in sm plugin which receive []net.HardwareAddress as parameter
	pi.addr = guidAddr.HardWareAddress()
	return nil
}

func (d *daemon) removeStaleGUID(allocatedGUID, existingPkey string) error {
	parsedPkey, err := utils.ParsePKey(existingPkey)
	if err != nil {
		log.Error().Msgf("failed to parse PKey %s with error: %v", existingPkey, err)
		return err
	}
	guidAddr, err := guid.ParseGUID(allocatedGUID)
	if err != nil {
		return fmt.Errorf("failed to parse user allocated guid %s with error: %v", allocatedGUID, err)
	}
	allocatedGUIDList := []net.HardwareAddr{guidAddr.HardWareAddress()}
	// Try to remove pKeys via subnet manager in backoff loop
	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		log.Info().Msgf("removing guids of previous pods from pKey %s"+
			" with subnet manager %s", existingPkey,
			d.smClient.Name())
		if err = d.smClient.RemoveGuidsFromPKey(parsedPkey, allocatedGUIDList); err != nil {
			log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
				" with subnet manager %s with error: %v", existingPkey,
				d.smClient.Name(), err)
			return false, nil //nolint:nilerr // retry pattern for exponential backoff
		}
		return true, nil
	}); err != nil {
		log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
			" with subnet manager %s", existingPkey, d.smClient.Name())
		return err
	}

	if err = d.guidPool.ReleaseGUID(allocatedGUID); err != nil {
		log.Warn().Msgf("failed to release guid \"%s\" with error: %v", allocatedGUID, err)
		return err
	}
	delete(d.guidPodNetworkMap, allocatedGUID)
	log.Info().Msgf("successfully released %s from pkey %s", allocatedGUID, existingPkey)
	return nil
}

// Update and set Pod's network annotation.
// If failed to update annotation, pod's GUID added into the list to be removed from Pkey.
func (d *daemon) updatePodNetworkAnnotation(pi *podNetworkInfo, removedList *[]net.HardwareAddr, pkey string) error {
	if pi.ibNetwork.CNIArgs == nil {
		pi.ibNetwork.CNIArgs = &map[string]interface{}{}
	}

	(*pi.ibNetwork.CNIArgs)[utils.InfiniBandAnnotation] = utils.ConfiguredInfiniBandPod
	(*pi.ibNetwork.CNIArgs)[utils.PkeyAnnotation] = pkey

	netAnnotations, err := json.Marshal(pi.networks)
	if err != nil {
		return fmt.Errorf("failed to dump networks %+v of pod into json with error: %v", pi.networks, err)
	}

	pi.pod.Annotations[v1.NetworkAttachmentAnnot] = string(netAnnotations)

	// Try to set pod's annotations in backoff loop
	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		if err = d.kubeClient.SetAnnotationsOnPod(pi.pod, pi.pod.Annotations); err != nil {
			if kerrors.IsNotFound(err) {
				return false, err
			}
			log.Warn().Msgf("failed to update pod annotations with err: %v", err)
			return false, nil
		}

		return true, nil
	}); err != nil {
		log.Error().Msgf("failed to update pod annotations")

		if err = d.guidPool.ReleaseGUID(pi.addr.String()); err != nil {
			log.Warn().Msgf("failed to release guid \"%s\" from removed pod \"%s\" in namespace "+
				"\"%s\" with error: %v", pi.addr.String(), pi.pod.Name, pi.pod.Namespace, err)
		} else {
			delete(d.guidPodNetworkMap, pi.addr.String())
		}

		*removedList = append(*removedList, pi.addr)
	}

	return nil
}

//nolint:nilerr
func (d *daemon) AddPeriodicUpdate() {
	log.Info().Msgf("running periodic add update")
	addMap, _ := d.podWatcher.GetHandler().GetResults()
	addMap.Lock()
	defer addMap.Unlock()
	// Contains ALL pods' networks
	netMap := networksMap{theMap: make(map[types.UID][]*v1.NetworkSelectionElement)}
	for networkID, podsInterface := range addMap.Items {
		log.Info().Msgf("processing network networkID %s", networkID)
		pods, ok := podsInterface.([]*kapi.Pod)
		if !ok {
			log.Error().Msgf(
				"invalid value for add map networks expected pods array \"[]*kubernetes.Pod\", found %T",
				podsInterface)
			continue
		}

		if len(pods) == 0 {
			continue
		}
		networkName, ibCniSpec, err := d.getIbSriovNetwork(networkID)
		if err != nil {
			// Do not drop the network; keep for next periodic run when NAD becomes available
			log.Warn().Msgf("NAD not ready for network %s: %v (will retry)", networkID, err)
			continue
		}

		guidList, passedPods := d.processPodsForNetwork(pods, networkName, ibCniSpec, netMap)

		// Get configured PKEY for network and add the relevant POD GUIDs as members of the PKey via Subnet Manager
		if ibCniSpec.PKey != "" && len(guidList) != 0 {
			var pKey int
			pKey, err = utils.ParsePKey(ibCniSpec.PKey)
			if err != nil {
				log.Error().Msgf("failed to parse PKey %s with error: %v", ibCniSpec.PKey, err)
				continue
			}

			// Try to add pKeys via subnet manager in backoff loop
			if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
				if err = d.smClient.AddGuidsToPKey(pKey, guidList); err != nil {
					log.Warn().Msgf("failed to config pKey with subnet manager %s with error : %v",
						d.smClient.Name(), err)
					return false, nil
				}
				return true, nil
			}); err != nil {
				log.Error().Msgf("failed to config pKey with subnet manager %s", d.smClient.Name())
				continue
			}
		}

		// Update annotations for PODs that finished the previous steps successfully
		var removedGUIDList []net.HardwareAddr
		for _, pi := range passedPods {
			err = d.updatePodNetworkAnnotation(pi, &removedGUIDList, ibCniSpec.PKey)
			if err != nil {
				log.Error().Msgf("%v", err)
			}
		}

		if ibCniSpec.PKey != "" && len(removedGUIDList) != 0 {
			// Already check the parse above
			pKey, _ := utils.ParsePKey(ibCniSpec.PKey)

			// Try to remove pKeys via subnet manager in backoff loop
			if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
				if err = d.smClient.RemoveGuidsFromPKey(pKey, removedGUIDList); err != nil {
					log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
						" with subnet manager %s with error: %v", ibCniSpec.PKey,
						d.smClient.Name(), err)
					return false, nil
				}
				return true, nil
			}); err != nil {
				log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
					" with subnet manager %s", ibCniSpec.PKey, d.smClient.Name())
				continue
			}
		}

		addMap.UnSafeRemove(networkID)
	}
	log.Info().Msg("add periodic update finished")
}

// get all GUIDs from Pod's networks with the same name (handles multiple interfaces)
func getAllPodGUIDsForNetwork(pod *kapi.Pod, networkName string) ([]net.HardwareAddr, error) {
	networks, netErr := netAttUtils.ParsePodNetworkAnnotation(pod)
	if netErr != nil {
		return nil, fmt.Errorf("failed to read pod networkName annotations pod namespace %s name %s, with error: %v",
			pod.Namespace, pod.Name, netErr)
	}

	matchingNetworks, netErr := utils.GetAllPodNetworks(networks, networkName)
	if netErr != nil {
		return nil, fmt.Errorf("failed to get pod networkName specs %s with error: %v", networkName, netErr)
	}

	guidAddrs := make([]net.HardwareAddr, 0, len(matchingNetworks))
	for _, network := range matchingNetworks {
		if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
			log.Debug().Msgf("network %+v is not InfiniBand configured, skipping", network)
			continue
		}

		allocatedGUID, netErr := utils.GetPodNetworkGUID(network)
		if netErr != nil {
			log.Debug().Msgf("failed to get GUID for network interface %s: %v", network.InterfaceRequest, netErr)
			continue
		}

		guidAddr, guidErr := net.ParseMAC(allocatedGUID)
		if guidErr != nil {
			log.Error().Msgf("failed to parse allocated Pod GUID %s, error: %v", allocatedGUID, guidErr)
			continue
		}

		guidAddrs = append(guidAddrs, guidAddr)
	}

	return guidAddrs, nil
}

//nolint:nilerr
func (d *daemon) DeletePeriodicUpdate() {
	log.Info().Msg("running delete periodic update")
	_, deleteMap := d.podWatcher.GetHandler().GetResults()
	deleteMap.Lock()
	defer deleteMap.Unlock()
	for networkID, podsInterface := range deleteMap.Items {
		log.Info().Msgf("processing network networkID %s", networkID)
		pods, ok := podsInterface.([]*kapi.Pod)
		if !ok {
			log.Error().Msgf("invalid value for add map networks expected pods array \"[]*kubernetes.Pod\", found %T",
				podsInterface)
			continue
		}

		if len(pods) == 0 {
			continue
		}

		networkName, ibCniSpec, err := d.getIbSriovNetwork(networkID)
		if err != nil {
			deleteMap.UnSafeRemove(networkID)
			log.Warn().Msgf("droping network: %v", err)
			continue
		}

		var guidList []net.HardwareAddr
		for _, pod := range pods {
			log.Debug().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)

			// Get all GUIDs for all interfaces with the same network name
			var podGUIDs []net.HardwareAddr
			podGUIDs, err = getAllPodGUIDsForNetwork(pod, networkName)
			if err != nil {
				log.Error().Msgf("%v", err)
				continue
			}

			// Process each GUID from the pod
			for _, guidAddr := range podGUIDs {
				podNetworkID := utils.GeneratePodNetworkID(pod, networkName)
				if guidPodEntry, exist := d.guidPodNetworkMap[guidAddr.String()]; exist {
					if podNetworkID == guidPodEntry {
						log.Info().Msgf("matched guid %s to pod %s, removing", guidAddr, guidPodEntry)
						guidList = append(guidList, guidAddr)
					} else {
						log.Warn().Msgf("guid %s is allocated to another pod %s not %s, not removing",
							guidAddr, guidPodEntry, podNetworkID)
					}
				} else {
					log.Warn().Msgf("guid %s is not allocated to any pod on delete", guidAddr)
				}
			}
		}

		if ibCniSpec.PKey != "" && len(guidList) != 0 {
			pKey, pkeyErr := utils.ParsePKey(ibCniSpec.PKey)
			if pkeyErr != nil {
				log.Error().Msgf("failed to parse PKey %s with error: %v", ibCniSpec.PKey, pkeyErr)
				continue
			}

			// Try to remove pKeys via subnet manager on backoff loop
			if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
				if err = d.smClient.RemoveGuidsFromPKey(pKey, guidList); err != nil {
					log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
						" with subnet manager %s with error: %v", ibCniSpec.PKey,
						d.smClient.Name(), err)
					return false, nil
				}
				return true, nil
			}); err != nil {
				log.Warn().Msgf("failed to remove guids of removed pods from pKey %s"+
					" with subnet manager %s", ibCniSpec.PKey, d.smClient.Name())
				continue
			}
		}

		for _, guidAddr := range guidList {
			if err = d.guidPool.ReleaseGUID(guidAddr.String()); err != nil {
				log.Error().Msgf("%v", err)
				continue
			}

			delete(d.guidPodNetworkMap, guidAddr.String())
		}
		deleteMap.UnSafeRemove(networkID)
	}

	log.Info().Msg("delete periodic update finished")
}

// ProcessNADChanges processes NAD add events
func (d *daemon) ProcessNADChanges() {
	log.Debug().Msg("Processing NAD changes...")

	nadHandler := d.nadWatcher.GetHandler().(*resEvenHandler.NADEventHandler)
	addedNADs, _ := nadHandler.GetResults()

	// Process NAD add events only
	addedNADs.Lock()
	for networkID, nad := range addedNADs.Items {
		nadObj := nad.(*v1.NetworkAttachmentDefinition)

		// Add-only: cache the NAD; ignore updates/deletes
		d.nadCache[networkID] = nadObj

		log.Info().Msgf("Successfully processed NAD event: %s", networkID)

		// Remove processed item
		addedNADs.UnSafeRemove(networkID)
	}
	addedNADs.Unlock()

	log.Debug().Msg("NAD changes processing completed")
}

// getCachedNAD retrieves NAD from cache, falling back to API if not cached
func (d *daemon) getCachedNAD(networkID string) (*v1.NetworkAttachmentDefinition, error) {
	// First check cache
	if nad, exists := d.nadCache[networkID]; exists {
		return nad, nil
	}

	// Fall back to API call (existing behavior)
	networkNamespace, networkName, err := utils.ParseNetworkID(networkID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse network id %s with error: %v", networkID, err)
	}

	var netAttInfo *v1.NetworkAttachmentDefinition
	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		var getErr error
		netAttInfo, getErr = d.kubeClient.GetNetworkAttachmentDefinition(networkNamespace, networkName)
		if getErr != nil {
			log.Warn().Msgf("failed to get network attachment %s with error %v", networkName, getErr)
			// keep retrying until backoff exhausted
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, fmt.Errorf("failed to get network attachment %s", networkName)
	}

	// Cache the result
	d.nadCache[networkID] = netAttInfo

	return netAttInfo, nil
}

// initGUIDPool initializes the GUID pool by first populating guidPodNetworkMap with existing pods,
// then syncing with subnet manager and cleaning up stale GUIDs
func (d *daemon) initGUIDPool() error {
	log.Info().Msg("Initializing GUID pool.")

	// First populate guidPodNetworkMap with existing pods
	var pods *kapi.PodList
	if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		var err error
		if pods, err = d.kubeClient.GetPods(kapi.NamespaceAll); err != nil {
			log.Warn().Msgf("failed to get pods from kubernetes: %v", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		err = fmt.Errorf("failed to get pods from kubernetes")
		log.Error().Msgf("%v", err)
		return err
	}

	for index := range pods.Items {
		log.Debug().Msgf("checking pod for network annotations %v", pods.Items[index])
		pod := pods.Items[index]
		if utils.PodIsFinished(&pod) {
			continue
		}
		networks, err := netAttUtils.ParsePodNetworkAnnotation(&pod)
		if err != nil {
			continue
		}

		for _, network := range networks {
			if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
				continue
			}

			podGUID, err := utils.GetPodNetworkGUID(network)
			if err != nil {
				continue
			}

			podNetworkID := string(pod.UID) + network.Name
			if _, exist := d.guidPodNetworkMap[podGUID]; exist {
				if podNetworkID != d.guidPodNetworkMap[podGUID] {
					return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
						podGUID, d.guidPodNetworkMap[podGUID])
				}
				continue
			}
			podPkey, _ := utils.GetPodNetworkPkey(network)
			if err = d.guidPool.AllocateGUID(podGUID, podPkey); err != nil {
				err = fmt.Errorf("failed to allocate guid for running pod: %v", err)
				log.Error().Msgf("%v", err)
				continue
			}

			d.guidPodNetworkMap[podGUID] = podNetworkID
		}
	}

	// Now sync with subnet manager and clean up stale GUIDs
	return d.syncWithSubnetManager()
}

// syncWithSubnetManager syncs the GUID pool with the subnet manager
// This is used both during initialization and when the pool is exhausted at runtime
func (d *daemon) syncWithSubnetManager() error {
	usedGuids, err := d.smClient.ListGuidsInUse()
	if err != nil {
		return err
	}

	// Reset guid pool with already allocated guids to avoid collisions
	err = d.guidPool.Reset(usedGuids)
	if err != nil {
		return err
	}

	// Remove stale GUIDs that are no longer in use by the subnet manager
	// This handles cleanup of GUIDs from deleted/finished pods
	for allocatedGUID, podNetworkID := range d.guidPodNetworkMap {
		if _, found := usedGuids[allocatedGUID]; !found {
			// If GUID is not found in the subnet manager's list of used GUIDs,
			// it means the pod was deleted/finished and we should clean it up
			log.Info().Msgf("removing stale GUID %s for pod network %s", allocatedGUID, podNetworkID)
			if err = d.guidPool.ReleaseGUID(allocatedGUID); err != nil {
				log.Warn().Msgf("failed to release stale guid \"%s\" with error: %v", allocatedGUID, err)
			} else {
				delete(d.guidPodNetworkMap, allocatedGUID)
				log.Info().Msgf("successfully cleaned up stale GUID %s", allocatedGUID)
			}
		}
	}

	return nil
}
