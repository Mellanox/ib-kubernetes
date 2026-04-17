// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	env "github.com/caarlos0/env/v11"
	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/Mellanox/ib-kubernetes/pkg/sm/plugins"

	"github.com/NVIDIA/ncx-infra-controller-rest/sdk/simple"
	"github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

const (
	pluginName           = "nico"
	specVersion          = "1.0"
	sdkCallTimeout       = 30 * time.Second
	nodeIBPollTimeout    = 600 * time.Second
	nodeIBPollInterval   = 10 * time.Second
	partitionStatusReady = "Ready"
)

// NicoConfig holds environment-based configuration for the NICO plugin.
type NicoConfig struct {
	BaseURL              string `env:"NICO_BASE_URL"`
	Org                  string `env:"NICO_ORG"`
	Token                string `env:"NICO_TOKEN"`
	SiteID               string `env:"NICO_SITE_ID"`
	VpcID                string `env:"NICO_VPC_ID"`
	DefaultPartitionName string `env:"NICO_DEFAULT_PARTITION_NAME" envDefault:"default"`
}

// pendingOp tracks a non-blocking AddGuidsToPKey operation for a single node.
// The daemon retries on ErrPending; when the background goroutine finishes,
// ready or err is set so the next retry either succeeds or returns the error.
type pendingOp struct {
	mu          sync.Mutex
	partitionID string
	started     bool
	ready       bool
	err         error
}

// nicoClientAPI abstracts the NCX SDK calls used by the plugin, enabling unit testing.
type nicoClientAPI interface {
	GetInfinibandPartitions(
		ctx context.Context, filter *simple.PaginationFilter,
	) ([]simple.InfinibandPartition, *standard.PaginationResponse, *simple.ApiError)
	GetInfinibandPartition(
		ctx context.Context, id string,
	) (*simple.InfinibandPartition, *simple.ApiError)
	CreateInfinibandPartition(
		ctx context.Context, request simple.InfinibandPartitionCreateRequest,
	) (*simple.InfinibandPartition, *simple.ApiError)
	DeleteInfinibandPartition(ctx context.Context, id string) *simple.ApiError
	GetInstances(
		ctx context.Context,
		instanceFilter *simple.InstanceFilter,
		paginationFilter *simple.PaginationFilter,
	) ([]standard.Instance, *standard.PaginationResponse, *simple.ApiError)
	GetInstance(
		ctx context.Context, id string,
	) (*standard.Instance, *simple.ApiError)
	UpdateInstance(
		ctx context.Context, id string, request simple.InstanceUpdateRequest,
	) (*standard.Instance, *simple.ApiError)
}

// nicoPlugin implements both SubnetManagerClient and FabricClient.
type nicoPlugin struct {
	PluginName         string
	SpecVersion        string
	conf               NicoConfig
	client             nicoClientAPI
	defaultPartitionID string

	// Caches (all sync.Map, write-once per key, populated on demand)
	nodeInstanceIDCache  sync.Map // nodeName -> instanceID (string)
	nodeGUIDCache        sync.Map // nodeName -> []plugins.IBDeviceGUID
	guidToNodeCache      sync.Map // GUID string (lowercase) -> nodeName
	pkeyToPartitionCache sync.Map // pkey hex string "0xNNNN" -> partitionID
	nameToPartitionCache sync.Map // partitionName -> partitionID

	// Pending operations for non-blocking AddGuidsToPKey
	pendingByNode sync.Map // nodeName -> *pendingOp

	// Per-node locks for serializing background operations
	nodeLocks      map[string]*sync.Mutex
	nodeLocksMutex sync.Mutex
}

// apiCtx returns a context with the standard SDK call timeout.
func apiCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), sdkCallTimeout)
}

// ========================================
// Constructor
// ========================================

func newNicoPlugin() (*nicoPlugin, error) {
	conf := NicoConfig{}
	if err := env.Parse(&conf); err != nil {
		return nil, err
	}

	// Validate required configuration
	if conf.BaseURL == "" || conf.Org == "" || conf.Token == "" {
		log.Error().Msg("NICO service configuration is incomplete")
		if conf.BaseURL == "" {
			log.Error().Msg("Missing NICO_BASE_URL")
		}
		if conf.Org == "" {
			log.Error().Msg("Missing NICO_ORG")
		}
		if conf.Token == "" {
			log.Error().Msg("Missing NICO_TOKEN")
		}
		return nil, fmt.Errorf("nico plugin configuration is incomplete - missing required environment variables")
	}

	// Create SDK client configuration
	config := simple.ClientConfig{
		BaseURL: conf.BaseURL,
		Org:     conf.Org,
		Token:   conf.Token,
	}

	log.Info().Msgf("Initializing NICO client for org: %s", conf.Org)

	client, err := simple.NewClient(config)
	if err != nil {
		log.Error().Msgf("Error creating NICO client: %v", err)
		return nil, fmt.Errorf("failed to create nico client: %v", err)
	}

	// Override Site/VPC IDs when explicitly configured (for multi-site deployments).
	// The SDK auto-detects defaults via Authenticate() if these are not set.
	if conf.SiteID != "" {
		client.SetSiteID(conf.SiteID)
		log.Info().Msgf("Using configured Site ID: %s", conf.SiteID)
	}
	if conf.VpcID != "" {
		client.SetVpcID(conf.VpcID)
		log.Info().Msgf("Using configured VPC ID: %s", conf.VpcID)
	}

	// Wrap init sequence in a deadline context (auth + partition lookup).
	initCtx, initCancel := apiCtx()
	defer initCancel()

	// Authenticate with NICO platform
	if authErr := client.Authenticate(initCtx); authErr != nil {
		log.Error().Msgf("Error authenticating with NICO: %v", authErr)
		return nil, fmt.Errorf("failed to authenticate with nico: %v", authErr)
	}

	log.Info().Msgf("Successfully authenticated with NICO server (siteID=%s)", client.GetSiteID())

	// Ensure default partition exists
	log.Info().Msgf("Checking for '%s' InfiniBand partition...", conf.DefaultPartitionName)
	defaultPartitionID, err := getOrCreateDefaultPartition(initCtx, client, conf.DefaultPartitionName)
	if err != nil {
		log.Error().Msgf("Failed to setup default partition: %v", err)
		return nil, fmt.Errorf("failed to setup default partition: %v", err)
	}
	log.Info().Msgf("Default partition '%s' ID: %s", conf.DefaultPartitionName, defaultPartitionID)

	return &nicoPlugin{
		PluginName:         pluginName,
		SpecVersion:        specVersion,
		conf:               conf,
		client:             client,
		defaultPartitionID: defaultPartitionID,
		nodeLocks:          make(map[string]*sync.Mutex),
	}, nil
}

