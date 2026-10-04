// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmsTypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	gcpkms "cloud.google.com/go/kms/apiv1"
	gcpkmspb "cloud.google.com/go/kms/apiv1/kmspb"
)

// KeyProvider wraps and unwraps data encryption keys (DEKs). Each
// implementation delegates to a different key management backend: a local
// 32-byte file, AWS KMS, GCP KMS, or HashiCorp Vault Transit.
//
// The CCattler secret store uses envelope encryption: each secret is encrypted
// with a random DEK, and the DEK is wrapped (encrypted) by the KeyProvider.
// Only the wrapped DEK + ciphertext are stored. This means the master key
// never touches secret plaintext directly, and key rotation only requires
// re-wrapping DEKs — not re-encrypting every secret.
type KeyProvider interface {
	// WrapKey encrypts a plaintext data encryption key (DEK) using the
	// provider's master key. Returns the wrapped (encrypted) DEK.
	WrapKey(ctx context.Context, plaintextDEK []byte) ([]byte, error)

	// UnwrapKey decrypts a wrapped DEK, returning the original plaintext DEK.
	UnwrapKey(ctx context.Context, wrappedDEK []byte) ([]byte, error)

	// ProviderName returns a short identifier for this provider (e.g. "local",
	// "aws-kms", "gcp-kms", "vault-transit").
	ProviderName() string
}

// dekLength is the size of generated data encryption keys in bytes (AES-256).
const dekLength = 32

// LocalKeyProvider wraps and unwraps DEKs using a local 32-byte AES-256 master
// key. This is the simplest provider — suitable for development, single-node
// clusters, or environments where KMS is unavailable. The master key must be
// stored securely on disk and is never transmitted over the network.
type LocalKeyProvider struct {
	masterKey []byte     // masterKey is the 32-byte AES-256 key encryption key.
	mutex     sync.Mutex // mutex serializes wrap/unwrap to avoid nonce reuse under concurrency.
}

// NewLocalKeyProvider creates a LocalKeyProvider from a 32-byte master key.
// Returns an error if the key is not exactly 32 bytes.
func NewLocalKeyProvider(masterKey []byte) (*LocalKeyProvider, error) {
	if len(masterKey) != dekLength {
		return nil, fmt.Errorf("local key provider: master key must be %d bytes, got %d", dekLength, len(masterKey))
	}
	return &LocalKeyProvider{masterKey: masterKey}, nil
}

// WrapKey encrypts the plaintext DEK using AES-256-GCM with the local master
// key. The returned bytes contain the nonce prepended to the ciphertext.
func (localKeyProvider *LocalKeyProvider) WrapKey(_ context.Context, plaintextDEK []byte) ([]byte, error) {
	localKeyProvider.mutex.Lock()
	defer localKeyProvider.mutex.Unlock()
	return encryptWithDEK(localKeyProvider.masterKey, plaintextDEK)
}

// UnwrapKey decrypts a wrapped DEK using AES-256-GCM with the local master key.
func (localKeyProvider *LocalKeyProvider) UnwrapKey(_ context.Context, wrappedDEK []byte) ([]byte, error) {
	localKeyProvider.mutex.Lock()
	defer localKeyProvider.mutex.Unlock()
	return decryptWithDEK(localKeyProvider.masterKey, wrappedDEK)
}

// ProviderName returns "local".
func (localKeyProvider *LocalKeyProvider) ProviderName() string {
	return "local"
}

// GenerateDEK creates a random 32-byte data encryption key for envelope
// encryption. Each secret gets its own DEK.
func GenerateDEK() ([]byte, error) {
	dataEncryptionKey := make([]byte, dekLength)
	if _, err := io.ReadFull(rand.Reader, dataEncryptionKey); err != nil {
		return nil, fmt.Errorf("generate DEK: %w", err)
	}
	return dataEncryptionKey, nil
}

// envelopeHeaderSize is the byte count of the 4-byte length prefix for the
// wrapped DEK in the envelope format.
const envelopeHeaderSize = 4

