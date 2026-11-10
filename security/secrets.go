// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"github.com/boyadzhievb/ccattler/store"
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

// PutSecret encrypts and stores a secret value using envelope encryption,
// bound to its store key secrets/{name}. The envelope blob is base64-encoded
// before storage.
func (secretStore *SecretStore) PutSecret(ctx context.Context, secretName string, plaintext []byte) error {
	secretStore.mutex.Lock()
	defer secretStore.mutex.Unlock()

	secretKey := SecretStorePrefix + secretName
	encodedEnvelope, err := SealSecretValue(ctx, secretStore.envelopeCodec, secretKey, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt secret %q: %w", secretName, err)
	}

	if _, err := secretStore.factStore.Put(ctx, secretKey, encodedEnvelope); err != nil {
		return fmt.Errorf("store secret %q: %w", secretName, err)
	}

	return nil
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

	envelopeBlob, err := base64.StdEncoding.DecodeString(string(fact.Value))
	if err != nil {
		return nil, fmt.Errorf("decode secret %q: %w", secretName, err)
	}

	plaintext, err := secretStore.envelopeCodec.OpenEnvelopeForKey(ctx, secretKey, envelopeBlob)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret %q: %w", secretName, err)
	}

	return plaintext, nil
}

// DeleteSecret removes an encrypted secret from the store.
func (secretStore *SecretStore) DeleteSecret(ctx context.Context, secretName string) error {
	secretKey := SecretStorePrefix + secretName
	return secretStore.factStore.Delete(ctx, secretKey)
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
