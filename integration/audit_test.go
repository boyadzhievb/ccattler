// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// setupAuditServer creates a memory-backed API server with audit store
// configured and a permissive authorizer granting cluster-wide workload read.
func setupAuditServer(t *testing.T) (store.StateStore, *api.Server) {
	t.Helper()
	factStore := store.NewMemoryStore()

	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"*": {{Capability: security.CapabilityWorkloadRead, Scope: security.ScopeCluster}},
	})

	apiServer := api.NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetAuditStore(factStore)

	return factStore, apiServer
}

// writeAuditEntry persists a single audit entry to the store at the given
// zero-padded sequence key.
func writeAuditEntry(t *testing.T, factStore store.StateStore, sequence int, entry security.AuditEntry) {
	t.Helper()
	entryJSON, marshalError := json.Marshal(entry)
	if marshalError != nil {
		t.Fatalf("marshal audit entry: %v", marshalError)
	}
	auditKey := fmt.Sprintf("audit/%020d", sequence)
	if _, putError := factStore.Put(context.Background(), auditKey, entryJSON); putError != nil {
		t.Fatalf("put audit entry: %v", putError)
	}
}

// auditRequest creates an HTTP request to /api/audit with the given query
// string and an admin principal in context.
func auditRequest(queryString string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/audit?"+queryString, nil)
	requestCtx := security.WithPrincipal(request.Context(), "user:admin")
	requestCtx = security.WithPrincipalStruct(requestCtx, security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "admin",
	})
	return request.WithContext(requestCtx)
}

// TestAuditEntryPersistence verifies that audit entries written to the store
// are returned by GET /api/audit in newest-first order.
func TestAuditEntryPersistence(t *testing.T) {
	t.Parallel()
	factStore, apiServer := setupAuditServer(t)
	defer factStore.Close()

	baseTime := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	writeAuditEntry(t, factStore, 1, security.AuditEntry{
		Timestamp:  baseTime,
		Principal:  "user:alice",
		Action:     "get",
		Target:     "desired/service/web",
		Decision:   "allow",
		AuthMethod: "mtls",
	})
	writeAuditEntry(t, factStore, 2, security.AuditEntry{
		Timestamp:  baseTime.Add(1 * time.Minute),
		Principal:  "user:bob",
		Action:     "put",
		Target:     "desired/service/api",
		Decision:   "allow",
		AuthMethod: "oidc",
	})
	writeAuditEntry(t, factStore, 3, security.AuditEntry{
		Timestamp:  baseTime.Add(2 * time.Minute),
		Principal:  "node:node-1",
		Action:     "scan",
		Target:     "observed/instance",
		Decision:   "allow",
		AuthMethod: "local-token",
	})

	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, auditRequest(""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var entries []security.AuditEntry
	if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &entries); unmarshalError != nil {
		t.Fatalf("unmarshal response: %v", unmarshalError)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].Principal != "node:node-1" {
		t.Errorf("expected newest entry first (node:node-1), got %s", entries[0].Principal)
	}
	if entries[2].Principal != "user:alice" {
		t.Errorf("expected oldest entry last (user:alice), got %s", entries[2].Principal)
	}

	if entries[0].AuthMethod != "local-token" {
		t.Errorf("expected auth_method local-token, got %s", entries[0].AuthMethod)
	}
	if entries[1].AuthMethod != "oidc" {
		t.Errorf("expected auth_method oidc, got %s", entries[1].AuthMethod)
	}
}

// TestAuditAPIFilterByPrincipal verifies that the principal query parameter
// filters audit entries to only those matching the specified principal.
func TestAuditAPIFilterByPrincipal(t *testing.T) {
	t.Parallel()
	factStore, apiServer := setupAuditServer(t)
	defer factStore.Close()

	baseTime := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	writeAuditEntry(t, factStore, 1, security.AuditEntry{
		Timestamp: baseTime,
		Principal: "user:alice",
		Action:    "get",
		Target:    "desired/service/web",
		Decision:  "allow",
	})
	writeAuditEntry(t, factStore, 2, security.AuditEntry{
		Timestamp: baseTime.Add(1 * time.Minute),
		Principal: "user:bob",
		Action:    "put",
		Target:    "desired/service/api",
		Decision:  "allow",
	})
	writeAuditEntry(t, factStore, 3, security.AuditEntry{
		Timestamp: baseTime.Add(2 * time.Minute),
		Principal: "user:alice",
		Action:    "delete",
		Target:    "desired/service/old",
		Decision:  "allow",
	})

	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, auditRequest("principal=user:alice"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var entries []security.AuditEntry
	if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &entries); unmarshalError != nil {
		t.Fatalf("unmarshal response: %v", unmarshalError)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for user:alice, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.Principal != "user:alice" {
			t.Errorf("expected principal user:alice, got %s", entry.Principal)
		}
	}
}

