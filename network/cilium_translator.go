// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build cilium

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

const (
	// ciliumStatePrefix is the root prefix for all Cilium state in etcd.
	ciliumStatePrefix = "cilium/state/"
	// ciliumIdentityMasterPrefix stores identity ID → label key mappings.
	ciliumIdentityMasterPrefix = ciliumStatePrefix + "identities/v1/id/"
	// ciliumIdentitySlavePrefix stores per-node label → identity references.
	ciliumIdentitySlavePrefix = ciliumStatePrefix + "identities/v1/value/"
	// ciliumIPPrefix stores IP → identity mappings per cluster.
	ciliumIPPrefix = ciliumStatePrefix + "ip/v1/"
	// ciliumServicePrefix stores service VIP definitions per cluster.
	ciliumServicePrefix = ciliumStatePrefix + "services/v1/"
	// ciliumNodePrefix stores per-node registration entries.
	ciliumNodePrefix = ciliumStatePrefix + "nodes/v1/"
	// ciliumLabelSourceContainer is the label source for CCattler containers.
	ciliumLabelSourceContainer = "container"
	// ciliumLeaseTTLSeconds is the TTL for Cilium kvstore leases (15 minutes).
	ciliumLeaseTTLSeconds int64 = 900
	// ciliumLeaseRenewalInterval is how often leases are renewed (5 minutes).
	ciliumLeaseRenewalInterval = 5 * time.Minute
	// ciliumMinUserIdentity is the first identity ID available for user workloads.
	// Cilium reserves 0-255 for well-known identities (host, world, etc.).
	ciliumMinUserIdentity = 256
)

// CiliumFactTranslator watches CCattler service/endpoint facts and writes
// Cilium-compatible kvstore entries to etcd. Cilium agents watch these keys
// and program eBPF maps for VIP load balancing, tunneling, and policy.
type CiliumFactTranslator struct {
	// factStore is the CCattler fact store (shared etcd instance).
	factStore store.StateStore
	// clusterName is the Cilium cluster name used in kvstore key paths.
	clusterName string
	// nodeID identifies this node for slave key writes and node registration.
	nodeID string
	// mutex serializes identity allocation and key writes.
	mutex sync.Mutex
	// nextIdentityID is the next numeric identity to allocate.
	nextIdentityID int
	// identityCache maps sorted label keys to allocated identity IDs.
	identityCache map[string]int
	// activeLease is the etcd lease used for all TTL-bound keys (slave, IP).
	activeLease int64
	// writtenServiceKeys tracks Cilium service keys for cleanup.
	writtenServiceKeys map[string]bool
	// writtenIPKeys tracks Cilium IP-to-identity keys for cleanup.
	writtenIPKeys map[string]bool
	// writtenIdentityMasterKeys tracks identity master keys for cleanup.
	writtenIdentityMasterKeys map[string]bool
	// writtenIdentitySlaveKeys tracks identity slave keys for cleanup.
	writtenIdentitySlaveKeys map[string]bool
	// stopLeaseRenewal signals the lease renewal goroutine to exit.
	stopLeaseRenewal chan struct{}
}

// NewCiliumFactTranslator creates a translator that writes Cilium kvstore
// entries to the given fact store under the specified cluster and node name.
func NewCiliumFactTranslator(factStore store.StateStore, clusterName string, nodeID string) *CiliumFactTranslator {
	return &CiliumFactTranslator{
		factStore:                 factStore,
		clusterName:               clusterName,
		nodeID:                    nodeID,
		nextIdentityID:            ciliumMinUserIdentity,
		identityCache:             make(map[string]int),
		writtenServiceKeys:        make(map[string]bool),
		writtenIPKeys:             make(map[string]bool),
		writtenIdentityMasterKeys: make(map[string]bool),
		writtenIdentitySlaveKeys:  make(map[string]bool),
		stopLeaseRenewal:          make(chan struct{}),
	}
}

