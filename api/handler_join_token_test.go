// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// joinTokenTestAuditCapacity is the in-memory audit log size for the test.
const joinTokenTestAuditCapacity = 100

// TestJoinTokenRoutesRequireClusterAdmin creates a join token through the API
// as a cluster admin and as an api-writer (spec api-route-authorization,
// "Join tokens are managed through the API"): the admin gets the token once,
// a record under enrollment/token/ and an audit entry without the value; the
// writer gets 403 and no record.
func TestJoinTokenRoutesRequireClusterAdmin(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	certificateAuthority, caError := security.NewCertificateAuthority(24 * time.Hour)
	if caError != nil {
		t.Fatal(caError)
	}
	apiServer := NewServer(factStore)
	apiServer.SetEnrollmentService(security.NewEnrollmentService(factStore, certificateAuthority, nil, time.Hour))
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)
	apiAuthorizer.GrantRole("user:writer", "api-writer")
	auditLog := security.NewInMemoryAuditLog(joinTokenTestAuditCapacity)
	apiAuthorizer.SetAuditLogger(auditLog)
	apiServer.SetAPIAuthorizer(apiAuthorizer)

	writerResponse := serveAsUser(apiServer, "writer", http.MethodPost, "/api/node/join-token", `{"node_id":"worker-9","ttl":"15m"}`)
	if writerResponse.Code != http.StatusForbidden {
		t.Errorf("api-writer creating a join token: HTTP %d, want 403 (%s)", writerResponse.Code, writerResponse.Body.String())
	}
	if tokenRecords, _ := factStore.Scan(context.Background(), security.JoinTokenPrefix); len(tokenRecords) != 0 {
		t.Fatalf("refused request wrote %d join token records", len(tokenRecords))
	}

	adminResponse := serveAsUser(apiServer, "admin", http.MethodPost, "/api/node/join-token", `{"node_id":"worker-9","ttl":"15m"}`)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin creating a join token: HTTP %d, want 200 (%s)", adminResponse.Code, adminResponse.Body.String())
	}
	var created struct {
		Token string `json:"token"`
	}
	if decodeError := json.Unmarshal(adminResponse.Body.Bytes(), &created); decodeError != nil || created.Token == "" {
		t.Fatalf("create response has no token: %v %s", decodeError, adminResponse.Body.String())
	}
	if tokenRecords, _ := factStore.Scan(context.Background(), security.JoinTokenPrefix); len(tokenRecords) != 1 {
		t.Errorf("want 1 join token record under %s, got %d", security.JoinTokenPrefix, len(tokenRecords))
	}
	assertJoinTokenAudited(t, auditLog, "user:admin", created.Token)
}

// newJoinTokenTestServer returns a server with enrollment, an admin and an
// api-writer, and the store and audit log behind it.
func newJoinTokenTestServer(t *testing.T) (*Server, store.StateStore, *security.InMemoryAuditLog) {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	certificateAuthority, caError := security.NewCertificateAuthority(24 * time.Hour)
	if caError != nil {
		t.Fatal(caError)
	}
	apiServer := NewServer(factStore)
	apiServer.SetEnrollmentService(security.NewEnrollmentService(factStore, certificateAuthority, nil, time.Hour))
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)
	apiAuthorizer.GrantRole("user:writer", "api-writer")
	auditLog := security.NewInMemoryAuditLog(joinTokenTestAuditCapacity)
	apiAuthorizer.SetAuditLogger(auditLog)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	return apiServer, factStore, auditLog
}

// TestJoinTokenListAndRevokeThroughAPI: a list shows ids, never values; a
// revoke takes the printed composite form, removes the record and is audited;
// a second revoke is 404; the writer is refused on every method.
func TestJoinTokenListAndRevokeThroughAPI(t *testing.T) {
	apiServer, factStore, auditLog := newJoinTokenTestServer(t)
	createResponse := serveAsUser(apiServer, "admin", http.MethodPost, JoinTokenPath, `{"ttl":"15m"}`)
	var created JoinTokenCreateResponse
	if decodeError := json.Unmarshal(createResponse.Body.Bytes(), &created); decodeError != nil || created.Token == "" {
		t.Fatalf("create: HTTP %d %s", createResponse.Code, createResponse.Body.String())
	}
	tokenValue, _, _ := strings.Cut(created.Token, ".")

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if writerResponse := serveAsUser(apiServer, "writer", method, JoinTokenPath, `{"token":"`+created.Token+`"}`); writerResponse.Code != http.StatusForbidden {
			t.Errorf("api-writer %s: HTTP %d, want 403", method, writerResponse.Code)
		}
	}

	listResponse := serveAsUser(apiServer, "admin", http.MethodGet, JoinTokenPath, "")
	var listed []JoinTokenListEntry
	if decodeError := json.Unmarshal(listResponse.Body.Bytes(), &listed); decodeError != nil || len(listed) != 1 {
		t.Fatalf("list: HTTP %d %s, want one entry", listResponse.Code, listResponse.Body.String())
	}
	if listed[0].ID != tokenValue[:joinTokenDisplayPrefixLength] || strings.Contains(listResponse.Body.String(), tokenValue) {
		t.Errorf("list entry %+v must name the 8-char id and never the value", listed[0])
	}

	revokeResponse := serveAsUser(apiServer, "admin", http.MethodDelete, JoinTokenPath, `{"token":"`+created.Token+`"}`)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("revoke with the composite token: HTTP %d %s", revokeResponse.Code, revokeResponse.Body.String())
	}
	if tokenRecords, _ := factStore.Scan(context.Background(), security.JoinTokenPrefix); len(tokenRecords) != 0 {
		t.Errorf("revoke left %d join token records", len(tokenRecords))
	}
	if againResponse := serveAsUser(apiServer, "admin", http.MethodDelete, JoinTokenPath, `{"token":"`+tokenValue+`"}`); againResponse.Code != http.StatusNotFound {
		t.Errorf("revoking an unknown token: HTTP %d, want 404", againResponse.Code)
	}
	revokeAudited := false
	for _, entry := range auditLog.Entries() {
		revokeAudited = revokeAudited || (entry.Action == joinTokenAuditAction+"revoke" && entry.Target == tokenValue[:joinTokenDisplayPrefixLength])
		if encodedEntry, _ := json.Marshal(entry); bytes.Contains(encodedEntry, []byte(tokenValue)) {
			t.Errorf("audit entry contains the token value: %s", encodedEntry)
		}
	}
	if !revokeAudited {
		t.Errorf("no join-token.revoke audit entry naming the token id in %+v", auditLog.Entries())
	}
}

// assertJoinTokenAudited checks for an allowed join-token entry by principal
// that never contains the secret part of the token.
func assertJoinTokenAudited(t *testing.T, auditLog *security.InMemoryAuditLog, principal string, tokenValue string) {
	t.Helper()
	for _, entry := range auditLog.Entries() {
		if entry.Principal != principal || !strings.Contains(entry.Action, "join-token") {
			continue
		}
		encodedEntry, _ := json.Marshal(entry)
		if bytes.Contains(encodedEntry, []byte(tokenValue)) {
			t.Errorf("audit entry contains the token value: %s", encodedEntry)
		}
		return
	}
	t.Errorf("no join-token audit entry for %s in %+v", principal, auditLog.Entries())
}
