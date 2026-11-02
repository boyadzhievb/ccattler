// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// auditLimitTestEntryCount is more than DefaultAuditQueryLimit so the default
// and explicit limits are distinguishable.
const auditLimitTestEntryCount = 150

// queryAuditEntries calls GET /api/audit with the raw query string and decodes
// the returned entries.
func queryAuditEntries(t *testing.T, baseURL, rawQuery string) []security.AuditEntry {
	t.Helper()
	response, requestError := http.Get(baseURL + "/api/audit?" + rawQuery)
	if requestError != nil {
		t.Fatal(requestError)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("query %q: expected 200, got %d", rawQuery, response.StatusCode)
	}
	var auditEntries []security.AuditEntry
	if decodeError := json.NewDecoder(response.Body).Decode(&auditEntries); decodeError != nil {
		t.Fatal(decodeError)
	}
	return auditEntries
}

// TestAuditQueryLimit verifies the limit parameter: default 100, explicit
// smaller limits honoured newest-first, and huge or invalid limits clamped
// without failing the request.
func TestAuditQueryLimit(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	for entryIndex := 0; entryIndex < auditLimitTestEntryCount; entryIndex++ {
		encodedEntry, _ := json.Marshal(security.AuditEntry{
			Timestamp: time.Now(), Principal: fmt.Sprintf("user-%d", entryIndex),
			Action: "workload.read", Target: "cluster", Decision: security.AuditDecisionAllow,
		})
		factStore.Put(ctx, fmt.Sprintf("%s/%020d", types.PrefixAudit, entryIndex), encodedEntry)
	}

	apiServer := NewServer(factStore)
	configureTestAuth(apiServer)
	apiServer.SetAuditStore(factStore)
	address, startError := apiServer.Start(":0")
	if startError != nil {
		t.Fatalf("start server: %v", startError)
	}
	defer apiServer.Close()
	baseURL := "http://" + address

	if defaultEntries := queryAuditEntries(t, baseURL, ""); len(defaultEntries) != security.DefaultAuditQueryLimit {
		t.Errorf("default limit: got %d entries, want %d", len(defaultEntries), security.DefaultAuditQueryLimit)
	}

	limitedEntries := queryAuditEntries(t, baseURL, "limit=3")
	if len(limitedEntries) != 3 || limitedEntries[0].Principal != fmt.Sprintf("user-%d", auditLimitTestEntryCount-1) {
		t.Errorf("limit=3: got %d entries starting with %q, want 3 newest-first", len(limitedEntries), limitedEntries[0].Principal)
	}

	for _, rawQuery := range []string{"limit=999999999999", "limit=-5", "limit=abc"} {
		expectedCount := auditLimitTestEntryCount
		if rawQuery != "limit=999999999999" {
			expectedCount = security.DefaultAuditQueryLimit
		}
		if clampedEntries := queryAuditEntries(t, baseURL, rawQuery); len(clampedEntries) != expectedCount {
			t.Errorf("%s: got %d entries, want %d", rawQuery, len(clampedEntries), expectedCount)
		}
	}
}
