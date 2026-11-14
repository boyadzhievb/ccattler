// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"strings"
	"testing"
)

// TestRootKeyCommandsExplainControlPlaneOnly runs the local-only root key
// commands on a host with no etcd endpoints and no root key file: they must
// say they run on a control-plane node instead of printing a raw etcd error
// (spec api-route-authorization, "Local-only commands say so").
func TestRootKeyCommandsExplainControlPlaneOnly(t *testing.T) {
	missingRootKeyPath := t.TempDir() + "/secret-root.key"
	commands := [][]string{
		{"secret", "root-key-status", "--path", missingRootKeyPath},
		{"secret", "rotate-root-key", "--commit", "--path", missingRootKeyPath},
	}
	for _, commandArgs := range commands {
		environment := []string{"HOME=" + t.TempDir(), "CCA_ENDPOINTS="}
		_, stderrContent, exitCode := runCCAWithEnv(t, environment, commandArgs...)
		if exitCode == 0 {
			t.Errorf("cca %s: exit 0, want non-zero", strings.Join(commandArgs, " "))
		}
		if !strings.Contains(stderrContent, "control-plane node") || strings.Contains(stderrContent, "etcd-client") {
			t.Errorf("cca %s: stderr does not say it must run on a control-plane node (or shows a raw etcd error):\n%s",
				strings.Join(commandArgs, " "), stderrContent)
		}
	}
}
