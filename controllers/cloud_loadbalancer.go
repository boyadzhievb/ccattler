// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// CloudLoadBalancerController watches services with "expose external" ports
// and manages cloud load balancers for them. It creates load balancers for
// newly externally-exposed services, updates backend lists when endpoints
// change, and deletes load balancers when services are removed or no longer
// externally exposed. Implements PostCommitController to defer provider
// calls until after the store transaction succeeds.
type CloudLoadBalancerController struct {
	cloudProvider cloud.CloudProvider // cloudProvider is the backend for load balancer API calls.
	factStore     store.StateStore    // factStore is used by the post-commit executor to read pending ops.
}

// NewCloudLoadBalancerController creates a CloudLoadBalancerController with the
// given cloud provider and fact store.
func NewCloudLoadBalancerController(cloudProvider cloud.CloudProvider, factStore store.StateStore) *CloudLoadBalancerController {
	return &CloudLoadBalancerController{
		cloudProvider: cloudProvider,
		factStore:     factStore,
	}
}

// Name returns "cloud-loadbalancer".
func (loadBalancerController *CloudLoadBalancerController) Name() string {
	return "cloud-loadbalancer"
}

// Watch returns the fact prefixes needed to reconcile cloud load balancers:
// desired service config (for expose external), endpoints (for backends),
// observed node addresses (for backend IPs), observed LB state, and
// derived pending operations.
func (loadBalancerController *CloudLoadBalancerController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanEndpoints,
		types.ScanObservedNodes,
		types.ScanObservedCloudLoadBalancers,
		types.ScanDerivedCloudLoadBalancers,
	}
}

// pendingCloudLBOperation describes a cloud load balancer operation that must
// execute after a successful CAS commit.
type pendingCloudLBOperation struct {
	Kind        string `json:"kind"`                  // Kind is "ensure" or "delete".
	ServiceName string `json:"service_name"`          // ServiceName is the target service.
	Port        int    `json:"port,omitempty"`        // Port is the external port (ensure only).
	TargetPort  int    `json:"target_port,omitempty"` // TargetPort is the backend port (ensure only).
	Protocol    string `json:"protocol,omitempty"`    // Protocol is "tcp" or "http" (ensure only).
}

// Reconcile compares desired external services against existing cloud load
// balancers and emits pending operations for the post-commit executor. Always
// emits "ensure" for active external services so that backend changes propagate
// to the cloud provider. Cancels orphaned pending "ensure" operations for
// services that are no longer externally exposed.
func (loadBalancerController *CloudLoadBalancerController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	externalServices := extractExternalServicePorts(facts)
	existingLoadBalancers := extractExistingLoadBalancers(facts)
	pendingEnsureKeysByService := extractPendingEnsureKeysByService(facts)
	appliedConfigHashes := collectPerPortAppliedConfigHashes(facts, types.ScanDerivedCloudLoadBalancers)
	endpointAddresses := extractEndpointAddressesPerService(facts)

	var proposedChanges []Change

	for serviceName, externalConfig := range externalServices {
		currentAddresses := endpointAddresses[serviceName]
		for _, portConfig := range externalConfig.ports {
			configHash := computeLoadBalancerConfigHash(
				serviceName, portConfig.port, portConfig.protocol, currentAddresses)
			perPortHashKey := fmt.Sprintf("%s:%d", serviceName, portConfig.port)
			if appliedHash, hashExists := appliedConfigHashes[perPortHashKey]; hashExists && appliedHash == configHash {
				continue
			}

			pendingOp := pendingCloudLBOperation{
				Kind:        "ensure",
				ServiceName: serviceName,
				Port:        portConfig.port,
				TargetPort:  portConfig.port,
				Protocol:    portConfig.protocol,
			}
			pendingJSON, marshalErr := json.Marshal(pendingOp)
			if marshalErr != nil {
				logging.Default().Error("failed to marshal pending LB operation",
					"service", serviceName, "error", marshalErr.Error())
				continue
			}
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyDerivedCloudLBPendingOperationForPort(serviceName, portConfig.port),
				Value: pendingJSON,
			})
		}
	}

	for serviceName := range existingLoadBalancers {
		if _, stillExternal := externalServices[serviceName]; !stillExternal {
			pendingOp := pendingCloudLBOperation{
				Kind:        "delete",
				ServiceName: serviceName,
			}
			pendingJSON, marshalErr := json.Marshal(pendingOp)
			if marshalErr != nil {
				logging.Default().Error("failed to marshal pending LB delete",
					"service", serviceName, "error", marshalErr.Error())
				continue
			}
			proposedChanges = append(proposedChanges, Change{
				Type:  store.OpPut,
				Key:   types.KeyDerivedCloudLBPendingOperation(serviceName),
				Value: pendingJSON,
			})
		}
	}

	for serviceName, pendingKeys := range pendingEnsureKeysByService {
		if _, stillExternal := externalServices[serviceName]; !stillExternal {
			if _, hasObservedLB := existingLoadBalancers[serviceName]; hasObservedLB {
				continue
			}
			for _, pendingKey := range pendingKeys {
				proposedChanges = append(proposedChanges, Change{
					Type: store.OpDelete,
					Key:  pendingKey,
				})
			}
		}
	}

	return proposedChanges, nil
}

