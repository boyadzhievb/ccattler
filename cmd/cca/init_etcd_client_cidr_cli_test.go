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
// IPv4 or IPv6 CIDR fails cca init with an error naming it, before any file is written.
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
}
