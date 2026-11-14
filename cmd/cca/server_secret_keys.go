// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
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
	go func() {
		if watchError := keyringCodec.RunKeyringWatch(ctx); watchError != nil {
			exitWithSecretKeyError(watchError)
		}
	}()
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

// minimumKeyringRetireGrace is the shortest --keyring-retire-grace accepted: a
// replica that missed the switch must have time to report or exit (review).
const minimumKeyringRetireGrace = time.Hour

// parseKeyringRetireGrace parses --keyring-retire-grace (a Go duration such as
// 168h); a malformed value or one below minimumKeyringRetireGrace is a startup error.
func parseKeyringRetireGrace(flagValue string) time.Duration {
	retireGrace, parseError := time.ParseDuration(flagValue)
	if parseError != nil || retireGrace < minimumKeyringRetireGrace {
		fmt.Fprintf(os.Stderr, "error: --keyring-retire-grace %q must be a duration of at least %s (e.g. 168h)\n", flagValue, minimumKeyringRetireGrace)
		os.Exit(1)
	}
	return retireGrace
}

// loadSecretRootKeyProvider returns the root key provider: a cloud key (AWS
// KMS, GCP Cloud KMS) that must wrap and unwrap a probe value, or a key file
// that must pass LoadSecretRootKeyFile and differ from the legacy key (R1).
func loadSecretRootKeyProvider(ctx context.Context, parsedConfig serverCommandConfig, legacyKey []byte) (security.KeyProvider, error) {
	if parsedConfig.rootKeyProviderName != "" {
		return loadCloudRootKeyProvider(ctx, parsedConfig)
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

// loadCloudRootKeyProvider builds the configured cloud key provider and
// round-trips a random probe through it, so a wrong key ID or missing
// permission fails at startup instead of on the first secret write.
func loadCloudRootKeyProvider(ctx context.Context, parsedConfig serverCommandConfig) (security.KeyProvider, error) {
	cloudKeyProviderFactory := security.LookupCloudKeyProviderFactory(parsedConfig.rootKeyProviderName)
	if cloudKeyProviderFactory == nil {
		return nil, fmt.Errorf("--root-key-provider %q is not available in this build (use a cloud build, cca-cloud)", parsedConfig.rootKeyProviderName)
	}
	if parsedConfig.rootKeyID == "" {
		return nil, errors.New("--root-key-provider needs --root-key-id")
	}
	cloudRootKeyProvider, factoryError := cloudKeyProviderFactory(ctx, parsedConfig.rootKeyID, parsedConfig.cloudRegion)
	if factoryError != nil {
		return nil, fmt.Errorf("cloud root key %s: %w", parsedConfig.rootKeyID, factoryError)
	}
	probeValue, probeError := security.GenerateDEK()
	if probeError != nil {
		return nil, probeError
	}
	wrappedProbe, wrapError := cloudRootKeyProvider.WrapKey(ctx, probeValue)
	if wrapError != nil {
		return nil, fmt.Errorf("cloud root key %s: wrap: %w", parsedConfig.rootKeyID, wrapError)
	}
	unwrappedProbe, unwrapError := cloudRootKeyProvider.UnwrapKey(ctx, wrappedProbe)
	if unwrapError != nil {
		return nil, fmt.Errorf("cloud root key %s: unwrap: %w", parsedConfig.rootKeyID, unwrapError)
	}
	if !bytes.Equal(unwrappedProbe, probeValue) {
		return nil, fmt.Errorf("cloud root key %s: unwrapped probe does not match", parsedConfig.rootKeyID)
	}
	return cloudRootKeyProvider, nil
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

// rootKeyRotationReplicaWait is how long rotate-root-key waits for the replica
// reports to expire (their lease TTL is 30 s after a server stops).
const rootKeyRotationReplicaWait = 45 * time.Second

// rootKeyRotationUsage is printed for invalid cca secret rotate-root-key arguments.
const rootKeyRotationUsage = "usage: cca secret rotate-root-key --commit|--finish [--path <file>]" //nolint:gosec // G101: usage text, not a credential

// executeSecretRotateRootKey implements "cca secret rotate-root-key
// --commit|--finish [--path <file>]" (Flow 30 part B), run with every server
// stopped and {path}.next on every replica: --commit (first host) re-wraps the
// keyring and swaps the files, --finish (other hosts) only swaps.
func executeSecretRotateRootKey(args []string) {
	rootKeyPath, rotationMode := parseRootKeyCommandArgs(args, rootKeyRotationUsage, "--commit", "--finish")
	requireControlPlaneNode("rotate-root-key", rootKeyPath)
	factStore := openLocalControlPlaneStore()
	defer func() { _ = factStore.Close() }()
	ctx := context.Background()
	fmt.Printf("Waiting up to %s for every API replica to stop...\n", rootKeyRotationReplicaWait)
	if waitError := security.WaitForStoppedKeyringReplicas(ctx, factStore, rootKeyRotationReplicaWait); waitError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", waitError)
		os.Exit(1)
	}
	var keyFingerprint string
	var rotateError error
	if rotationMode == "--commit" {
		legacyKey, legacyKeyError := security.LoadLegacySecretMasterKeyFile(legacySecretMasterKeyPath)
		if legacyKeyError != nil && !errors.Is(legacyKeyError, security.ErrLegacySecretMasterKeyMissing) {
			fmt.Fprintf(os.Stderr, "error: %v\n", legacyKeyError)
			os.Exit(1)
		}
		keyFingerprint, rotateError = security.CommitRootKeyRotation(ctx, factStore, rootKeyPath, legacyKey)
	} else {
		keyFingerprint, rotateError = security.FinishRootKeyRotation(ctx, factStore, rootKeyPath)
	}
	if rotateError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", rotateError)
		os.Exit(1)
	}
	fmt.Printf("%s opens the keyring (fingerprint %s); the previous key, if any, is %s%s.\n",
		rootKeyPath, keyFingerprint, rootKeyPath, security.RootKeyOldSuffix)
}

// executeSecretRootKeyStatus implements "cca secret root-key-status [--path
// <file>]": prints which of the key file and {path}.next open the keyring and
// the rotation stage (idle|staged|committed|invalid). Read-only; the playbook
// decides from it whether to roll back or go forward.
func executeSecretRootKeyStatus(args []string) {
	rootKeyPath, _ := parseRootKeyCommandArgs(args, "usage: cca secret root-key-status [--path <file>]")
	requireControlPlaneNode("root-key-status", rootKeyPath)
	factStore := openLocalControlPlaneStore()
	defer func() { _ = factStore.Close() }()
	filesStatus, inspectError := security.InspectRootKeyFiles(context.Background(), factStore, rootKeyPath)
	if inspectError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", inspectError)
		os.Exit(1)
	}
	fmt.Printf("stage=%s key=%s next=%s old=%t\n", filesStatus.Stage, filesStatus.Current, filesStatus.Next, filesStatus.OldExists)
}

