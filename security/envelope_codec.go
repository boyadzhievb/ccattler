// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// keyringRefreshFallbackInterval re-reads the keyring even without watch
// events, so a missed event cannot keep a replica on a stale write mode.
const keyringRefreshFallbackInterval = 60 * time.Second

// keyringReplicaReportTTLSeconds is the lease TTL of a replica's keyring
// report; a stopped replica's report expires after it.
const keyringReplicaReportTTLSeconds = 30

// keyringReplicaReportRenewInterval renews the report lease well inside its TTL.
const keyringReplicaReportRenewInterval = 10 * time.Second

// SecretEnvelopeCodec seals and opens secret envelopes stored at storeKey.
// The store key is bound into v2 envelopes, so a value copied to another key
// does not open (Flow 29).
type SecretEnvelopeCodec interface {
	// SealEnvelopeForKey encrypts plaintext for the given store key.
	SealEnvelopeForKey(ctx context.Context, storeKey string, plaintext []byte) ([]byte, error)
	// OpenEnvelopeForKey decrypts an envelope read from the given store key.
	OpenEnvelopeForKey(ctx context.Context, storeKey string, envelope []byte) ([]byte, error)
}

// KeyringEnvelopeCodec is the control plane's envelope codec: v2 envelopes
// under the keyring's active KEK once the migration has started (or on a
// fresh cluster), v1 under the legacy key before that; v1 envelopes open with
// the legacy key until it is retired.
type KeyringEnvelopeCodec struct {
	factStore         store.StateStore // factStore holds the keyring and migration state.
	rootKeyProvider   KeyProvider      // rootKeyProvider unwraps KEKs and the root check.
	legacyKeyProvider KeyProvider      // legacyKeyProvider opens and writes v1 envelopes; nil on fresh clusters.
	snapshot          *keyringSnapshot // snapshot is the current keyring view.
	mutex             sync.RWMutex     // mutex guards snapshot.
}

// NewKeyringEnvelopeCodec creates the keyring on first use, proves the root
// key against keyring/root_check and loads the KEKs. legacyKeyProvider may be
// nil; it is required while the keyring still records the legacy key.
func NewKeyringEnvelopeCodec(ctx context.Context, factStore store.StateStore, rootKeyProvider KeyProvider, legacyKeyProvider KeyProvider) (*KeyringEnvelopeCodec, error) {
	if ensureError := ensureKeyring(ctx, factStore, rootKeyProvider, legacyKeyProvider != nil); ensureError != nil {
		return nil, ensureError
	}
	keyringCodec := &KeyringEnvelopeCodec{factStore: factStore, rootKeyProvider: rootKeyProvider, legacyKeyProvider: legacyKeyProvider}
	if refreshError := keyringCodec.Refresh(ctx); refreshError != nil {
		return nil, refreshError
	}
	snapshot := keyringCodec.currentSnapshot()
	if snapshot.legacyKeyState == LegacyKeyPresent && legacyKeyProvider == nil {
		return nil, errors.New("the keyring still needs the legacy secret-master.key (migration not completed); restore it on this replica")
	}
	return keyringCodec, nil
}

// Refresh re-reads the keyring and migration state from the store.
func (keyringCodec *KeyringEnvelopeCodec) Refresh(ctx context.Context) error {
	snapshot, loadError := loadKeyringSnapshot(ctx, keyringCodec.factStore, keyringCodec.rootKeyProvider)
	if loadError != nil {
		return loadError
	}
	keyringCodec.mutex.Lock()
	keyringCodec.snapshot = snapshot
	keyringCodec.mutex.Unlock()
	return nil
}

// currentSnapshot returns the snapshot under the read lock.
func (keyringCodec *KeyringEnvelopeCodec) currentSnapshot() *keyringSnapshot {
	keyringCodec.mutex.RLock()
	defer keyringCodec.mutex.RUnlock()
	return keyringCodec.snapshot
}

