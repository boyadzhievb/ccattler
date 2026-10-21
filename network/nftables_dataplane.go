// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/boyadzhievb/ccattler/logging"
)

// nftablesTableName is the nftables table that holds all CCattler rules.
const nftablesTableName = "cca"

// nftablesVIPDispatchMap is the verdict map that dispatches VIP:proto:port
// to per-service chains with O(1) hash lookup.
const nftablesVIPDispatchMap = "vip_dispatch"

// nftablesServiceChainPrefix is prepended to the sanitized service name
// to form per-service chain names (e.g. "svc_web").
const nftablesServiceChainPrefix = "svc_"

// nftablesRouteChainPrefix is prepended to the sanitized route name
// to form per-route chain names (e.g. "rt_web_public").
const nftablesRouteChainPrefix = "rt_"

// NftablesDataPlane programs nftables nat-table rules for VIP-based load
// balancing using hash set lookups. This replaces the iptables provider's
// linear chain walk with O(1) verdict map dispatching. Full kernel
// conntrack integration — DNAT, MASQUERADE, and return-path NAT all use
// standard nftables statements.
//
// Table layout:
//
//	table ip cca {
//	    chain prerouting { type nat hook prerouting priority dstnat; }
//	    chain output     { type nat hook output priority dstnat; }
//	    chain postrouting { type nat hook postrouting priority srcnat; masquerade }
//	    map vip_dispatch { type ipv4_addr . inet_proto . inet_service : verdict }
//	    chain svc_web { dnat to numgen inc mod N map { ... } }
//	}
type NftablesDataPlane struct {
	// mutex serializes reconciliation calls.
	mutex sync.Mutex
	// activeServiceChains tracks which per-service chains currently exist.
	activeServiceChains map[string]bool
	// activeRouteChains tracks which per-route chains currently exist.
	activeRouteChains map[string]bool
	// activeVIPAddresses tracks VIP addresses assigned to the dummy interface.
	activeVIPAddresses map[string]bool
	// cookieProxy manages per-route cookie-sticky reverse proxy listeners.
	cookieProxy *CookieStickyProxy
	// initialized tracks whether the nftables table has been created.
	initialized bool
}

// NewNftablesDataPlane creates a NftablesDataPlane ready to program
// nftables DNAT rules on the local host.
func NewNftablesDataPlane() *NftablesDataPlane {
	return &NftablesDataPlane{
		activeServiceChains: make(map[string]bool),
		activeRouteChains:   make(map[string]bool),
		activeVIPAddresses:  make(map[string]bool),
		cookieProxy:         NewCookieStickyProxy(),
	}
}

// ReconcileVIPDataPlane generates a complete nftables ruleset from the
// desired service configurations and applies it atomically. Stale chains
// and VIP addresses are removed.
func (nftablesDataPlane *NftablesDataPlane) ReconcileVIPDataPlane(ctx context.Context, serviceConfigs []ServiceVIPConfig) error {
	nftablesDataPlane.mutex.Lock()
	defer nftablesDataPlane.mutex.Unlock()

	if err := nftablesDataPlane.ensureDummyInterface(); err != nil {
		return fmt.Errorf("setting up dummy interface: %w", err)
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)

	ruleset := nftablesDataPlane.buildVIPRuleset(serviceConfigs, desiredServiceChains, desiredVIPAddresses)

	if err := applyNftablesRuleset(ruleset); err != nil {
		return fmt.Errorf("applying nftables ruleset: %w", err)
	}

	nftablesDataPlane.initialized = true

	for _, serviceConfig := range serviceConfigs {
		if err := nftablesDataPlane.ensureVIPAddress(serviceConfig.VirtualIP); err != nil {
			logging.Default().Error("failed to add VIP", "vip", serviceConfig.VirtualIP, "error", err.Error())
		}
	}

	nftablesDataPlane.removeStaleVIPAddresses(desiredVIPAddresses)
	nftablesDataPlane.activeServiceChains = desiredServiceChains

	return nil
}

// ReconcileRouteDataPlane programs nftables chains for route-based traffic
// distribution. Source-IP sticky routes use jhash. Cookie-sticky routes
// redirect to the userspace cookie proxy.
func (nftablesDataPlane *NftablesDataPlane) ReconcileRouteDataPlane(ctx context.Context, routeConfigs []RouteDataPlaneConfig) error {
	nftablesDataPlane.mutex.Lock()
	defer nftablesDataPlane.mutex.Unlock()

	nftablesDataPlane.cookieProxy.ReconcileRoutes(routeConfigs)

	desiredRouteChains := make(map[string]bool)
	ruleset := nftablesDataPlane.buildRouteRuleset(routeConfigs, desiredRouteChains)

	if ruleset != "" {
		if err := applyNftablesRuleset(ruleset); err != nil {
			return fmt.Errorf("applying route nftables ruleset: %w", err)
		}
	}

	nftablesDataPlane.activeRouteChains = desiredRouteChains
	return nil
}

