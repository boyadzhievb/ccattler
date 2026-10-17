// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"sort"
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
// plane provider to program iptables DNAT rules. Also reads route facts
// and programs route-specific forwarding (sticky sessions). For instances
// running on this node, the backend is the container IP:port (reachable
// via Docker bridge). For instances on other nodes, the backend is the
// remote node's advertise address + host port (reachable via LAN).
func (dataPlaneReconciler *DataPlaneReconciler) Reconcile(ctx context.Context) {
	if dataPlaneReconciler.dataPlaneProvider == nil {
		return
	}

	serviceVIPConfigs, err := dataPlaneReconciler.buildServiceVIPConfigs(ctx)
	if err != nil {
		logging.Default().Error("dataplane: failed to build VIP configs", "agent", dataPlaneReconciler.nodeID, "error", err.Error())
		return
	}

	if vipReconcileError := dataPlaneReconciler.dataPlaneProvider.ReconcileVIPDataPlane(ctx, serviceVIPConfigs); vipReconcileError != nil {
		logging.Default().Error("dataplane: reconcile error", "agent", dataPlaneReconciler.nodeID, "error", vipReconcileError.Error())
	}

	routeConfigs, err := dataPlaneReconciler.buildRouteDataPlaneConfigs(ctx, serviceVIPConfigs)
	if err != nil {
		logging.Default().Error("dataplane: failed to build route configs", "agent", dataPlaneReconciler.nodeID, "error", err.Error())
		return
	}

	if len(routeConfigs) > 0 {
		if err := dataPlaneReconciler.dataPlaneProvider.ReconcileRouteDataPlane(ctx, routeConfigs); err != nil {
			logging.Default().Error("dataplane: route reconcile error", "agent", dataPlaneReconciler.nodeID, "error", err.Error())
		}
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

// buildRouteDataPlaneConfigs reads desired route facts and derived backend
// facts to construct route configurations for the data plane. It resolves
// each route's target service to its VIP (from the already-built VIP configs)
// and parses sticky session settings.
func (dataPlaneReconciler *DataPlaneReconciler) buildRouteDataPlaneConfigs(ctx context.Context, serviceVIPConfigs []network.ServiceVIPConfig) ([]network.RouteDataPlaneConfig, error) {
	desiredRouteFacts, err := dataPlaneReconciler.factStore.Scan(ctx, types.ScanDesiredRoutes)
	if err != nil {
		return nil, fmt.Errorf("scanning desired routes: %w", err)
	}
	if len(desiredRouteFacts) == 0 {
		return nil, nil
	}

	serviceVIPLookup := make(map[string]network.ServiceVIPConfig, len(serviceVIPConfigs))
	for _, vipConfig := range serviceVIPConfigs {
		serviceVIPLookup[vipConfig.ServiceName] = vipConfig
	}

	derivedRouteFacts, err := dataPlaneReconciler.factStore.Scan(ctx, types.ScanDerivedRoutes)
	if err != nil {
		return nil, fmt.Errorf("scanning derived routes: %w", err)
	}

	parsedRoutes := parseRouteFactsForDataPlane(desiredRouteFacts, derivedRouteFacts)

	var routeConfigs []network.RouteDataPlaneConfig
	for _, parsedRoute := range parsedRoutes {
		vipConfig, hasVIP := serviceVIPLookup[parsedRoute.serviceName]
		if !hasVIP || vipConfig.VirtualIP == "" {
			continue
		}
		routeConfig := buildRouteConfigFromEntry(parsedRoute, vipConfig)
		routeConfigs = append(routeConfigs, routeConfig)
	}

	return routeConfigs, nil
}

// buildRouteConfigFromEntry converts a parsed route entry and its service's VIP
// config into a RouteDataPlaneConfig with resolved backends and cookie settings.
func buildRouteConfigFromEntry(parsedRoute dataPlaneRouteEntry, vipConfig network.ServiceVIPConfig) network.RouteDataPlaneConfig {
	routePort := parsedRoute.servicePort
	if routePort == 0 {
		routePort = vipConfig.Port
	}

	routeConfig := network.RouteDataPlaneConfig{
		RouteName:    parsedRoute.routeName,
		ServiceName:  parsedRoute.serviceName,
		VirtualIP:    vipConfig.VirtualIP,
		Port:         routePort,
		StickyMethod: parsedRoute.stickyMethod,
		Backends:     parseBackendAddressList(parsedRoute.backendAddresses),
	}

	if parsedRoute.stickyMethod == "cookie" {
		routeConfig.CookieConfig = &network.RouteCookieConfig{
			Name:     parsedRoute.cookieName,
			Secure:   parsedRoute.cookieSecure,
			HTTPOnly: parsedRoute.cookieHTTPOnly,
			SameSite: parsedRoute.cookieSameSite,
		}
	}

	return routeConfig
}

// parseBackendAddressList converts a list of "host:port" strings into
// DataPlaneBackend structs, skipping malformed entries.
func parseBackendAddressList(addresses []string) []network.DataPlaneBackend {
	var backends []network.DataPlaneBackend
	for _, backendAddress := range addresses {
		colonIndex := strings.LastIndex(backendAddress, ":")
		if colonIndex < 0 {
			continue
		}
		backendIP := backendAddress[:colonIndex]
		backendPort, _ := strconv.Atoi(backendAddress[colonIndex+1:])
		if backendPort <= 0 {
			continue
		}
		backends = append(backends, network.DataPlaneBackend{
			Address: backendIP,
			Port:    backendPort,
		})
	}
	return backends
}

// dataPlaneRouteEntry holds the parsed fields from desired + derived route facts.
type dataPlaneRouteEntry struct {
	routeName        string
	serviceName      string
	servicePort      int
	stickyMethod     string
	cookieName       string
	cookieSecure     bool
	cookieHTTPOnly   bool
	cookieSameSite   string
	backendAddresses []string
}

// parseRouteFactsForDataPlane extracts route definitions from desired and
// derived route facts into structured entries for data plane programming.
func parseRouteFactsForDataPlane(desiredFacts []store.Fact, derivedFacts []store.Fact) []dataPlaneRouteEntry {
	routeEntries := make(map[string]*dataPlaneRouteEntry)

	for _, fact := range desiredFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredRoutes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		routeName := pathParts[0]

		if routeEntries[routeName] == nil {
			routeEntries[routeName] = &dataPlaneRouteEntry{routeName: routeName}
		}
		entry := routeEntries[routeName]

		if len(pathParts) == 1 {
			continue
		}

		fieldPath := pathParts[1]
		fieldValue := string(fact.Value)
		switch fieldPath {
		case "service":
			entry.serviceName = fieldValue
		case "port":
			entry.servicePort, _ = strconv.Atoi(fieldValue)
		case "sticky":
			entry.stickyMethod = fieldValue
		case "cookie/name":
			entry.cookieName = fieldValue
		case "cookie/secure":
			entry.cookieSecure = fieldValue == "true"
		case "cookie/httponly":
			entry.cookieHTTPOnly = fieldValue == "true"
		case "cookie/samesite":
			entry.cookieSameSite = fieldValue
		}
	}

	for _, fact := range derivedFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedRoutes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		routeName := pathParts[0]

		if routeEntries[routeName] == nil {
			continue
		}

		if len(pathParts) == 2 && pathParts[1] == "backends" {
			backendsValue := string(fact.Value)
			if backendsValue != "" {
				routeEntries[routeName].backendAddresses = strings.Split(backendsValue, ",")
			}
		}
	}

	var sortedEntries []dataPlaneRouteEntry
	sortedRouteNames := make([]string, 0, len(routeEntries))
	for routeName := range routeEntries {
		sortedRouteNames = append(sortedRouteNames, routeName)
	}
	sort.Strings(sortedRouteNames)

	for _, routeName := range sortedRouteNames {
		entry := routeEntries[routeName]
		if entry.serviceName != "" {
			sortedEntries = append(sortedEntries, *entry)
		}
	}

	return sortedEntries
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
			// Local backends use the container bridge IP directly,
			// looked up from the observed instance IP fact. This avoids
			// hairpin NAT (DNAT to the node's own advertise address
			// routes through loopback where MASQUERADE is excluded).
			localBackend := dataPlaneReconciler.resolveLocalBackend(ctx, instanceID, containerPort, endpointIP, endpointPort)
			backends = append(backends, localBackend)
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

// resolveLocalBackend returns a backend for an instance on this node. It
// reads the container's bridge IP from the observed instance IP fact so that
// DNAT targets the container directly via the bridge, avoiding hairpin NAT.
// Falls back to the endpoint address if the container IP is not available.
func (dataPlaneReconciler *DataPlaneReconciler) resolveLocalBackend(ctx context.Context, instanceID string, containerPort int, fallbackIP string, fallbackPort int) network.DataPlaneBackend {
	containerIPFact, err := dataPlaneReconciler.factStore.Get(ctx, types.KeyObservedInstanceIP(instanceID))
	if err == nil && len(containerIPFact.Value) > 0 {
		backendPort := containerPort
		if backendPort <= 0 {
			backendPort = fallbackPort
		}
		return network.DataPlaneBackend{
			Address: string(containerIPFact.Value),
			Port:    backendPort,
		}
	}
	return network.DataPlaneBackend{
		Address: fallbackIP,
		Port:    fallbackPort,
	}
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
