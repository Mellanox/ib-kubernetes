package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	v1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	netAttUtils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/rs/zerolog/log"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

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
	watcher           watcher.Watcher
	kubeClient        k8sClient.Client
	guidPool          guid.Pool
	smClient          plugins.SubnetManagerClient
	guidPodNetworkMap map[string]string // allocated guid mapped to the pod and network
}

// Temporary struct used to proceed pods' networks
type PodWrapper struct {
	pod       *kapi.Pod
	ibNetwork *v1.NetworkSelectionElement
	networks  []*v1.NetworkSelectionElement
	addr      net.HardwareAddr // GUID allocated for ibNetwork and saved as net.HardwareAddr
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
	client, err := k8sClient.NewK8sClient()

	if err != nil {
		return nil, err
	}

	guidPool, err := guid.NewPool(&daemonConfig.GUIDPool)
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

	if err := smClient.Validate(); err != nil {
		return nil, err
	}

	podWatcher := watcher.NewWatcher(podEventHandler, client)
	return &daemon{
		config:            daemonConfig,
		watcher:           podWatcher,
		kubeClient:        client,
		guidPool:          guidPool,
		smClient:          smClient,
		guidPodNetworkMap: make(map[string]string)}, nil
}

func (d *daemon) Run() {
	// setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Init the guid pool
	if err := d.initPool(); err != nil {
		log.Error().Msgf("initPool(): Daemon could not init the guid pool: %v", err)
		os.Exit(1)
	}

	// Run periodic tasks
	// closing the channel will stop the goroutines executed in the wait.Until() calls below
	stopPeriodicsChan := make(chan struct{})
	go wait.Until(d.AddPeriodicUpdate, time.Duration(d.config.PeriodicUpdate)*time.Second, stopPeriodicsChan)
	go wait.Until(d.DeletePeriodicUpdate, time.Duration(d.config.PeriodicUpdate)*time.Second, stopPeriodicsChan)
	defer close(stopPeriodicsChan)

	// Run Watcher in background, calling watcherStopFunc() will stop the watcher
	watcherStopFunc := d.watcher.RunBackground()
	defer watcherStopFunc()

	// Run until interrupted by os signals
	sig := <-sigChan
	log.Info().Msgf("Received signal %s. Terminating...", sig)
}

// If network identified by networkID is IbSriov return network name and spec
func (d *daemon) getIbSriovNetwork(networkID string) (string, *utils.IbSriovCniSpec, error) {
	networkNamespace, networkName, err := utils.ParseNetworkID(networkID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse network id %s with error: %v", networkID, err)
	}

	netAttInfo, err := d.kubeClient.GetNetworkAttachmentDefinition(networkNamespace, networkName)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get networkName attachment %s with error: %v", networkName, err)
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
		return "", nil, fmt.Errorf("failed to get InfiniBand SR-IOV CNI spec from network attachment %+v, with error %v",
			networkSpec, err)
	}

	log.Debug().Msgf("ib-sriov CNI spec %+v", ibCniSpec)
	return networkName, ibCniSpec, nil
}

// Get Pod's IB network specified by name. Update netMap with Pod's networks.
func getIbSriovPodNetwork(netName string, pw *PodWrapper, netMap map[types.UID][]*v1.NetworkSelectionElement) error {
	var err error
	networks, ok := netMap[pw.pod.UID]
	if !ok {
		networks, err = netAttUtils.ParsePodNetworkAnnotation(pw.pod)
		if err != nil {
			return fmt.Errorf("failed to read pod networkName annotations pod namespace %s name %s, with error: %v",
				pw.pod.Namespace, pw.pod.Name, err)
		}

		netMap[pw.pod.UID] = networks
	}
	network, err := utils.GetPodNetwork(networks, netName)
	if err != nil {
		return fmt.Errorf("failed to get pod networkName spec %s with error: %v", netName, err)
	}

	pw.ibNetwork = network
	pw.networks = networks
	return nil
}

// Verify if GUID already exist for given network ID and allocates new one if not
func (d *daemon) mapGUIDToPodNetwork(allocatedGUID, podNetworkID string, podUID types.UID) error {
	if _, exist := d.guidPodNetworkMap[allocatedGUID]; exist {
		if podNetworkID != d.guidPodNetworkMap[allocatedGUID] {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
				allocatedGUID, d.guidPodNetworkMap[allocatedGUID])
		}
	} else if err := d.guidPool.AllocateGUID(allocatedGUID); err != nil {
		return fmt.Errorf("failed to allocate GUID for pod ID %s, wit error: %v", podUID, err)
	} else {
		d.guidPodNetworkMap[allocatedGUID] = podNetworkID
	}

	return nil
}