// Initialize scans etcd for existing Cilium identity allocations to resume
// the next available identity ID, creates a lease for TTL-bound keys, and
// starts the lease renewal goroutine.
func (ciliumTranslator *CiliumFactTranslator) Initialize(ctx context.Context) error {
	ciliumTranslator.mutex.Lock()
	defer ciliumTranslator.mutex.Unlock()

	if scanError := ciliumTranslator.recoverIdentityAllocations(ctx); scanError != nil {
		return fmt.Errorf("recover identity allocations: %w", scanError)
	}

	leaseID, grantError := ciliumTranslator.factStore.GrantLease(ctx, ciliumLeaseTTLSeconds)
	if grantError != nil {
		return fmt.Errorf("grant cilium lease: %w", grantError)
	}
	ciliumTranslator.activeLease = leaseID

	go ciliumTranslator.runLeaseRenewalLoop()

	logging.Default().Info("cilium translator initialized",
		"nextIdentity", strconv.Itoa(ciliumTranslator.nextIdentityID),
		"leaseID", strconv.FormatInt(leaseID, 10))
	return nil
}

// recoverIdentityAllocations scans existing Cilium identity master keys to
// find the highest allocated ID and resumes allocation from max+1.
func (ciliumTranslator *CiliumFactTranslator) recoverIdentityAllocations(ctx context.Context) error {
	existingIdentities, scanError := ciliumTranslator.factStore.Scan(ctx, ciliumIdentityMasterPrefix)
	if scanError != nil {
		return scanError
	}

	maxIdentityID := ciliumMinUserIdentity - 1
	for _, identityFact := range existingIdentities {
		idString := strings.TrimPrefix(identityFact.Key, ciliumIdentityMasterPrefix)
		parsedID, parseError := strconv.Atoi(idString)
		if parseError != nil {
			continue
		}
		if parsedID > maxIdentityID {
			maxIdentityID = parsedID
		}
		ciliumTranslator.identityCache[string(identityFact.Value)] = parsedID
		ciliumTranslator.writtenIdentityMasterKeys[identityFact.Key] = true
	}

	ciliumTranslator.nextIdentityID = maxIdentityID + 1
	return nil
}

// runLeaseRenewalLoop renews the active etcd lease periodically until
// stopLeaseRenewal is closed.
func (ciliumTranslator *CiliumFactTranslator) runLeaseRenewalLoop() {
	renewalTicker := time.NewTicker(ciliumLeaseRenewalInterval)
	defer renewalTicker.Stop()

	for {
		select {
		case <-ciliumTranslator.stopLeaseRenewal:
			return
		case <-renewalTicker.C:
			ctx, cancelFunc := context.WithTimeout(context.Background(), 10*time.Second)
			_, renewError := ciliumTranslator.factStore.RenewLease(ctx, ciliumTranslator.activeLease)
			cancelFunc()
			if renewError != nil {
				logging.Default().Error("cilium: lease renewal failed", "error", renewError.Error())
			}
		}
	}
}

// ciliumServiceEntry is the JSON structure Cilium expects for service kvstore keys.
type ciliumServiceEntry struct {
	Name          string                `json:"name"`
	Namespace     string                `json:"namespace"`
	Cluster       string                `json:"cluster"`
	Frontend      ciliumServiceFrontend `json:"frontend"`
	Backends      []ciliumBackendEntry  `json:"backends"`
	TrafficPolicy string                `json:"traffic_policy"`
	Type          string                `json:"type"`
}

// ciliumServiceFrontend describes the VIP address and port for a Cilium service.
type ciliumServiceFrontend struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// ciliumBackendEntry describes a single backend endpoint for a Cilium service.
type ciliumBackendEntry struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// ciliumIPIdentityPair is the JSON structure for Cilium IP-to-identity mappings.
type ciliumIPIdentityPair struct {
	IP         string                 `json:"IP"`
	HostIP     string                 `json:"HostIP,omitempty"`
	ID         int                    `json:"ID"`
	Key        string                 `json:"Key"`
	Metadata   string                 `json:"Metadata,omitempty"`
	NamedPorts []ciliumNamedPortEntry `json:"NamedPorts,omitempty"`
}

// ciliumNamedPortEntry describes a named port in a Cilium IP identity mapping.
type ciliumNamedPortEntry struct {
	Name     string `json:"Name"`
	Port     int    `json:"Port"`
	Protocol string `json:"Protocol"`
}

// ciliumNodeRegistration is the JSON structure for Cilium node kvstore keys.
type ciliumNodeRegistration struct {
	Name        string              `json:"name"`
	Cluster     string              `json:"cluster"`
	Addresses   []ciliumNodeAddress `json:"addresses"`
	PodCIDR     string              `json:"pod_cidr,omitempty"`
	HealthIP    string              `json:"health_check_ip,omitempty"`
}

