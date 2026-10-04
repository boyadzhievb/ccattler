// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

func TestKMSSecretProviderDecryptsWithLocalKey(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()

	masterKey := make([]byte, 32)
	for byteIndex := range masterKey {
		masterKey[byteIndex] = byte(byteIndex)
	}

	secretStore, secretStoreError := security.NewSecretStoreWithMasterKey(memoryStore, masterKey)
	if secretStoreError != nil {
		t.Fatalf("NewSecretStoreWithMasterKey: %v", secretStoreError)
	}
	if putError := secretStore.PutSecret(ctx, "db.password", []byte("s3cret")); putError != nil {
		t.Fatalf("PutSecret: %v", putError)
	}

	grantKey := "desired/service/api/secret/db.password"
	if _, putError := memoryStore.Put(ctx, grantKey, []byte("/run/secrets/db-password")); putError != nil {
		t.Fatalf("put grant: %v", putError)
	}

	localProvider, localError := NewLocalSecretProvider(memoryStore, masterKey)
	if localError != nil {
		t.Fatalf("NewLocalSecretProvider: %v", localError)
	}

	plaintext, mountPath, getError := localProvider.GetSecretForService(ctx, "api", "db.password")
	if getError != nil {
		t.Fatalf("GetSecretForService: %v", getError)
	}
	if string(plaintext) != "s3cret" {
		t.Errorf("plaintext: got %q, want %q", string(plaintext), "s3cret")
	}
	if mountPath != "/run/secrets/db-password" {
		t.Errorf("mountPath: got %q, want %q", mountPath, "/run/secrets/db-password")
	}
}

func TestKMSSecretProviderDeniesUnauthorizedService(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()

	masterKey := make([]byte, 32)
	for byteIndex := range masterKey {
		masterKey[byteIndex] = byte(byteIndex)
	}

	secretStore, _ := security.NewSecretStoreWithMasterKey(memoryStore, masterKey)
	secretStore.PutSecret(ctx, "db.password", []byte("s3cret"))

	localProvider, _ := NewLocalSecretProvider(memoryStore, masterKey)

	_, _, getError := localProvider.GetSecretForService(ctx, "unauthorized-service", "db.password")
	if getError == nil {
		t.Fatal("expected error for unauthorized service, got nil")
	}
}

func TestExtractRegionFromAWSKeyARN(t *testing.T) {
	testCases := []struct {
		keyARN         string
		expectedRegion string
	}{
		{"arn:aws:kms:us-east-1:123456789:key/abc-def", "us-east-1"},
		{"arn:aws:kms:eu-west-2:999:key/xyz", "eu-west-2"},
		{"alias/ccattler-secrets", ""},
		{"not-an-arn", ""},
		{"", ""},
	}
	for _, testCase := range testCases {
		actualRegion := extractRegionFromAWSKeyARN(testCase.keyARN)
		if actualRegion != testCase.expectedRegion {
			t.Errorf("extractRegionFromAWSKeyARN(%q) = %q, want %q",
				testCase.keyARN, actualRegion, testCase.expectedRegion)
		}
	}
}

func TestCreateAgentKeyProviderRejectsUnknownProvider(t *testing.T) {
	ctx := context.Background()
	_, providerError := CreateAgentKeyProvider(ctx, "unknown-kms", "some-key")
	if providerError == nil {
		t.Fatal("expected error for unknown KMS provider, got nil")
	}
}