// Cleanup removes all nftables state managed by CCattler: the entire cca
// table, dummy interface, VIP addresses, and cookie proxy listeners.
func (nftablesDataPlane *NftablesDataPlane) Cleanup(ctx context.Context) error {
	nftablesDataPlane.mutex.Lock()
	defer nftablesDataPlane.mutex.Unlock()

	_ = runNft("delete", "table", "ip", nftablesTableName)

	for vipAddress := range nftablesDataPlane.activeVIPAddresses {
		removeVIPAddressFromInterface(vipAddress)
	}

	deleteDummyInterface()
	nftablesDataPlane.cookieProxy.Cleanup()

	nftablesDataPlane.activeServiceChains = make(map[string]bool)
	nftablesDataPlane.activeRouteChains = make(map[string]bool)
	nftablesDataPlane.activeVIPAddresses = make(map[string]bool)
	nftablesDataPlane.initialized = false

	return nil
}

// buildVIPRuleset generates a complete nftables ruleset for the given
// service configurations. The ruleset is applied atomically with nft -f.
func (nftablesDataPlane *NftablesDataPlane) buildVIPRuleset(
	serviceConfigs []ServiceVIPConfig,
	desiredServiceChains map[string]bool,
	desiredVIPAddresses map[string]bool,
) string {
	var rulesetBuilder strings.Builder

	fmt.Fprintf(&rulesetBuilder, "table ip %s {}\n", nftablesTableName)
	fmt.Fprintf(&rulesetBuilder, "delete table ip %s\n", nftablesTableName)
	fmt.Fprintf(&rulesetBuilder, "table ip %s {\n", nftablesTableName)

	fmt.Fprintf(&rulesetBuilder, "    map %s {\n", nftablesVIPDispatchMap)
	rulesetBuilder.WriteString("        type ipv4_addr . inet_proto . inet_service : verdict\n")
	rulesetBuilder.WriteString("    }\n\n")

	rulesetBuilder.WriteString("    chain prerouting {\n")
	rulesetBuilder.WriteString("        type nat hook prerouting priority -100; policy accept;\n")
	fmt.Fprintf(&rulesetBuilder, "        ip daddr . ip protocol . th dport vmap @%s\n", nftablesVIPDispatchMap)
	rulesetBuilder.WriteString("    }\n\n")

	rulesetBuilder.WriteString("    chain output {\n")
	rulesetBuilder.WriteString("        type nat hook output priority -100; policy accept;\n")
	fmt.Fprintf(&rulesetBuilder, "        ip daddr . ip protocol . th dport vmap @%s\n", nftablesVIPDispatchMap)
	rulesetBuilder.WriteString("    }\n\n")

	writePostRoutingChain(&rulesetBuilder)

	var dispatchEntries []string

	sortedConfigs := make([]ServiceVIPConfig, len(serviceConfigs))
	copy(sortedConfigs, serviceConfigs)
	sort.Slice(sortedConfigs, func(i, j int) bool {
		return sortedConfigs[i].ServiceName < sortedConfigs[j].ServiceName
	})

	for _, serviceConfig := range sortedConfigs {
		desiredVIPAddresses[serviceConfig.VirtualIP] = true
		if len(serviceConfig.Backends) == 0 {
			continue
		}

		chainName := nftablesServiceChainPrefix + sanitizeNftablesChainName(serviceConfig.ServiceName)
		desiredServiceChains[chainName] = true

		protocol := serviceConfig.EffectiveProtocol()
		dispatchEntries = append(dispatchEntries,
			fmt.Sprintf("        %s . %s . %d : goto %s",
				serviceConfig.VirtualIP, protocol, serviceConfig.Port, chainName))

		fmt.Fprintf(&rulesetBuilder, "    chain %s {\n", chainName)
		writeBackendDNATRules(&rulesetBuilder, serviceConfig.Backends, protocol)
		rulesetBuilder.WriteString("    }\n\n")
	}

	writeVerdictMapWithElements(&rulesetBuilder, dispatchEntries)

	rulesetBuilder.WriteString("}\n")
	return rulesetBuilder.String()
}

// writePostRoutingChain writes the postrouting masquerade chain.
func writePostRoutingChain(rulesetBuilder *strings.Builder) {
	rulesetBuilder.WriteString("    chain postrouting {\n")
	rulesetBuilder.WriteString("        type nat hook postrouting priority 100; policy accept;\n")
	rulesetBuilder.WriteString("        ct status dnat masquerade\n")
	rulesetBuilder.WriteString("    }\n\n")
}