// ExecutePostCommitOperations reads pending cloud LB operations from the store
// and calls the cloud provider. On success the pending key is deleted and
// observed state is written. On failure the pending key is left for retry.
func (loadBalancerController *CloudLoadBalancerController) ExecutePostCommitOperations(ctx context.Context) error {
	pendingFacts, scanError := loadBalancerController.factStore.Scan(ctx, types.ScanDerivedCloudLoadBalancers)
	if scanError != nil {
		return scanError
	}

	for _, pendingFact := range pendingFacts {
		if !strings.Contains(pendingFact.Key, "/pending_operation") {
			continue
		}

		var pendingOp pendingCloudLBOperation
		if unmarshalErr := json.Unmarshal(pendingFact.Value, &pendingOp); unmarshalErr != nil {
			logging.Default().Error("corrupt pending LB operation",
				"key", pendingFact.Key, "error", unmarshalErr.Error())
			continue
		}

		switch pendingOp.Kind {
		case "ensure":
			loadBalancerController.executeEnsureLoadBalancer(ctx, pendingOp, pendingFact.Key)
		case "delete":
			loadBalancerController.executeDeleteLoadBalancer(ctx, pendingOp, pendingFact.Key)
		default:
			logging.Default().Error("unknown pending LB operation kind",
				"service", pendingOp.ServiceName, "kind", pendingOp.Kind)
		}
	}

	return nil
}