// getOrCreateDefaultPartition checks if the named partition exists and creates it if not.
func getOrCreateDefaultPartition(ctx context.Context, client nicoClientAPI, name string) (string, error) {
	if id, found := findPartitionByName(ctx, client, name); found {
		log.Info().Msgf("Found existing '%s' partition with ID: %s", name, id)
		return id, nil
	}

	log.Info().Msgf("'%s' partition not found, creating...", name)
	request := simple.InfinibandPartitionCreateRequest{
		Name: name,
	}

	partition, apiErr := client.CreateInfinibandPartition(ctx, request)
	if apiErr != nil {
		if apiErr.Code == 409 {
			log.Info().Msgf("'%s' partition already exists (created by another process), looking up...", name)
			if id, found := findPartitionByName(ctx, client, name); found {
				log.Info().Msgf("Found '%s' partition with ID: %s", name, id)
				return id, nil
			}
			return "", fmt.Errorf("'%s' partition reported as existing but not found", name)
		}
		return "", fmt.Errorf("failed to create '%s' partition: code %d, message %s",
			name, apiErr.Code, apiErr.Message)
	}

	log.Info().Msgf("Successfully created '%s' partition with ID: %s", name, partition.ID)
	return partition.ID, nil
}

// findPartitionByName paginates through all partitions to find one by name. Returns (id, found).
func findPartitionByName(ctx context.Context, client nicoClientAPI, name string) (string, bool) {
	pageSize := 50
	pageNumber := 1

	for {
		filter := &simple.PaginationFilter{
			PageNumber: &pageNumber,
			PageSize:   &pageSize,
		}
		partitions, paginationResp, apiErr := client.GetInfinibandPartitions(ctx, filter)
		if apiErr != nil {
			log.Warn().Int("code", apiErr.Code).Str("message", apiErr.Message).
				Msg("findPartitionByName: failed to list partitions")
			return "", false
		}

		for _, p := range partitions {
			if p.Name == name {
				return p.ID, true
			}
		}

		if paginationResp == nil || len(partitions) < pageSize {
			break
		}
		totalPages := (paginationResp.Total + pageSize - 1) / pageSize
		if pageNumber >= totalPages {
			break
		}
		pageNumber++
	}

	return "", false
}

// ========================================
// SubnetManagerClient interface
// ========================================

func (c *nicoPlugin) Name() string {
	return c.PluginName
}

func (c *nicoPlugin) Spec() string {
	return c.SpecVersion
}

func (c *nicoPlugin) Validate() error {
	if c.client == nil {
		return fmt.Errorf("nico client not initialized")
	}

	pageSize := 1
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, apiErr := c.client.GetInfinibandPartitions(ctx, &simple.PaginationFilter{PageSize: &pageSize})
	if apiErr != nil {
		return fmt.Errorf("nico connectivity check failed: code %d, message %s", apiErr.Code, apiErr.Message)
	}

	log.Debug().Msgf("NICO plugin validation successful (defaultPartitionID=%s)", c.defaultPartitionID)
	return nil
}

// ListGuidsInUse returns an empty map. PF GUIDs are hardware-assigned and not
// managed by ib-kubernetes's GUID pool.
func (c *nicoPlugin) ListGuidsInUse() (map[string]string, error) {
	return map[string]string{}, nil
}