// writeVerdictMapWithElements writes the populated verdict map when there
// are dispatch entries to include.
func writeVerdictMapWithElements(rulesetBuilder *strings.Builder, dispatchEntries []string) {
	if len(dispatchEntries) == 0 {
		return
	}
	fmt.Fprintf(rulesetBuilder, "    map %s {\n", nftablesVIPDispatchMap)
	rulesetBuilder.WriteString("        type ipv4_addr . inet_proto . inet_service : verdict\n")
	rulesetBuilder.WriteString("        elements = {\n")
	for entryIndex, entry := range dispatchEntries {
		if entryIndex < len(dispatchEntries)-1 {
			rulesetBuilder.WriteString(entry + ",\n")
		} else {
			rulesetBuilder.WriteString(entry + "\n")
		}
	}
	rulesetBuilder.WriteString("        }\n")
	rulesetBuilder.WriteString("    }\n\n")
}

// writeBackendDNATRules writes nftables DNAT rules for the given backends
// using numgen round-robin into the provided builder. Includes an explicit
// transport protocol match so nftables accepts the :port syntax — older
// nftables (e.g. 1.0.2 on Ubuntu 22.04) requires this context.
func writeBackendDNATRules(rulesetBuilder *strings.Builder, backends []DataPlaneBackend, protocol string) {
	if len(backends) == 1 {
		fmt.Fprintf(rulesetBuilder, "        meta l4proto %s dnat to %s:%d\n",
			protocol, backends[0].Address, backends[0].Port)
		return
	}

	fmt.Fprintf(rulesetBuilder, "        meta l4proto %s dnat to numgen inc mod %d map {\n", protocol, len(backends))
	for backendIndex, backend := range backends {
		separator := ","
		if backendIndex == len(backends)-1 {
			separator = ""
		}
		fmt.Fprintf(rulesetBuilder, "            %d : %s . %d%s\n",
			backendIndex, backend.Address, backend.Port, separator)
	}
	rulesetBuilder.WriteString("        }\n")
}

// buildRouteRuleset generates nftables rules for route-specific chains.
func (nftablesDataPlane *NftablesDataPlane) buildRouteRuleset(
	routeConfigs []RouteDataPlaneConfig,
	desiredRouteChains map[string]bool,
) string {
	if len(routeConfigs) == 0 {
		return ""
	}

	var rulesetBuilder strings.Builder

	sortedRoutes := make([]RouteDataPlaneConfig, len(routeConfigs))
	copy(sortedRoutes, routeConfigs)
	sort.Slice(sortedRoutes, func(i, j int) bool {
		return sortedRoutes[i].RouteName < sortedRoutes[j].RouteName
	})

	for _, routeConfig := range sortedRoutes {
		if len(routeConfig.Backends) == 0 || routeConfig.VirtualIP == "" {
			continue
		}

		switch routeConfig.StickyMethod {
		case "source-ip":
			chainName := nftablesRouteChainPrefix + sanitizeNftablesChainName(routeConfig.RouteName)
			desiredRouteChains[chainName] = true
			writeSourceIPStickyChain(&rulesetBuilder, routeConfig, chainName)

		case "cookie":
			chainName := nftablesRouteChainPrefix + sanitizeNftablesChainName(routeConfig.RouteName)
			desiredRouteChains[chainName] = true
			proxyPort := nftablesDataPlane.cookieProxy.ListenPort(routeConfig.RouteName)
			if proxyPort > 0 {
				writeCookieProxyChain(&rulesetBuilder, chainName, proxyPort)
			}
		}
	}

	return rulesetBuilder.String()
}

// writeSourceIPStickyChain writes an nftables chain that uses jhash on
// the source IP for consistent backend selection.
func writeSourceIPStickyChain(rulesetBuilder *strings.Builder, routeConfig RouteDataPlaneConfig, chainName string) {
	fmt.Fprintf(rulesetBuilder, "    chain %s {\n", chainName)

	if len(routeConfig.Backends) == 1 {
		fmt.Fprintf(rulesetBuilder, "        meta l4proto tcp dnat to %s:%d\n",
			routeConfig.Backends[0].Address, routeConfig.Backends[0].Port)
	} else {
		fmt.Fprintf(rulesetBuilder, "        meta l4proto tcp dnat to jhash ip saddr mod %d map {\n", len(routeConfig.Backends))
		for backendIndex, backend := range routeConfig.Backends {
			separator := ","
			if backendIndex == len(routeConfig.Backends)-1 {
				separator = ""
			}
			fmt.Fprintf(rulesetBuilder, "            %d : %s . %d%s\n",
				backendIndex, backend.Address, backend.Port, separator)
		}
		rulesetBuilder.WriteString("        }\n")
	}

	rulesetBuilder.WriteString("    }\n\n")
}

