// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// SecretStorePrefix is the fact store prefix for encrypted secrets.
const SecretStorePrefix = "secrets/"

// SecretGrantPrefix is the fact store prefix for secret grants.
const SecretGrantPrefix = "desired/"

// SecretStore manages encrypted secrets in the fact store using envelope
// encryption. Each secret is encrypted with a per-secret data encryption key
// (DEK); the envelope codec wraps the DEK (keyring KEK for v2, legacy key for
// v1, Flow 29). Only the wrapped DEK and ciphertext are stored.
type SecretStore struct {
	factStore     store.StateStore    // factStore holds the encrypted secret blobs.
	envelopeCodec SecretEnvelopeCodec // envelopeCodec seals and opens envelopes bound to their store key.
	mutex         sync.RWMutex        // mutex serializes concurrent access to the store.
}

// NewSecretStore creates a secret store backed by the given fact store and
// envelope codec (the server passes its KeyringEnvelopeCodec).
func NewSecretStore(factStore store.StateStore, envelopeCodec SecretEnvelopeCodec) *SecretStore {
	return &SecretStore{
		factStore:     factStore,
		envelopeCodec: envelopeCodec,
	}
}

// NewSecretStoreWithMasterKey creates a secret store using a local 32-byte
// master key. This is a convenience wrapper that creates a LocalKeyProvider
// internally. Returns an error if the key is not exactly 32 bytes.
func NewSecretStoreWithMasterKey(factStore store.StateStore, masterKey []byte) (*SecretStore, error) {
	localKeyProvider, err := NewLocalKeyProvider(masterKey)
	if err != nil {
		return nil, err
	}
	return NewSecretStore(factStore, legacyOnlyEnvelopeCodec{keyProvider: localKeyProvider}), nil
}

// PutSecret encrypts and stores a secret value bound to secrets/{name} and
// keeps its value generation (M236 D1): 1 on create, unchanged (nothing
// written) for the same plaintext, +1 for a new one. Envelope and generation
// are written in one transaction guarded on the previous envelope revision.
func (secretStore *SecretStore) PutSecret(ctx context.Context, secretName string, plaintext []byte) error {
	secretStore.mutex.Lock()
	defer secretStore.mutex.Unlock()

	secretKey := SecretStorePrefix + secretName
	nextGeneration, envelopeGuard, valueChanged, generationError := secretStore.computeNextSecretGeneration(ctx, secretName, plaintext)
	if generationError != nil {
		return generationError
	}
	if !valueChanged {
		return nil
	}
	encodedEnvelope, err := SealSecretValue(ctx, secretStore.envelopeCodec, secretKey, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt secret %q: %w", secretName, err)
	}
	writeOperations := []store.Op{
		{Type: store.OpPut, Key: secretKey, Value: encodedEnvelope},
		{Type: store.OpPut, Key: types.KeyDesiredSecretGeneration(secretName), Value: []byte(strconv.FormatInt(nextGeneration, 10))},
	}
	committed, err := secretStore.factStore.Transaction(ctx, []store.Compare{envelopeGuard}, writeOperations, nil)
	if err != nil {
		return fmt.Errorf("store secret %q: %w", secretName, err)
	}
	if !committed {
		return fmt.Errorf("store secret %q: %w", secretName, errSecretChangedConcurrently)
	}
	return nil
}

// errSecretChangedConcurrently is returned by PutSecret and DeleteSecret when
// another writer changed the secret between reading and writing it; the
// caller may retry.
var errSecretChangedConcurrently = errors.New("secret changed concurrently, retry")

// computeNextSecretGeneration compares plaintext with the stored value and
// returns the generation to write, the guard on the current envelope, and
// whether anything must be written. An unreadable current value counts as
// changed; a missing generation fact counts as generation 1.
func (secretStore *SecretStore) computeNextSecretGeneration(ctx context.Context, secretName string, plaintext []byte) (int64, store.Compare, bool, error) {
	secretKey := SecretStorePrefix + secretName
	currentFact, getError := secretStore.factStore.Get(ctx, secretKey)
	if errors.Is(getError, store.ErrKeyNotFound) {
		return 1, store.Compare{Key: secretKey, Revision: 0}, true, nil
	}
	if getError != nil {
		return 0, store.Compare{}, false, fmt.Errorf("read secret %q: %w", secretName, getError)
	}
	envelopeGuard := store.Compare{Key: secretKey, Revision: currentFact.Revision}
	currentPlaintext, openError := secretStore.openStoredSecretValue(ctx, secretName, currentFact.Value)
	if openError == nil && subtle.ConstantTimeCompare(currentPlaintext, plaintext) == 1 {
		return 0, envelopeGuard, false, nil
	}
	currentGeneration := int64(1)
	generationFact, generationError := secretStore.factStore.Get(ctx, types.KeyDesiredSecretGeneration(secretName))
	if generationError != nil && !errors.Is(generationError, store.ErrKeyNotFound) {
		return 0, store.Compare{}, false, fmt.Errorf("read generation of secret %q: %w", secretName, generationError)
	}
	if generationError == nil {
		parsedGeneration, parseError := strconv.ParseInt(string(generationFact.Value), 10, 64)
		if parseError != nil || parsedGeneration < 1 {
			return 0, store.Compare{}, false, fmt.Errorf("invalid generation %q of secret %q", generationFact.Value, secretName)
		}
		currentGeneration = parsedGeneration
	}
	return currentGeneration + 1, envelopeGuard, true, nil
}

