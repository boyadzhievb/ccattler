package network

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// iptablesMainChain is the top-level CCattler chain in the nat table.
// All VIP DNAT rules live in per-service sub-chains jumped to from here.
const iptablesMainChain = "CCA_SERVICES"

// iptablesServiceChainPrefix is prepended to the service name to form
// the per-service iptables chain name (e.g. "CCA_SVC_web").
const iptablesServiceChainPrefix = "CCA_SVC_"

// vipDummyInterface is the name of the dummy network interface used to
// assign VIP addresses so the kernel accepts packets destined for them.
const vipDummyInterface = "cca0"

// IptablesDataPlane programs iptables nat-table DNAT rules for VIP-based
// load balancing, analogous to kube-proxy in iptables mode. For each
// service VIP it creates a per-service chain with round-robin DNAT rules
// using the iptables statistic module (--mode nth). VIP addresses are
// assigned to a dummy interface (cca0) so the kernel accepts the packets.
//
// Chain layout:
//
//	PREROUTING → CCA_SERVICES → CCA_SVC_web → DNAT to backends
//	OUTPUT     → CCA_SERVICES → CCA_SVC_web → DNAT to backends
type IptablesDataPlane struct {
	// mutex serializes reconciliation calls.
	mutex sync.Mutex
	// activeServiceChains tracks which per-service chains currently exist
	// in iptables, so stale chains can be detected and removed.
	activeServiceChains map[string]bool
	// activeVIPAddresses tracks which VIP addresses are currently assigned
	// to the dummy interface, so stale addresses can be removed.
	activeVIPAddresses map[string]bool
	// initialized tracks whether the global chain and jump rules have been set up.
	initialized bool
}

// NewIptablesDataPlane creates an IptablesDataPlane ready to program
// iptables DNAT rules on the local host.
func NewIptablesDataPlane() *IptablesDataPlane {
	return &IptablesDataPlane{
		activeServiceChains: make(map[string]bool),
		activeVIPAddresses:  make(map[string]bool),
	}
}

// ReconcileVIPDataPlane ensures iptables rules and VIP addresses match the
// desired service configurations. Stale chains and addresses are removed.
// The method is idempotent — calling with the same input twice is safe.
func (iptablesDataPlane *IptablesDataPlane) ReconcileVIPDataPlane(ctx context.Context, serviceConfigs []ServiceVIPConfig) error {
	iptablesDataPlane.mutex.Lock()
	defer iptablesDataPlane.mutex.Unlock()

	if err := iptablesDataPlane.ensureGlobalChainAndJumpRules(); err != nil {
		return fmt.Errorf("setting up global chain: %w", err)
	}

	if err := iptablesDataPlane.ensureDummyInterface(); err != nil {
		return fmt.Errorf("setting up dummy interface: %w", err)
	}

	desiredServiceChains := make(map[string]bool)
	desiredVIPAddresses := make(map[string]bool)

	for _, serviceConfig := range serviceConfigs {
		if len(serviceConfig.Backends) == 0 {
			continue
		}

		chainName := iptablesServiceChainPrefix + sanitizeChainName(serviceConfig.ServiceName)
		desiredServiceChains[chainName] = true
		desiredVIPAddresses[serviceConfig.VirtualIP] = true

		if err := iptablesDataPlane.reconcileServiceChain(serviceConfig, chainName); err != nil {
			log.Printf("dataplane: failed to reconcile chain %s: %v", chainName, err)
			continue
		}

		if err := iptablesDataPlane.ensureVIPAddress(serviceConfig.VirtualIP); err != nil {
			log.Printf("dataplane: failed to add VIP %s: %v", serviceConfig.VirtualIP, err)
		}
	}

	iptablesDataPlane.removeStaleServiceChains(desiredServiceChains)
	iptablesDataPlane.removeStaleVIPAddresses(desiredVIPAddresses)

	return nil
}

// Cleanup removes all CCattler-managed iptables chains, rules, and the
// dummy interface. Safe to call even if nothing was set up.
func (iptablesDataPlane *IptablesDataPlane) Cleanup(_ context.Context) error {
	iptablesDataPlane.mutex.Lock()
	defer iptablesDataPlane.mutex.Unlock()

	for chainName := range iptablesDataPlane.activeServiceChains {
		removeJumpRuleFromMainChain(chainName)
		flushAndDeleteChain(chainName)
	}
	iptablesDataPlane.activeServiceChains = make(map[string]bool)

	removeJumpFromBuiltinChain("PREROUTING", iptablesMainChain)
	removeJumpFromBuiltinChain("OUTPUT", iptablesMainChain)
	flushAndDeleteChain(iptablesMainChain)

	for vipAddress := range iptablesDataPlane.activeVIPAddresses {
		removeVIPAddressFromInterface(vipAddress)
	}
	iptablesDataPlane.activeVIPAddresses = make(map[string]bool)

	deleteDummyInterface()
	iptablesDataPlane.initialized = false
	return nil
}

