// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"errors"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// readSecretGeneration returns the stored generation of secretName, or "" when
// the generation fact does not exist.
func readSecretGeneration(t *testing.T, factStore store.StateStore, secretName string) string {
	t.Helper()
	generationFact, getError := factStore.Get(context.Background(), types.KeyDesiredSecretGeneration(secretName))
	if errors.Is(getError, store.ErrKeyNotFound) {
		return ""
	}
	if getError != nil {
		t.Fatalf("read generation of %s: %v", secretName, getError)
	}
	return string(generationFact.Value)
}

// TestSecretGenerationRaisedOnlyOnValueChange checks the value generation rule
// (spec instance-config-reload, M236 D1): create starts at 1, the same value and
// a re-encrypted envelope keep it, a new value raises it, delete removes it.
func TestSecretGenerationRaisedOnlyOnValueChange(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	secretStore, storeError := NewSecretStoreWithMasterKey(factStore, make([]byte, 32))
	if storeError != nil {
		t.Fatalf("secret store: %v", storeError)
	}

	generationSteps := []struct {
		description        string // description names the step.
		plaintext          string // plaintext is stored by PutSecret.
		expectedGeneration string // expectedGeneration is the generation after the step.
	}{
		{"create", "first", "1"},
		{"same value", "first", "1"},
		{"new value", "second", "2"},
		{"same value again", "second", "2"},
		{"third value", "third", "3"},
	}
	for _, generationStep := range generationSteps {
		if putError := secretStore.PutSecret(ctx, "api-key", []byte(generationStep.plaintext)); putError != nil {
			t.Fatalf("%s: put: %v", generationStep.description, putError)
		}
		if actualGeneration := readSecretGeneration(t, factStore, "api-key"); actualGeneration != generationStep.expectedGeneration {
			t.Fatalf("%s: generation %q, want %q", generationStep.description, actualGeneration, generationStep.expectedGeneration)
		}
	}

	// Re-encryption (keyring rotation, Flow 30) writes a new envelope directly.
	resealedEnvelope, sealError := SealSecretValue(ctx, secretStore.envelopeCodec, SecretStorePrefix+"api-key", []byte("third"))
	if sealError != nil {
		t.Fatalf("reseal: %v", sealError)
	}
	if _, putError := factStore.Put(ctx, SecretStorePrefix+"api-key", resealedEnvelope); putError != nil {
		t.Fatalf("write resealed envelope: %v", putError)
	}
	if putError := secretStore.PutSecret(ctx, "api-key", []byte("third")); putError != nil {
		t.Fatalf("put after re-encryption: %v", putError)
	}
	if actualGeneration := readSecretGeneration(t, factStore, "api-key"); actualGeneration != "3" {
		t.Fatalf("after re-encryption: generation %q, want 3", actualGeneration)
	}

	if deleteError := secretStore.DeleteSecret(ctx, "api-key"); deleteError != nil {
		t.Fatalf("delete: %v", deleteError)
	}
	if actualGeneration := readSecretGeneration(t, factStore, "api-key"); actualGeneration != "" {
		t.Fatalf("after delete: generation %q, want none", actualGeneration)
	}
}

// TestSecretGenerationMissingFactTreatedAsOne checks that a secret stored
// before generations existed gets generation 2 on its first changed value.
func TestSecretGenerationMissingFactTreatedAsOne(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	secretStore, storeError := NewSecretStoreWithMasterKey(factStore, make([]byte, 32))
	if storeError != nil {
		t.Fatalf("secret store: %v", storeError)
	}
	legacyEnvelope, sealError := SealSecretValue(ctx, secretStore.envelopeCodec, SecretStorePrefix+"old-key", []byte("old"))
	if sealError != nil {
		t.Fatalf("seal: %v", sealError)
	}
	if _, putError := factStore.Put(ctx, SecretStorePrefix+"old-key", legacyEnvelope); putError != nil {
		t.Fatalf("write legacy secret: %v", putError)
	}
	if putError := secretStore.PutSecret(ctx, "old-key", []byte("new")); putError != nil {
		t.Fatalf("put: %v", putError)
	}
	if actualGeneration := readSecretGeneration(t, factStore, "old-key"); actualGeneration != "2" {
		t.Fatalf("generation %q, want 2", actualGeneration)
	}
}
