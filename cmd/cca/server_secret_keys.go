// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/boyadzhievb/ccattler/security"
)

// legacySecretMasterKeyPath is the legacy master key that wraps v1 envelopes.
// It was copied to every worker before M174 and is treated as compromised
// (Flow 29); it is retired by the M177 migration.
const legacySecretMasterKeyPath = "/etc/ccattler/secret-master.key" //nolint:gosec // G101: file path, not a credential

// loadSecretKeyProvider returns the key provider for secret envelopes after
// the root key startup checks (Flow 29 step 1). The in-memory store uses
// ephemeral keys that never touch disk; a persistent store requires both
// key files (or a KMS root key) and exits on any check failure.
func loadSecretKeyProvider(ctx context.Context, parsedConfig serverCommandConfig) security.KeyProvider {
	if parsedConfig.storeBackend == "memory" {
		return newEphemeralSecretKeyProvider()
	}
	legacyKey, legacyKeyError := security.LoadLegacySecretMasterKeyFile(legacySecretMasterKeyPath)
	if legacyKeyError != nil {
		exitWithSecretKeyError(legacyKeyError)
	}
	if rootKeyError := validateSecretRootKey(ctx, parsedConfig, legacyKey); rootKeyError != nil {
		exitWithSecretKeyError(rootKeyError)
	}
	legacyKeyProvider, providerError := security.NewLocalKeyProvider(legacyKey)
	if providerError != nil {
		exitWithSecretKeyError(providerError)
	}
	return legacyKeyProvider
}

// newEphemeralSecretKeyProvider creates an in-memory key for the in-memory
// store: secrets live only as long as the process, so the key does too.
func newEphemeralSecretKeyProvider() security.KeyProvider {
	ephemeralKey, generateError := security.GenerateMasterKey()
	if generateError != nil {
		exitWithSecretKeyError(generateError)
	}
	ephemeralKeyProvider, providerError := security.NewLocalKeyProvider(ephemeralKey)
	if providerError != nil {
		exitWithSecretKeyError(providerError)
	}
	fmt.Println("Secret keys: ephemeral (in-memory store); nothing is written to disk")
	return ephemeralKeyProvider
}

// validateSecretRootKey checks the root key: a KMS key must wrap and unwrap
// a probe value; a key file must pass LoadSecretRootKeyFile and differ from
// the legacy key (decision R1).
func validateSecretRootKey(ctx context.Context, parsedConfig serverCommandConfig, legacyKey []byte) error {
	if parsedConfig.kmsProviderName != "" {
		return validateKMSRootKey(ctx, parsedConfig)
	}
	rootKey, loadError := security.LoadSecretRootKeyFile(parsedConfig.secretRootKeyPath)
	if loadError != nil {
		return loadError
	}
	return security.ValidateSecretRootKeyDistinctFromLegacy(rootKey, legacyKey)
}

// validateKMSRootKey builds the configured KMS key provider and round-trips a
// random probe through it, so a wrong key ID or missing permission fails at
// startup instead of on the first secret write.
func validateKMSRootKey(ctx context.Context, parsedConfig serverCommandConfig) error {
	kmsFactory := security.LookupKMSKeyProviderFactory(parsedConfig.kmsProviderName)
	if kmsFactory == nil {
		return fmt.Errorf("--kms-provider %q is not available in this build (use a cloud build, cca-cloud)", parsedConfig.kmsProviderName)
	}
	if parsedConfig.kmsKeyID == "" {
		return errors.New("--kms-provider needs --kms-key-id")
	}
	kmsKeyProvider, factoryError := kmsFactory(ctx, parsedConfig.kmsKeyID, parsedConfig.cloudRegion)
	if factoryError != nil {
		return fmt.Errorf("KMS root key %s: %w", parsedConfig.kmsKeyID, factoryError)
	}
	probeValue, probeError := security.GenerateDEK()
	if probeError != nil {
		return probeError
	}
	wrappedProbe, wrapError := kmsKeyProvider.WrapKey(ctx, probeValue)
	if wrapError != nil {
		return fmt.Errorf("KMS root key %s: wrap: %w", parsedConfig.kmsKeyID, wrapError)
	}
	unwrappedProbe, unwrapError := kmsKeyProvider.UnwrapKey(ctx, wrappedProbe)
	if unwrapError != nil {
		return fmt.Errorf("KMS root key %s: unwrap: %w", parsedConfig.kmsKeyID, unwrapError)
	}
	if !bytes.Equal(unwrappedProbe, probeValue) {
		return fmt.Errorf("KMS root key %s: unwrapped probe does not match", parsedConfig.kmsKeyID)
	}
	return nil
}

// exitWithSecretKeyError prints a secret key startup error with the fix for
// a missing file and exits; the server never generates keys itself.
func exitWithSecretKeyError(keyError error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", keyError)
	if errors.Is(keyError, security.ErrSecretRootKeyMissing) {
		fmt.Fprintln(os.Stderr, "  Fix: sudo cca secret generate-root-key   (once, on one control-plane node; copy it to the other replicas)")
	}
	if errors.Is(keyError, security.ErrLegacySecretMasterKeyMissing) {
		fmt.Fprintln(os.Stderr, "  Fix: run 'cca init' and its setup.sh, or copy secret-master.key from another control-plane replica")
	}
	os.Exit(1)
}

// executeSecretGenerateRootKey implements "cca secret generate-root-key
// [--path <file>]": a local operation on a control-plane node that writes a
// fresh root key (0400) and refuses to overwrite an existing one. Used once
// when upgrading a cluster that predates M177; copy the file to every replica.
func executeSecretGenerateRootKey(args []string) {
	rootKeyPath := security.DefaultSecretRootKeyPath
	for argIndex := 0; argIndex < len(args); argIndex++ {
		if args[argIndex] == "--path" && argIndex+1 < len(args) {
			argIndex++
			rootKeyPath = args[argIndex]
			continue
		}
		fmt.Fprintln(os.Stderr, "usage: cca secret generate-root-key [--path <file>]")
		os.Exit(1)
	}
	if generateError := security.GenerateSecretRootKeyFile(rootKeyPath); generateError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", generateError)
		os.Exit(1)
	}
	fmt.Printf("Generated secret root key at %s (mode 0400). Copy it to every control-plane replica; never to workers.\n", rootKeyPath)
}
