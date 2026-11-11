// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// legacySecretMasterKeyPath is the legacy master key that wraps v1 envelopes.
// It was copied to every worker before M174 and is treated as compromised
// (Flow 29); it is retired by the M177 migration.
const legacySecretMasterKeyPath = "/etc/ccattler/secret-master.key" //nolint:gosec // G101: file path, not a credential

// startSecretEnvelopeCodec loads the root key (and the legacy key when the
// file exists), opens or creates the keyring (Flow 29 steps 1–3) and starts
// the keyring watch and this replica's keyring report. Any failure exits:
// a replica with a missing or different root key never serves.
func startSecretEnvelopeCodec(ctx context.Context, factStore store.StateStore, parsedConfig serverCommandConfig) *security.KeyringEnvelopeCodec {
	rootKeyProvider, legacyKeyProvider := loadSecretKeyProviders(ctx, parsedConfig)
	keyringCodec, codecError := security.NewKeyringEnvelopeCodec(ctx, factStore, rootKeyProvider, legacyKeyProvider)
	if codecError != nil {
		exitWithSecretKeyError(codecError)
	}
	go keyringCodec.RunKeyringWatch(ctx)
	go keyringCodec.RunKeyringReplicaReporter(ctx, parsedConfig.nodeID, version)
	fmt.Printf("Secret keyring ready (root key: %s, legacy key: %t)\n", rootKeyProvider.ProviderName(), legacyKeyProvider != nil)
	return keyringCodec
}

// loadSecretKeyProviders returns the root key provider and the legacy key
// provider (nil when secret-master.key does not exist, e.g. a fresh M177
// cluster). The in-memory store uses an ephemeral root key and no legacy key.
func loadSecretKeyProviders(ctx context.Context, parsedConfig serverCommandConfig) (security.KeyProvider, security.KeyProvider) {
	if parsedConfig.storeBackend == "memory" {
		return newEphemeralSecretKeyProvider(), nil
	}
	legacyKey, legacyKeyError := security.LoadLegacySecretMasterKeyFile(legacySecretMasterKeyPath)
	if legacyKeyError != nil && !errors.Is(legacyKeyError, security.ErrLegacySecretMasterKeyMissing) {
		exitWithSecretKeyError(legacyKeyError)
	}
	rootKeyProvider, rootKeyError := loadSecretRootKeyProvider(ctx, parsedConfig, legacyKey)
	if rootKeyError != nil {
		exitWithSecretKeyError(rootKeyError)
	}
	if legacyKey == nil {
		return rootKeyProvider, nil
	}
	legacyKeyProvider, providerError := security.NewLocalKeyProvider(legacyKey)
	if providerError != nil {
		exitWithSecretKeyError(providerError)
	}
	return rootKeyProvider, legacyKeyProvider
}

// newEphemeralSecretKeyProvider creates an in-memory root key for the
// in-memory store: secrets live only as long as the process, so the key does too.
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

// parseKeyringRetireGrace parses --keyring-retire-grace (a Go duration such as
// 168h); a malformed or non-positive value is a startup error.
func parseKeyringRetireGrace(flagValue string) time.Duration {
	retireGrace, parseError := time.ParseDuration(flagValue)
	if parseError != nil || retireGrace <= 0 {
		fmt.Fprintf(os.Stderr, "error: --keyring-retire-grace %q must be a positive duration (e.g. 168h)\n", flagValue)
		os.Exit(1)
	}
	return retireGrace
}

// loadSecretRootKeyProvider returns the root key provider: a KMS key that
// must wrap and unwrap a probe value, or a key file that must pass
// LoadSecretRootKeyFile and differ from the legacy key (decision R1).
func loadSecretRootKeyProvider(ctx context.Context, parsedConfig serverCommandConfig, legacyKey []byte) (security.KeyProvider, error) {
	if parsedConfig.kmsProviderName != "" {
		return loadKMSRootKeyProvider(ctx, parsedConfig)
	}
	rootKey, loadError := security.LoadSecretRootKeyFile(parsedConfig.secretRootKeyPath)
	if loadError != nil {
		return nil, loadError
	}
	if distinctError := security.ValidateSecretRootKeyDistinctFromLegacy(rootKey, legacyKey); distinctError != nil {
		return nil, distinctError
	}
	return security.NewLocalKeyProvider(rootKey)
}

// loadKMSRootKeyProvider builds the configured KMS key provider and
// round-trips a random probe through it, so a wrong key ID or missing
// permission fails at startup instead of on the first secret write.
func loadKMSRootKeyProvider(ctx context.Context, parsedConfig serverCommandConfig) (security.KeyProvider, error) {
	kmsFactory := security.LookupKMSKeyProviderFactory(parsedConfig.kmsProviderName)
	if kmsFactory == nil {
		return nil, fmt.Errorf("--kms-provider %q is not available in this build (use a cloud build, cca-cloud)", parsedConfig.kmsProviderName)
	}
	if parsedConfig.kmsKeyID == "" {
		return nil, errors.New("--kms-provider needs --kms-key-id")
	}
	kmsKeyProvider, factoryError := kmsFactory(ctx, parsedConfig.kmsKeyID, parsedConfig.cloudRegion)
	if factoryError != nil {
		return nil, fmt.Errorf("KMS root key %s: %w", parsedConfig.kmsKeyID, factoryError)
	}
	probeValue, probeError := security.GenerateDEK()
	if probeError != nil {
		return nil, probeError
	}
	wrappedProbe, wrapError := kmsKeyProvider.WrapKey(ctx, probeValue)
	if wrapError != nil {
		return nil, fmt.Errorf("KMS root key %s: wrap: %w", parsedConfig.kmsKeyID, wrapError)
	}
	unwrappedProbe, unwrapError := kmsKeyProvider.UnwrapKey(ctx, wrappedProbe)
	if unwrapError != nil {
		return nil, fmt.Errorf("KMS root key %s: unwrap: %w", parsedConfig.kmsKeyID, unwrapError)
	}
	if !bytes.Equal(unwrappedProbe, probeValue) {
		return nil, fmt.Errorf("KMS root key %s: unwrapped probe does not match", parsedConfig.kmsKeyID)
	}
	return kmsKeyProvider, nil
}

// exitWithSecretKeyError prints a secret key startup error with the fix for
// a missing file and exits; the server never generates keys itself.
func exitWithSecretKeyError(keyError error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", keyError)
	if errors.Is(keyError, security.ErrSecretRootKeyMissing) {
		fmt.Fprintln(os.Stderr, "  Fix: sudo cca secret generate-root-key   (once, on one control-plane node; copy it to the other replicas)")
	}
	if errors.Is(keyError, security.ErrKeyringRootCheckFailed) {
		fmt.Fprintln(os.Stderr, "  Fix: copy secret-root.key from a running control-plane replica; every replica needs the same root key")
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