// SealEnvelope encrypts plaintext using envelope encryption: generates a random
// DEK, encrypts the plaintext with the DEK, wraps the DEK via the KeyProvider,
// and returns a single blob: [4-byte wrappedDEK length][wrappedDEK][ciphertext].
func SealEnvelope(ctx context.Context, keyProvider KeyProvider, plaintext []byte) ([]byte, error) {
	dataEncryptionKey, err := GenerateDEK()
	if err != nil {
		return nil, err
	}

	ciphertext, err := encryptWithDEK(dataEncryptionKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal envelope: encrypt: %w", err)
	}

	wrappedDEK, err := keyProvider.WrapKey(ctx, dataEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("seal envelope: wrap DEK: %w", err)
	}

	return packEnvelope(wrappedDEK, ciphertext), nil
}

// OpenEnvelope decrypts an envelope blob: extracts the wrapped DEK, unwraps it
// via the KeyProvider, then decrypts the ciphertext with the recovered DEK.
func OpenEnvelope(ctx context.Context, keyProvider KeyProvider, envelope []byte) ([]byte, error) {
	wrappedDEK, ciphertext, err := unpackEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	dataEncryptionKey, err := keyProvider.UnwrapKey(ctx, wrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("open envelope: unwrap DEK: %w", err)
	}

	plaintext, err := decryptWithDEK(dataEncryptionKey, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("open envelope: decrypt: %w", err)
	}

	return plaintext, nil
}

// RewrapEnvelope re-wraps an existing envelope's DEK with a new KeyProvider
// without decrypting or re-encrypting the secret payload. This is the key
// rotation operation: the ciphertext is unchanged, only the DEK wrapper
// changes.
func RewrapEnvelope(ctx context.Context, oldKeyProvider KeyProvider, newKeyProvider KeyProvider, envelope []byte) ([]byte, error) {
	wrappedDEK, ciphertext, err := unpackEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	dataEncryptionKey, err := oldKeyProvider.UnwrapKey(ctx, wrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("rewrap: unwrap with old provider: %w", err)
	}

	newWrappedDEK, err := newKeyProvider.WrapKey(ctx, dataEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("rewrap: wrap with new provider: %w", err)
	}

	return packEnvelope(newWrappedDEK, ciphertext), nil
}

// packEnvelope serializes a wrapped DEK and ciphertext into the envelope
// binary format: [4-byte wrappedDEK length (big-endian)][wrappedDEK][ciphertext].
func packEnvelope(wrappedDEK []byte, ciphertext []byte) []byte {
	envelope := make([]byte, envelopeHeaderSize+len(wrappedDEK)+len(ciphertext))
	binary.BigEndian.PutUint32(envelope[:envelopeHeaderSize], uint32(len(wrappedDEK))) //nolint:gosec // wrappedDEK is a KMS-wrapped key, always < 1KB
	copy(envelope[envelopeHeaderSize:], wrappedDEK)
	copy(envelope[envelopeHeaderSize+len(wrappedDEK):], ciphertext)
	return envelope
}

// unpackEnvelope splits an envelope blob into its wrapped DEK and ciphertext.
func unpackEnvelope(envelope []byte) ([]byte, []byte, error) {
	if len(envelope) < envelopeHeaderSize {
		return nil, nil, fmt.Errorf("envelope too short: %d bytes", len(envelope))
	}
	wrappedDEKLength := binary.BigEndian.Uint32(envelope[:envelopeHeaderSize])
	if int(wrappedDEKLength) > len(envelope)-envelopeHeaderSize {
		return nil, nil, fmt.Errorf("envelope corrupt: wrapped DEK length %d exceeds data", wrappedDEKLength)
	}
	wrappedDEK := envelope[envelopeHeaderSize : envelopeHeaderSize+wrappedDEKLength]
	ciphertext := envelope[envelopeHeaderSize+wrappedDEKLength:]
	return wrappedDEK, ciphertext, nil
}