// ciliumNodeAddress describes an address type for a Cilium node registration.
type ciliumNodeAddress struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

// ReconcileServices translates CCattler ServiceVIPConfig entries into Cilium
// kvstore service entries. Each service becomes one Cilium service entry.
func (ciliumTranslator *CiliumFactTranslator) ReconcileServices(ctx context.Context, serviceConfigs []ServiceVIPConfig) error {
	ciliumTranslator.mutex.Lock()
	defer ciliumTranslator.mutex.Unlock()

	desiredServiceKeys := make(map[string]bool)

	for _, serviceConfig := range serviceConfigs {
		serviceKey := ciliumTranslator.serviceKeyPath(serviceConfig.ServiceName)
		desiredServiceKeys[serviceKey] = true

		ciliumBackends := make([]ciliumBackendEntry, 0, len(serviceConfig.Backends))
		for _, backend := range serviceConfig.Backends {
			ciliumBackends = append(ciliumBackends, ciliumBackendEntry{
				Address: backend.Address,
				Port:    backend.Port,
			})
		}

		serviceEntry := ciliumServiceEntry{
			Name:      serviceConfig.ServiceName,
			Namespace: "default",
			Cluster:   ciliumTranslator.clusterName,
			Frontend: ciliumServiceFrontend{
				Address:  serviceConfig.VirtualIP,
				Port:     serviceConfig.Port,
				Protocol: strings.ToUpper(serviceConfig.EffectiveProtocol()),
			},
			Backends:      ciliumBackends,
			TrafficPolicy: "Cluster",
			Type:          "ClusterIP",
		}

		if writeError := ciliumTranslator.writeServiceEntry(ctx, serviceKey, serviceEntry); writeError != nil {
			logging.Default().Error("cilium: service write failed",
				"service", serviceConfig.ServiceName, "error", writeError.Error())
		}
	}

	ciliumTranslator.removeStaleKeys(ctx, ciliumTranslator.writtenServiceKeys, desiredServiceKeys)
	return nil
}

// ReconcileRoutes translates CCattler route configurations into Cilium
// service entries with affinity configured via Cilium's native session support.
func (ciliumTranslator *CiliumFactTranslator) ReconcileRoutes(ctx context.Context, routeConfigs []RouteDataPlaneConfig) error {
	ciliumTranslator.mutex.Lock()
	defer ciliumTranslator.mutex.Unlock()

	for _, routeConfig := range routeConfigs {
		serviceKey := ciliumTranslator.routeServiceKeyPath(routeConfig.RouteName)

		ciliumBackends := make([]ciliumBackendEntry, 0, len(routeConfig.Backends))
		for _, backend := range routeConfig.Backends {
			ciliumBackends = append(ciliumBackends, ciliumBackendEntry{
				Address: backend.Address,
				Port:    backend.Port,
			})
		}

		serviceEntry := ciliumServiceEntry{
			Name:      routeConfig.RouteName,
			Namespace: "routes",
			Cluster:   ciliumTranslator.clusterName,
			Frontend: ciliumServiceFrontend{
				Address:  routeConfig.VirtualIP,
				Port:     routeConfig.Port,
				Protocol: "TCP",
			},
			Backends:      ciliumBackends,
			TrafficPolicy: "Cluster",
			Type:          "ClusterIP",
		}

		if writeError := ciliumTranslator.writeServiceEntry(ctx, serviceKey, serviceEntry); writeError != nil {
			logging.Default().Error("cilium: route service write failed",
				"route", routeConfig.RouteName, "error", writeError.Error())
		}
	}

	return nil
}

// WriteEndpointIPIdentity writes a Cilium IP-to-identity mapping and the
// corresponding identity master and slave keys for a container endpoint.
func (ciliumTranslator *CiliumFactTranslator) WriteEndpointIPIdentity(ctx context.Context, containerIP string, hostIP string, serviceName string, containerPort int) error {
	ciliumTranslator.mutex.Lock()
	defer ciliumTranslator.mutex.Unlock()

	labelKey := BuildCiliumLabelKey(serviceName)
	identityID := ciliumTranslator.allocateOrReuseIdentity(labelKey)

	if writeError := ciliumTranslator.writeIdentityMasterKey(ctx, identityID, labelKey); writeError != nil {
		return fmt.Errorf("write identity master: %w", writeError)
	}

	if writeError := ciliumTranslator.writeIdentitySlaveKey(ctx, identityID, labelKey); writeError != nil {
		return fmt.Errorf("write identity slave: %w", writeError)
	}

	return ciliumTranslator.writeIPIdentityMapping(ctx, containerIP, hostIP, identityID, labelKey, containerPort)
}

