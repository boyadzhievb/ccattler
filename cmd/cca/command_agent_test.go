// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"errors"
	"testing"
)

// TestDeriveGateHTTPSListenAddress verifies the gate HTTPS listener binds the
// same host as the plain proxy on port 443.
func TestDeriveGateHTTPSListenAddress(t *testing.T) {
	testCases := map[string]string{
		"192.168.124.20:8080": "192.168.124.20:443",
		"0.0.0.0:80":          "0.0.0.0:443",
		"[::1]:8080":          "[::1]:443",
		"not-an-address":      "0.0.0.0:443",
	}
	for proxyListenAddress, expectedAddress := range testCases {
		if derivedAddress := deriveGateHTTPSListenAddress(proxyListenAddress); derivedAddress != expectedAddress {
			t.Errorf("deriveGateHTTPSListenAddress(%q) = %q, want %q", proxyListenAddress, derivedAddress, expectedAddress)
		}
	}
}

// TestParseAgentProxyTLSListen verifies --proxy-tls-listen is parsed, enables
// the proxy, and that --proxy alone defaults the HTTPS listener.
func TestParseAgentProxyTLSListen(t *testing.T) {
	explicitConfig := parseAgentCommandArgs([]string{"--node-id", "node-1", "--proxy-tls-listen", "10.0.0.5:8443"})
	if !explicitConfig.proxyEnabled || explicitConfig.proxyTLSListenAddress != "10.0.0.5:8443" {
		t.Errorf("explicit flag: got enabled=%v address=%q", explicitConfig.proxyEnabled, explicitConfig.proxyTLSListenAddress)
	}

	defaultConfig := parseAgentCommandArgs([]string{"--node-id", "node-1", "--proxy-listen", "10.0.0.5:8080"})
	if defaultConfig.proxyTLSListenAddress != "10.0.0.5:443" {
		t.Errorf("default HTTPS address: got %q, want 10.0.0.5:443", defaultConfig.proxyTLSListenAddress)
	}

	disabledConfig := parseAgentCommandArgs([]string{"--node-id", "node-1"})
	if disabledConfig.proxyEnabled || disabledConfig.proxyTLSListenAddress != "" {
		t.Errorf("no proxy flags: got enabled=%v address=%q", disabledConfig.proxyEnabled, disabledConfig.proxyTLSListenAddress)
	}
}

// TestResolveRuleBasedDataPlaneBackend verifies backend selection fails loudly
// when the chosen backend's host tool is missing, instead of starting a data
// plane that cannot program rules.
func TestResolveRuleBasedDataPlaneBackend(t *testing.T) {
	toolMissing := func() error { return errors.New("not found") }
	toolPresent := func() error { return nil }
	testCases := []struct {
		name             string
		requestedBackend string
		nftables         func() error
		iptables         func() error
		expectedBackend  string
		expectError      bool
	}{
		{"auto prefers nftables", "auto", toolPresent, toolPresent, "nftables", false},
		{"auto falls back to iptables", "auto", toolMissing, toolPresent, "iptables", false},
		{"auto with neither tool fails", "auto", toolMissing, toolMissing, "", true},
		{"forced nftables without nft fails", "nftables", toolMissing, toolPresent, "", true},
		{"forced nftables with nft", "nftables", toolPresent, toolMissing, "nftables", false},
		{"forced iptables without iptables fails", "iptables", toolPresent, toolMissing, "", true},
		{"forced iptables with iptables", "iptables", toolMissing, toolPresent, "iptables", false},
		{"shadow needs iptables", "shadow", toolPresent, toolMissing, "", true},
		{"shadow does not need nft", "shadow", toolMissing, toolPresent, "shadow", false},
		{"ebpf needs neither tool", "ebpf", toolMissing, toolMissing, "ebpf", false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			toolProbes := dataPlaneToolProbes{nftablesAvailable: testCase.nftables, iptablesAvailable: testCase.iptables}
			resolvedBackend, resolveError := resolveRuleBasedDataPlaneBackend(testCase.requestedBackend, toolProbes)
			if testCase.expectError != (resolveError != nil) {
				t.Fatalf("error = %v, expectError %v", resolveError, testCase.expectError)
			}
			if resolvedBackend != testCase.expectedBackend {
				t.Errorf("backend = %q, want %q", resolvedBackend, testCase.expectedBackend)
			}
		})
	}
}
