// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// DataPlaneReconciler encapsulates data plane reconciliation logic, reading
// VIP and endpoint facts from the store and programming DNAT rules via the
// data plane provider. Extracted from Agent to isolate the data plane concern.
type DataPlaneReconciler struct {
	nodeID            string                    // nodeID is the unique identifier for the node this reconciler manages.
	factStore         store.StateStore          // factStore is the fact store used to read VIP, endpoint, and node facts.
	dataPlaneProvider network.DataPlaneProvider // dataPlaneProvider programs VIP DNAT rules on the host.
	advertiseAddress  string                    // advertiseAddress is this node's LAN-routable IP for cross-host data plane.
}

// NewDataPlaneReconciler creates a DataPlaneReconciler wired to the given
// fact store and data plane provider for the specified node.
func NewDataPlaneReconciler(nodeID string, factStore store.StateStore, dataPlaneProvider network.DataPlaneProvider, advertiseAddress string) *DataPlaneReconciler {
	return &DataPlaneReconciler{
		nodeID:            nodeID,
		factStore:         factStore,
		dataPlaneProvider: dataPlaneProvider,
		advertiseAddress:  advertiseAddress,
	}
}

// Reconcile reads VIP and endpoint facts from the store, resolves
// each endpoint to a host-accessible backend address, and calls the data
// plane provider to program iptables DNAT rules. For instances running on
// this node, the backend is the container IP:port (reachable via Docker
// bridge). For instances on other nodes, the backend is the remote node's
// advertise address + host port (reachable via LAN).
func (dataPlaneReconciler *DataPlaneReconciler) Reconcile(ctx context.Context) {
	if dataPlaneReconciler.dataPlaneProvider == nil {
		return
	}

	serviceVIPConfigs, err := dataPlaneReconciler.buildServiceVIPConfigs(ctx)
	if err != nil {
		logging.Default().Error("dataplane: failed to build VIP configs", "agent", dataPlaneReconciler.nodeID, "error", err.Error())
		return
	}

	if err := dataPlaneReconciler.dataPlaneProvider.ReconcileVIPDataPlane(ctx, serviceVIPConfigs); err != nil {
		logging.Default().Error("dataplane: reconcile error", "agent", dataPlaneReconciler.nodeID, "error", err.Error())
	}
}

// buildServiceVIPConfigs scans VIP facts and endpoint facts to construct
// the complete set of service VIP configurations for the data plane.
func (dataPlaneReconciler *DataPlaneReconciler) buildServiceVIPConfigs(ctx context.Context) ([]network.ServiceVIPConfig, error) {
	vipFacts, err := dataPlaneReconciler.factStore.Scan(ctx, types.ScanNetworkVIPs)
	if err != nil {
		return nil, fmt.Errorf("scanning VIP facts: %w", err)
	}

	serviceVIPMap := make(map[string]*network.ServiceVIPConfig)
	for _, vipFact := range vipFacts {
		relativePath := strings.TrimPrefix(vipFact.Key, types.ScanNetworkVIPs)

		if strings.HasSuffix(relativePath, "/port") {
			serviceName := strings.TrimSuffix(relativePath, "/port")
			if existingConfig, exists := serviceVIPMap[serviceName]; exists {
				parsedPort, _ := strconv.Atoi(string(vipFact.Value))
				existingConfig.Port = parsedPort
			}
			continue
		}

		serviceName := relativePath
		parsedPort := 0
		portFact, portErr := dataPlaneReconciler.factStore.Get(ctx, types.KeyNetworkVIPServicePort(serviceName))
		if portErr == nil {
			portValue, parseError := strconv.Atoi(string(portFact.Value))
			if parseError != nil {
				logging.Default().Warn("corrupt VIP port fact", "service", serviceName, "value", string(portFact.Value))
				continue
			}
			parsedPort = portValue
		}

		serviceVIPMap[serviceName] = &network.ServiceVIPConfig{
			ServiceName: serviceName,
			VirtualIP:   string(vipFact.Value),
			Port:        parsedPort,
		}
	}

	var serviceVIPConfigs []network.ServiceVIPConfig
	for serviceName, vipConfig := range serviceVIPMap {
		if vipConfig.Port == 0 {
			logging.Default().Warn("skipping service with no VIP port configured", "service", serviceName)
			continue
		}

		backends, err := dataPlaneReconciler.resolveServiceBackends(ctx, serviceName)
		if err != nil {
			logging.Default().Error("dataplane: failed to resolve backends", "agent", dataPlaneReconciler.nodeID, "service", serviceName, "error", err.Error())
			continue
		}
		vipConfig.Backends = backends
		serviceVIPConfigs = append(serviceVIPConfigs, *vipConfig)
	}

	return serviceVIPConfigs, nil
}

