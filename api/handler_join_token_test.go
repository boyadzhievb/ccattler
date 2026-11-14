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