// Allocate GUID if network don't have it and update Pod's networks annotation
func (d *daemon) ProceedNetworkGUID(pw *PodWrapper, networkID string) error {
	var guidAddr guid.GUID
	allocatedGUID, err := utils.GetPodNetworkGUID(pw.ibNetwork)
	podNetworkID := string(pw.pod.UID) + networkID
	if err == nil {
		// User allocated guid manually or Pod's network was rescheduled
		err = d.mapGUIDToPodNetwork(allocatedGUID, podNetworkID, pw.pod.UID)
		if err != nil {
			return err
		}

		guidAddr, err = guid.ParseGUID(allocatedGUID)
		if err != nil {
			return fmt.Errorf("failed to parse user allocated guid %s with error: %v", allocatedGUID, err)
		}
	} else {
		guidAddr, err = d.guidPool.GenerateGUID()
		if err != nil {
			return fmt.Errorf("failed to generate GUID for pod ID %s, wit error: %v", pw.pod.UID, err)
		}

		allocatedGUID = guidAddr.String()
		err = d.mapGUIDToPodNetwork(allocatedGUID, podNetworkID, pw.pod.UID)
		if err != nil {
			return err
		}

		if err = utils.SetPodNetworkGUID(pw.ibNetwork, allocatedGUID); err != nil {
			return fmt.Errorf("failed to set pod network guid with error: %v ", err)
		}

		// Update Pod's network annotation here, so if network will be rescheduled we wouldn't allocate it again
		netAnnotations, err := json.Marshal(pw.networks)
		if err != nil {
			return fmt.Errorf("failed to dump networks %+v of pod into json with error: %v", pw.networks, err)
		}

		pw.pod.Annotations[v1.NetworkAttachmentAnnot] = string(netAnnotations)
	}

	// used GUID as net.HardwareAddress to use it in sm plugin which receive []net.HardwareAddress as parameter
	pw.addr = guidAddr.HardWareAddress()
	return nil
}

// Update and set Pod's network annotation
func (d *daemon) UpdatePodNetworkAnnotation(pw *PodWrapper, removedList *[]net.HardwareAddr) error {
	(*pw.ibNetwork.CNIArgs)[utils.InfiniBandAnnotation] = utils.ConfiguredInfiniBandPod

	netAnnotations, err := json.Marshal(pw.networks)
	if err != nil {
		return fmt.Errorf("failed to dump networks %+v of pod into json with error: %v", pw.networks, err)
	}

	pw.pod.Annotations[v1.NetworkAttachmentAnnot] = string(netAnnotations)

	if err := d.kubeClient.SetAnnotationsOnPod(pw.pod, pw.pod.Annotations); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return fmt.Errorf("failed to update pod annotations with err: %v", err)
		}

		if err = d.guidPool.ReleaseGUID(pw.addr.String()); err != nil {
			log.Warn().Msgf("failed to release guid \"%s\" from removed pod \"%s\" in namespace "+
				"\"%s\" with error: %v", pw.addr.String(), pw.pod.Name, pw.pod.Namespace, err)
		} else {
			delete(d.guidPodNetworkMap, pw.addr.String())
		}

		*removedList = append(*removedList, pw.addr)
	}

	return nil
}

func (d *daemon) AddPeriodicUpdate() {
	log.Info().Msgf("running periodic add update")
	addMap, _ := d.watcher.GetHandler().GetResults()
	addMap.Lock()
	defer addMap.Unlock()
	// Contains ALL pods' networks
	podNetworksMap := map[types.UID][]*v1.NetworkSelectionElement{}
	for networkID, podsInterface := range addMap.Items {
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

		networkName, ibCniSpec, err := d.GetIbSriovNetwork(networkID)
		if err != nil {
			if strings.Contains(err.Error(), "SR-IOV CNI spec") {
				// Not an IB SR-IOV network
				addMap.UnSafeRemove(networkID)
			}
			log.Err(err)
			continue
		}

		var guidList []net.HardwareAddr
		var passedPods []*PodWrapper
		var failedPods []*kapi.Pod
		for _, pod := range pods {
			pw := PodWrapper{
				pod: pod,
			}
			log.Debug().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)
			err := getIbSriovPodNetwork(networkName, &pw, podNetworksMap)
			if err != nil {
				failedPods = append(failedPods, pod)
				log.Err(err)
				continue
			}
			if err = d.ProceedNetworkGUID(&pw, networkID); err != nil {
				if !strings.Contains(err.Error(), "already allocated") {
					failedPods = append(failedPods, pod)
				}
				log.Err(err)
				continue
			}

			guidList = append(guidList, pw.addr)
			passedPods = append(passedPods, &pw)
		}

		// Get configured PKEY for network and add the relevant POD GUIDs as members of the PKey via Subnet Manager
		if ibCniSpec.PKey != "" && len(guidList) != 0 {
			pKey, err := utils.ParsePKey(ibCniSpec.PKey)
			if err != nil {
				log.Error().Msgf("failed to parse PKey %s with error: %v", ibCniSpec.PKey, err)
				continue
			}

			if err = d.smClient.AddGuidsToPKey(pKey, guidList); err != nil {
				log.Error().Msgf("failed to config pKey with subnet manager %s with error: %v",
					d.smClient.Name(), err)
				continue
			}
		}

		// Update annotations for PODs that finished the previous steps successfully
		var removedGUIDList []net.HardwareAddr
		for _, pw := range passedPods {
			err := d.UpdatePodNetworkAnnotation(pw, &removedGUIDList)
			if err != nil {
				failedPods = append(failedPods, pw.pod)
				log.Err(err)
				continue
			}
		}

		if ibCniSpec.PKey != "" && len(removedGUIDList) != 0 {
			// Already check the parse above
			pKey, _ := utils.ParsePKey(ibCniSpec.PKey)
			if pkeyErr := d.smClient.RemoveGuidsFromPKey(pKey, removedGUIDList); pkeyErr != nil {
				log.Warn().Msgf("failed to remove guids of removed pods from pKey %s with subnet manager %s with error: %v",
					ibCniSpec.PKey, d.smClient.Name(), pkeyErr)
				continue
			}
		}

		if len(failedPods) == 0 {
			addMap.UnSafeRemove(networkID)
		} else {
			// Requeue faild pods for the network, they will be processed in the next AddPeriodicUpdate iteration
			addMap.UnSafeSet(networkID, failedPods)
		}
	}
	log.Info().Msg("add periodic update finished")
}