// executeEnsureLoadBalancer builds the current backend list from the store,
// calls EnsureLoadBalancer, and writes the result to observed state.
func (loadBalancerController *CloudLoadBalancerController) executeEnsureLoadBalancer(
	ctx context.Context,
	pendingOp pendingCloudLBOperation,
	pendingKey string,
) {
	endpointFacts, _ := loadBalancerController.factStore.Scan(ctx, types.ScanEndpoints)
	nodeFacts, _ := loadBalancerController.factStore.Scan(ctx, types.ScanObservedNodes)
	instanceFacts, _ := loadBalancerController.factStore.Scan(ctx, types.ScanObservedInstances)

	allFacts := append(append(endpointFacts, nodeFacts...), instanceFacts...)
	serviceEndpoints := extractServiceEndpointBackends(allFacts)
	nodeAddresses := extractNodeAddressesFromFacts(allFacts)

	backends := buildLoadBalancerBackends(serviceEndpoints[pendingOp.ServiceName], nodeAddresses)
	loadBalancerConfig := cloud.LoadBalancerConfig{
		ServiceName: pendingOp.ServiceName,
		Port:        pendingOp.Port,
		TargetPort:  pendingOp.TargetPort,
		Protocol:    pendingOp.Protocol,
		Backends:    backends,
	}

	externalAddress, ensureError := loadBalancerController.cloudProvider.EnsureLoadBalancer(ctx, loadBalancerConfig)
	if ensureError != nil {
		logging.Default().Error("cloud LB ensure failed, will retry",
			"service", pendingOp.ServiceName, "error", ensureError.Error())
		return
	}

	if _, putErr := loadBalancerController.factStore.Put(ctx,
		types.KeyObservedCloudLoadBalancerAddress(pendingOp.ServiceName),
		[]byte(externalAddress)); putErr != nil {
		logging.Default().Error("failed to write LB address", "service", pendingOp.ServiceName, "error", putErr.Error())
	}
	if _, putErr := loadBalancerController.factStore.Put(ctx,
		types.KeyObservedCloudLoadBalancerState(pendingOp.ServiceName),
		[]byte(string(cloud.LoadBalancerStateActive))); putErr != nil {
		logging.Default().Error("failed to write LB state", "service", pendingOp.ServiceName, "error", putErr.Error())
	}

	// Persist the applied config hash so Reconcile can skip unchanged configurations.
	endpointAddressesByService := extractEndpointAddressesPerService(endpointFacts)
	appliedConfigHash := computeLoadBalancerConfigHash(
		pendingOp.ServiceName, pendingOp.Port, pendingOp.Protocol,
		endpointAddressesByService[pendingOp.ServiceName])
	if _, putErr := loadBalancerController.factStore.Put(ctx,
		types.KeyDerivedCloudLBAppliedHashForPort(pendingOp.ServiceName, pendingOp.Port),
		[]byte(appliedConfigHash)); putErr != nil {
		logging.Default().Error("failed to write LB applied config hash",
			"service", pendingOp.ServiceName, "port", strconv.Itoa(pendingOp.Port), "error", putErr.Error())
	}

	if deleteErr := loadBalancerController.factStore.Delete(ctx, pendingKey); deleteErr != nil {
		logging.Default().Error("failed to delete pending LB op", "service", pendingOp.ServiceName, "error", deleteErr.Error())
	}
}

// executeDeleteLoadBalancer calls DeleteLoadBalancer and removes observed state.
func (loadBalancerController *CloudLoadBalancerController) executeDeleteLoadBalancer(
	ctx context.Context,
	pendingOp pendingCloudLBOperation,
	pendingKey string,
) {
	deleteError := loadBalancerController.cloudProvider.DeleteLoadBalancer(ctx, pendingOp.ServiceName)
	if deleteError != nil {
		logging.Default().Error("cloud LB delete failed, will retry",
			"service", pendingOp.ServiceName, "error", deleteError.Error())
		return
	}

	if deleteErr := loadBalancerController.factStore.Delete(ctx, types.KeyObservedCloudLoadBalancerAddress(pendingOp.ServiceName)); deleteErr != nil {
		logging.Default().Error("failed to delete LB address", "service", pendingOp.ServiceName, "error", deleteErr.Error())
	}
	if deleteErr := loadBalancerController.factStore.Delete(ctx, types.KeyObservedCloudLoadBalancerState(pendingOp.ServiceName)); deleteErr != nil {
		logging.Default().Error("failed to delete LB state", "service", pendingOp.ServiceName, "error", deleteErr.Error())
	}
	deletePerPortAppliedHashes(ctx, loadBalancerController.factStore, pendingOp.ServiceName)

	if deleteErr := loadBalancerController.factStore.Delete(ctx, pendingKey); deleteErr != nil {
		logging.Default().Error("failed to delete pending LB op", "service", pendingOp.ServiceName, "error", deleteErr.Error())
	}
}

