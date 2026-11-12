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
// under the keyring's active KEK unless keyring/legacy is present (upgraded
// cluster before the migration gate), then v1 under the legacy key; v1
// envelopes open with the legacy key until it is retired.
type KeyringEnvelopeCodec struct {
	factStore         store.StateStore // factStore holds the keyring and migration state.
	rootKeyProvider   KeyProvider      // rootKeyProvider unwraps KEKs and the root check.
	legacyKeyProvider KeyProvider      // legacyKeyProvider opens and writes v1 envelopes; nil on fresh clusters and after retirement.
	snapshot          *keyringSnapshot // snapshot is the current keyring view.
	mutex             sync.RWMutex     // mutex guards snapshot and legacyKeyProvider.
	refreshMutex      sync.Mutex       // refreshMutex serializes Refresh so an older read never replaces a newer snapshot.
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
	snapshot, currentLegacyKeyProvider := keyringCodec.currentState()
	if snapshot.legacyKeyState.needsLegacyKey() && currentLegacyKeyProvider == nil {
		return nil, fmt.Errorf("the keyring is %s and still needs the legacy secret-master.key; restore it on this replica", snapshot.legacyKeyState)
	}
	return keyringCodec, nil
}

// Refresh re-reads the keyring from the store. Refreshes run one at a time,
// so a slow read can never replace a newer snapshot. Once the legacy key is
// retired, this replica drops it from memory for good.
func (keyringCodec *KeyringEnvelopeCodec) Refresh(ctx context.Context) error {
	keyringCodec.refreshMutex.Lock()
	defer keyringCodec.refreshMutex.Unlock()
	snapshot, loadError := loadKeyringSnapshot(ctx, keyringCodec.factStore, keyringCodec.rootKeyProvider)
	if loadError != nil {
		return loadError
	}
	keyringCodec.mutex.Lock()
	keyringCodec.snapshot = snapshot
	droppedLegacyKey := snapshot.legacyKeyState == LegacyKeyRetired && keyringCodec.legacyKeyProvider != nil
	if droppedLegacyKey {
		keyringCodec.legacyKeyProvider = nil
	}
	keyringCodec.mutex.Unlock()
	if droppedLegacyKey {
		logging.Default().Warn("legacy secret key is retired: dropped from memory and never loaded again; delete /etc/ccattler/secret-master.key from this node")
	}
	return nil
}

// currentSnapshot returns the snapshot under the read lock.
func (keyringCodec *KeyringEnvelopeCodec) currentSnapshot() *keyringSnapshot {
	snapshot, _ := keyringCodec.currentState()
	return snapshot
}

// currentState returns the snapshot and the legacy key provider (nil when
// absent or retired) under one read lock.
func (keyringCodec *KeyringEnvelopeCodec) currentState() (*keyringSnapshot, KeyProvider) {
	keyringCodec.mutex.RLock()
	defer keyringCodec.mutex.RUnlock()
	return keyringCodec.snapshot, keyringCodec.legacyKeyProvider
}

// SealEnvelopeForKey writes v2 with the active KEK unless keyring/legacy is
// present, then v1 with the legacy key (upgraded clusters before the
// migration gate, while old agents may still decrypt locally).
func (keyringCodec *KeyringEnvelopeCodec) SealEnvelopeForKey(ctx context.Context, storeKey string, plaintext []byte) ([]byte, error) {
	snapshot, currentLegacyKeyProvider := keyringCodec.currentState()
	if snapshot.legacyKeyState.writesV2() {
		return sealEnvelopeV2(snapshot.keyEncryptionKeys[snapshot.activeKEKVersion], snapshot.activeKEKVersion, storeKey, plaintext)
	}
	if currentLegacyKeyProvider == nil {
		return nil, errors.New("seal: v1 writes need the legacy key, which this replica does not hold")
	}
	return SealEnvelope(ctx, currentLegacyKeyProvider, plaintext)
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
	snapshot, currentLegacyKeyProvider := keyringCodec.currentState()
	if snapshot.legacyKeyState == LegacyKeyRetired {
		return nil, errors.New("open: v1 envelope refused, the legacy key is retired")
	}
	if currentLegacyKeyProvider == nil {
		return nil, errors.New("open: v1 envelope needs the legacy key, which this replica does not hold")
	}
	return OpenEnvelope(ctx, currentLegacyKeyProvider, envelope)
}

