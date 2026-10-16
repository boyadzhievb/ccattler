// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"fmt"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// KMSSecretProvider decrypts secrets at materialization time using a cloud KMS
// KeyProvider. The agent creates this provider with scoped KMS credentials tied
// to the node's identity, so only the agent's node can unwrap the per-secret
// DEKs. This implements the SecretProvider interface.
type KMSSecretProvider struct {
	secretStore *security.SecretStore // secretStore handles envelope decryption via the KeyProvider.
}

// NewKMSSecretProvider creates a KMSSecretProvider that decrypts secrets
// using the given KeyProvider (e.g. AWSKMSKeyProvider or GCPKMSKeyProvider).
// The KeyProvider wraps and unwraps per-secret DEKs via the cloud KMS service.
func NewKMSSecretProvider(factStore store.StateStore, keyProvider security.KeyProvider) *KMSSecretProvider {
	return &KMSSecretProvider{
		secretStore: security.NewSecretStore(factStore, keyProvider),
	}
}

// NewLocalSecretProvider creates a SecretProvider using a local 32-byte master
// key for envelope encryption. This is the default for single-node or
// development clusters where cloud KMS is not configured.
func NewLocalSecretProvider(factStore store.StateStore, masterKey []byte) (*KMSSecretProvider, error) {
	secretStoreInstance, constructionError := security.NewSecretStoreWithMasterKey(factStore, masterKey)
	if constructionError != nil {
		return nil, fmt.Errorf("create local secret provider: %w", constructionError)
	}
	return &KMSSecretProvider{secretStore: secretStoreInstance}, nil
}

// GetSecretForService retrieves and decrypts a secret for the given service
// using the configured KeyProvider. The decryption happens at this call site —
// the agent does not hold any plaintext secrets in memory between calls.
func (kmsProvider *KMSSecretProvider) GetSecretForService(ctx context.Context, serviceName string, secretName string) ([]byte, string, error) {
	return kmsProvider.secretStore.GetSecretForService(ctx, serviceName, secretName)
}

// defaultAWSKMSRegion is the fallback region when the key ARN does not contain
// a region component.
const defaultAWSKMSRegion = "us-east-1"

// CreateAgentKeyProvider creates the appropriate KeyProvider based on the
// provider name and key identifier. Cloud KMS providers (aws-kms, gcp-kms) are
// available only when built with -tags cloudprovider; without that tag the
// registry lookup returns nil and an actionable error is returned.
func CreateAgentKeyProvider(ctx context.Context, providerName string, keyID string) (security.KeyProvider, error) {
	factory := security.LookupKMSKeyProviderFactory(providerName)
	if factory == nil {
		return nil, fmt.Errorf("unsupported KMS provider %q — build with -tags cloudprovider for aws-kms/gcp-kms", providerName)
	}
	var region string
	if providerName == "aws-kms" {
		region = extractRegionFromAWSKeyARN(keyID)
		if region == "" {
			region = defaultAWSKMSRegion
		}
	}
	return factory(ctx, keyID, region)
}

// extractRegionFromAWSKeyARN extracts the AWS region from a KMS key ARN.
// ARN format: arn:aws:kms:REGION:ACCOUNT:key/KEY-ID
// Returns empty string if the ARN doesn't match the expected format.
func extractRegionFromAWSKeyARN(keyARN string) string {
	const arnMinParts = 6
	const regionPartIndex = 3
	parts := splitString(keyARN, ":", arnMinParts+1)
	if len(parts) >= arnMinParts && parts[0] == "arn" && parts[2] == "kms" {
		return parts[regionPartIndex]
	}
	return ""
}

// splitString splits a string by the separator and returns up to maxParts parts.
func splitString(input string, separator string, maxParts int) []string {
	result := make([]string, 0, maxParts)
	remaining := input
	for len(result) < maxParts-1 {
		separatorIndex := indexOf(remaining, separator)
		if separatorIndex < 0 {
			break
		}
		result = append(result, remaining[:separatorIndex])
		remaining = remaining[separatorIndex+len(separator):]
	}
	result = append(result, remaining)
	return result
}

// indexOf returns the index of the first occurrence of substr in s, or -1.
func indexOf(inputString string, substring string) int {
	for charIndex := 0; charIndex <= len(inputString)-len(substring); charIndex++ {
		if inputString[charIndex:charIndex+len(substring)] == substring {
			return charIndex
		}
	}
	return -1
}