// get GUID from Pod's network
func getPodGUIDForNetwork(pod *kapi.Pod, networkName string) (net.HardwareAddr, error) {
	networks, netErr := netAttUtils.ParsePodNetworkAnnotation(pod)
	if netErr != nil {
		return nil, fmt.Errorf("failed to read pod networkName annotations pod namespace %s name %s, with error: %v",
			pod.Namespace, pod.Name, netErr)
	}

	network, netErr := utils.GetPodNetwork(networks, networkName)
	if netErr != nil {
		return nil, fmt.Errorf("failed to get pod networkName spec %s with error: %v", networkName, netErr)
	}

	if !utils.IsPodNetworkConfiguredWithInfiniBand(network) {
		return nil, fmt.Errorf("network %+v is not InfiniBand configured", network)
	}

	allocatedGUID, netErr := utils.GetPodNetworkGUID(network)
	if netErr != nil {
		return nil, netErr
	}

	guidAddr, guidErr := net.ParseMAC(allocatedGUID)
	if guidErr != nil {
		return nil, fmt.Errorf("failed to parse allocated pod with error: %v", guidErr)
	}

	return guidAddr, nil
}

func (d *daemon) DeletePeriodicUpdate() {
	log.Info().Msg("running delete periodic update")
	_, deleteMap := d.watcher.GetHandler().GetResults()
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
			log.Error().Msgf("%v", err)
			continue
		}

		var guidList []net.HardwareAddr
		var failedPods []*kapi.Pod
		var guidAddr net.HardwareAddr
		for _, pod := range pods {
			log.Debug().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)
			guidAddr, err = getPodGUIDForNetwork(pod, networkName)
			if err != nil {
				if !strings.Contains(err.Error(), "is not Infiniband configured") {
					failedPods = append(failedPods, pod)
				}
				log.Error().Msgf("%v", err)
				continue
			}

			guidList = append(guidList, guidAddr)
		}

		if ibCniSpec.PKey != "" && len(guidList) != 0 {
			pKey, pkeyErr := utils.ParsePKey(ibCniSpec.PKey)
			if pkeyErr != nil {
				log.Error().Msgf("failed to parse PKey %s with error: %v", ibCniSpec.PKey, pkeyErr)
				continue
			}

			if pkeyErr = d.smClient.RemoveGuidsFromPKey(pKey, guidList); pkeyErr != nil {
				log.Error().Msgf("failed to config pKey with subnet manager %s with error: %v",
					d.smClient.Name(), pkeyErr)
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
		if len(failedPods) == 0 {
			deleteMap.UnSafeRemove(networkID)
		} else {
			deleteMap.UnSafeSet(networkID, failedPods)
		}
	}

	log.Info().Msg("delete periodic update finished")
}

//  initPool check the guids that are already allocated by the running pods
func (d *daemon) initPool() error {
	log.Info().Msg("Initializing GUID pool.")
	pods, err := d.kubeClient.GetPods(kapi.NamespaceAll)
	if err != nil {
		err = fmt.Errorf("failed to get pods from kubernetes: %v", err)
		log.Err(err)
		return err
	}

	for index := range pods.Items {
		log.Debug().Msgf("checking pod for network annotations %v", pods.Items[index])
		pod := pods.Items[index]
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

			if err = d.guidPool.AllocateGUID(podGUID); err != nil {
				err = fmt.Errorf("failed to allocate guid for running pod: %v", err)
				log.Err(err)
				continue
			}

			d.guidPodNetworkMap[podGUID] = podNetworkID
		}
	}

	return nil
}