// WriteNodeRegistration writes a Cilium node registration entry so other
// Cilium agents can discover this node for tunnel setup and policy routing.
func (ciliumTranslator *CiliumFactTranslator) WriteNodeRegistration(ctx context.Context, nodeIP string, podCIDR string) error {
	ciliumTranslator.mutex.Lock()
	defer ciliumTranslator.mutex.Unlock()

	nodeReg := ciliumNodeRegistration{
		Name:    ciliumTranslator.nodeID,
		Cluster: ciliumTranslator.clusterName,
		Addresses: []ciliumNodeAddress{
			{Type: "InternalIP", Address: nodeIP},
		},
		PodCIDR:  podCIDR,
		HealthIP: nodeIP,
	}

	nodeJSON, marshalError := json.Marshal(nodeReg)
	if marshalError != nil {
		return fmt.Errorf("marshal node registration: %w", marshalError)
	}

	nodeKey := fmt.Sprintf("%s%s/%s", ciliumNodePrefix, ciliumTranslator.clusterName, ciliumTranslator.nodeID)
	if _, putError := ciliumTranslator.factStore.PutWithLease(ctx, nodeKey, nodeJSON, ciliumTranslator.activeLease); putError != nil {
		return fmt.Errorf("write node registration: %w", putError)
	}

	return nil
}

// CleanupAll removes all Cilium kvstore keys written by this translator and
// stops the lease renewal goroutine.
func (ciliumTranslator *CiliumFactTranslator) CleanupAll(ctx context.Context) error {
	close(ciliumTranslator.stopLeaseRenewal)

	ciliumTranslator.mutex.Lock()
	defer ciliumTranslator.mutex.Unlock()

	if ciliumTranslator.activeLease != 0 {
		if revokeError := ciliumTranslator.factStore.RevokeLease(ctx, ciliumTranslator.activeLease); revokeError != nil {
			logging.Default().Error("cilium: lease revoke failed", "error", revokeError.Error())
		}
	}

	var lastError error
	allTrackedKeyMaps := []map[string]bool{
		ciliumTranslator.writtenServiceKeys,
		ciliumTranslator.writtenIPKeys,
		ciliumTranslator.writtenIdentityMasterKeys,
		ciliumTranslator.writtenIdentitySlaveKeys,
	}
	for _, keyMap := range allTrackedKeyMaps {
		for keyToDelete := range keyMap {
			if deleteError := ciliumTranslator.factStore.Delete(ctx, keyToDelete); deleteError != nil {
				logging.Default().Error("cilium: cleanup failed", "key", keyToDelete, "error", deleteError.Error())
				lastError = deleteError
			}
		}
	}

	return lastError
}

// writeServiceEntry marshals and writes a service entry to the kvstore.
func (ciliumTranslator *CiliumFactTranslator) writeServiceEntry(ctx context.Context, serviceKey string, serviceEntry ciliumServiceEntry) error {
	serviceJSON, marshalError := json.Marshal(serviceEntry)
	if marshalError != nil {
		return marshalError
	}
	if _, putError := ciliumTranslator.factStore.Put(ctx, serviceKey, serviceJSON); putError != nil {
		return putError
	}
	ciliumTranslator.writtenServiceKeys[serviceKey] = true
	return nil
}

// writeIdentityMasterKey writes the identity master key (permanent, no lease).
func (ciliumTranslator *CiliumFactTranslator) writeIdentityMasterKey(ctx context.Context, identityID int, labelKey string) error {
	masterKey := fmt.Sprintf("%s%d", ciliumIdentityMasterPrefix, identityID)
	if ciliumTranslator.writtenIdentityMasterKeys[masterKey] {
		return nil
	}
	if _, putError := ciliumTranslator.factStore.Put(ctx, masterKey, []byte(labelKey)); putError != nil {
		return putError
	}
	ciliumTranslator.writtenIdentityMasterKeys[masterKey] = true
	return nil
}