// ensureGlobalChainAndJumpRules creates the CCA_SERVICES chain in the nat
// table and inserts jump rules from PREROUTING and OUTPUT. Idempotent.
func (iptablesDataPlane *IptablesDataPlane) ensureGlobalChainAndJumpRules() error {
	if iptablesDataPlane.initialized {
		return nil
	}

	runIptables("-t", "nat", "-N", iptablesMainChain)

	if err := ensureJumpToChain("PREROUTING", iptablesMainChain); err != nil {
		return err
	}
	if err := ensureJumpToChain("OUTPUT", iptablesMainChain); err != nil {
		return err
	}

	iptablesDataPlane.initialized = true
	return nil
}

// ensureDummyInterface creates the cca0 dummy interface and brings it up.
func (iptablesDataPlane *IptablesDataPlane) ensureDummyInterface() error {
	runCommand("ip", "link", "add", vipDummyInterface, "type", "dummy")
	if err := runCommandStrict("ip", "link", "set", vipDummyInterface, "up"); err != nil {
		return fmt.Errorf("bringing up %s: %w", vipDummyInterface, err)
	}
	return nil
}

// ensureVIPAddress adds a /32 VIP address to the dummy interface. Idempotent.
func (iptablesDataPlane *IptablesDataPlane) ensureVIPAddress(virtualIP string) error {
	runCommand("ip", "addr", "add", virtualIP+"/32", "dev", vipDummyInterface)
	iptablesDataPlane.activeVIPAddresses[virtualIP] = true
	return nil
}

// reconcileServiceChain creates or updates the per-service iptables chain
// with round-robin DNAT rules for the given backends. The chain is flushed
// and rebuilt on every call to ensure convergence.
func (iptablesDataPlane *IptablesDataPlane) reconcileServiceChain(serviceConfig ServiceVIPConfig, chainName string) error {
	runIptables("-t", "nat", "-N", chainName)

	if err := runIptablesStrict("-t", "nat", "-F", chainName); err != nil {
		return fmt.Errorf("flushing chain %s: %w", chainName, err)
	}

	backendCount := len(serviceConfig.Backends)
	for backendIndex, backend := range serviceConfig.Backends {
		destination := fmt.Sprintf("%s:%d", backend.Address, backend.Port)
		remainingBackends := backendCount - backendIndex

		if remainingBackends > 1 {
			if err := runIptablesStrict("-t", "nat", "-A", chainName,
				"-m", "statistic", "--mode", "nth",
				"--every", fmt.Sprintf("%d", remainingBackends), "--packet", "0",
				"-j", "DNAT", "--to-destination", destination); err != nil {
				return fmt.Errorf("adding DNAT rule for %s: %w", destination, err)
			}
		} else {
			if err := runIptablesStrict("-t", "nat", "-A", chainName,
				"-j", "DNAT", "--to-destination", destination); err != nil {
				return fmt.Errorf("adding final DNAT rule for %s: %w", destination, err)
			}
		}
	}

	if err := ensureJumpFromMainToService(serviceConfig.VirtualIP, serviceConfig.Port, chainName); err != nil {
		return fmt.Errorf("adding jump to %s: %w", chainName, err)
	}

	iptablesDataPlane.activeServiceChains[chainName] = true
	return nil
}

// removeStaleServiceChains removes per-service chains that are no longer needed.
func (iptablesDataPlane *IptablesDataPlane) removeStaleServiceChains(desiredChains map[string]bool) {
	for chainName := range iptablesDataPlane.activeServiceChains {
		if !desiredChains[chainName] {
			removeJumpRuleFromMainChain(chainName)
			flushAndDeleteChain(chainName)
			delete(iptablesDataPlane.activeServiceChains, chainName)
			log.Printf("dataplane: removed stale chain %s", chainName)
		}
	}
}

// removeStaleVIPAddresses removes VIP addresses from the dummy interface
// that are no longer assigned to any service.
func (iptablesDataPlane *IptablesDataPlane) removeStaleVIPAddresses(desiredAddresses map[string]bool) {
	for vipAddress := range iptablesDataPlane.activeVIPAddresses {
		if !desiredAddresses[vipAddress] {
			removeVIPAddressFromInterface(vipAddress)
			delete(iptablesDataPlane.activeVIPAddresses, vipAddress)
			log.Printf("dataplane: removed stale VIP %s", vipAddress)
		}
	}
}

