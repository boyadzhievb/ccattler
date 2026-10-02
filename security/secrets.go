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
// (DEK), and the DEK is wrapped by the configured KeyProvider. Only the
// wrapped DEK and ciphertext are stored — the master key never touches
// secret plaintext directly.
type SecretStore struct {
	factStore   store.StateStore // factStore holds the encrypted secret blobs.
	keyProvider KeyProvider      // keyProvider wraps and unwraps per-secret DEKs.
	mutex       sync.RWMutex     // mutex serializes concurrent access to the store.
}

// NewSecretStore creates a secret store backed by the given fact store and
// KeyProvider. The KeyProvider handles all key wrapping — for local keys use
// NewLocalKeyProvider, for cloud KMS use the appropriate provider.
func NewSecretStore(factStore store.StateStore, keyProvider KeyProvider) *SecretStore {
	return &SecretStore{
		factStore:   factStore,
		keyProvider: keyProvider,
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
	return NewSecretStore(factStore, localKeyProvider), nil
}

// PutSecret encrypts and stores a secret value using envelope encryption.
// A random DEK is generated for each secret, the plaintext is encrypted with
// the DEK, and the DEK is wrapped by the KeyProvider. The envelope blob is
// base64-encoded before storage.
func (secretStore *SecretStore) PutSecret(ctx context.Context, secretName string, plaintext []byte) error {
	secretStore.mutex.Lock()
	defer secretStore.mutex.Unlock()

	envelopeBlob, err := SealEnvelope(ctx, secretStore.keyProvider, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt secret %q: %w", secretName, err)
	}

	encodedEnvelope := base64.StdEncoding.EncodeToString(envelopeBlob)
	secretKey := SecretStorePrefix + secretName
	if _, err := secretStore.factStore.Put(ctx, secretKey, []byte(encodedEnvelope)); err != nil {
		return fmt.Errorf("store secret %q: %w", secretName, err)
	}

	return nil
}

// GetSecret retrieves and decrypts a secret value using envelope encryption.
func (secretStore *SecretStore) GetSecret(ctx context.Context, secretName string) ([]byte, error) {
	secretStore.mutex.RLock()
	defer secretStore.mutex.RUnlock()

	secretKey := SecretStorePrefix + secretName
	fact, err := secretStore.factStore.Get(ctx, secretKey)
	if err != nil {
		return nil, fmt.Errorf("secret %q not found", secretName)
	}

	envelopeBlob, err := base64.StdEncoding.DecodeString(string(fact.Value))
	if err != nil {
		return nil, fmt.Errorf("decode secret %q: %w", secretName, err)
	}

	plaintext, err := OpenEnvelope(ctx, secretStore.keyProvider, envelopeBlob)
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

// RotateKeyProvider re-wraps all stored secrets' DEKs from the old provider
// to a new provider without decrypting or re-encrypting the secret payloads.
// After rotation, the SecretStore's active KeyProvider is updated to the new
// one. This is the master key rotation operation.
func (secretStore *SecretStore) RotateKeyProvider(ctx context.Context, newKeyProvider KeyProvider) error {
	secretStore.mutex.Lock()
	defer secretStore.mutex.Unlock()

	facts, err := secretStore.factStore.Scan(ctx, SecretStorePrefix)
	if err != nil {
		return fmt.Errorf("rotate: scan secrets: %w", err)
	}

	for _, fact := range facts {
		envelopeBlob, decodeErr := base64.StdEncoding.DecodeString(string(fact.Value))
		if decodeErr != nil {
			return fmt.Errorf("rotate: decode %s: %w", fact.Key, decodeErr)
		}

		rewrappedEnvelope, rewrapErr := RewrapEnvelope(ctx, secretStore.keyProvider, newKeyProvider, envelopeBlob)
		if rewrapErr != nil {
			return fmt.Errorf("rotate: rewrap %s: %w", fact.Key, rewrapErr)
		}

		encodedEnvelope := base64.StdEncoding.EncodeToString(rewrappedEnvelope)
		if _, putErr := secretStore.factStore.Put(ctx, fact.Key, []byte(encodedEnvelope)); putErr != nil {
			return fmt.Errorf("rotate: store %s: %w", fact.Key, putErr)
		}
	}

	secretStore.keyProvider = newKeyProvider
	return nil
}

// KeyProviderName returns the name of the active KeyProvider.
func (secretStore *SecretStore) KeyProviderName() string {
	return secretStore.keyProvider.ProviderName()
}

// GenerateMasterKey generates a random 32-byte master key for the secret store.
// Uses the same key generation as DEKs since both are 32-byte AES-256 keys.
func GenerateMasterKey() ([]byte, error) {
	return GenerateDEK()
}
