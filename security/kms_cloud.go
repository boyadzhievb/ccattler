//go:build cloudprovider

// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmsTypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	gcpkms "cloud.google.com/go/kms/apiv1"
	gcpkmspb "cloud.google.com/go/kms/apiv1/kmspb"
)

func init() {
	RegisterKMSKeyProviderFactory("aws-kms", func(ctx context.Context, keyID string, region string) (KeyProvider, error) {
		return NewAWSKMSKeyProvider(ctx, keyID, region)
	})
	RegisterKMSKeyProviderFactory("gcp-kms", func(ctx context.Context, keyID string, _ string) (KeyProvider, error) {
		return NewGCPKMSKeyProvider(ctx, keyID)
	})
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