// TestAuditAPIFilterBySince verifies that the since query parameter filters
// audit entries to only those with timestamps after the specified time.
func TestAuditAPIFilterBySince(t *testing.T) {
	t.Parallel()
	factStore, apiServer := setupAuditServer(t)
	defer factStore.Close()

	baseTime := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	writeAuditEntry(t, factStore, 1, security.AuditEntry{
		Timestamp: baseTime,
		Principal: "user:alice",
		Action:    "get",
		Target:    "desired/service/web",
		Decision:  "allow",
	})
	writeAuditEntry(t, factStore, 2, security.AuditEntry{
		Timestamp: baseTime.Add(30 * time.Minute),
		Principal: "user:bob",
		Action:    "put",
		Target:    "desired/service/api",
		Decision:  "allow",
	})
	writeAuditEntry(t, factStore, 3, security.AuditEntry{
		Timestamp: baseTime.Add(60 * time.Minute),
		Principal: "node:node-1",
		Action:    "scan",
		Target:    "observed/instance",
		Decision:  "allow",
	})

	sinceValue := baseTime.Add(15 * time.Minute).Format(time.RFC3339)
	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, auditRequest("since="+sinceValue))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var entries []security.AuditEntry
	if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &entries); unmarshalError != nil {
		t.Fatalf("unmarshal response: %v", unmarshalError)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after since filter, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.Timestamp.Before(baseTime.Add(15 * time.Minute)) {
			t.Errorf("entry timestamp %v should be after since filter", entry.Timestamp)
		}
	}
}

// TestAuditGCIntegration verifies the full lifecycle: entries are written to
// the store, the GC controller reconciles and produces delete changes for
// expired entries, and the API only returns the surviving entries.
func TestAuditGCIntegration(t *testing.T) {
	t.Parallel()
	factStore, apiServer := setupAuditServer(t)
	defer factStore.Close()

	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	writeAuditEntry(t, factStore, 1, security.AuditEntry{
		Timestamp:  now.Add(-10 * 24 * time.Hour),
		Principal:  "user:old-alice",
		Action:     "get",
		Target:     "desired/service/expired",
		Decision:   "allow",
		AuthMethod: "mtls",
	})
	writeAuditEntry(t, factStore, 2, security.AuditEntry{
		Timestamp:  now.Add(-8 * 24 * time.Hour),
		Principal:  "user:old-bob",
		Action:     "put",
		Target:     "desired/service/also-expired",
		Decision:   "allow",
		AuthMethod: "oidc",
	})
	writeAuditEntry(t, factStore, 3, security.AuditEntry{
		Timestamp:  now.Add(-1 * time.Hour),
		Principal:  "user:recent",
		Action:     "scan",
		Target:     "observed/instance",
		Decision:   "allow",
		AuthMethod: "local-token",
	})

	gcController := controllers.NewAuditLogGCController()
	gcController.NowFunc = func() time.Time { return now }

	auditFacts, scanError := factStore.Scan(context.Background(), "audit/")
	if scanError != nil {
		t.Fatalf("scan audit facts: %v", scanError)
	}

	changes, reconcileError := gcController.Reconcile(context.Background(), auditFacts)
	if reconcileError != nil {
		t.Fatalf("reconcile: %v", reconcileError)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 GC deletions, got %d", len(changes))
	}

	for _, change := range changes {
		if deleteError := factStore.Delete(context.Background(), change.Key); deleteError != nil {
			t.Fatalf("delete %s: %v", change.Key, deleteError)
		}
	}

	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, auditRequest(""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var survivingEntries []security.AuditEntry
	if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &survivingEntries); unmarshalError != nil {
		t.Fatalf("unmarshal response: %v", unmarshalError)
	}

	if len(survivingEntries) != 1 {
		t.Fatalf("expected 1 surviving entry after GC, got %d", len(survivingEntries))
	}
	if survivingEntries[0].Principal != "user:recent" {
		t.Errorf("expected surviving entry to be user:recent, got %s", survivingEntries[0].Principal)
	}
}
