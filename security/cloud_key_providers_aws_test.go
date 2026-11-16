//go:build cloudprovider

// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmsTypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

const (
	// awsKMSIntegrationTestProviderName is the `--root-key-provider` value for AWS KMS.
	awsKMSIntegrationTestProviderName = "aws-kms"
	// awsKMSIntegrationTestUnknownKeyID is a well-formed key ID no KMS key has.
	awsKMSIntegrationTestUnknownKeyID = "00000000-0000-0000-0000-000000000000"
	// awsKMSIntegrationTestBitFlipMask inverts every bit of the byte it is XORed with.
	awsKMSIntegrationTestBitFlipMask = 0xFF
	// awsKMSIntegrationTestRegion is the region the AWS Emulator serves.
	awsKMSIntegrationTestRegion = "us-east-1"
	// awsKMSIntegrationTestAlias is the alias the alias test points at its key.
	awsKMSIntegrationTestAlias = "alias/ccattler-kms-integration-test"
	// awsKMSIntegrationTestKeyDeletionDays is the shortest deletion window KMS accepts.
	awsKMSIntegrationTestKeyDeletionDays = 7
	// awsKMSIntegrationTestDEKBytes is the size of a data encryption key (AES-256).
	awsKMSIntegrationTestDEKBytes = 32
)

// skipWithoutLocalAWSEmulator skips unless the SDK points at a local AWS
// Emulator (scripts/aws-emulator.sh). These tests create KMS keys, which cost
// money and cannot be deleted at once on real AWS, so they never run there.
func skipWithoutLocalAWSEmulator(testHandle *testing.T) {
	testHandle.Helper()
	if !strings.HasPrefix(os.Getenv("AWS_ENDPOINT_URL"), "https://127.0.0.1:") ||
		os.Getenv("AWS_SHARED_CREDENTIALS_FILE") == "" {
		testHandle.Skip("skipping: no local AWS Emulator (run scripts/aws-emulator.sh test)")
	}
	// A service-specific endpoint overrides AWS_ENDPOINT_URL and could be real AWS.
	if os.Getenv("AWS_ENDPOINT_URL_KMS") != "" {
		testHandle.Skip("skipping: AWS_ENDPOINT_URL_KMS is set, KMS requests would bypass the emulator")
	}
}

// createAWSKMSIntegrationTestKey creates a symmetric KMS key in the emulator,
// schedules its deletion at test end and returns the client and the key ID.
func createAWSKMSIntegrationTestKey(testHandle *testing.T) (*awskms.Client, string) {
	testHandle.Helper()
	testContext := context.Background()
	sdkConfig, loadError := awsconfig.LoadDefaultConfig(testContext, awsconfig.WithRegion(awsKMSIntegrationTestRegion))
	if loadError != nil {
		testHandle.Fatalf("load SDK config: %v", loadError)
	}
	kmsClient := awskms.NewFromConfig(sdkConfig)
	createOutput, createError := kmsClient.CreateKey(testContext, &awskms.CreateKeyInput{
		Description: aws.String("ccattler KMS integration test"),
	})
	if createError != nil {
		testHandle.Fatalf("CreateKey: %v", createError)
	}
	keyID := aws.ToString(createOutput.KeyMetadata.KeyId)
	testHandle.Cleanup(func() {
		_, scheduleError := kmsClient.ScheduleKeyDeletion(testContext, &awskms.ScheduleKeyDeletionInput{
			KeyId:               aws.String(keyID),
			PendingWindowInDays: aws.Int32(awsKMSIntegrationTestKeyDeletionDays),
		})
		if scheduleError != nil {
			testHandle.Logf("ScheduleKeyDeletion(%s): %v", keyID, scheduleError)
		}
	})
	return kmsClient, keyID
}

// newAWSKMSIntegrationTestProvider builds the provider the way `cca server
// --root-key-provider aws-kms` does: through the registered factory.
func newAWSKMSIntegrationTestProvider(testHandle *testing.T, keyID string) KeyProvider {
	testHandle.Helper()
	providerFactory := LookupCloudKeyProviderFactory(awsKMSIntegrationTestProviderName)
	if providerFactory == nil {
		testHandle.Fatal("aws-kms factory not registered in a cloudprovider build")
	}
	keyProvider, providerError := providerFactory(context.Background(), keyID, awsKMSIntegrationTestRegion)
	if providerError != nil {
		testHandle.Fatalf("aws-kms factory: %v", providerError)
	}
	return keyProvider
}

// newAWSKMSIntegrationTestDEK returns a random data encryption key.
func newAWSKMSIntegrationTestDEK(testHandle *testing.T) []byte {
	testHandle.Helper()
	plaintextDEK := make([]byte, awsKMSIntegrationTestDEKBytes)
	if _, randomError := rand.Read(plaintextDEK); randomError != nil {
		testHandle.Fatalf("random DEK: %v", randomError)
	}
	return plaintextDEK
}