// externalPortConfig holds the port and protocol for one externally exposed port.
type externalPortConfig struct {
	port     int
	protocol string
}

// externalServiceConfig holds all externally exposed ports for a service.
type externalServiceConfig struct {
	ports []externalPortConfig
}

// extractExternalServicePorts builds a map of service names to their external
// port configurations from desired facts. Collects all ports per service.
func extractExternalServicePorts(facts []store.Fact) map[string]externalServiceConfig {
	externalServices := make(map[string]externalServiceConfig)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(factEntry.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) >= 4 && pathParts[1] == "expose" && pathParts[3] == "external" {
			serviceName := pathParts[0]
			port, parseError := strconv.Atoi(pathParts[2])
			if parseError != nil {
				continue
			}
			protocol := string(factEntry.Value)
			if protocol == "" {
				protocol = "tcp"
			}
			serviceConfig := externalServices[serviceName]
			serviceConfig.ports = append(serviceConfig.ports, externalPortConfig{
				port:     port,
				protocol: protocol,
			})
			externalServices[serviceName] = serviceConfig
		}
	}
	return externalServices
}

// endpointBackend holds the parsed address and port from an endpoint fact.
type endpointBackend struct {
	address string
	port    int
	nodeID  string
}

// extractServiceEndpointBackends parses endpoint facts into a map of service
// name to backend list.
func extractServiceEndpointBackends(facts []store.Fact) map[string][]endpointBackend {
	serviceEndpoints := make(map[string][]endpointBackend)

	instanceNodes := collectStringValuesBySuffix(facts, types.ScanObservedInstances, "node")

	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		serviceName, instanceID, hasSuffix := splitFactKeyIntoEntityAndSuffix(factEntry.Key, types.ScanEndpoints)
		if !hasSuffix {
			continue
		}

		addressPort := string(factEntry.Value)
		colonIndex := strings.LastIndex(addressPort, ":")
		if colonIndex < 0 {
			continue
		}
		address := addressPort[:colonIndex]
		port, parseError := strconv.Atoi(addressPort[colonIndex+1:])
		if parseError != nil {
			continue
		}

		serviceEndpoints[serviceName] = append(serviceEndpoints[serviceName], endpointBackend{
			address: address,
			port:    port,
			nodeID:  instanceNodes[instanceID],
		})
	}
	return serviceEndpoints
}

// extractNodeAddressesFromFacts builds a map of node ID to advertised address.
func extractNodeAddressesFromFacts(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanObservedNodes, "address")
}

// buildLoadBalancerBackends converts endpoint backends to cloud load balancer
// backend targets, using node addresses where available.
func buildLoadBalancerBackends(endpoints []endpointBackend, nodeAddresses map[string]string) []cloud.LoadBalancerBackend {
	var backends []cloud.LoadBalancerBackend
	for _, endpoint := range endpoints {
		backendAddress := endpoint.address
		if endpoint.nodeID != "" {
			if nodeAddress, exists := nodeAddresses[endpoint.nodeID]; exists {
				backendAddress = nodeAddress
			}
		}
		backends = append(backends, cloud.LoadBalancerBackend{
			NodeID:  endpoint.nodeID,
			Address: backendAddress,
			Port:    endpoint.port,
		})
	}
	return backends
}

// extractExistingLoadBalancers returns a map of service name to external
// address for all load balancers currently tracked in the store.
func extractExistingLoadBalancers(facts []store.Fact) map[string]string {
	return collectStringValuesBySuffix(facts, types.ScanObservedCloudLoadBalancers, "address")
}

