// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newTestRoleServer creates a test API server with an in-memory store and
// cluster-admin granted to the test principal.
func newTestRoleServer(testingContext *testing.T) (*Server, store.StateStore) {
	testingContext.Helper()
	factStore := store.NewMemoryStore()
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.GrantRole("user:admin", "cluster-admin")

	testServer := &Server{
		factStore:     factStore,
		apiAuthorizer: apiAuthorizer,
	}
	return testServer, factStore
}

// requestWithAdminPrincipal attaches a cluster-admin principal to the request
// context so the handler's capability check passes.
func requestWithAdminPrincipal(request *http.Request) *http.Request {
	principal := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "admin",
	}
	contextWithPrincipal := security.WithPrincipalStruct(request.Context(), principal)
	return request.WithContext(contextWithPrincipal)
}

// TestRoleGrantRole verifies that granting a builtin role writes the correct
// auth/grant/ fact to the store.
func TestRoleGrantRole(testingContext *testing.T) {
	testServer, factStore := newTestRoleServer(testingContext)

	requestBody := `{"principal":"user:alice","role":"api-reader"}`
	request := httptest.NewRequest(http.MethodPost, "/api/role", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	if recorder.Code != http.StatusOK {
		testingContext.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	grantKey := types.KeyAuthGrant("user", "alice", "api-reader")
	fact, getError := factStore.Get(context.Background(), grantKey)
	if getError != nil || fact == nil {
		testingContext.Fatalf("grant fact not found at %s", grantKey)
	}
	if string(fact.Value) != "true" {
		testingContext.Errorf("grant value = %q, want %q", fact.Value, "true")
	}
}

// TestRoleGrantCapability verifies that granting a direct capability creates an
// inline role with the capability and scope, plus a grant binding.
func TestRoleGrantCapability(testingContext *testing.T) {
	testServer, factStore := newTestRoleServer(testingContext)

	requestBody := `{"principal":"user:bob","capability":"workload.read","scope":"team/payments"}`
	request := httptest.NewRequest(http.MethodPost, "/api/role", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	if recorder.Code != http.StatusOK {
		testingContext.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	inlineRoleName := "inline-user-bob"
	capabilityKey := types.KeyAuthRoleCapability(inlineRoleName, "workload.read")
	scopeKey := types.KeyAuthRoleScope(inlineRoleName, "team/payments")
	grantKey := types.KeyAuthGrant("user", "bob", inlineRoleName)

	for _, expectedKey := range []string{capabilityKey, scopeKey, grantKey} {
		fact, getError := factStore.Get(context.Background(), expectedKey)
		if getError != nil || fact == nil {
			testingContext.Errorf("expected fact at %s, not found", expectedKey)
		}
	}
}

// TestRoleGrantInvalidCapability verifies that granting an unknown capability
// returns a 400 error.
func TestRoleGrantInvalidCapability(testingContext *testing.T) {
	testServer, _ := newTestRoleServer(testingContext)

	requestBody := `{"principal":"user:eve","capability":"nonexistent.cap"}`
	request := httptest.NewRequest(http.MethodPost, "/api/role", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testingContext.Fatalf("expected 400, got %d", recorder.Code)
	}
}

// TestRoleRevokeRole verifies that revoking a role deletes the grant fact.
func TestRoleRevokeRole(testingContext *testing.T) {
	testServer, factStore := newTestRoleServer(testingContext)

	grantKey := types.KeyAuthGrant("user", "alice", "api-reader")
	factStore.Put(context.Background(), grantKey, []byte("true"))

	requestBody := `{"principal":"user:alice","role":"api-reader"}`
	request := httptest.NewRequest(http.MethodDelete, "/api/role", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	if recorder.Code != http.StatusOK {
		testingContext.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	fact, _ := factStore.Get(context.Background(), grantKey)
	if fact != nil {
		testingContext.Error("grant fact should be deleted after revoke")
	}
}

// TestRoleList verifies that the list endpoint returns all grants.
func TestRoleList(testingContext *testing.T) {
	testServer, factStore := newTestRoleServer(testingContext)

	factStore.Put(context.Background(), types.KeyAuthGrant("user", "alice", "api-reader"), []byte("true"))
	factStore.Put(context.Background(), types.KeyAuthGrant("user", "bob", "api-writer"), []byte("true"))
	factStore.Put(context.Background(), types.KeyAuthRoleCapability("api-reader", "workload.read"), []byte("true"))
	factStore.Put(context.Background(), types.KeyAuthRoleCapability("api-writer", "workload.create"), []byte("true"))

	request := httptest.NewRequest(http.MethodGet, "/api/role", nil)
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	if recorder.Code != http.StatusOK {
		testingContext.Fatalf("expected 200, got %d", recorder.Code)
	}

	var listResponse struct {
		Grants []roleListResponseEntry `json:"grants"`
	}
	if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &listResponse); unmarshalError != nil {
		testingContext.Fatalf("invalid JSON response: %v", unmarshalError)
	}

	if len(listResponse.Grants) != 2 {
		testingContext.Fatalf("expected 2 grants, got %d", len(listResponse.Grants))
	}

	if listResponse.Grants[0].Principal != "user:alice" {
		testingContext.Errorf("first grant principal = %q, want user:alice", listResponse.Grants[0].Principal)
	}
}

// TestRoleListFilterByPrincipal verifies principal filtering on the list endpoint.
func TestRoleListFilterByPrincipal(testingContext *testing.T) {
	testServer, factStore := newTestRoleServer(testingContext)

	factStore.Put(context.Background(), types.KeyAuthGrant("user", "alice", "api-reader"), []byte("true"))
	factStore.Put(context.Background(), types.KeyAuthGrant("user", "bob", "api-writer"), []byte("true"))

	request := httptest.NewRequest(http.MethodGet, "/api/role?principal=user:bob", nil)
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	var listResponse struct {
		Grants []roleListResponseEntry `json:"grants"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &listResponse)

	if len(listResponse.Grants) != 1 {
		testingContext.Fatalf("expected 1 grant for bob, got %d", len(listResponse.Grants))
	}
	if listResponse.Grants[0].Principal != "user:bob" {
		testingContext.Errorf("filtered grant principal = %q, want user:bob", listResponse.Grants[0].Principal)
	}
}

// TestRoleCheckAllowed verifies the check endpoint returns allowed=true when
// the principal has the capability.
func TestRoleCheckAllowed(testingContext *testing.T) {
	testServer, _ := newTestRoleServer(testingContext)

	requestBody := `{"principal":"user:admin","capability":"cluster.admin"}`
	request := httptest.NewRequest(http.MethodPost, "/api/role/check", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRoleCheck(recorder, request)

	if recorder.Code != http.StatusOK {
		testingContext.Fatalf("expected 200, got %d", recorder.Code)
	}

	var checkResponse struct {
		Allowed bool `json:"allowed"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &checkResponse)

	if !checkResponse.Allowed {
		testingContext.Error("expected allowed=true for cluster-admin checking cluster.admin")
	}
}

// TestRoleCheckDenied verifies the check endpoint returns allowed=false when
// the principal lacks the capability.
func TestRoleCheckDenied(testingContext *testing.T) {
	testServer, _ := newTestRoleServer(testingContext)

	requestBody := `{"principal":"user:nobody","capability":"workload.create"}`
	request := httptest.NewRequest(http.MethodPost, "/api/role/check", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRoleCheck(recorder, request)

	var checkResponse struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &checkResponse)

	if checkResponse.Allowed {
		testingContext.Error("expected allowed=false for user:nobody")
	}
	if checkResponse.Reason == "" {
		testingContext.Error("expected a denial reason")
	}
}

// TestRoleGrantMissingPrincipal verifies that a request without a principal
// returns 400.
func TestRoleGrantMissingPrincipal(testingContext *testing.T) {
	testServer, _ := newTestRoleServer(testingContext)

	requestBody := `{"role":"api-reader"}`
	request := httptest.NewRequest(http.MethodPost, "/api/role", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testingContext.Fatalf("expected 400, got %d", recorder.Code)
	}
}

// TestRoleGrantInvalidPrincipalFormat verifies that a principal without a colon
// returns 400.
func TestRoleGrantInvalidPrincipalFormat(testingContext *testing.T) {
	testServer, _ := newTestRoleServer(testingContext)

	requestBody := `{"principal":"alice","role":"api-reader"}`
	request := httptest.NewRequest(http.MethodPost, "/api/role", bytes.NewBufferString(requestBody))
	request = requestWithAdminPrincipal(request)
	recorder := httptest.NewRecorder()

	testServer.handleRole(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testingContext.Fatalf("expected 400, got %d", recorder.Code)
	}
}
