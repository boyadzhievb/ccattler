// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runCommandStartupTimeout bounds how long cca run may take to serve its
// local status API.
const runCommandStartupTimeout = 15 * time.Second

// TestRunCommandNeverOpensEtcd: with etcd endpoints configured (as on every
// control-plane host), cca run still starts on its in-memory store instead
// of writing the cluster's etcd behind the API (review SEC-M1); the removed
// --endpoints flag is refused.
func TestRunCommandNeverOpensEtcd(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a cca run process")
	}
	configurationPath := writeTempFile(t, t.TempDir(), "run.cca", "service web {\n    image \"sleep 300\"\n    instances 1\n}\n")
	localAPIAddress := unusedLocalAddress(t)
	runCommand := exec.Command(ccaBinaryPath, "run", configurationPath) //nolint:gosec // test runs the binary under test
	runCommand.Env = append(os.Environ(), "CCA_ENDPOINTS="+unusedLocalAddress(t), "CCA_API=http://"+localAPIAddress, "HOME="+t.TempDir())
	if startError := runCommand.Start(); startError != nil {
		t.Fatal(startError)
	}
	t.Cleanup(func() {
		_ = runCommand.Process.Kill()
		_ = runCommand.Wait()
	})
	if !waitForServerReady(localAPIAddress, runCommandStartupTimeout) {
		t.Fatalf("cca run with CCA_ENDPOINTS set did not start its in-memory API on %s (did it try etcd?)", localAPIAddress)
	}

	_, flagStderr, flagExit := runCCA(t, "run", "--endpoints", "localhost:2379", configurationPath)
	if flagExit == 0 || !strings.Contains(flagStderr, "--endpoints") {
		t.Errorf("cca run --endpoints: exit %d stderr %q, want the removed flag refused", flagExit, flagStderr)
	}
}