// AddGuidsToPKey is NON-BLOCKING. On first call for a node, it spawns a
// background goroutine that runs ibAssociateOnNode + polls checkNodeIBStatus.
// Returns ErrPending while the goroutine is running. On subsequent calls:
//   - If ready: returns nil (daemon proceeds with pod annotation).
//   - If error: returns the error and clears state so next call retries.
//   - If still running: returns ErrPending.
func (c *nicoPlugin) AddGuidsToPKey(pkey int, guids []net.HardwareAddr) error {
	if len(guids) == 0 {
		return nil
	}

	pkeyStr := fmt.Sprintf("0x%x", pkey)

	// Look up partitionID from pkey cache, fall back to backend lookup on miss
	// (happens after daemon restart when NAD already has PKey but cache is cold).
	partitionID, err := c.resolvePartitionIDByPKey(pkeyStr)
	if err != nil {
		return err
	}

	// Resolve each GUID to its node and group by node
	guidsByNode := make(map[string][]net.HardwareAddr)
	for _, guid := range guids {
		guidStr := strings.ToLower(guid.String())
		nodeVal, found := c.guidToNodeCache.Load(guidStr)
		if !found {
			log.Warn().Str("guid", guidStr).Str("pkey", pkeyStr).
				Msg("nico plugin: GUID not in guidToNodeCache, skipping")
			continue
		}
		nodeName := nodeVal.(string)
		guidsByNode[nodeName] = append(guidsByNode[nodeName], guid)
	}

	if len(guidsByNode) == 0 {
		return fmt.Errorf("none of the %d GUIDs could be resolved to a node", len(guids))
	}

	// Process each node
	var pendingCount int
	var firstErr error
	for nodeName, nodeGuids := range guidsByNode {
		err := c.addGuidsForNode(nodeName, partitionID, nodeGuids)
		if err == plugins.ErrPending {
			pendingCount++
		} else if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if firstErr != nil {
		return firstErr
	}

	if pendingCount > 0 {
		return plugins.ErrPending
	}

	return nil
}

// addGuidsForNode handles the pending-op state machine for a single node.
func (c *nicoPlugin) addGuidsForNode(nodeName, partitionID string, _ []net.HardwareAddr) error {
	for range 2 {
		val, _ := c.pendingByNode.LoadOrStore(nodeName, &pendingOp{
			partitionID: partitionID,
		})
		op := val.(*pendingOp)

		op.mu.Lock()

		if op.ready {
			if op.partitionID == partitionID {
				op.mu.Unlock()
				return nil
			}
			// Different partition (node re-assigned after teardown + re-onboard)
			op.mu.Unlock()
			c.pendingByNode.Delete(nodeName)
			log.Info().Str("node", nodeName).
				Str("old_partition", op.partitionID).Str("new_partition", partitionID).
				Msg("nico plugin: partition changed, clearing stale pendingOp")
			continue
		}

		if op.err != nil {
			savedErr := op.err
			op.mu.Unlock()
			c.pendingByNode.Delete(nodeName)
			log.Warn().Str("node", nodeName).Str("partition_id", partitionID).Err(savedErr).
				Msg("nico plugin: pending operation failed, clearing for retry")
			return savedErr
		}

		if op.started {
			op.mu.Unlock()
			log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
				Msg("nico plugin: operation in progress")
			return plugins.ErrPending
		}

		// Not yet started — kick off the background goroutine
		op.partitionID = partitionID
		op.started = true
		op.mu.Unlock()
		go c.processNodeAsync(nodeName, op)

		log.Info().Str("node", nodeName).Str("partition_id", partitionID).
			Msg("nico plugin: started background IB association")
		return plugins.ErrPending
	}

	return fmt.Errorf("nico plugin: failed to create pendingOp for node %s after retries", nodeName)
}

// processNodeAsync runs ibAssociateOnNode + polls checkNodeIBStatus in the background.
func (c *nicoPlugin) processNodeAsync(nodeName string, op *pendingOp) {
	partitionID := op.partitionID
	startTime := time.Now()

	log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
		Msg("nico plugin: processNodeAsync started")

	// Step 1: Associate IB interfaces with the partition.
	// Hold the per-node lock only during the mutating backend call so we
	// serialize concurrent add/reset attempts on the same node. Releasing
	// before the read-only poll lets same-node pod-delete events proceed
	// without waiting up to nodeIBPollTimeout for readiness.
	nodeLock := c.getNodeLock(nodeName)
	nodeLock.Lock()
	assocErr := c.ibAssociateOnNode(nodeName, partitionID)
	nodeLock.Unlock()

	if assocErr != nil {
		op.mu.Lock()
		op.err = fmt.Errorf("ibAssociateOnNode failed for node '%s': %w", nodeName, assocErr)
		op.mu.Unlock()
		log.Warn().Str("node", nodeName).Str("partition_id", partitionID).Err(assocErr).
			Msg("nico plugin: ibAssociateOnNode failed")
		return
	}

	log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
		Msg("nico plugin: ibAssociateOnNode succeeded, polling IB status")

	// Step 2: Poll until IB interfaces are Ready or timeout (read-only; safe
	// to run without the per-node lock since only op.mu guards op state).
	pollCtx, cancelPoll := context.WithTimeout(context.Background(), nodeIBPollTimeout)
	defer cancelPoll()

	pollErr := wait.PollUntilContextTimeout(pollCtx, nodeIBPollInterval, nodeIBPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			ready, checkErr := c.checkNodeIBStatus(ctx, nodeName, partitionID)
			if checkErr != nil {
				log.Warn().Str("node", nodeName).Str("partition_id", partitionID).Err(checkErr).
					Msg("nico plugin: checkNodeIBStatus error, will retry")
				return false, nil
			}
			return ready, nil
		})

	duration := time.Since(startTime)

	if pollErr != nil {
		op.mu.Lock()
		op.err = fmt.Errorf("IB status poll timed out for node '%s' after %.0fs: %w",
			nodeName, duration.Seconds(), pollErr)
		op.mu.Unlock()
		log.Error().Str("node", nodeName).Str("partition_id", partitionID).
			Float64("elapsed_sec", duration.Seconds()).
			Msg("nico plugin: checkNodeIBStatus timeout")
		return
	}

	op.mu.Lock()
	op.ready = true
	op.mu.Unlock()

	log.Info().Str("node", nodeName).Str("partition_id", partitionID).
		Float64("elapsed_sec", duration.Seconds()).
		Msg("nico plugin: IB configuration ready")
}