// parseRootKeyCommandArgs parses --path and exactly one of modeFlags (none
// when modeFlags is empty); anything else prints usageText and exits.
func parseRootKeyCommandArgs(args []string, usageText string, modeFlags ...string) (string, string) {
	rootKeyPath, selectedMode := security.DefaultSecretRootKeyPath, ""
	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch {
		case args[argIndex] == "--path" && argIndex+1 < len(args):
			argIndex++
			rootKeyPath = args[argIndex]
		case selectedMode == "" && slices.Contains(modeFlags, args[argIndex]):
			selectedMode = args[argIndex]
		default:
			fmt.Fprintln(os.Stderr, usageText)
			os.Exit(1)
		}
	}
	if len(modeFlags) > 0 && selectedMode == "" {
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}
	return rootKeyPath, selectedMode
}

// requireControlPlaneNode exits with an explanation unless this host has etcd
// endpoints and a root key at rootKeyPath or its .next (a rotation interrupted
// between its two renames leaves only .next). The root key commands work on
// etcd directly while every API server is stopped (Flow 30), so only there.
func requireControlPlaneNode(commandName string, rootKeyPath string) {
	missingParts := findMissingControlPlaneParts(rootKeyPath)
	if len(missingParts) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "error: cca secret %s runs only on a control-plane node, which this host is not: %s\n",
		commandName, strings.Join(missingParts, "; "))
	os.Exit(1)
}

// findMissingControlPlaneParts lists what this host lacks to be a control-plane
// node for the root key commands: etcd endpoints, and a key file at
// rootKeyPath or rootKeyPath.next. Empty means the commands may run.
func findMissingControlPlaneParts(rootKeyPath string) []string {
	var missingParts []string
	if resolveStoreDefaults().storeBackend != "etcd" {
		missingParts = append(missingParts, fmt.Sprintf("no etcd endpoints (%s or %s)", endpointsEnvVariable, endpointsConfigPath))
	}
	if !rootKeyFileExists(rootKeyPath) && !rootKeyFileExists(rootKeyPath+security.RootKeyNextSuffix) {
		missingParts = append(missingParts, fmt.Sprintf("no root key at %s or %s%s", rootKeyPath, rootKeyPath, security.RootKeyNextSuffix))
	}
	return missingParts
}

// rootKeyFileExists reports whether a root key file is present at keyPath,
// without following a symlink and without reading it.
func rootKeyFileExists(keyPath string) bool {
	_, statError := os.Lstat(keyPath) //nolint:gosec // G703: the operator's own --path on this host; only its existence is checked
	return statError == nil
}

// openLocalControlPlaneStore connects to etcd the way cca server does
// (endpoints and TLS from the node's defaults) or exits.
func openLocalControlPlaneStore() store.StateStore {
	storeDefaults := resolveStoreDefaultsForDaemon()
	factStore, storeError := createStateStoreFromServerConfig(storeDefaults.storeBackend, storeDefaults.etcdEndpoints, storeDefaults.storeKeyPrefix,
		storeDefaults.etcdCertPath, storeDefaults.etcdKeyPath, storeDefaults.etcdCACertPath)
	if storeError != nil {
		fmt.Fprintf(os.Stderr, "error: connect to etcd at %s: %v\n", storeDefaults.etcdEndpoints, storeError)
		os.Exit(1)
	}
	return factStore
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
