// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestInitRefusesEtcdAccessCAAsSingleCA (SA2-L3): an HA secondary given a
// three-CA cluster's etcd-access-ca.pem as its single --ca is refused, so it
// cannot sign node identities with the CA etcd trusts.
func TestInitRefusesEtcdAccessCAAsSingleCA(testContext *testing.T) {
	firstDirectory := filepath.Join(testContext.TempDir(), "first")
	if _, stderrContent, exitCode := runCCA(testContext, "init", "--node-id", "ctrl", "--advertise-address", "127.0.0.1",
		"-o", firstDirectory); exitCode != 0 {
		testContext.Fatalf("first cca init: exit %d: %s", exitCode, stderrContent)
	}
	_, stderrContent, exitCode := runCCA(testContext, "init", "--node-id", "ctrl-2", "--advertise-address", "127.0.0.1",
		"--ca", filepath.Join(firstDirectory, "etcd-access-ca.pem"), "--ca-key", filepath.Join(firstDirectory, "etcd-access-ca-key.pem"),
		"-o", filepath.Join(testContext.TempDir(), "second"))
	if exitCode == 0 || !strings.Contains(stderrContent, "etcd access CA of a three-CA cluster") {
		testContext.Errorf("cca init --ca etcd-access-ca.pem: exit %d, stderr %q; want the CA-kind refusal", exitCode, stderrContent)
	}
}