// RunKeyringWatch refreshes once after subscribing (closing the gap since the
// codec load), then on keyring/ changes and every fallback interval. Returns
// nil when ctx ends, or ErrKeyringRootCheckFailed when the root key changed
// underneath (Flow 30): the replica must exit.
func (keyringCodec *KeyringEnvelopeCodec) RunKeyringWatch(ctx context.Context) error {
	keyringEvents := keyringCodec.subscribeKeyringWatch(ctx)
	if fatalError := keyringCodec.refreshKeyringFromWatch(ctx); fatalError != nil {
		return fatalError
	}
	fallbackTicker := time.NewTicker(keyringRefreshFallbackInterval)
	defer fallbackTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, isOpen := <-keyringEvents:
			if !isOpen && ctx.Err() == nil {
				logging.Default().Warn("keyring watch closed, re-subscribing")
				keyringEvents = keyringCodec.subscribeKeyringWatch(ctx)
			}
		case <-fallbackTicker.C:
			if keyringEvents == nil {
				keyringEvents = keyringCodec.subscribeKeyringWatch(ctx)
			}
		}
		if fatalError := keyringCodec.refreshKeyringFromWatch(ctx); fatalError != nil {
			return fatalError
		}
	}
}

// refreshKeyringFromWatch refreshes the snapshot for RunKeyringWatch. It
// returns ErrKeyringRootCheckFailed (fatal, the replica exits); any other
// refresh error is logged and the previous snapshot stays in use.
func (keyringCodec *KeyringEnvelopeCodec) refreshKeyringFromWatch(ctx context.Context) error {
	refreshError := keyringCodec.Refresh(ctx)
	if refreshError == nil || ctx.Err() != nil {
		return nil
	}
	if errors.Is(refreshError, ErrKeyringRootCheckFailed) {
		return refreshError
	}
	logging.Default().Error("keyring refresh failed", "error", refreshError.Error())
	return nil
}

// subscribeKeyringWatch watches keyring/ (the only authority for write mode).
// On failure it logs and returns nil; the fallback tick retries.
func (keyringCodec *KeyringEnvelopeCodec) subscribeKeyringWatch(ctx context.Context) <-chan store.Event {
	keyringEvents, watchError := keyringCodec.factStore.Watch(ctx, types.PrefixKeyring+"/", store.WatchOption{Prefix: true})
	if watchError != nil {
		logging.Default().Warn("keyring watch unavailable, retrying on the fallback interval", "error", watchError.Error())
		return nil
	}
	return keyringEvents
}

// keyringReplicaReportRootCheckOK is the root_check value a running replica
// reports; a replica whose root check fails exits instead of reporting.
const keyringReplicaReportRootCheckOK = "ok"

// keyringReplicaReport is the JSON a replica publishes at
// observed/keyring/replica/{node-id}.
type keyringReplicaReport struct {
	Version          string         `json:"version"`            // Version is the cca build version.
	ActiveKEKVersion int            `json:"active_kek_version"` // ActiveKEKVersion is the KEK this replica seals with.
	LegacyKeyState   LegacyKeyState `json:"legacy_key_state"`   // LegacyKeyState is the keyring state this replica acts on (v1 vs v2 writes).
	RootCheck        string         `json:"root_check"`         // RootCheck is keyringReplicaReportRootCheckOK.
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

// publishKeyringReplicaReport renews the report lease (granting a new one
// when it is gone) and rewrites the report with this replica's current
// keyring view, so the gates see what each live replica acts on now.
// Returns the lease in use, 0 after a failure (logged; retried next tick).
func (keyringCodec *KeyringEnvelopeCodec) publishKeyringReplicaReport(ctx context.Context, nodeID string, buildVersion string, leaseID int64) int64 {
	if leaseID != 0 {
		if _, renewError := keyringCodec.factStore.RenewLease(ctx, leaseID); renewError != nil {
			logging.Default().Warn("keyring replica report: lease renewal failed, granting a new lease", "error", renewError.Error())
			leaseID = 0
		}
	}
	if leaseID == 0 {
		newLeaseID, grantError := keyringCodec.factStore.GrantLease(ctx, keyringReplicaReportTTLSeconds)
		if grantError != nil {
			logging.Default().Warn("keyring replica report: lease grant failed", "error", grantError.Error())
			return 0
		}
		leaseID = newLeaseID
	}
	snapshot := keyringCodec.currentSnapshot()
	reportJSON, marshalError := json.Marshal(keyringReplicaReport{Version: buildVersion, ActiveKEKVersion: snapshot.activeKEKVersion,
		LegacyKeyState: snapshot.legacyKeyState, RootCheck: keyringReplicaReportRootCheckOK})
	if marshalError != nil {
		logging.Default().Warn("keyring replica report: encode failed", "error", marshalError.Error())
		return leaseID
	}
	if _, putError := keyringCodec.factStore.PutWithLease(ctx, types.KeyObservedKeyringReplica(nodeID), reportJSON, leaseID); putError != nil {
		logging.Default().Warn("keyring replica report: write failed", "error", putError.Error())
		return 0
	}
	return leaseID
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