// encryptWithDEK encrypts plaintext using AES-256-GCM with a data encryption key.
func encryptWithDEK(dataEncryptionKey []byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(dataEncryptionKey)
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

// decryptWithDEK decrypts ciphertext produced by encryptWithDEK.
func decryptWithDEK(dataEncryptionKey []byte, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(dataEncryptionKey)
	if err != nil {
		return nil, err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short for nonce")
	}
	nonce, ciphertextBody := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return aesGCM.Open(nil, nonce, ciphertextBody, nil)
}

// AWSKMSKeyProvider wraps and unwraps DEKs using AWS KMS Encrypt/Decrypt.
// The keyID is the ARN or alias of the KMS key (e.g.
// "arn:aws:kms:us-east-1:123:key/abc" or "alias/ccattler-secrets").
type AWSKMSKeyProvider struct {
	keyID     string         // keyID is the AWS KMS key ARN or alias.
	region    string         // region is the AWS region for KMS API calls.
	kmsClient *awskms.Client // kmsClient is the AWS KMS API client.
}

// NewAWSKMSKeyProvider creates a provider that delegates wrap/unwrap to AWS
// KMS. Loads credentials from the default AWS credential chain. Returns an
// error if the SDK config cannot be loaded.
func NewAWSKMSKeyProvider(ctx context.Context, keyID string, region string) (*AWSKMSKeyProvider, error) {
	sdkConfig, loadError := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if loadError != nil {
		return nil, fmt.Errorf("aws-kms: load SDK config: %w", loadError)
	}
	return &AWSKMSKeyProvider{
		keyID:     keyID,
		region:    region,
		kmsClient: awskms.NewFromConfig(sdkConfig),
	}, nil
}

// WrapKey encrypts the plaintext DEK using AWS KMS Encrypt with the configured
// key. The encrypted ciphertext blob is suitable for storage alongside the
// secret ciphertext.
func (awsKMSProvider *AWSKMSKeyProvider) WrapKey(ctx context.Context, plaintextDEK []byte) ([]byte, error) {
	encryptInput := &awskms.EncryptInput{
		KeyId:               &awsKMSProvider.keyID,
		Plaintext:           plaintextDEK,
		EncryptionAlgorithm: kmsTypes.EncryptionAlgorithmSpecSymmetricDefault,
	}
	encryptOutput, encryptError := awsKMSProvider.kmsClient.Encrypt(ctx, encryptInput)
	if encryptError != nil {
		return nil, fmt.Errorf("aws-kms: Encrypt(key=%s): %w", awsKMSProvider.keyID, encryptError)
	}
	return encryptOutput.CiphertextBlob, nil
}

// UnwrapKey decrypts the wrapped DEK using AWS KMS Decrypt. The key ID used
// for encryption is embedded in the ciphertext blob, so KMS can determine
// which key to use.
func (awsKMSProvider *AWSKMSKeyProvider) UnwrapKey(ctx context.Context, wrappedDEK []byte) ([]byte, error) {
	decryptInput := &awskms.DecryptInput{
		CiphertextBlob:      wrappedDEK,
		EncryptionAlgorithm: kmsTypes.EncryptionAlgorithmSpecSymmetricDefault,
	}
	decryptOutput, decryptError := awsKMSProvider.kmsClient.Decrypt(ctx, decryptInput)
	if decryptError != nil {
		return nil, fmt.Errorf("aws-kms: Decrypt: %w", decryptError)
	}
	return decryptOutput.Plaintext, nil
}

// ProviderName returns "aws-kms".
func (awsKMSProvider *AWSKMSKeyProvider) ProviderName() string {
	return "aws-kms"
}

// GCPKMSKeyProvider wraps and unwraps DEKs using Google Cloud KMS
// Encrypt/Decrypt. The keyName is the full resource name (e.g.
// "projects/P/locations/L/keyRings/R/cryptoKeys/K").
type GCPKMSKeyProvider struct {
	keyName   string                      // keyName is the full GCP KMS key resource name.
	kmsClient *gcpkms.KeyManagementClient // kmsClient is the GCP Cloud KMS API client.
}

// NewGCPKMSKeyProvider creates a provider that delegates wrap/unwrap to GCP
// Cloud KMS. Uses Application Default Credentials. Returns an error if the
// client cannot be created.
func NewGCPKMSKeyProvider(ctx context.Context, keyName string) (*GCPKMSKeyProvider, error) {
	kmsClient, clientError := gcpkms.NewKeyManagementClient(ctx)
	if clientError != nil {
		return nil, fmt.Errorf("gcp-kms: create client: %w", clientError)
	}
	return &GCPKMSKeyProvider{
		keyName:   keyName,
		kmsClient: kmsClient,
	}, nil
}

// WrapKey encrypts the plaintext DEK using GCP Cloud KMS Encrypt with the
// configured key.
func (gcpKMSProvider *GCPKMSKeyProvider) WrapKey(ctx context.Context, plaintextDEK []byte) ([]byte, error) {
	encryptRequest := &gcpkmspb.EncryptRequest{
		Name:      gcpKMSProvider.keyName,
		Plaintext: plaintextDEK,
	}
	encryptResponse, encryptError := gcpKMSProvider.kmsClient.Encrypt(ctx, encryptRequest)
	if encryptError != nil {
		return nil, fmt.Errorf("gcp-kms: Encrypt(key=%s): %w", gcpKMSProvider.keyName, encryptError)
	}
	return encryptResponse.Ciphertext, nil
}

// UnwrapKey decrypts the wrapped DEK using GCP Cloud KMS Decrypt.
func (gcpKMSProvider *GCPKMSKeyProvider) UnwrapKey(ctx context.Context, wrappedDEK []byte) ([]byte, error) {
	decryptRequest := &gcpkmspb.DecryptRequest{
		Name:       gcpKMSProvider.keyName,
		Ciphertext: wrappedDEK,
	}
	decryptResponse, decryptError := gcpKMSProvider.kmsClient.Decrypt(ctx, decryptRequest)
	if decryptError != nil {
		return nil, fmt.Errorf("gcp-kms: Decrypt(key=%s): %w", gcpKMSProvider.keyName, decryptError)
	}
	return decryptResponse.Plaintext, nil
}

// ProviderName returns "gcp-kms".
func (gcpKMSProvider *GCPKMSKeyProvider) ProviderName() string {
	return "gcp-kms"
}

// VaultTransitKeyProvider wraps and unwraps DEKs using HashiCorp Vault's
// Transit secrets engine. The keyName is the name of the Transit key.
type VaultTransitKeyProvider struct {
	vaultAddress string // vaultAddress is the Vault server URL (e.g. "https://vault:8200").
	keyName      string // keyName is the Transit key name.
	mountPath    string // mountPath is the Transit engine mount (default "transit").
}

// NewVaultTransitKeyProvider creates a provider that delegates wrap/unwrap to
// Vault Transit. Requires VAULT_TOKEN or Vault agent authentication.
func NewVaultTransitKeyProvider(vaultAddress string, keyName string, mountPath string) *VaultTransitKeyProvider {
	if mountPath == "" {
		mountPath = "transit"
	}
	return &VaultTransitKeyProvider{vaultAddress: vaultAddress, keyName: keyName, mountPath: mountPath}
}

// WrapKey encrypts the DEK using Vault Transit encrypt.
func (vaultProvider *VaultTransitKeyProvider) WrapKey(ctx context.Context, plaintextDEK []byte) ([]byte, error) {
	return nil, fmt.Errorf("vault-transit: WrapKey not yet connected (addr=%s, key=%s) — requires vault API client", vaultProvider.vaultAddress, vaultProvider.keyName)
}

// UnwrapKey decrypts the wrapped DEK using Vault Transit decrypt.
func (vaultProvider *VaultTransitKeyProvider) UnwrapKey(ctx context.Context, wrappedDEK []byte) ([]byte, error) {
	return nil, fmt.Errorf("vault-transit: UnwrapKey not yet connected (addr=%s, key=%s) — requires vault API client", vaultProvider.vaultAddress, vaultProvider.keyName)
}

// ProviderName returns "vault-transit".
func (vaultProvider *VaultTransitKeyProvider) ProviderName() string {
	return "vault-transit"
}