// extractPendingEnsureKeysByService returns a map of service names to their
// pending "ensure" operation fact keys. Used to detect and clean up orphaned
// pending operations for services that have been deleted. Matches both legacy
// per-service keys and per-port keys.
func extractPendingEnsureKeysByService(facts []store.Fact) map[string][]string {
	pendingKeysByService := make(map[string][]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanDerivedCloudLoadBalancers) {
		if !strings.Contains(factEntry.Key, "/pending_operation") {
			continue
		}
		var pendingOp pendingCloudLBOperation
		if unmarshalErr := json.Unmarshal(factEntry.Value, &pendingOp); unmarshalErr != nil {
			continue
		}
		if pendingOp.Kind == "ensure" {
			pendingKeysByService[pendingOp.ServiceName] = append(
				pendingKeysByService[pendingOp.ServiceName], factEntry.Key)
		}
	}
	return pendingKeysByService
}

// deletePerPortAppliedHashes scans derived cloud LB facts for the given service
// and deletes all per-port applied_hash entries plus the legacy per-service key.
func deletePerPortAppliedHashes(ctx context.Context, factStore store.StateStore, serviceName string) {
	derivedFacts, scanErr := factStore.Scan(ctx, types.ScanDerivedCloudLoadBalancers)
	if scanErr != nil {
		logging.Default().Error("failed to scan for LB applied hashes", "service", serviceName, "error", scanErr.Error())
		return
	}
	for _, factEntry := range derivedFacts {
		entityName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(factEntry.Key, types.ScanDerivedCloudLoadBalancers)
		if !hasSuffix || entityName != serviceName {
			continue
		}
		if suffix == "applied_hash" || strings.HasPrefix(suffix, "applied_hash/") {
			if deleteErr := factStore.Delete(ctx, factEntry.Key); deleteErr != nil {
				logging.Default().Error("failed to delete LB applied hash",
					"key", factEntry.Key, "error", deleteErr.Error())
			}
		}
	}
}

// collectPerPortAppliedConfigHashes scans derived cloud LB facts and returns
// applied config hashes keyed by "serviceName:port". Matches keys with suffix
// "applied_hash/{port}" produced by KeyDerivedCloudLBAppliedHashForPort.
func collectPerPortAppliedConfigHashes(facts []store.Fact, scanPrefix string) map[string]string {
	perPortHashes := make(map[string]string)
	for _, factEntry := range store.FactsWithPrefix(facts, scanPrefix) {
		entityName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(factEntry.Key, scanPrefix)
		if !hasSuffix {
			continue
		}
		if strings.HasPrefix(suffix, "applied_hash/") {
			portSuffix := strings.TrimPrefix(suffix, "applied_hash/")
			compositeKey := entityName + ":" + portSuffix
			perPortHashes[compositeKey] = string(factEntry.Value)
		}
	}
	return perPortHashes
}

// computeLoadBalancerConfigHash computes a stable SHA-256 hash of the load
// balancer configuration for a service. The hash covers service name, port,
// protocol, and sorted backend addresses, enabling change detection across
// reconciliation cycles.
func computeLoadBalancerConfigHash(serviceName string, servicePort int, serviceProtocol string, backendAddresses []string) string {
	sortedAddresses := make([]string, len(backendAddresses))
	copy(sortedAddresses, backendAddresses)
	sort.Strings(sortedAddresses)
	hashInput := fmt.Sprintf("%s:%d:%s:%s", serviceName, servicePort, serviceProtocol, strings.Join(sortedAddresses, ","))
	hashDigest := sha256.Sum256([]byte(hashInput))
	return hex.EncodeToString(hashDigest[:])
}

// extractEndpointAddressesPerService collects all endpoint address:port values
// from endpoint facts, grouped by service name. Used to compute the load
// balancer config hash for change detection.
func extractEndpointAddressesPerService(facts []store.Fact) map[string][]string {
	addressesByService := make(map[string][]string)
	for _, factEntry := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		serviceName, _, hasSuffix := splitFactKeyIntoEntityAndSuffix(factEntry.Key, types.ScanEndpoints)
		if !hasSuffix {
			continue
		}
		addressesByService[serviceName] = append(addressesByService[serviceName], string(factEntry.Value))
	}
	return addressesByService
}
