// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// losingTransactionStore fails every transaction guard, as a keyring rotation
// rewriting the envelope between PutSecret's read and write would.
type losingTransactionStore struct {
	store.StateStore // StateStore serves every other call.
}

// Transaction reports a failed guard without writing anything.
func (losingStore *losingTransactionStore) Transaction(_ context.Context, _ []store.Compare, _ []store.Op, _ []store.Op) (bool, error) {
	return false, nil
}

// TestSecretPutReturnsConflictWhenChangedConcurrently checks that a lost
// PutSecret guard is HTTP 409 with a retry hint, not 500 (M236 review).
func TestSecretPutReturnsConflictWhenChangedConcurrently(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	secretStore, storeError := security.NewSecretStoreWithMasterKey(&losingTransactionStore{StateStore: memoryStore}, make([]byte, 32))
	if storeError != nil {
		t.Fatalf("secret store: %v", storeError)
	}
	apiServer := NewServer(memoryStore)
	configureTestAuth(apiServer)
	apiServer.SetSecretStore(secretStore)
	address, startError := apiServer.Start(":0")
	if startError != nil {
		t.Fatalf("start server: %v", startError)
	}
	defer apiServer.Close()

	putResponse, putError := http.Post("http://"+address+"/api/secret?name=db-password", "text/plain", strings.NewReader("hunter2"))
	if putError != nil {
		t.Fatalf("put: %v", putError)
	}
	responseBody, _ := io.ReadAll(putResponse.Body)
	putResponse.Body.Close()
	if putResponse.StatusCode != http.StatusConflict || !strings.Contains(string(responseBody), "secret changed concurrently, retry") {
		t.Fatalf("got HTTP %d %s, want 409 with a retry hint", putResponse.StatusCode, responseBody)
	}
}