// TestAWSIntegrationKMSWrapUnwrapRoundTrip wraps a DEK with a KMS key and gets
// the same DEK back; the wrapped form must not contain the plaintext.
func TestAWSIntegrationKMSWrapUnwrapRoundTrip(testHandle *testing.T) {
	skipWithoutLocalAWSEmulator(testHandle)
	_, keyID := createAWSKMSIntegrationTestKey(testHandle)
	keyProvider := newAWSKMSIntegrationTestProvider(testHandle, keyID)
	testContext := context.Background()

	if providerName := keyProvider.ProviderName(); providerName != awsKMSIntegrationTestProviderName {
		testHandle.Errorf("ProviderName: got %q, want %q", providerName, awsKMSIntegrationTestProviderName)
	}
	plaintextDEK := newAWSKMSIntegrationTestDEK(testHandle)
	wrappedDEK, wrapError := keyProvider.WrapKey(testContext, plaintextDEK)
	if wrapError != nil {
		testHandle.Fatalf("WrapKey: %v", wrapError)
	}
	if len(wrappedDEK) == 0 || bytes.Contains(wrappedDEK, plaintextDEK) {
		testHandle.Fatalf("wrapped DEK is empty or contains the plaintext (%d bytes)", len(wrappedDEK))
	}
	unwrappedDEK, unwrapError := keyProvider.UnwrapKey(testContext, wrappedDEK)
	if unwrapError != nil {
		testHandle.Fatalf("UnwrapKey: %v", unwrapError)
	}
	if !bytes.Equal(unwrappedDEK, plaintextDEK) {
		testHandle.Error("unwrapped DEK differs from the original")
	}
}

// TestAWSIntegrationKMSWrapByAlias wraps through an alias (the documented
// `--root-key-id alias/...` form) and unwraps with a provider holding the key ID.
func TestAWSIntegrationKMSWrapByAlias(testHandle *testing.T) {
	skipWithoutLocalAWSEmulator(testHandle)
	kmsClient, keyID := createAWSKMSIntegrationTestKey(testHandle)
	testContext := context.Background()
	// A previous run's alias may still exist; its key is pending deletion.
	_, _ = kmsClient.DeleteAlias(testContext, &awskms.DeleteAliasInput{AliasName: aws.String(awsKMSIntegrationTestAlias)})
	_, aliasError := kmsClient.CreateAlias(testContext, &awskms.CreateAliasInput{
		AliasName:   aws.String(awsKMSIntegrationTestAlias),
		TargetKeyId: aws.String(keyID),
	})
	if aliasError != nil {
		testHandle.Fatalf("CreateAlias: %v", aliasError)
	}
	testHandle.Cleanup(func() {
		_, _ = kmsClient.DeleteAlias(testContext, &awskms.DeleteAliasInput{AliasName: aws.String(awsKMSIntegrationTestAlias)})
	})

	plaintextDEK := newAWSKMSIntegrationTestDEK(testHandle)
	aliasProvider := newAWSKMSIntegrationTestProvider(testHandle, awsKMSIntegrationTestAlias)
	wrappedDEK, wrapError := aliasProvider.WrapKey(testContext, plaintextDEK)
	if wrapError != nil {
		testHandle.Fatalf("WrapKey by alias: %v", wrapError)
	}
	keyIDProvider := newAWSKMSIntegrationTestProvider(testHandle, keyID)
	unwrappedDEK, unwrapError := keyIDProvider.UnwrapKey(testContext, wrappedDEK)
	if unwrapError != nil {
		testHandle.Fatalf("UnwrapKey: %v", unwrapError)
	}
	if !bytes.Equal(unwrappedDEK, plaintextDEK) {
		testHandle.Error("unwrapped DEK differs from the original")
	}
}

// TestAWSIntegrationKMSRejectsBadInput checks the failure paths: an unknown
// key cannot wrap, and a modified wrapped DEK cannot be unwrapped.
func TestAWSIntegrationKMSRejectsBadInput(testHandle *testing.T) {
	skipWithoutLocalAWSEmulator(testHandle)
	_, keyID := createAWSKMSIntegrationTestKey(testHandle)
	testContext := context.Background()
	plaintextDEK := newAWSKMSIntegrationTestDEK(testHandle)

	unknownKeyProvider := newAWSKMSIntegrationTestProvider(testHandle, awsKMSIntegrationTestUnknownKeyID)
	_, unknownKeyError := unknownKeyProvider.WrapKey(testContext, plaintextDEK)
	var notFoundError *kmsTypes.NotFoundException
	if !errors.As(unknownKeyError, &notFoundError) {
		testHandle.Errorf("WrapKey with an unknown key ID: got %v, want NotFoundException", unknownKeyError)
	}

	keyProvider := newAWSKMSIntegrationTestProvider(testHandle, keyID)
	wrappedDEK, wrapError := keyProvider.WrapKey(testContext, plaintextDEK)
	if wrapError != nil {
		testHandle.Fatalf("WrapKey: %v", wrapError)
	}
	tamperedDEK := bytes.Clone(wrappedDEK)
	tamperedDEK[len(tamperedDEK)-1] ^= awsKMSIntegrationTestBitFlipMask
	_, tamperedError := keyProvider.UnwrapKey(testContext, tamperedDEK)
	var invalidCiphertextError *kmsTypes.InvalidCiphertextException
	if !errors.As(tamperedError, &invalidCiphertextError) {
		testHandle.Errorf("UnwrapKey of a modified wrapped DEK: got %v, want InvalidCiphertextException", tamperedError)
	}
}