// RemoveGuidsFromPKey resets IB interfaces on affected nodes back to the default partition.
// Best-effort: errors are logged but do not fail the call.
func (c *nicoPlugin) RemoveGuidsFromPKey(_ int, guids []net.HardwareAddr) error {
	if len(guids) == 0 {
		return nil
	}

	nodesSet := make(map[string]struct{})
	for _, guid := range guids {
		guidStr := strings.ToLower(guid.String())
		if nodeVal, found := c.guidToNodeCache.Load(guidStr); found {
			nodesSet[nodeVal.(string)] = struct{}{}
		}
	}

	for nodeName := range nodesSet {
		c.pendingByNode.Delete(nodeName)

		if err := c.resetNodeIBPartitions(nodeName); err != nil {
			log.Warn().Str("node", nodeName).Err(err).
				Msg("nico plugin: ResetNodeIBPartitions failed (best-effort)")
		}
	}

	return nil
}

// ========================================
// FabricClient interface
// ========================================

// CreateIBPartition creates an IB partition with the given name.
// Returns the partition key (PKey) assigned by the backend.
// Idempotent: 409 conflict is handled by looking up the existing partition.
func (c *nicoPlugin) CreateIBPartition(name string) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("nico client not initialized")
	}
	if name == "" {
		return "", fmt.Errorf("partition name cannot be empty")
	}

	ctx, cancel := apiCtx()
	defer cancel()

	request := simple.InfinibandPartitionCreateRequest{
		Name: name,
	}

	log.Info().Msgf("CreateIBPartition: Creating partition with name='%s'", name)

	partition, apiErr := c.client.CreateInfinibandPartition(ctx, request)
	if apiErr != nil {
		if apiErr.Code == 409 {
			log.Info().Msgf("CreateIBPartition: Partition '%s' already exists (409), looking up existing partition", name)

			existingID, existingPKey, status, lookupErr := c.getPartitionByName(name)
			if lookupErr != nil {
				log.Error().Msgf("CreateIBPartition: Failed to lookup existing partition '%s': %v", name, lookupErr)
				return "", fmt.Errorf("partition exists but lookup failed: %v", lookupErr)
			}
			if existingID == "" {
				log.Info().Msgf(
					"CreateIBPartition: Partition '%s' not found in list after 409 (deletion in progress), will retry", name)
				return "", fmt.Errorf("partition '%s' conflict (409) but not found in list, will retry", name)
			}

			if status == "Deleting" || status == "PendingDelete" || status == "Terminating" {
				log.Info().Str("partition", name).Str("status", status).Str("id", existingID).
					Msg("CreateIBPartition: existing partition is being deleted, will retry on next cycle")
				return "", fmt.Errorf("partition '%s' is in %s state, waiting for deletion to complete", name, status)
			}

			c.nameToPartitionCache.Store(name, existingID)
			if existingPKey != "" {
				c.pkeyToPartitionCache.Store(existingPKey, existingID)
			}

			log.Info().Msgf("CreateIBPartition: Using existing partition '%s' ID=%s PKey=%s Status=%s",
				name, existingID, existingPKey, status)
			return existingPKey, nil
		}

		log.Error().Msgf("CreateIBPartition: Failed to create partition '%s': code=%d, message=%s",
			name, apiErr.Code, apiErr.Message)
		return "", fmt.Errorf("create InfiniBand partition failed: code %d message %s",
			apiErr.Code, apiErr.Message)
	}

	c.nameToPartitionCache.Store(name, partition.ID)

	partitionKey := ""
	if partition.PartitionKey != nil {
		partitionKey = *partition.PartitionKey
		c.pkeyToPartitionCache.Store(partitionKey, partition.ID)
	}

	log.Info().Msgf("CreateIBPartition: Successfully created partition '%s' ID=%s PKey=%s",
		partition.Name, partition.ID, partitionKey)
	return partitionKey, nil
}

// DeleteIBPartition deletes an IB partition by name.
// Idempotent: deleting a non-existent partition is not an error.
func (c *nicoPlugin) DeleteIBPartition(name string) error {
	if c.client == nil {
		return fmt.Errorf("nico client not initialized")
	}
	if name == "" {
		return fmt.Errorf("partition name cannot be empty")
	}

	partitionID := ""
	if val, ok := c.nameToPartitionCache.Load(name); ok {
		partitionID = val.(string)
	} else {
		id, _, _, lookupErr := c.getPartitionByName(name)
		if lookupErr != nil {
			return fmt.Errorf("failed to look up partition '%s': %v", name, lookupErr)
		}
		if id == "" {
			log.Info().Msgf("DeleteIBPartition: Partition '%s' not found, nothing to delete", name)
			return nil
		}
		partitionID = id
	}

	ctx, cancel := apiCtx()
	defer cancel()

	log.Info().Msgf("DeleteIBPartition: Deleting partition '%s' (ID=%s)", name, partitionID)

	apiErr := c.client.DeleteInfinibandPartition(ctx, partitionID)
	if apiErr != nil {
		if apiErr.Code == 404 {
			log.Info().Msgf("DeleteIBPartition: Partition '%s' not found (already deleted)", name)
		} else {
			log.Error().Msgf("DeleteIBPartition: Failed to delete partition '%s': code=%d, message=%s",
				name, apiErr.Code, apiErr.Message)
			return fmt.Errorf("delete InfiniBand partition failed: code %d message %s",
				apiErr.Code, apiErr.Message)
		}
	} else {
		log.Info().Msgf("DeleteIBPartition: Successfully deleted partition '%s' (ID=%s)", name, partitionID)
	}

	c.nameToPartitionCache.Delete(name)
	c.pkeyToPartitionCache.Range(func(key, value interface{}) bool {
		if value.(string) == partitionID {
			c.pkeyToPartitionCache.Delete(key)
			return false
		}
		return true
	})

	return nil
}

