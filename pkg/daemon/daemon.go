package daemon

import (
	"encoding/json"
	"errors"
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

const GUIDInUFMFinalizer = "ufm.together.ai/guid-cleanup-protection"

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

	// Reset guid pool with already allocated guids to avoid collisions
	err = syncGUIDPool(smClient, guidPool)
	if err != nil {
		return nil, err
	}

	podWatcher := watcher.NewWatcher(podEventHandler, client)
	return &daemon{
		config:            daemonConfig,
		watcher:           podWatcher,
		kubeClient:        client,
		guidPool:          guidPool,
		smClient:          smClient,
		guidPodNetworkMap: make(map[string]string),
	}, nil
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
//
//nolint:nilerr
func (d *daemon) getIbSriovNetwork(networkID string) (string, *utils.IbSriovCniSpec, error) {
	networkNamespace, networkName, err := utils.ParseNetworkID(networkID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse network id %s with error: %v", networkID, err)
	}

	// Try to get net-attach-def in backoff loop
	var netAttInfo *v1.NetworkAttachmentDefinition
	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		netAttInfo, err = d.kubeClient.GetNetworkAttachmentDefinition(networkNamespace, networkName)
		if err != nil {
			log.Warn().Msgf("failed to get networkName attachment %s with error %v",
				networkName, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return "", nil, fmt.Errorf("failed to get networkName attachment %s", networkName)
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
func getPodNetworkInfo(netName string, pod *kapi.Pod, netMap networksMap) (*podNetworkInfo, error) {
	networks, err := netMap.getPodNetworks(pod)
	if err != nil {
		return nil, err
	}

	var network *v1.NetworkSelectionElement
	network, err = utils.GetPodNetwork(networks, netName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod network spec for network %s with error: %v", netName, err)
	}

	return &podNetworkInfo{
		pod:       pod,
		networks:  networks,
		ibNetwork: network,
	}, nil
}

// Verify if GUID already exist for given network ID and allocates new one if not
func (d *daemon) allocatePodNetworkGUID(allocatedGUID, podNetworkID string, podUID types.UID) error {
	if mappedID, exist := d.guidPodNetworkMap[allocatedGUID]; exist {
		if podNetworkID != mappedID {
			return fmt.Errorf("failed to allocate requested guid %s, already allocated for %s",
				allocatedGUID, mappedID)
		}
	} else if err := d.guidPool.AllocateGUID(allocatedGUID); err != nil {
		return fmt.Errorf("failed to allocate GUID for pod ID %s, wit error: %v", podUID, err)
	} else {
		d.guidPodNetworkMap[allocatedGUID] = podNetworkID
	}

	return nil
}

// Allocate network GUID, update Pod's networks annotation and add GUID to the podNetworkInfo instance
func (d *daemon) processNetworkGUID(networkID string, spec *utils.IbSriovCniSpec, pi *podNetworkInfo) error {
	var guidAddr guid.GUID
	allocatedGUID, err := utils.GetPodNetworkGUID(pi.ibNetwork)
	podNetworkID := utils.GeneratePodNetworkID(pi.pod, networkID)
	if err == nil {
		log.Warn().Msgf("GUID Already allocated for : %v", networkID)
		// User allocated guid manually or Pod's network was rescheduled
		guidAddr, err = guid.ParseGUID(allocatedGUID)
		if err != nil {
			return fmt.Errorf("failed to parse user allocated guid %s with error: %v", allocatedGUID, err)
		}

		err = d.allocatePodNetworkGUID(allocatedGUID, podNetworkID, pi.pod.UID)
		if err != nil {
			return err
		}
	} else {
		guidAddr, err = d.guidPool.GenerateGUID()
		if err != nil {
			switch {
			case errors.Is(err, guid.ErrGUIDPoolExhausted):
				err = syncGUIDPool(d.smClient, d.guidPool)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("failed to generate GUID for pod ID %s, with error: %v", pi.pod.UID, err)
			}
		}

		allocatedGUID = guidAddr.String()
		err = d.allocatePodNetworkGUID(allocatedGUID, podNetworkID, pi.pod.UID)
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

func syncGUIDPool(smClient plugins.SubnetManagerClient, guidPool guid.Pool) error {
	usedGuids, err := smClient.ListGuidsInUse()
	if err != nil {
		return err
	}

	// Reset guid pool with already allocated guids to avoid collisions
	err = guidPool.Reset(usedGuids)
	if err != nil {
		return err
	}
	return nil
}

// Update and set Pod's network annotation.
// If failed to update annotation, pod's GUID added into the list to be removed from Pkey.
func (d *daemon) updatePodNetworkAnnotation(pi *podNetworkInfo, removedList *[]net.HardwareAddr) error {
	if pi.ibNetwork.CNIArgs == nil {
		pi.ibNetwork.CNIArgs = &map[string]interface{}{}
	}

	(*pi.ibNetwork.CNIArgs)[utils.InfiniBandAnnotation] = utils.ConfiguredInfiniBandPod
	netAnnotations, err := json.Marshal(pi.networks)
	if err != nil {
		return fmt.Errorf("failed to dump networks %+v of pod into json with error: %v", pi.networks, err)
	}

	pi.pod.Annotations[v1.NetworkAttachmentAnnot] = string(netAnnotations)

	// Try to set pod's annotations in backoff loop
	if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
		log.Info().Msgf("updatePodNetworkAnnotation(): Updating pod annotation for pod: %s with anootation: %s", pi.pod.Name, pi.pod.Annotations)
		if err = d.kubeClient.SetAnnotationsOnPod(pi.pod, pi.pod.Annotations); err != nil {
			if kerrors.IsNotFound(err) {
				return false, err
			}
			log.Warn().Msgf("failed to update pod annotations with err: %v", err)
			return false, nil
		}
		log.Info().Msgf("updatePodNetworkAnnotation(): Success on updating pod annotation for pod: %s with anootation: %s", pi.pod.Name, pi.pod.Annotations)
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
	addMap, _ := d.watcher.GetHandler().GetResults()
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

		log.Info().Msgf("processing network networkID %s", networkID)
		networkName, ibCniSpec, err := d.getIbSriovNetwork(networkID)
		if err != nil {
			addMap.UnSafeRemove(networkID)
			log.Error().Msgf("droping network: %v", err)
			continue
		}

		var guidList []net.HardwareAddr
		var passedPods []*podNetworkInfo
		for _, pod := range pods {
			log.Info().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)
			var pi *podNetworkInfo
			pi, err = getPodNetworkInfo(networkName, pod, netMap)
			if err != nil {
				log.Error().Msgf("%v", err)
				continue
			}
			if err = d.processNetworkGUID(networkName, ibCniSpec, pi); err != nil {
				log.Error().Msgf("%v", err)
				continue
			}

			guidList = append(guidList, pi.addr)
			passedPods = append(passedPods, pi)
		}
		log.Info().Interface("pods", passedPods).Msg("Passed pods")
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
			} else {
				// AddGuidsToPKey successful, now add GUIDs to limited partition if configured
				if d.config.DefaultLimitedPartition != "" {
					limitedPKey, err := utils.ParsePKey(d.config.DefaultLimitedPartition)
					if err != nil {
						log.Error().Msgf("failed to parse DEFAULT_LIMITED_PARTITION %s: %v", d.config.DefaultLimitedPartition, err)
					} else {
						// Try to add GUIDs to limited partition in backoff loop
						if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
							if err = d.smClient.AddGuidsToLimitedPKey(limitedPKey, guidList); err != nil {
								log.Warn().Msgf("failed to add GUIDs to limited partition 0x%04X with subnet manager %s with error: %v",
									limitedPKey, d.smClient.Name(), err)
								return false, nil
							}
							return true, nil
						}); err != nil {
							log.Error().Msgf("failed to add GUIDs to limited partition 0x%04X with subnet manager %s", limitedPKey, d.smClient.Name())
						} else {
							log.Info().Msgf("successfully added GUIDs %v to limited partition 0x%04X", guidList, limitedPKey)
						}
					}
				}

				// Add finalizer to NetworkAttachmentDefinition
				networkNamespace, networkName, _ := utils.ParseNetworkID(networkID)
				if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
					if err := d.kubeClient.AddFinalizerToNetworkAttachmentDefinition(
						networkNamespace, networkName, GUIDInUFMFinalizer); err != nil {
						log.Warn().Msgf("failed to add finalizer to NetworkAttachmentDefinition %s/%s: %v",
							networkNamespace, networkName, err)
						return false, nil
					}
					return true, nil
				}); err != nil {
					log.Error().Msgf("failed to add finalizer to NetworkAttachmentDefinition %s/%s",
						networkNamespace, networkName)
				} else {
					log.Info().Msgf("added finalizer %s to NetworkAttachmentDefinition %s/%s",
						GUIDInUFMFinalizer, networkNamespace, networkName)
				}
			}
		}

		// Update annotations for PODs that finished the previous steps successfully
		var removedGUIDList []net.HardwareAddr
		for _, pi := range passedPods {
			log.Info().Msgf("Updating annotations for the pod %s, network %s", pi.pod.Name, pi.ibNetwork.Name)
			err = d.updatePodNetworkAnnotation(pi, &removedGUIDList)
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
			} else {
				// RemoveGuidsFromPKey successful, now remove GUIDs from limited partition if configured
				if d.config.DefaultLimitedPartition != "" {
					limitedPKey, err := utils.ParsePKey(d.config.DefaultLimitedPartition)
					if err != nil {
						log.Error().Msgf("failed to parse DEFAULT_LIMITED_PARTITION %s: %v", d.config.DefaultLimitedPartition, err)
					} else {
						// Try to remove GUIDs from limited partition in backoff loop
						if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
							if err = d.smClient.RemoveGuidsFromPKey(limitedPKey, removedGUIDList); err != nil {
								log.Warn().Msgf("failed to remove GUIDs from limited partition 0x%04X with subnet manager %s with error: %v",
									limitedPKey, d.smClient.Name(), err)
								return false, nil
							}
							return true, nil
						}); err != nil {
							log.Error().Msgf("failed to remove GUIDs from limited partition 0x%04X with subnet manager %s", limitedPKey, d.smClient.Name())
						} else {
							log.Info().Msgf("successfully removed GUIDs %v from limited partition 0x%04X", removedGUIDList, limitedPKey)
						}
					}
				}

				// Remove finalizer from NetworkAttachmentDefinition
				networkNamespace, networkName, _ := utils.ParseNetworkID(networkID)
				if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
					if err := d.kubeClient.RemoveFinalizerFromNetworkAttachmentDefinition(
						networkNamespace, networkName, GUIDInUFMFinalizer); err != nil {
						log.Warn().Msgf("failed to remove finalizer from NetworkAttachmentDefinition %s/%s: %v",
							networkNamespace, networkName, err)
						return false, nil
					}
					return true, nil
				}); err != nil {
					log.Error().Msgf("failed to remove finalizer from NetworkAttachmentDefinition %s/%s",
						networkNamespace, networkName)
				} else {
					log.Info().Msgf("removed finalizer %s from NetworkAttachmentDefinition %s/%s",
						GUIDInUFMFinalizer, networkNamespace, networkName)
				}
			}
		}

		addMap.UnSafeRemove(networkID)
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
		return nil, fmt.Errorf("failed to parse allocated Pod GUID, error: %v", guidErr)
	}

	return guidAddr, nil
}