// writeCookieProxyChain writes an nftables chain that redirects traffic
// to the local cookie proxy port.
func writeCookieProxyChain(rulesetBuilder *strings.Builder, chainName string, proxyPort int) {
	fmt.Fprintf(rulesetBuilder, "    chain %s {\n", chainName)
	fmt.Fprintf(rulesetBuilder, "        redirect to :%d\n", proxyPort)
	rulesetBuilder.WriteString("    }\n\n")
}

// sanitizeNftablesChainName replaces characters that are invalid in nftables
// chain names with underscores. Unlike iptables, nftables treats hyphens as
// the subtraction operator, so only letters, digits, and underscores are safe.
func sanitizeNftablesChainName(serviceName string) string {
	var sanitized strings.Builder
	for _, character := range serviceName {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' {
			sanitized.WriteRune(character)
		} else {
			sanitized.WriteRune('_')
		}
	}
	return sanitized.String()
}

// ensureDummyInterface creates the cca0 dummy interface and brings it up.
func (nftablesDataPlane *NftablesDataPlane) ensureDummyInterface() error {
	if err := runCommandIdempotent("ip", "link", "add", vipDummyInterface, "type", "dummy"); err != nil {
		return fmt.Errorf("creating dummy interface %s: %w", vipDummyInterface, err)
	}
	if err := runCommandStrict("ip", "link", "set", vipDummyInterface, "up"); err != nil {
		return fmt.Errorf("bringing up %s: %w", vipDummyInterface, err)
	}
	return nil
}

// ensureVIPAddress assigns a /32 VIP address to the cca0 dummy interface.
func (nftablesDataPlane *NftablesDataPlane) ensureVIPAddress(virtualIP string) error {
	if nftablesDataPlane.activeVIPAddresses[virtualIP] {
		return nil
	}
	if err := runCommandIdempotent("ip", "addr", "add", virtualIP+"/32", "dev", vipDummyInterface); err != nil {
		return fmt.Errorf("adding VIP %s to %s: %w", virtualIP, vipDummyInterface, err)
	}
	nftablesDataPlane.activeVIPAddresses[virtualIP] = true
	return nil
}

// removeStaleVIPAddresses removes VIP addresses from the dummy interface
// that are no longer assigned to any service.
func (nftablesDataPlane *NftablesDataPlane) removeStaleVIPAddresses(desiredAddresses map[string]bool) {
	for vipAddress := range nftablesDataPlane.activeVIPAddresses {
		if !desiredAddresses[vipAddress] {
			removeVIPAddressFromInterface(vipAddress)
			delete(nftablesDataPlane.activeVIPAddresses, vipAddress)
			logging.Default().Info("removed stale VIP", "vip", vipAddress)
		}
	}
}

// applyNftablesRuleset writes the ruleset to a temp file and applies it
// atomically with nft -f. The entire ruleset either succeeds or fails
// as one transaction.
func applyNftablesRuleset(ruleset string) error {
	tempFile, err := os.CreateTemp("", "cca-nft-*.conf")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tempFilePath := tempFile.Name()
	defer func() { _ = os.Remove(tempFilePath) }()

	if _, writeErr := tempFile.WriteString(ruleset); writeErr != nil {
		_ = tempFile.Close()
		return fmt.Errorf("writing ruleset: %w", writeErr)
	}
	if closeErr := tempFile.Close(); closeErr != nil {
		return fmt.Errorf("closing ruleset file: %w", closeErr)
	}

	output, applyErr := exec.Command("nft", "-f", tempFilePath).CombinedOutput() //nolint:gosec // nft path comes from validated temp file
	if applyErr != nil {
		return fmt.Errorf("nft -f failed: %w\nruleset:\n%s\noutput: %s", applyErr, ruleset, string(output))
	}

	return nil
}

// runNft executes an nft command and returns any error.
func runNft(nftArgs ...string) error {
	output, err := exec.Command("nft", nftArgs...).CombinedOutput() //nolint:gosec // nft commands with validated arguments
	if err != nil {
		return fmt.Errorf("nft %s: %w (output: %s)", strings.Join(nftArgs, " "), err, string(output))
	}
	return nil
}

// CheckNftablesAvailable probes whether the system supports nftables
// by attempting to list tables. Returns nil if nftables is available.
func CheckNftablesAvailable() error {
	_, err := exec.LookPath("nft")
	if err != nil {
		return fmt.Errorf("nft binary not found: %w", err)
	}
	output, runErr := exec.Command("nft", "list", "tables").CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("nftables not supported by kernel: %w (output: %s)", runErr, string(output))
	}
	return nil
}