// IsPartitionReady checks whether a partition is ready for pod binding.
func (c *nicoPlugin) IsPartitionReady(name string) (bool, string, error) {
	if c.client == nil {
		return false, "", fmt.Errorf("nico client not initialized")
	}

	partitionIDVal, ok := c.nameToPartitionCache.Load(name)
	if !ok {
		id, _, _, lookupErr := c.getPartitionByName(name)
		if lookupErr != nil {
			return false, "", fmt.Errorf("failed to look up partition '%s': %v", name, lookupErr)
		}
		if id == "" {
			return false, "", fmt.Errorf("partition '%s' not found", name)
		}
		c.nameToPartitionCache.Store(name, id)
		partitionIDVal = id
	}
	partitionID := partitionIDVal.(string)

	ctx, cancel := apiCtx()
	defer cancel()

	partition, apiErr := c.client.GetInfinibandPartition(ctx, partitionID)
	if apiErr != nil {
		return false, "", fmt.Errorf("get InfiniBand partition failed: code %d message %s",
			apiErr.Code, apiErr.Message)
	}

	ready := partition.Status == partitionStatusReady
	partitionKey := ""
	if partition.PartitionKey != nil {
		partitionKey = *partition.PartitionKey
	}

	if ready && partitionKey != "" {
		c.pkeyToPartitionCache.Store(partitionKey, partitionID)
	}

	log.Debug().Str("name", name).Str("partition_id", partitionID).
		Str("status", partition.Status).Str("pkey", partitionKey).Bool("ready", ready).
		Msg("IsPartitionReady")

	return ready, partitionKey, nil
}

// GetNodeIBDevices returns the IB PF devices on a node with their hardware GUIDs.
// Results are cached; subsequent calls return the cached value.
func (c *nicoPlugin) GetNodeIBDevices(nodeName string) ([]plugins.IBDeviceGUID, error) {
	if val, ok := c.nodeGUIDCache.Load(nodeName); ok {
		return val.([]plugins.IBDeviceGUID), nil
	}

	ctx, cancel := apiCtx()
	defer cancel()

	instance, err := c.getInstanceByName(ctx, nodeName)
	if err != nil {
		return nil, err
	}

	c.nodeInstanceIDCache.Store(nodeName, instanceGetID(instance))

	ibInterfaces := instanceGetIBInterfaces(instance)
	devices := make([]plugins.IBDeviceGUID, 0, len(ibInterfaces))
	for i := range ibInterfaces {
		iface := &ibInterfaces[i]
		if !ibIfaceGetIsPhysical(iface) {
			continue
		}
		guidStr := ibIfaceGetGUID(iface)
		if guidStr == "" {
			log.Debug().Str("node", nodeName).Str("device", ibIfaceGetDevice(iface)).
				Int("device_instance", ibIfaceGetDeviceInstance(iface)).
				Msg("GetNodeIBDevices: skipping interface with empty GUID")
			continue
		}

		hwAddr, parseErr := net.ParseMAC(guidStr)
		if parseErr != nil {
			log.Warn().Str("node", nodeName).Str("guid", guidStr).Err(parseErr).
				Msg("GetNodeIBDevices: failed to parse GUID as MAC address, skipping")
			continue
		}

		devices = append(devices, plugins.IBDeviceGUID{
			Device:         ibIfaceGetDevice(iface),
			DeviceInstance: ibIfaceGetDeviceInstance(iface),
			GUID:           hwAddr,
		})

		c.guidToNodeCache.Store(strings.ToLower(hwAddr.String()), nodeName)
	}

	if len(devices) == 0 {
		return nil, fmt.Errorf("node '%s' has no parseable IB device GUIDs", nodeName)
	}

	sort.Slice(devices, func(i, j int) bool {
		return devices[i].DeviceInstance < devices[j].DeviceInstance
	})

	c.nodeGUIDCache.Store(nodeName, devices)

	log.Info().Str("node", nodeName).Int("devices", len(devices)).
		Msg("GetNodeIBDevices: cached IB devices")

	return devices, nil
}

// ========================================
// Internal helpers
// ========================================

// getInstanceByName fetches the NICO instance record for a Kubernetes node by name.
func (c *nicoPlugin) getInstanceByName(ctx context.Context, nodeName string) (*standard.Instance, error) {
	instances, _, apiErr := c.client.GetInstances(ctx, &simple.InstanceFilter{Name: &nodeName}, nil)
	if apiErr != nil {
		return nil, fmt.Errorf("failed to get instance for node '%s': code %d, message %s",
			nodeName, apiErr.Code, apiErr.Message)
	}
	for i := range instances {
		if instanceGetName(&instances[i]) == nodeName {
			if len(instanceGetIBInterfaces(&instances[i])) == 0 {
				return nil, fmt.Errorf("node '%s' has no InfiniBand interfaces", nodeName)
			}
			return &instances[i], nil
		}
	}
	return nil, fmt.Errorf("no instance found for node '%s'", nodeName)
}