// SealSecretValue seals plaintext into the stored secret format for the
// store key it will be written to (secrets/{name}): an envelope encoded as
// base64. Used by PutSecret and by controllers that write system-owned secrets.
func SealSecretValue(ctx context.Context, envelopeCodec SecretEnvelopeCodec, secretKey string, plaintext []byte) ([]byte, error) {
	envelopeBlob, err := envelopeCodec.SealEnvelopeForKey(ctx, secretKey, plaintext)
	if err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(envelopeBlob)), nil
}

// GetSecret retrieves and decrypts a secret value using envelope encryption.
func (secretStore *SecretStore) GetSecret(ctx context.Context, secretName string) ([]byte, error) {
	return secretStore.GetSecretThroughStore(ctx, secretStore.factStore, secretName)
}

// GetSecretThroughStore reads the envelope through readStore (e.g. a store
// bound to the secret-delivery principal) and opens it with this store's codec.
func (secretStore *SecretStore) GetSecretThroughStore(ctx context.Context, readStore store.StateStore, secretName string) ([]byte, error) {
	secretStore.mutex.RLock()
	defer secretStore.mutex.RUnlock()

	secretKey := SecretStorePrefix + secretName
	fact, err := readStore.Get(ctx, secretKey)
	if err != nil {
		return nil, fmt.Errorf("secret %q not found: %w", secretName, err)
	}
	return secretStore.openStoredSecretValue(ctx, secretName, fact.Value)
}

// openStoredSecretValue decodes and opens the stored form (base64 envelope)
// of secretName. The caller holds the mutex.
func (secretStore *SecretStore) openStoredSecretValue(ctx context.Context, secretName string, storedValue []byte) ([]byte, error) {
	envelopeBlob, err := base64.StdEncoding.DecodeString(string(storedValue))
	if err != nil {
		return nil, fmt.Errorf("decode secret %q: %w", secretName, err)
	}

	plaintext, err := secretStore.envelopeCodec.OpenEnvelopeForKey(ctx, SecretStorePrefix+secretName, envelopeBlob)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret %q: %w", secretName, err)
	}

	return plaintext, nil
}

// DeleteSecret removes an encrypted secret and its value generation (M236 D1)
// from the store in one transaction guarded on the envelope's revision. A
// missing secret returns store.ErrKeyNotFound, as before M236.
func (secretStore *SecretStore) DeleteSecret(ctx context.Context, secretName string) error {
	secretStore.mutex.Lock()
	defer secretStore.mutex.Unlock()
	secretKey := SecretStorePrefix + secretName
	currentFact, getError := secretStore.factStore.Get(ctx, secretKey)
	if getError != nil {
		return getError
	}
	deleteOperations := []store.Op{
		{Type: store.OpDelete, Key: secretKey},
		{Type: store.OpDelete, Key: types.KeyDesiredSecretGeneration(secretName)},
	}
	committed, err := secretStore.factStore.Transaction(ctx, []store.Compare{{Key: secretKey, Revision: currentFact.Revision}}, deleteOperations, nil)
	if err != nil {
		return err
	}
	if !committed {
		return fmt.Errorf("delete secret %q: %w", secretName, errSecretChangedConcurrently)
	}
	return nil
}

// ListSecrets returns the names of all stored secrets.
func (secretStore *SecretStore) ListSecrets(ctx context.Context) ([]string, error) {
	facts, err := secretStore.factStore.Scan(ctx, SecretStorePrefix)
	if err != nil {
		return nil, err
	}

	secretNames := make([]string, 0, len(facts))
	for _, fact := range facts {
		secretName := strings.TrimPrefix(fact.Key, SecretStorePrefix)
		secretNames = append(secretNames, secretName)
	}
	return secretNames, nil
}

// IsAuthorized checks whether a service has a grant to access a secret.
// Grants are stored as facts at desired/service/{service}/secret/{name}.
func (secretStore *SecretStore) IsAuthorized(ctx context.Context, serviceName, secretName string) (bool, error) {
	grantKey := fmt.Sprintf("%sservice/%s/secret/%s", SecretGrantPrefix, serviceName, secretName)
	_, err := secretStore.factStore.Get(ctx, grantKey)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// GetSecretForService retrieves a secret only if the service has a grant.
// Returns the plaintext, mount path, and any error.
func (secretStore *SecretStore) GetSecretForService(ctx context.Context, serviceName, secretName string) ([]byte, string, error) {
	authorized, err := secretStore.IsAuthorized(ctx, serviceName, secretName)
	if err != nil {
		return nil, "", err
	}
	if !authorized {
		return nil, "", fmt.Errorf("service %q has no grant for secret %q", serviceName, secretName)
	}

	grantKey := fmt.Sprintf("%sservice/%s/secret/%s", SecretGrantPrefix, serviceName, secretName)
	grantFact, err := secretStore.factStore.Get(ctx, grantKey)
	if err != nil {
		return nil, "", err
	}
	mountPath := string(grantFact.Value)

	plaintext, err := secretStore.GetSecret(ctx, secretName)
	if err != nil {
		return nil, "", err
	}

	return plaintext, mountPath, nil
}

// GenerateMasterKey generates a random 32-byte master key for the secret store.
// Uses the same key generation as DEKs since both are 32-byte AES-256 keys.
func GenerateMasterKey() ([]byte, error) {
	return GenerateDEK()
}
