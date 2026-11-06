// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// deliveryHandlerAuditCapacity is the in-memory audit capacity for handler tests.
const deliveryHandlerAuditCapacity = 100

// newSecretDeliveryTestServer returns an API server with delivery enabled,
// node-1 enrolled and instance inst-1 of checkout placed on it with one secret.
func newSecretDeliveryTestServer(testContext *testing.T) *Server {
	testContext.Helper()
	ctx := context.Background()
	rawStore := store.NewMemoryStore()
	masterKey, _ := security.GenerateMasterKey()
	secretStore, _ := security.NewSecretStoreWithMasterKey(rawStore, masterKey)
	rbacAuthorizer := security.NewRBACAuthorizer()
	for _, builtinRole := range security.BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	rbacAuthorizer.BindRole(security.RoleBinding{Principal: security.SecretDeliveryPrincipal, RoleName: "secret-delivery"})
	deliveryStore := security.NewAuthorizedStore(rawStore, rbacAuthorizer, nil)
	deliveryStore.SetServicePrincipal(security.SecretDeliveryPrincipal)
	for factKey, factValue := range map[string]string{
		security.EnrolledNodePrefix + "node-1":                      "{}",
		types.KeyPlacementInstance("inst-1"):                        "node-1",
		types.KeyObservedInstanceService("inst-1"):                  "checkout",
		types.ScanDesiredServiceSecrets("checkout") + "db.password": "/run/secrets/db",
	} {
		_, _ = rawStore.Put(ctx, factKey, []byte(factValue))
	}
	if putError := secretStore.PutSecret(ctx, "db.password", []byte("s3cret")); putError != nil {
		testContext.Fatalf("PutSecret: %v", putError)
	}
	apiServer := NewServer(rawStore)
	apiServer.SetSecretDeliveryService(security.NewSecretDeliveryService(deliveryStore, secretStore, security.NewInMemoryAuditLog(deliveryHandlerAuditCapacity)))
	return apiServer
}

// postNodeSecretDelivery sends a delivery request as the given principal and
// auth method; withTLS simulates an mTLS connection.
func postNodeSecretDelivery(apiServer *Server, principal security.Principal, authMethod string, withTLS bool, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, NodeSecretDeliveryPath, strings.NewReader(body))
	requestContext := security.WithPrincipalStruct(request.Context(), principal)
	requestContext = security.WithAuthMethod(requestContext, authMethod)
	request = request.WithContext(requestContext)
	if withTLS {
		request.TLS = &tls.ConnectionState{}
	}
	responseRecorder := httptest.NewRecorder()
	apiServer.handleNodeSecretDelivery(responseRecorder, request)
	return responseRecorder
}

func TestNodeSecretDeliveryHandlerServesNodeOverMTLS(testContext *testing.T) {
	apiServer := newSecretDeliveryTestServer(testContext)
	nodePrincipal := security.Principal{Kind: security.PrincipalKindNode, Name: "node-1"}
	responseRecorder := postNodeSecretDelivery(apiServer, nodePrincipal, "mtls", true, `{"instance_id":"inst-1"}`)
	if responseRecorder.Code != http.StatusOK {
		testContext.Fatalf("status = %d, body %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var delivered security.DeliveredInstanceSecrets
	if decodeError := json.Unmarshal(responseRecorder.Body.Bytes(), &delivered); decodeError != nil {
		testContext.Fatalf("decode: %v", decodeError)
	}
	if string(delivered.Secrets["db.password"]) != "s3cret" || responseRecorder.Header().Get("Cache-Control") != "no-store" {
		testContext.Fatalf("unexpected delivery %+v", delivered)
	}
}

func TestNodeSecretDeliveryHandlerRefusesNonNodeCallers(testContext *testing.T) {
	apiServer := newSecretDeliveryTestServer(testContext)
	testCases := []struct {
		name       string             // name describes the caller.
		principal  security.Principal // principal is the authenticated caller.
		authMethod string             // authMethod is the authentication method.
		withTLS    bool               // withTLS marks the request as TLS.
	}{
		{name: "user", principal: security.Principal{Kind: security.PrincipalKindUser, Name: "local-admin"}, authMethod: "local-token", withTLS: true},
		{name: "service token", principal: security.Principal{Kind: security.PrincipalKindService, Name: "checkout"}, authMethod: "oidc", withTLS: true},
		{name: "node without mtls", principal: security.Principal{Kind: security.PrincipalKindNode, Name: "node-1"}, authMethod: "oidc", withTLS: true},
		{name: "node without tls", principal: security.Principal{Kind: security.PrincipalKindNode, Name: "node-1"}, authMethod: "mtls", withTLS: false},
	}
	for _, testCase := range testCases {
		responseRecorder := postNodeSecretDelivery(apiServer, testCase.principal, testCase.authMethod, testCase.withTLS, `{"instance_id":"inst-1"}`)
		if responseRecorder.Code != http.StatusForbidden || strings.Contains(responseRecorder.Body.String(), "s3cret") {
			testContext.Fatalf("%s: status = %d, body %s", testCase.name, responseRecorder.Code, responseRecorder.Body.String())
		}
	}
}

func TestNodeSecretDeliveryHandlerRefusesOtherNodesInstance(testContext *testing.T) {
	apiServer := newSecretDeliveryTestServer(testContext)
	nodePrincipal := security.Principal{Kind: security.PrincipalKindNode, Name: "node-2"}
	responseRecorder := postNodeSecretDelivery(apiServer, nodePrincipal, "mtls", true, `{"instance_id":"inst-1"}`)
	var refusal NodeSecretDeliveryRefusalResponse
	_ = json.Unmarshal(responseRecorder.Body.Bytes(), &refusal)
	if responseRecorder.Code != http.StatusForbidden || refusal.Reason != string(security.SecretDeliveryNotAuthorized) {
		testContext.Fatalf("status = %d, refusal %+v", responseRecorder.Code, refusal)
	}
}