// resolveServiceBackends reads endpoint facts for a service and resolves
// each endpoint to a host-accessible backend address. Local instances use
// the container IP:port; remote instances use the node's advertise address
// and host port.
func (dataPlaneReconciler *DataPlaneReconciler) resolveServiceBackends(ctx context.Context, serviceName string) ([]network.DataPlaneBackend, error) {
	endpointPrefix := fmt.Sprintf("%s/service/%s/", types.PrefixEndpoint, serviceName)
	endpointFacts, err := dataPlaneReconciler.factStore.Scan(ctx, endpointPrefix)
	if err != nil {
		return nil, fmt.Errorf("scanning endpoints for %s: %w", serviceName, err)
	}

	var backends []network.DataPlaneBackend
	for _, endpointFact := range endpointFacts {
		endpointSuffix := strings.TrimPrefix(endpointFact.Key, endpointPrefix)
		suffixParts := strings.SplitN(endpointSuffix, "/", 2)
		instanceID := suffixParts[0]
		containerPort := 0
		if len(suffixParts) > 1 {
			containerPort, _ = strconv.Atoi(suffixParts[1])
		}
		endpointValue := string(endpointFact.Value)

		colonIndex := strings.LastIndex(endpointValue, ":")
		if colonIndex < 0 {
			continue
		}
		endpointIP := endpointValue[:colonIndex]
		endpointPort, _ := strconv.Atoi(endpointValue[colonIndex+1:])

		instanceNodeFact, err := dataPlaneReconciler.factStore.Get(ctx, types.KeyObservedInstanceNode(instanceID))
		if err != nil {
			backends = append(backends, network.DataPlaneBackend{
				Address: endpointIP,
				Port:    endpointPort,
			})
			continue
		}

		instanceNodeID := string(instanceNodeFact.Value)

		if instanceNodeID == dataPlaneReconciler.nodeID {
			// Local backends use the container bridge IP directly.
			// This avoids hairpin NAT (DNAT to self via the host
			// address would require MASQUERADE on loopback).
			backends = append(backends, network.DataPlaneBackend{
				Address: endpointIP,
				Port:    endpointPort,
			})
		} else {
			remoteBackend := dataPlaneReconciler.resolveRemoteBackend(ctx, instanceID, instanceNodeID, containerPort)
			if remoteBackend.Port > 0 {
				backends = append(backends, remoteBackend)
			}
		}
	}

	return backends, nil
}

// resolveHostPort looks up the host port for an instance, trying the
// per-container-port key first, then falling back to the legacy single-port key.
func resolveHostPort(ctx context.Context, factStore store.StateStore, instanceID string, containerPort int) int {
	if containerPort > 0 {
		portFact, err := factStore.Get(ctx, types.KeyObservedInstanceHostPortForPort(instanceID, containerPort))
		if err == nil {
			hostPort, parseErr := strconv.Atoi(string(portFact.Value))
			if parseErr == nil && hostPort > 0 {
				return hostPort
			}
		}
	}
	portFact, err := factStore.Get(ctx, types.KeyObservedInstanceHostPort(instanceID))
	if err == nil {
		hostPort, parseErr := strconv.Atoi(string(portFact.Value))
		if parseErr == nil && hostPort > 0 {
			return hostPort
		}
	}
	return 0
}

// resolveRemoteBackend determines the host-accessible address for an
// instance running on a different node. It looks up the remote node's
// advertise address and the instance's host port mapping. Returns an
// empty backend (zero port) if the remote address cannot be resolved —
// container-bridge IPs (172.x) are not routable cross-node, so falling
// back to them would create broken DNAT rules.
func (dataPlaneReconciler *DataPlaneReconciler) resolveRemoteBackend(ctx context.Context, instanceID string, remoteNodeID string, containerPort int) network.DataPlaneBackend {
	nodeAddressFact, err := dataPlaneReconciler.factStore.Get(ctx, types.KeyObservedNodeAddress(remoteNodeID))
	if err != nil {
		logging.Default().Debug("remote backend: no advertise address for node", "node", remoteNodeID, "instance", instanceID)
		return network.DataPlaneBackend{}
	}
	nodeAddress := string(nodeAddressFact.Value)

	hostPort := resolveHostPort(ctx, dataPlaneReconciler.factStore, instanceID, containerPort)
	if hostPort <= 0 {
		logging.Default().Debug("remote backend: no host port for instance", "instance", instanceID, "node", remoteNodeID)
		return network.DataPlaneBackend{}
	}

	return network.DataPlaneBackend{Address: nodeAddress, Port: hostPort}
}

// PublishAdvertiseAddress writes this node's routable address to the
// store so other nodes' data plane providers can resolve cross-host
// backend addresses.
func (dataPlaneReconciler *DataPlaneReconciler) PublishAdvertiseAddress(ctx context.Context) {
	if dataPlaneReconciler.advertiseAddress == "" {
		return
	}
	if _, putError := dataPlaneReconciler.factStore.Put(ctx, types.KeyObservedNodeAddress(dataPlaneReconciler.nodeID), []byte(dataPlaneReconciler.advertiseAddress)); putError != nil {
		logging.Default().Error("failed to publish advertise address", "node", dataPlaneReconciler.nodeID, "error", putError.Error())
	}
}

// publishInstanceHostPorts writes per-container-port host port mappings to
// the store. Each containerPort gets its own key so the endpoint controller
// can resolve the correct host port for multi-port services.
func publishInstanceHostPorts(ctx context.Context, factStore store.StateStore, instanceID string, portMappings map[int]int) {
	for containerPort, hostPort := range portMappings {
		if hostPort <= 0 {
			continue
		}
		hostPortKey := types.KeyObservedInstanceHostPortForPort(instanceID, containerPort)
		if _, putError := factStore.Put(ctx, hostPortKey, []byte(strconv.Itoa(hostPort))); putError != nil {
			logging.Default().Error("failed to publish instance host port",
				"instance", instanceID, "containerPort", strconv.Itoa(containerPort), "error", putError.Error())
		}
	}
}