// writeIdentitySlaveKey writes the per-node identity slave key with a lease.
func (ciliumTranslator *CiliumFactTranslator) writeIdentitySlaveKey(ctx context.Context, identityID int, labelKey string) error {
	slaveKey := fmt.Sprintf("%s%s/%s", ciliumIdentitySlavePrefix, labelKey, ciliumTranslator.nodeID)
	identityValue := []byte(strconv.Itoa(identityID))
	if _, putError := ciliumTranslator.factStore.PutWithLease(ctx, slaveKey, identityValue, ciliumTranslator.activeLease); putError != nil {
		return putError
	}
	ciliumTranslator.writtenIdentitySlaveKeys[slaveKey] = true
	return nil
}

// writeIPIdentityMapping writes the IP-to-identity mapping with a lease.
func (ciliumTranslator *CiliumFactTranslator) writeIPIdentityMapping(ctx context.Context, containerIP string, hostIP string, identityID int, labelKey string, containerPort int) error {
	ipIdentityPair := ciliumIPIdentityPair{
		IP:     containerIP,
		HostIP: hostIP,
		ID:     identityID,
		Key:    labelKey,
	}
	if containerPort > 0 {
		ipIdentityPair.NamedPorts = []ciliumNamedPortEntry{
			{Name: "svc", Port: containerPort, Protocol: "TCP"},
		}
	}

	pairJSON, marshalError := json.Marshal(ipIdentityPair)
	if marshalError != nil {
		return fmt.Errorf("marshal IP identity pair: %w", marshalError)
	}

	ipKey := fmt.Sprintf("%s%s/%s", ciliumIPPrefix, ciliumTranslator.clusterName, containerIP)
	if _, putError := ciliumTranslator.factStore.PutWithLease(ctx, ipKey, pairJSON, ciliumTranslator.activeLease); putError != nil {
		return fmt.Errorf("write IP identity: %w", putError)
	}
	ciliumTranslator.writtenIPKeys[ipKey] = true
	return nil
}

// serviceKeyPath returns the Cilium kvstore key for a service definition.
func (ciliumTranslator *CiliumFactTranslator) serviceKeyPath(serviceName string) string {
	return fmt.Sprintf("%s%s/default/%s", ciliumServicePrefix, ciliumTranslator.clusterName, serviceName)
}

// routeServiceKeyPath returns the Cilium kvstore key for a route-as-service.
func (ciliumTranslator *CiliumFactTranslator) routeServiceKeyPath(routeName string) string {
	return fmt.Sprintf("%s%s/routes/%s", ciliumServicePrefix, ciliumTranslator.clusterName, routeName)
}

// allocateOrReuseIdentity returns a numeric identity for the given label key,
// reusing a previously allocated ID if the label key is unchanged.
func (ciliumTranslator *CiliumFactTranslator) allocateOrReuseIdentity(labelKey string) int {
	if existingID, alreadyAllocated := ciliumTranslator.identityCache[labelKey]; alreadyAllocated {
		return existingID
	}
	allocatedID := ciliumTranslator.nextIdentityID
	ciliumTranslator.nextIdentityID++
	ciliumTranslator.identityCache[labelKey] = allocatedID
	return allocatedID
}

// removeStaleKeys deletes tracked keys that are not in the desired set.
func (ciliumTranslator *CiliumFactTranslator) removeStaleKeys(ctx context.Context, tracked map[string]bool, desired map[string]bool) {
	for existingKey := range tracked {
		if !desired[existingKey] {
			if deleteError := ciliumTranslator.factStore.Delete(ctx, existingKey); deleteError != nil {
				logging.Default().Error("cilium: stale key delete failed",
					"key", existingKey, "error", deleteError.Error())
			}
			delete(tracked, existingKey)
		}
	}
}

// BuildCiliumLabelKey converts a CCattler service name into a sorted Cilium
// label key string. Format: "source:key=value;..." sorted alphabetically.
func BuildCiliumLabelKey(serviceName string) string {
	labels := map[string]string{
		"app": serviceName,
	}

	sortedLabelPairs := make([]string, 0, len(labels))
	for labelName, labelValue := range labels {
		sortedLabelPairs = append(sortedLabelPairs,
			fmt.Sprintf("%s:%s=%s", ciliumLabelSourceContainer, labelName, labelValue))
	}
	sort.Strings(sortedLabelPairs)

	var labelKeyBuilder strings.Builder
	for _, labelPair := range sortedLabelPairs {
		labelKeyBuilder.WriteString(labelPair)
		labelKeyBuilder.WriteByte(';')
	}
	return labelKeyBuilder.String()
}
