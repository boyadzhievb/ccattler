// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// RouteController watches desired route definitions and endpoint facts, then
// writes derived backend lists for each route. The agent data plane reads
// these derived facts to program iptables DNAT or start sticky session proxies.
type RouteController struct{}

// NewRouteController returns a ready-to-use RouteController.
func NewRouteController() *RouteController { return &RouteController{} }

// Name returns "route", identifying this controller in logs and runner bookkeeping.
func (routeController *RouteController) Name() string { return "route" }

// Watch returns the fact prefixes the route controller monitors: desired route
// definitions (for target service and sticky config), endpoints (for backend
// addresses), and existing derived route facts (for staleness detection).
func (routeController *RouteController) Watch() []string {
	return []string{
		types.ScanDesiredRoutes,
		types.ScanEndpoints,
		types.ScanDerivedRoutes,
	}
}

// Reconcile examines desired route facts and endpoint facts, resolves each
// route's target service to its healthy backend addresses, and emits changes
// to create or update derived backend lists and remove stale ones.
func (routeController *RouteController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredRoutes := parseDesiredRoutes(facts)
	serviceBackends := collectServiceBackendsFromEndpoints(facts)
	existingDerivedRouteKeys := parseExistingDerivedRouteKeys(facts)

	return computeRouteDiffChanges(desiredRoutes, serviceBackends, existingDerivedRouteKeys), nil
}

// parsedRoute holds the fields extracted from desired route facts.
type parsedRoute struct {
	serviceName string // target service name
	servicePort int    // target service port (0 = use endpoint port)
}

// parseDesiredRoutes scans desired/route/ facts and returns a map from route
// name to its parsed configuration.
func parseDesiredRoutes(facts []store.Fact) map[string]parsedRoute {
	routeServiceNames := make(map[string]string)
	routeServicePorts := make(map[string]int)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredRoutes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredRoutes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		routeName := pathParts[0]

		if len(pathParts) == 1 {
			continue // root marker fact
		}

		fieldName := pathParts[1]
		switch fieldName {
		case "service":
			routeServiceNames[routeName] = string(fact.Value)
		case "port":
			routeServicePorts[routeName] = parsePortValue(string(fact.Value))
		}
	}

	desiredRoutes := make(map[string]parsedRoute, len(routeServiceNames))
	for routeName, serviceName := range routeServiceNames {
		desiredRoutes[routeName] = parsedRoute{
			serviceName: serviceName,
			servicePort: routeServicePorts[routeName],
		}
	}
	return desiredRoutes
}

// parsePortValue converts a string port value to an integer, returning 0 on failure.
func parsePortValue(portString string) int {
	portValue := 0
	for _, digit := range portString {
		if digit < '0' || digit > '9' {
			return 0
		}
		portValue = portValue*10 + int(digit-'0')
	}
	return portValue
}

// collectServiceBackendsFromEndpoints scans endpoint facts and returns a map
// from service name to a sorted list of backend addresses (host:port).
func collectServiceBackendsFromEndpoints(facts []store.Fact) map[string][]string {
	serviceBackendSet := make(map[string]map[string]bool)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
		// after trimming ScanEndpoints (endpoint/service/), relative is {serviceName}/{instanceID}/{port}
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) < 3 {
			continue
		}
		serviceName := pathParts[0]
		backendAddress := string(fact.Value)
		if backendAddress == "" {
			continue
		}

		if serviceBackendSet[serviceName] == nil {
			serviceBackendSet[serviceName] = make(map[string]bool)
		}
		serviceBackendSet[serviceName][backendAddress] = true
	}

	serviceBackends := make(map[string][]string, len(serviceBackendSet))
	for serviceName, addressSet := range serviceBackendSet {
		sortedAddresses := make([]string, 0, len(addressSet))
		for address := range addressSet {
			sortedAddresses = append(sortedAddresses, address)
		}
		sort.Strings(sortedAddresses)
		serviceBackends[serviceName] = sortedAddresses
	}
	return serviceBackends
}

// parseExistingDerivedRouteKeys scans derived/route/ facts and returns a map
// from full key to current value for staleness detection.
func parseExistingDerivedRouteKeys(facts []store.Fact) map[string]string {
	existingKeys := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedRoutes) {
		existingKeys[fact.Key] = string(fact.Value)
	}
	return existingKeys
}

// computeRouteDiffChanges compares desired routes against existing derived facts
// and returns changes to create/update backend lists and remove stale entries.
func computeRouteDiffChanges(
	desiredRoutes map[string]parsedRoute,
	serviceBackends map[string][]string,
	existingDerivedRouteKeys map[string]string,
) []Change {
	var changes []Change
	desiredDerivedKeys := make(map[string]bool)

	sortedRouteNames := make([]string, 0, len(desiredRoutes))
	for routeName := range desiredRoutes {
		sortedRouteNames = append(sortedRouteNames, routeName)
	}
	sort.Strings(sortedRouteNames)

	for _, routeName := range sortedRouteNames {
		routeConfig := desiredRoutes[routeName]
		backends := serviceBackends[routeConfig.serviceName]
		filteredBackends := filterBackendsByPort(backends, routeConfig.servicePort)
		backendsValue := strings.Join(filteredBackends, ",")

		backendsKey := types.KeyDerivedRouteBackends(routeName)
		desiredDerivedKeys[backendsKey] = true

		if existingValue, exists := existingDerivedRouteKeys[backendsKey]; !exists || existingValue != backendsValue {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   backendsKey,
				Value: []byte(backendsValue),
			})
		}
	}

	sortedExistingKeys := make([]string, 0, len(existingDerivedRouteKeys))
	for existingKey := range existingDerivedRouteKeys {
		sortedExistingKeys = append(sortedExistingKeys, existingKey)
	}
	sort.Strings(sortedExistingKeys)

	for _, existingKey := range sortedExistingKeys {
		if !desiredDerivedKeys[existingKey] {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  existingKey,
			})
		}
	}

	return changes
}

// filterBackendsByPort returns backends that match the route's target port.
// If targetPort is 0, all backends are returned (port not specified in route).
func filterBackendsByPort(backends []string, targetPort int) []string {
	if targetPort == 0 {
		return backends
	}

	targetSuffix := fmt.Sprintf(":%d", targetPort)
	var filteredBackends []string
	for _, backend := range backends {
		if strings.HasSuffix(backend, targetSuffix) {
			filteredBackends = append(filteredBackends, backend)
		}
	}
	return filteredBackends
}