// getInstanceIDForNode returns the NICO instanceID for a Kubernetes node name.
func (c *nicoPlugin) getInstanceIDForNode(ctx context.Context, nodeName string) (string, error) {
	if v, ok := c.nodeInstanceIDCache.Load(nodeName); ok {
		return v.(string), nil
	}

	log.Debug().Msgf("InstanceID not cached for node '%s', querying GetInstances API", nodeName)
	instance, err := c.getInstanceByName(ctx, nodeName)
	if err != nil {
		return "", err
	}

	instanceID := instanceGetID(instance)
	c.nodeInstanceIDCache.Store(nodeName, instanceID)
	log.Info().Msgf("Cached instanceID '%s' for node '%s'", instanceID, nodeName)
	return instanceID, nil
}

// getPartitionByName looks up a partition by name. Returns (id, pkey, status, error).
func (c *nicoPlugin) getPartitionByName(name string) (string, string, string, error) {
	ctx, cancel := apiCtx()
	defer cancel()

	pageSize := 50
	pageNumber := 1

	for {
		filter := &simple.PaginationFilter{
			PageNumber: &pageNumber,
			PageSize:   &pageSize,
		}

		partitions, paginationResp, apiErr := c.client.GetInfinibandPartitions(ctx, filter)
		if apiErr != nil {
			return "", "", "", fmt.Errorf("list InfiniBand partitions failed: code %d message %s",
				apiErr.Code, apiErr.Message)
		}

		for _, partition := range partitions {
			if partition.Name == name {
				partitionKey := ""
				if partition.PartitionKey != nil {
					partitionKey = *partition.PartitionKey
				}
				return partition.ID, partitionKey, partition.Status, nil
			}
		}

		if paginationResp == nil || len(partitions) < pageSize {
			break
		}
		totalPages := (paginationResp.Total + pageSize - 1) / pageSize
		if pageNumber >= totalPages {
			break
		}
		pageNumber++
	}

	return "", "", "", nil
}

// resolvePartitionIDByPKey returns the partitionID for a given pkey string.
// Checks the cache first; on miss, queries the backend and populates the cache.
// This covers daemon-restart cases where NADs already have PKey persisted but
// the in-memory pkey cache is cold.
func (c *nicoPlugin) resolvePartitionIDByPKey(pkeyStr string) (string, error) {
	if val, ok := c.pkeyToPartitionCache.Load(pkeyStr); ok {
		return val.(string), nil
	}

	ctx, cancel := apiCtx()
	defer cancel()

	pageSize := 50
	pageNumber := 1

	for {
		filter := &simple.PaginationFilter{
			PageNumber: &pageNumber,
			PageSize:   &pageSize,
		}

		partitions, paginationResp, apiErr := c.client.GetInfinibandPartitions(ctx, filter)
		if apiErr != nil {
			return "", fmt.Errorf("list InfiniBand partitions failed: code %d message %s",
				apiErr.Code, apiErr.Message)
		}

		for _, partition := range partitions {
			if partition.PartitionKey == nil || *partition.PartitionKey != pkeyStr {
				continue
			}
			c.pkeyToPartitionCache.Store(pkeyStr, partition.ID)
			c.nameToPartitionCache.Store(partition.Name, partition.ID)
			log.Info().Msgf("Resolved partition %s (id=%s) for pkey %s via backend lookup",
				partition.Name, partition.ID, pkeyStr)
			return partition.ID, nil
		}

		if paginationResp == nil || len(partitions) < pageSize {
			break
		}
		totalPages := (paginationResp.Total + pageSize - 1) / pageSize
		if pageNumber >= totalPages {
			break
		}
		pageNumber++
	}

	return "", fmt.Errorf("no partition found for pkey %s; partition may not be ready yet", pkeyStr)
}

// deduplicateIBInterfaces removes duplicate DeviceInstance entries,
// keeping only the one with the most recent Updated time.
func deduplicateIBInterfaces(interfaces []standard.InfiniBandInterface) []standard.InfiniBandInterface {
	if len(interfaces) == 0 {
		return interfaces
	}

	bestByDeviceInstance := make(map[int]standard.InfiniBandInterface)

	for _, ibIface := range interfaces {
		di := ibIfaceGetDeviceInstance(&ibIface)
		existing, found := bestByDeviceInstance[di]
		switch {
		case !found:
			bestByDeviceInstance[di] = ibIface
		case ibIfaceGetUpdated(&ibIface).After(ibIfaceGetUpdated(&existing)):
			log.Debug().Msgf("Dedup: DeviceInstance=%d - replacing older entry (Updated=%v) with newer (Updated=%v)",
				di, ibIfaceGetUpdated(&existing), ibIfaceGetUpdated(&ibIface))
			bestByDeviceInstance[di] = ibIface
		default:
			log.Debug().Msgf("Dedup: DeviceInstance=%d - keeping existing entry (Updated=%v), discarding older (Updated=%v)",
				di, ibIfaceGetUpdated(&existing), ibIfaceGetUpdated(&ibIface))
		}
	}

	deviceInstances := make([]int, 0, len(bestByDeviceInstance))
	for di := range bestByDeviceInstance {
		deviceInstances = append(deviceInstances, di)
	}
	sort.Ints(deviceInstances)

	result := make([]standard.InfiniBandInterface, 0, len(bestByDeviceInstance))
	for _, di := range deviceInstances {
		result = append(result, bestByDeviceInstance[di])
	}

	if len(result) < len(interfaces) {
		log.Debug().Msgf("Dedup: Reduced IB interfaces from %d to %d (removed %d duplicate DeviceInstance entries)",
			len(interfaces), len(result), len(interfaces)-len(result))
	}

	return result
}

