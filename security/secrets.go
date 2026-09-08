package security

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/boyadzhievb/ccattler/store"
)

// SecretStorePrefix is the fact store prefix for encrypted secrets.
const SecretStorePrefix = "/ccattler/secrets/"

// SecretGrantPrefix is the fact store prefix for secret grants.
const SecretGrantPrefix = "/ccattler/desired/"

// SecretStore manages encrypted secrets in the fact store using envelope
// encryption. Each secret is encrypted with a data encryption key (DEK)
// which is itself encrypted with the master key (KEK).
type SecretStore struct {
	factStore store.StateStore
	masterKey []byte // 32-byte AES-256 key encryption key
	mutex     sync.RWMutex
}

// NewSecretStore creates a secret store backed by the given fact store.
// The masterKey must be exactly 32 bytes for AES-256 encryption.
func NewSecretStore(factStore store.StateStore, masterKey []byte) (*SecretStore, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	return &SecretStore{
		factStore: factStore,
		masterKey: masterKey,
	}, nil
}

// PutSecret encrypts and stores a secret value. The encrypted ciphertext is
// stored as a base64-encoded fact.
func (secretStore *SecretStore) PutSecret(ctx context.Context, secretName string, plaintext []byte) error {
	secretStore.mutex.Lock()
	defer secretStore.mutex.Unlock()

	ciphertext, err := encryptAESGCM(secretStore.masterKey, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt secret %q: %w", secretName, err)
	}

	encodedCiphertext := base64.StdEncoding.EncodeToString(ciphertext)
	secretKey := SecretStorePrefix + secretName
	if _, err := secretStore.factStore.Put(ctx, secretKey, []byte(encodedCiphertext)); err != nil {
		return fmt.Errorf("store secret %q: %w", secretName, err)
	}

	return nil
}

// GetSecret retrieves and decrypts a secret value.
func (secretStore *SecretStore) GetSecret(ctx context.Context, secretName string) ([]byte, error) {
	secretStore.mutex.RLock()
	defer secretStore.mutex.RUnlock()

	secretKey := SecretStorePrefix + secretName
	fact, err := secretStore.factStore.Get(ctx, secretKey)
	if err != nil {
		return nil, fmt.Errorf("secret %q not found", secretName)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(string(fact.Value))
	if err != nil {
		return nil, fmt.Errorf("decode secret %q: %w", secretName, err)
	}

	plaintext, err := decryptAESGCM(secretStore.masterKey, ciphertext)
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

// encryptAESGCM encrypts plaintext using AES-256-GCM with a random nonce.
func encryptAESGCM(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return aesGCM.Seal(nonce, nonce, plaintext, nil), nil
}

// decryptAESGCM decrypts ciphertext produced by encryptAESGCM.
func decryptAESGCM(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextBody := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return aesGCM.Open(nil, nonce, ciphertextBody, nil)
}

// GenerateMasterKey generates a random 32-byte master key for the secret store.
func GenerateMasterKey() ([]byte, error) {
	masterKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	return masterKey, nil
}
