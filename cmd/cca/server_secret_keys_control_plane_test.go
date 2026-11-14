// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
)

// TestControlPlaneCheckAcceptsInterruptedRotation: with endpoints set, a key
// at the path or only at .next (a rotation interrupted between its renames)
// passes; no key at all, or no endpoints, is reported.
func TestControlPlaneCheckAcceptsInterruptedRotation(t *testing.T) {
	keyDirectory := t.TempDir()
	rootKeyPath := filepath.Join(keyDirectory, "secret-root.key")
	t.Setenv(endpointsEnvVariable, "https://127.0.0.1:2379")

	if missingParts := findMissingControlPlaneParts(rootKeyPath); len(missingParts) != 1 {
		t.Errorf("no key file: missing %v, want only the root key", missingParts)
	}
	if writeError := os.WriteFile(rootKeyPath+security.RootKeyNextSuffix, []byte("staged"), 0o400); writeError != nil {
		t.Fatal(writeError)
	}
	if missingParts := findMissingControlPlaneParts(rootKeyPath); len(missingParts) != 0 {
		t.Errorf("only %s%s present: missing %v, want none", rootKeyPath, security.RootKeyNextSuffix, missingParts)
	}
	t.Setenv(endpointsEnvVariable, "")
	if fileExists(endpointsConfigPath) {
		t.Skipf("%s exists on this host; the no-endpoints case cannot be checked here", endpointsConfigPath)
	}
	if missingParts := findMissingControlPlaneParts(rootKeyPath); len(missingParts) != 1 {
		t.Errorf("no endpoints: missing %v, want only the endpoints", missingParts)
	}
}