// SealEnvelopeForKey writes v2 with the active KEK once the migration state
// allows it, otherwise v1 with the legacy key (upgraded clusters before the
// migration gate, while old agents may still decrypt locally).
func (keyringCodec *KeyringEnvelopeCodec) SealEnvelopeForKey(ctx context.Context, storeKey string, plaintext []byte) ([]byte, error) {
	snapshot := keyringCodec.currentSnapshot()
	if snapshot.migrationState.writesV2() {
		return sealEnvelopeV2(snapshot.keyEncryptionKeys[snapshot.activeKEKVersion], snapshot.activeKEKVersion, storeKey, plaintext)
	}
	if keyringCodec.legacyKeyProvider == nil {
		return nil, errors.New("seal: v1 writes need the legacy key, which this replica does not hold")
	}
	return SealEnvelope(ctx, keyringCodec.legacyKeyProvider, plaintext)
}

// OpenEnvelopeForKey opens v2 envelopes with the KEK they name (re-reading the
// keyring once for an unknown version) and v1 envelopes with the legacy key
// unless it is retired.
func (keyringCodec *KeyringEnvelopeCodec) OpenEnvelopeForKey(ctx context.Context, storeKey string, envelope []byte) ([]byte, error) {
	if !isEnvelopeV2(envelope) {
		return keyringCodec.openLegacyEnvelope(ctx, envelope)
	}
	kekVersion, wrappedDEK, sealedValue, parseError := parseEnvelopeV2(envelope)
	if parseError != nil {
		return nil, fmt.Errorf("open %s: %w", storeKey, parseError)
	}
	keyEncryptionKey := keyringCodec.currentSnapshot().keyEncryptionKeys[kekVersion]
	if keyEncryptionKey == nil {
		if refreshError := keyringCodec.Refresh(ctx); refreshError != nil {
			return nil, refreshError
		}
		keyEncryptionKey = keyringCodec.currentSnapshot().keyEncryptionKeys[kekVersion]
	}
	if keyEncryptionKey == nil {
		return nil, fmt.Errorf("open %s: KEK version %d is not in the keyring", storeKey, kekVersion)
	}
	return openEnvelopeV2Payload(keyEncryptionKey, wrappedDEK, sealedValue, storeKey)
}

// openLegacyEnvelope opens a v1 envelope with the legacy key, refused once
// the legacy key is retired.
func (keyringCodec *KeyringEnvelopeCodec) openLegacyEnvelope(ctx context.Context, envelope []byte) ([]byte, error) {
	if keyringCodec.currentSnapshot().legacyKeyState == LegacyKeyRetired {
		return nil, errors.New("open: v1 envelope refused, the legacy key is retired")
	}
	if keyringCodec.legacyKeyProvider == nil {
		return nil, errors.New("open: v1 envelope needs the legacy key, which this replica does not hold")
	}
	return OpenEnvelope(ctx, keyringCodec.legacyKeyProvider, envelope)
}

// RunKeyringWatch keeps the snapshot current: it refreshes on changes under
// keyring/ and observed/keyring/migration and every
// keyringRefreshFallbackInterval. Blocks until ctx is cancelled.
func (keyringCodec *KeyringEnvelopeCodec) RunKeyringWatch(ctx context.Context) {
	keyringEvents, keyringWatchError := keyringCodec.factStore.Watch(ctx, types.PrefixKeyring+"/", store.WatchOption{Prefix: true})
	migrationEvents, migrationWatchError := keyringCodec.factStore.Watch(ctx, types.KeyObservedKeyringMigration, store.WatchOption{})
	if keyringWatchError != nil || migrationWatchError != nil {
		logging.Default().Warn("keyring watch unavailable, refreshing on the fallback interval only")
	}
	fallbackTicker := time.NewTicker(keyringRefreshFallbackInterval)
	defer fallbackTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, isOpen := <-keyringEvents:
			if !isOpen {
				keyringEvents = nil
			}
		case _, isOpen := <-migrationEvents:
			if !isOpen {
				migrationEvents = nil
			}
		case <-fallbackTicker.C:
		}
		if refreshError := keyringCodec.Refresh(ctx); refreshError != nil && ctx.Err() == nil {
			logging.Default().Error("keyring refresh failed", "error", refreshError.Error())
		}
	}
}