// toUpdateRequest converts a standard.InfiniBandInterface to a simple SDK update request.
func toUpdateRequest(
	iface *standard.InfiniBandInterface, partitionID string,
) simple.InfiniBandInterfaceCreateOrUpdateRequest {
	return simple.InfiniBandInterfaceCreateOrUpdateRequest{
		PartitionID:       partitionID,
		Device:            ibIfaceGetDevice(iface),
		DeviceInstance:    ibIfaceGetDeviceInstance(iface),
		IsPhysical:        ibIfaceGetIsPhysical(iface),
		VirtualFunctionID: ibIfaceGetVirtualFunctionID(iface),
	}
}

// ibAssociateOnNode associates all IB interfaces on a node with the given partition.
func (c *nicoPlugin) ibAssociateOnNode(nodeName string, partitionID string) error {
	log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
		Msg("ibAssociateOnNode: associating node with partition")

	if c.client == nil {
		return fmt.Errorf("nico client not initialized")
	}

	ctx, cancel := apiCtx()
	defer cancel()

	instance, err := c.getInstanceByName(ctx, nodeName)
	if err != nil {
		return err
	}
	instanceID := instanceGetID(instance)

	ibInterfaces := instanceGetIBInterfaces(instance)
	log.Debug().Str("node", nodeName).Int("ib_interfaces", len(ibInterfaces)).
		Msg("ibAssociateOnNode: fetched instance")

	dedupedInterfaces := deduplicateIBInterfaces(ibInterfaces)

	updatedIBInterfaces := make([]simple.InfiniBandInterfaceCreateOrUpdateRequest, 0, len(dedupedInterfaces))
	for idx := range dedupedInterfaces {
		iface := &dedupedInterfaces[idx]
		log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
			Int("idx", idx).Str("device", ibIfaceGetDevice(iface)).
			Int("device_instance", ibIfaceGetDeviceInstance(iface)).
			Msg("ibAssociateOnNode: updating IB interface partition")

		updatedIBInterfaces = append(updatedIBInterfaces, toUpdateRequest(iface, partitionID))
	}

	updateRequest := simple.InstanceUpdateRequest{
		InfinibandInterfaces: updatedIBInterfaces,
	}

	log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
		Str("instance_id", instanceID).Int("ib_interfaces", len(updatedIBInterfaces)).
		Msg("ibAssociateOnNode: calling UpdateInstance")

	updatedInstance, apiErr := c.client.UpdateInstance(ctx, instanceID, updateRequest)
	if apiErr != nil {
		log.Error().Str("node", nodeName).Str("partition_id", partitionID).
			Int("code", apiErr.Code).Str("message", apiErr.Message).
			Msg("ibAssociateOnNode: UpdateInstance failed")
		return fmt.Errorf("failed to update instance: code %d, message %s", apiErr.Code, apiErr.Message)
	}

	log.Info().Str("node", nodeName).Str("partition_id", partitionID).
		Str("instance_id", instanceGetID(updatedInstance)).Int("ib_interfaces", len(updatedIBInterfaces)).
		Msg("ibAssociateOnNode: UpdateInstance succeeded")

	c.nodeInstanceIDCache.Store(nodeName, instanceID)

	return nil
}

// checkNodeIBStatus checks if all IB interfaces on a node with the expected
// partitionID are in Ready status.
func (c *nicoPlugin) checkNodeIBStatus(ctx context.Context, nodeName string, partitionID string) (bool, error) {
	log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
		Msg("checkNodeIBStatus: polling IB status")

	if c.client == nil {
		return false, fmt.Errorf("nico client not initialized")
	}

	instanceID, err := c.getInstanceIDForNode(ctx, nodeName)
	if err != nil {
		return false, err
	}

	log.Debug().Str("node", nodeName).Str("instance_id", instanceID).
		Msg("checkNodeIBStatus: calling GetInstance")
	instance, apiErr := c.client.GetInstance(ctx, instanceID)
	if apiErr != nil {
		log.Error().Str("node", nodeName).Str("instance_id", instanceID).
			Int("code", apiErr.Code).Str("message", apiErr.Message).
			Msg("checkNodeIBStatus: GetInstance failed")
		return false, fmt.Errorf("failed to get instance: code %d, message %s", apiErr.Code, apiErr.Message)
	}

	ibInterfaces := instanceGetIBInterfaces(instance)
	if len(ibInterfaces) == 0 {
		log.Debug().Str("node", nodeName).Msg("checkNodeIBStatus: no IB interfaces found")
		return false, nil
	}

	// Filter to interfaces with the expected partitionID
	targetInterfaces := make(map[int]standard.InfiniBandInterface)
	for i := range ibInterfaces {
		iface := &ibInterfaces[i]
		if ibIfaceGetPartitionID(iface) == partitionID {
			targetInterfaces[ibIfaceGetDeviceInstance(iface)] = ibInterfaces[i]
		}
	}

	if len(targetInterfaces) == 0 {
		log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
			Msg("checkNodeIBStatus: no interfaces found with expected partition_id")
		return false, nil
	}

	log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
		Int("ib_devices", len(targetInterfaces)).
		Msg("checkNodeIBStatus: checking IB device readiness")

	allReady := true
	for deviceInstance, ibIface := range targetInterfaces {
		status := ibIfaceGetStatus(&ibIface)
		if status != partitionStatusReady {
			log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
				Int("device_instance", deviceInstance).Str("status", status).
				Msg("checkNodeIBStatus: waiting for Ready")
			allReady = false
		}
	}

	if allReady {
		log.Info().Str("node", nodeName).Str("partition_id", partitionID).
			Int("ib_devices", len(targetInterfaces)).
			Msg("checkNodeIBStatus: all IB devices are Ready")
		return true, nil
	}

	log.Debug().Str("node", nodeName).Str("partition_id", partitionID).
		Msg("checkNodeIBStatus: not ready yet")
	return false, nil
}