// ensureJumpToChain inserts a jump rule from a builtin chain (PREROUTING or
// OUTPUT) to the target chain if one does not already exist.
func ensureJumpToChain(builtinChain string, targetChain string) error {
	checkResult := runIptables("-t", "nat", "-C", builtinChain, "-j", targetChain)
	if checkResult == nil {
		return nil
	}
	return runIptablesStrict("-t", "nat", "-I", builtinChain, "1", "-j", targetChain)
}

// ensureJumpFromMainToService adds a rule in CCA_SERVICES that jumps to
// the per-service chain when the destination matches the VIP:port. Uses
// -C to check first so duplicates are not created.
func ensureJumpFromMainToService(virtualIP string, port int, serviceChain string) error {
	ruleArgs := []string{"-t", "nat", "-C", iptablesMainChain,
		"-d", virtualIP + "/32", "-p", "tcp", "--dport", fmt.Sprintf("%d", port),
		"-j", serviceChain}
	if runIptables(ruleArgs...) == nil {
		return nil
	}
	insertArgs := []string{"-t", "nat", "-A", iptablesMainChain,
		"-d", virtualIP + "/32", "-p", "tcp", "--dport", fmt.Sprintf("%d", port),
		"-j", serviceChain}
	return runIptablesStrict(insertArgs...)
}

// removeJumpRuleFromMainChain removes all rules in CCA_SERVICES that jump
// to the given per-service chain.
func removeJumpRuleFromMainChain(serviceChain string) {
	for attempt := 0; attempt < 20; attempt++ {
		output, err := exec.Command("iptables", "-t", "nat", "-S", iptablesMainChain).CombinedOutput()
		if err != nil {
			return
		}
		found := false
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, serviceChain) {
				ruleSpec := strings.TrimPrefix(line, "-A "+iptablesMainChain+" ")
				deleteArgs := append([]string{"-t", "nat", "-D", iptablesMainChain}, strings.Fields(ruleSpec)...)
				runIptables(deleteArgs...)
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
}

// removeJumpFromBuiltinChain removes a jump rule from a builtin chain.
func removeJumpFromBuiltinChain(builtinChain string, targetChain string) {
	runIptables("-t", "nat", "-D", builtinChain, "-j", targetChain)
}

// flushAndDeleteChain flushes and deletes an iptables chain from the nat table.
func flushAndDeleteChain(chainName string) {
	runIptables("-t", "nat", "-F", chainName)
	runIptables("-t", "nat", "-X", chainName)
}

// removeVIPAddressFromInterface removes a /32 VIP address from the dummy interface.
func removeVIPAddressFromInterface(virtualIP string) {
	runCommand("ip", "addr", "del", virtualIP+"/32", "dev", vipDummyInterface)
}

// deleteDummyInterface removes the cca0 dummy network interface.
func deleteDummyInterface() {
	runCommand("ip", "link", "del", vipDummyInterface)
}

// sanitizeChainName replaces characters that are invalid in iptables chain
// names with underscores. Chain names may contain letters, digits, hyphens,
// and underscores.
func sanitizeChainName(serviceName string) string {
	var sanitized strings.Builder
	for _, character := range serviceName {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			sanitized.WriteRune(character)
		} else {
			sanitized.WriteRune('_')
		}
	}
	return sanitized.String()
}

// runIptables executes an iptables command, ignoring errors (used for
// idempotent operations like -N on an existing chain). Returns the error
// for callers that need to check existence via -C.
func runIptables(arguments ...string) error {
	return exec.Command("iptables", arguments...).Run()
}

// runIptablesStrict executes an iptables command and returns any error.
// Used for operations that must succeed (like adding a rule to an
// existing chain).
func runIptablesStrict(arguments ...string) error {
	output, err := exec.Command("iptables", arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s: %s: %w", strings.Join(arguments, " "), string(output), err)
	}
	return nil
}

// runCommand executes a system command, ignoring errors (used for
// idempotent operations like creating an interface that may already exist).
func runCommand(name string, arguments ...string) {
	exec.Command(name, arguments...).Run()
}

// runCommandStrict executes a system command and returns any error.
func runCommandStrict(name string, arguments ...string) error {
	output, err := exec.Command(name, arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s: %w", name, strings.Join(arguments, " "), string(output), err)
	}
	return nil
}