// keyringReplicaReport is the JSON a replica publishes at
// observed/keyring/replica/{node-id}.
type keyringReplicaReport struct {
	Version          string `json:"version"`            // Version is the cca build version.
	ActiveKEKVersion int    `json:"active_kek_version"` // ActiveKEKVersion is the KEK this replica seals with.
	RootCheck        string `json:"root_check"`         // RootCheck is "ok" (a failed check stops the replica).
}

// RunKeyringReplicaReporter publishes this replica's keyring report under a
// lease and renews it, so the migration gate sees only live replicas.
// Blocks until ctx is cancelled.
func (keyringCodec *KeyringEnvelopeCodec) RunKeyringReplicaReporter(ctx context.Context, nodeID string, buildVersion string) {
	var leaseID int64
	renewTicker := time.NewTicker(keyringReplicaReportRenewInterval)
	defer renewTicker.Stop()
	for {
		leaseID = keyringCodec.publishKeyringReplicaReport(ctx, nodeID, buildVersion, leaseID)
		select {
		case <-ctx.Done():
			return
		case <-renewTicker.C:
		}
	}
}

// publishKeyringReplicaReport renews the report lease, or grants a new lease
// and rewrites the report when the lease is gone. Returns the lease in use.
func (keyringCodec *KeyringEnvelopeCodec) publishKeyringReplicaReport(ctx context.Context, nodeID string, buildVersion string, leaseID int64) int64 {
	if leaseID != 0 {
		if _, renewError := keyringCodec.factStore.RenewLease(ctx, leaseID); renewError == nil {
			return leaseID
		}
	}
	reportJSON, marshalError := json.Marshal(keyringReplicaReport{
		Version: buildVersion, ActiveKEKVersion: keyringCodec.currentSnapshot().activeKEKVersion, RootCheck: "ok"})
	if marshalError != nil {
		return 0
	}
	newLeaseID, grantError := keyringCodec.factStore.GrantLease(ctx, keyringReplicaReportTTLSeconds)
	if grantError != nil {
		logging.Default().Warn("keyring replica report: lease grant failed", "error", grantError.Error())
		return 0
	}
	if _, putError := keyringCodec.factStore.PutWithLease(ctx, types.KeyObservedKeyringReplica(nodeID), reportJSON, newLeaseID); putError != nil {
		logging.Default().Warn("keyring replica report: write failed", "error", putError.Error())
		return 0
	}
	return newLeaseID
}

// legacyOnlyEnvelopeCodec seals and opens v1 envelopes with one key provider.
// It backs NewSecretStoreWithMasterKey and NewCredentialStoreWithMasterKey,
// which have no keyring (single-key tools and tests).
type legacyOnlyEnvelopeCodec struct {
	keyProvider KeyProvider // keyProvider wraps every DEK directly (v1).
}

// SealEnvelopeForKey seals a v1 envelope; the store key is not bound in v1.
func (legacyCodec legacyOnlyEnvelopeCodec) SealEnvelopeForKey(ctx context.Context, _ string, plaintext []byte) ([]byte, error) {
	return SealEnvelope(ctx, legacyCodec.keyProvider, plaintext)
}

// OpenEnvelopeForKey opens a v1 envelope.
func (legacyCodec legacyOnlyEnvelopeCodec) OpenEnvelopeForKey(ctx context.Context, _ string, envelope []byte) ([]byte, error) {
	return OpenEnvelope(ctx, legacyCodec.keyProvider, envelope)
}
