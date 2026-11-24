// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitRejectsInvalidEtcdClientCIDR: a --etcd-client-cidr value that is not an
// IPv4 or IPv6 CIDR, or one that covers every address, fails cca init with an error
// naming it, before any file is written.
func TestInitRejectsInvalidEtcdClientCIDR(testContext *testing.T) {
	for _, invalidValue := range []string{"192.0.2.0/33", "192.0.2.1", "not-a-network", "192.0.2.0/24,"} {
		testContext.Run(invalidValue, func(subtestContext *testing.T) {
			outputDirectory := filepath.Join(subtestContext.TempDir(), "cca-init")
			_, stderrContent, exitCode := runCCA(subtestContext, "init", "--etcd-client-cidr", invalidValue, "-o", outputDirectory)
			if exitCode == 0 {
				subtestContext.Fatalf("cca init accepted --etcd-client-cidr %q", invalidValue)
			}
			if !strings.Contains(stderrContent, "etcd-client-cidr") || !strings.Contains(stderrContent, strings.TrimSuffix(invalidValue, ",")) {
				subtestContext.Errorf("error does not name the flag and %q: %s", invalidValue, stderrContent)
			}
			if _, statError := os.Stat(outputDirectory); !os.IsNotExist(statError) {
				subtestContext.Errorf("cca init wrote %s although the flag was invalid", outputDirectory)
			}
		})
	}
	// A prefix covering every address, also in its IPv4-mapped IPv6 form, would open etcd to all.
	// The leading-zero forms got through a second parser in review round 3; the union
	// subcases cover two halves that together open etcd to every address.
	for _, allowAllValue := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96",
		"0.0.0.0/00", "::/000", "::ffff:0.0.0.0/096",
		"0.0.0.0/1,128.0.0.0/1", "::/1,8000::/1", "0.0.0.0/1,128.0.0.0/2,192.0.0.0/2,10.0.0.0/8"} {
		testContext.Run(allowAllValue, func(subtestContext *testing.T) {
			outputDirectory := filepath.Join(subtestContext.TempDir(), "cca-init")
			_, stderrContent, exitCode := runCCA(subtestContext, "init", "--etcd-client-cidr", allowAllValue, "-o", outputDirectory)
			if exitCode == 0 {
				subtestContext.Fatalf("cca init accepted --etcd-client-cidr %q, which covers every address", allowAllValue)
			}
			if !strings.Contains(stderrContent, "every address") || !strings.Contains(stderrContent, allowAllValue) {
				subtestContext.Errorf("error does not name %q and say it covers every address: %s", allowAllValue, stderrContent)
			}
			if _, statError := os.Stat(outputDirectory); !os.IsNotExist(statError) {
				subtestContext.Errorf("cca init wrote %s although the flag covers every address", outputDirectory)
			}
		})
	}
}

// TestInitWarnsOnWideEtcdClientCIDR: a network wider than /8 (IPv4) or /32 (IPv6), or
// one with addresses that are not private or loopback (link-local included), is
// accepted with a warning naming it and M280, and its rule is written; a private
// network gets no warning.
func TestInitWarnsOnWideEtcdClientCIDR(testContext *testing.T) {
	for _, testCase := range []struct {
		value          string // --etcd-client-cidr value
		wantWarning    bool   // a warning naming the value is printed
		wantNotPrivate bool   // the warning says the network is not private or loopback
	}{
		{value: "10.0.0.0/7", wantWarning: true, wantNotPrivate: true},
		{value: "203.0.113.0/24", wantWarning: true, wantNotPrivate: true},
		{value: "169.254.0.0/16", wantWarning: true, wantNotPrivate: true},
		{value: "2001::/16", wantWarning: true, wantNotPrivate: true},
		{value: "0.0.0.0/1", wantWarning: true, wantNotPrivate: true},
		{value: "10.0.0.0/8"},
		{value: "192.168.1.0/24"},
		{value: "fd00:1234::/48"},
	} {
		testContext.Run(testCase.value, func(subtestContext *testing.T) {
			outputDirectory := filepath.Join(subtestContext.TempDir(), "cca-init")
			stdoutContent, stderrContent, exitCode := runCCA(subtestContext, "init", "--etcd-client-cidr", testCase.value,
				"--advertise-address", "192.0.2.10", "--node-id", "ctrl", "-o", outputDirectory)
			if exitCode != 0 {
				subtestContext.Fatalf("cca init refused %q: %s", testCase.value, stderrContent)
			}
			combinedOutput := stdoutContent + stderrContent
			hasWarning := strings.Contains(combinedOutput, "WARNING") && strings.Contains(combinedOutput, testCase.value) &&
				strings.Contains(combinedOutput, "M280")
			if hasWarning != testCase.wantWarning {
				subtestContext.Errorf("warning = %v, want %v; output:\n%s", hasWarning, testCase.wantWarning, combinedOutput)
			}
			if saysNotPrivate := strings.Contains(combinedOutput, "is not private or loopback"); saysNotPrivate != testCase.wantNotPrivate {
				subtestContext.Errorf("\"not private or loopback\" = %v, want %v; output:\n%s", saysNotPrivate, testCase.wantNotPrivate, combinedOutput)
			}
			setupScript, readError := os.ReadFile(filepath.Join(outputDirectory, "setup.sh")) // #nosec G304 -- test temp file
			if readError != nil || !strings.Contains(string(setupScript), "port port=\"2379\"") {
				subtestContext.Errorf("setup.sh has no 2379 rule for %q: %v", testCase.value, readError)
			}
		})
	}
}