// resetNodeIBPartitions resets all IB interfaces on a node to the default partition.
// Idempotent: if every IB interface already references the default partition we skip
// UpdateInstance entirely, so repeated pod-delete events for the same node (from informer
// resync or OnUpdate → OnDelete forwarding) are harmless no-ops instead of back-to-back
// UpdateInstance calls. The per-node lock serializes concurrent invocations for the
// same node so two delete events can't race into a double-update.
func (c *nicoPlugin) resetNodeIBPartitions(nodeName string) error {
	log.Info().Msgf("resetNodeIBPartitions: resetting node '%s' to default partition", nodeName)

	if c.client == nil {
		return fmt.Errorf("nico client not initialized")
	}

	nodeLock := c.getNodeLock(nodeName)
	nodeLock.Lock()
	defer nodeLock.Unlock()

	ctx, cancel := apiCtx()
	defer cancel()

	instance, err := c.getInstanceByName(ctx, nodeName)
	if err != nil {
		return err
	}
	instanceID := instanceGetID(instance)

	ibInterfaces := instanceGetIBInterfaces(instance)
	if allInterfacesOnPartition(ibInterfaces, c.defaultPartitionID) {
		log.Info().Str("node", nodeName).
			Msg("resetNodeIBPartitions: already on default partition, skipping UpdateInstance")
		return nil
	}

	dedupedInterfaces := deduplicateIBInterfaces(ibInterfaces)

	restoredIBInterfaces := make([]simple.InfiniBandInterfaceCreateOrUpdateRequest, 0, len(dedupedInterfaces))
	for idx := range dedupedInterfaces {
		iface := &dedupedInterfaces[idx]
		log.Debug().Msgf(
			"  [%d] Restoring IB Interface: Device='%s', DeviceInstance=%d, IsPhysical=%t, "+
				"PartitionID='%s' -> '%s' (default)",
			idx, ibIfaceGetDevice(iface), ibIfaceGetDeviceInstance(iface), ibIfaceGetIsPhysical(iface),
			ibIfaceGetPartitionID(iface), c.defaultPartitionID,
		)

		restoredIBInterfaces = append(restoredIBInterfaces, toUpdateRequest(iface, c.defaultPartitionID))
	}

	updateRequest := simple.InstanceUpdateRequest{
		InfinibandInterfaces: restoredIBInterfaces,
	}

	log.Debug().Msgf(
		"resetNodeIBPartitions: Calling UpdateInstance to reset node '%s' (instanceID=%s) "+
			"to default partition (partitionID=%s) with %d IB interface(s)",
		nodeName, instanceID, c.defaultPartitionID, len(restoredIBInterfaces))

	updatedInstance, apiErr := c.client.UpdateInstance(ctx, instanceID, updateRequest)
	if apiErr != nil {
		log.Warn().Msgf(
			"resetNodeIBPartitions: UpdateInstance failed for node '%s': code=%d, message='%s' (best-effort)",
			nodeName, apiErr.Code, apiErr.Message)
		return fmt.Errorf("failed to update instance: code %d, message %s", apiErr.Code, apiErr.Message)
	}

	log.Debug().Msgf("resetNodeIBPartitions: UpdateInstance succeeded for node '%s': interfaces=%d",
		nodeName, len(instanceGetIBInterfaces(updatedInstance)))

	return nil
}

// allInterfacesOnPartition reports whether every IB interface in the slice
// references partitionID. Empty slice returns false so callers don't mistake
// "no IB interfaces" for "already on default partition".
func allInterfacesOnPartition(interfaces []standard.InfiniBandInterface, partitionID string) bool {
	if len(interfaces) == 0 {
		return false
	}
	for i := range interfaces {
		if ibIfaceGetPartitionID(&interfaces[i]) != partitionID {
			return false
		}
	}
	return true
}

// getNodeLock returns or creates a per-node mutex.
func (c *nicoPlugin) getNodeLock(nodeName string) *sync.Mutex {
	c.nodeLocksMutex.Lock()
	defer c.nodeLocksMutex.Unlock()

	if lock, exists := c.nodeLocks[nodeName]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	c.nodeLocks[nodeName] = lock
	return lock
}

// ========================================
// Plugin entry point
// ========================================

// Initialize is the Go plugin entry point. The daemon loads this .so file and
// calls Initialize() to get a SubnetManagerClient. The daemon then type-asserts
// to FabricClient if the plugin supports partition management.
func Initialize() (plugins.SubnetManagerClient, error) {
	log.Info().Msg("Initializing nico plugin")
	return newNicoPlugin()
}
