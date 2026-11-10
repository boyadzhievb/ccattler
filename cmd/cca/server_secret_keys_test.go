// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
)

// TestValidateSecretRootKeyChecks verifies the server-side root key checks:
// a valid distinct key passes; missing, identical-to-legacy and unknown KMS
// providers fail.
func TestValidateSecretRootKeyChecks(t *testing.T) {
	temporaryDirectory := t.TempDir()
	rootKeyPath := filepath.Join(temporaryDirectory, "secret-root.key")
	if generateError := security.GenerateSecretRootKeyFile(rootKeyPath); generateError != nil {
		t.Fatalf("generate: %v", generateError)
	}
	rootKey, _ := security.LoadSecretRootKeyFile(rootKeyPath)
	distinctLegacyKey := bytes.Repeat([]byte{9}, security.SecretRootKeySize)
	ctx := context.Background()

	if validateError := validateSecretRootKey(ctx, serverCommandConfig{secretRootKeyPath: rootKeyPath}, distinctLegacyKey); validateError != nil {
		t.Errorf("valid root key refused: %v", validateError)
	}
	if validateError := validateSecretRootKey(ctx, serverCommandConfig{secretRootKeyPath: rootKeyPath}, rootKey); validateError == nil {
		t.Error("root key identical to the legacy key accepted")
	}
	missingKeyConfig := serverCommandConfig{secretRootKeyPath: filepath.Join(temporaryDirectory, "absent.key")}
	if validateError := validateSecretRootKey(ctx, missingKeyConfig, distinctLegacyKey); validateError == nil {
		t.Error("missing root key accepted")
	}
	unknownKMSConfig := serverCommandConfig{kmsProviderName: "no-such-kms", kmsKeyID: "key"}
	if validateError := validateSecretRootKey(ctx, unknownKMSConfig, distinctLegacyKey); validateError == nil || !strings.Contains(validateError.Error(), "not available") {
		t.Errorf("unknown KMS provider error = %v, want 'not available'", validateError)
	}
}

// TestWriteInitSecretKeysWritesBothKeys verifies a first-node init writes a
// root key (0400) distinct from the legacy master key.
func TestWriteInitSecretKeysWritesBothKeys(t *testing.T) {
	outputDirectory := t.TempDir()
	writeInitSecretKeys(outputDirectory)
	rootKey, rootKeyError := security.LoadSecretRootKeyFile(filepath.Join(outputDirectory, "secret-root.key"))
	if rootKeyError != nil {
		t.Fatalf("root key: %v", rootKeyError)
	}
	legacyKey, legacyKeyError := security.LoadLegacySecretMasterKeyFile(filepath.Join(outputDirectory, "secret-master.key"))
	if legacyKeyError != nil {
		t.Fatalf("legacy key: %v", legacyKeyError)
	}
	if bytes.Equal(rootKey, legacyKey) {
		t.Error("root key equals the legacy key")
	}
}

// TestSetupScriptNeverReplacesSecretKeys verifies setup.sh installs secret
// keys only when present in the init output and absent on the host, so an
// HA secondary keeps the first node's copies.
func TestSetupScriptNeverReplacesSecretKeys(t *testing.T) {
	setupScript := buildSetupScript("192.0.2.10", "node-1", "/tmp/cca-init", "10.100.0.0/16", "10.200.0.0/24")
	for _, keyFile := range []string{"secret-root.key", "secret-master.key"} {
		guard := `if [ -f "$SOURCE_DIR/` + keyFile + `" ] && [ ! -f /etc/ccattler/` + keyFile + ` ]; then`
		if !strings.Contains(setupScript, guard) {
			t.Errorf("setup.sh lacks the install guard for %s", keyFile)
		}
	}
	if strings.Contains(setupScript, `cp "$SOURCE_DIR/secret-master.key"`) {
		t.Error("setup.sh still copies secret-master.key unconditionally")
	}
	if !strings.Contains(setupScript, `install -m 400 "$SOURCE_DIR/secret-root.key"`) {
		t.Error("setup.sh does not install the root key with mode 0400")
	}
}