//nolint:nilerr
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
			deleteMap.UnSafeRemove(networkID)
			log.Warn().Msgf("droping network: %v", err)
			continue
		}

		var guidList []net.HardwareAddr
		var guidAddr net.HardwareAddr
		for _, pod := range pods {
			log.Debug().Msgf("pod namespace %s name %s", pod.Namespace, pod.Name)
			guidAddr, err = getPodGUIDForNetwork(pod, networkName)
			if err != nil {
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
			} else {
				// RemoveGuidsFromPKey successful, now remove GUIDs from limited partition if configured
				if d.config.DefaultLimitedPartition != "" {
					limitedPKey, err := utils.ParsePKey(d.config.DefaultLimitedPartition)
					if err != nil {
						log.Error().Msgf("failed to parse DEFAULT_LIMITED_PARTITION %s: %v", d.config.DefaultLimitedPartition, err)
					} else {
						// Try to remove GUIDs from limited partition in backoff loop
						if err = wait.ExponentialBackoff(backoffValues, func() (bool, error) {
							if err = d.smClient.RemoveGuidsFromPKey(limitedPKey, guidList); err != nil {
								log.Warn().Msgf("failed to remove GUIDs from limited partition 0x%04X with subnet manager %s with error: %v",
									limitedPKey, d.smClient.Name(), err)
								return false, nil
							}
							return true, nil
						}); err != nil {
							log.Error().Msgf("failed to remove GUIDs from limited partition 0x%04X with subnet manager %s", limitedPKey, d.smClient.Name())
						} else {
							log.Info().Msgf("successfully removed GUIDs %v from limited partition 0x%04X", guidList, limitedPKey)
						}
					}
				}

				// Remove finalizer from NetworkAttachmentDefinition
				networkNamespace, networkName, _ := utils.ParseNetworkID(networkID)
				if err := wait.ExponentialBackoff(backoffValues, func() (bool, error) {
					if err := d.kubeClient.RemoveFinalizerFromNetworkAttachmentDefinition(
						networkNamespace, networkName, GUIDInUFMFinalizer); err != nil {
						log.Warn().Msgf("failed to remove finalizer from NetworkAttachmentDefinition %s/%s: %v",
							networkNamespace, networkName, err)
						return false, nil
					}
					return true, nil
				}); err != nil {
					log.Error().Msgf("failed to remove finalizer from NetworkAttachmentDefinition %s/%s",
						networkNamespace, networkName)
				} else {
					log.Info().Msgf("removed finalizer %s from NetworkAttachmentDefinition %s/%s",
						GUIDInUFMFinalizer, networkNamespace, networkName)
				}
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

// initPool check the guids that are already allocated by the running pods
func (d *daemon) initPool() error {
	log.Info().Msg("Initializing GUID pool.")

	// Try to get pod list from k8s client in backoff loop
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
				log.Error().Msgf("%v", err)
				continue
			}

			d.guidPodNetworkMap[podGUID] = podNetworkID
		}
	}

	return nil
}
