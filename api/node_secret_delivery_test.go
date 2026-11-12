// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		types.KeyDesiredService("checkout"):                         "",
		types.ScanDesiredServiceSecrets("checkout") + "db.password": "/run/secrets/db",
	} {
		_, _ = rawStore.Put(ctx, factKey, []byte(factValue))
	}
	if putError := secretStore.PutSecret(ctx, "db.password", []byte("s3cret")); putError != nil {
		testContext.Fatalf("PutSecret: %v", putError)
	}
	putGateKeyHandlerTestFacts(testContext, rawStore, secretStore)
	apiServer := NewServer(rawStore)
	auditLog := security.NewInMemoryAuditLog(deliveryHandlerAuditCapacity)
	apiServer.SetSecretDeliveryService(security.NewSecretDeliveryService(deliveryStore, secretStore, auditLog))
	apiServer.SetGateKeyDeliveryService(security.NewGateKeyDeliveryService(deliveryStore, secretStore, auditLog))
	return apiServer
}

// putGateKeyHandlerTestFacts makes node-1 a gate proxy node and adds the auto
// TLS gate "web" with a certificate fact and its sealed system key.
func putGateKeyHandlerTestFacts(testContext *testing.T, rawStore store.StateStore, secretStore *security.SecretStore) {
	testContext.Helper()
	ctx := context.Background()
	certificateAuthority, caError := security.NewCertificateAuthority(time.Hour)
	if caError != nil {
		testContext.Fatal(caError)
	}
	issued, issueError := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{CommonName: "web.example.com", DNSNames: []string{"web.example.com"}, TTL: time.Hour})
	if issueError != nil {
		testContext.Fatal(issueError)
	}
	for factKey, factValue := range map[string]string{
		types.KeyObservedNodeGateProxy("node-1"): string(types.GateProxyEnabled),
		types.KeyDesiredRouteHost("web"):         "web.example.com",
		types.KeyDesiredRouteTLSMode("web"):      string(types.RouteTLSModeAuto),
		types.KeyDerivedRouteTLSCert("web"):      string(issued.CertificatePEM),
	} {
		_, _ = rawStore.Put(ctx, factKey, []byte(factValue))
	}
	if putError := secretStore.PutSecret(ctx, types.SecretNameRouteTLSKey("web"), issued.PrivateKeyPEM); putError != nil {
		testContext.Fatal(putError)
	}
}

// postNodeSecretDelivery sends a delivery request as the given principal and
// auth method; withTLS simulates an mTLS connection.
func postNodeSecretDelivery(apiServer *Server, principal security.Principal, authMethod string, withTLS bool, body string) *httptest.ResponseRecorder {
	return postNodeDeliveryRequest(apiServer.handleNodeSecretDelivery, NodeSecretDeliveryPath, principal, authMethod, withTLS, body)
}

// postNodeDeliveryRequest sends a POST to a node delivery handler as the given
// principal and auth method; withTLS simulates an mTLS connection.
func postNodeDeliveryRequest(handler http.HandlerFunc, path string, principal security.Principal, authMethod string, withTLS bool, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	requestContext := security.WithPrincipalStruct(request.Context(), principal)
	requestContext = security.WithAuthMethod(requestContext, authMethod)
	request = request.WithContext(requestContext)
	if withTLS {
		request.TLS = &tls.ConnectionState{}
	}
	responseRecorder := httptest.NewRecorder()
	handler(responseRecorder, request)
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

func TestNodeGateKeyDeliveryHandlerServesGateProxyNodeOnly(testContext *testing.T) {
	apiServer := newSecretDeliveryTestServer(testContext)
	nodePrincipal := security.Principal{Kind: security.PrincipalKindNode, Name: "node-1"}
	responseRecorder := postNodeDeliveryRequest(apiServer.handleNodeGateKeyDelivery, NodeGateKeyDeliveryPath, nodePrincipal, "mtls", true, "")
	if responseRecorder.Code != http.StatusOK || responseRecorder.Header().Get("Cache-Control") != "no-store" {
		testContext.Fatalf("status = %d, body %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var delivered security.DeliveredGateKeys
	if decodeError := json.Unmarshal(responseRecorder.Body.Bytes(), &delivered); decodeError != nil {
		testContext.Fatalf("decode: %v", decodeError)
	}
	if len(delivered.Gates) != 1 || delivered.Gates[0].Gate != "web" || !strings.Contains(delivered.Gates[0].PrivateKeyPEM, "PRIVATE KEY") {
		testContext.Fatalf("unexpected delivery %+v", delivered)
	}

	userPrincipal := security.Principal{Kind: security.PrincipalKindUser, Name: "local-admin"}
	userResponse := postNodeDeliveryRequest(apiServer.handleNodeGateKeyDelivery, NodeGateKeyDeliveryPath, userPrincipal, "local-token", true, "")
	if userResponse.Code != http.StatusForbidden || strings.Contains(userResponse.Body.String(), "PRIVATE KEY") {
		testContext.Fatalf("user: status = %d", userResponse.Code)
	}
	otherNode := security.Principal{Kind: security.PrincipalKindNode, Name: "node-2"}
	otherResponse := postNodeDeliveryRequest(apiServer.handleNodeGateKeyDelivery, NodeGateKeyDeliveryPath, otherNode, "mtls", true, "")
	if otherResponse.Code != http.StatusForbidden || strings.Contains(otherResponse.Body.String(), "PRIVATE KEY") {
		testContext.Fatalf("unenrolled node: status = %d", otherResponse.Code)
	}
}

// putGateKeyRequest sends PUT /api/gate-key as principal through the handler.
func putGateKeyRequest(apiServer *Server, principal security.Principal, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPut, GateKeyPath, strings.NewReader(body))
	request = request.WithContext(security.WithPrincipalStruct(request.Context(), principal))
	responseRecorder := httptest.NewRecorder()
	apiServer.handleGateKey(responseRecorder, request)
	return responseRecorder
}

func TestGateKeyHandlerRequiresSecretWriteAndRejectsMismatch(testContext *testing.T) {
	apiServer := newSecretDeliveryTestServer(testContext)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:admin", security.CapabilitySecretWrite, security.ScopeCluster)
	apiAuthorizer.Grant("user:deployer", security.CapabilityWorkloadCreate, security.ScopeCluster)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	adminPrincipal := security.Principal{Kind: security.PrincipalKindUser, Name: "admin"}

	certificateAuthority, _ := security.NewCertificateAuthority(time.Hour)
	issued, _ := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{CommonName: "manual.example.com", DNSNames: []string{"manual.example.com"}, TTL: time.Hour})
	other, _ := certificateAuthority.IssueCertificate(security.IssueCertificateRequest{CommonName: "manual.example.com", DNSNames: []string{"manual.example.com"}, TTL: time.Hour})
	ctx := context.Background()
	_, _ = apiServer.factStore.Put(ctx, types.KeyDesiredRouteTLSMode("manual"), []byte(types.RouteTLSModeManual))
	_, _ = apiServer.factStore.Put(ctx, types.KeyDesiredRouteTLSCert("manual"), issued.CertificatePEM)

	requestBody := func(privateKeyPEM []byte) string {
		encoded, _ := json.Marshal(GateKeyRequest{Gate: "manual", PrivateKeyPEM: string(privateKeyPEM)})
		return string(encoded)
	}
	deployerResponse := putGateKeyRequest(apiServer, security.Principal{Kind: security.PrincipalKindUser, Name: "deployer"}, requestBody(issued.PrivateKeyPEM))
	if deployerResponse.Code != http.StatusForbidden {
		testContext.Fatalf("workload.create only: status = %d", deployerResponse.Code)
	}
	if mismatchResponse := putGateKeyRequest(apiServer, adminPrincipal, requestBody(other.PrivateKeyPEM)); mismatchResponse.Code != http.StatusConflict {
		testContext.Fatalf("mismatch: status = %d, body %s", mismatchResponse.Code, mismatchResponse.Body.String())
	}
	if emptyResponse := putGateKeyRequest(apiServer, adminPrincipal, `{"gate":"manual"}`); emptyResponse.Code != http.StatusBadRequest {
		testContext.Fatalf("missing key: status = %d", emptyResponse.Code)
	}
	if okResponse := putGateKeyRequest(apiServer, adminPrincipal, requestBody(issued.PrivateKeyPEM)); okResponse.Code != http.StatusOK {
		testContext.Fatalf("matching key: status = %d, body %s", okResponse.Code, okResponse.Body.String())
	}
	sealedFact, getError := apiServer.factStore.Get(ctx, "secrets/"+types.SecretNameRouteTLSKey("manual"))
	if getError != nil || strings.Contains(string(sealedFact.Value), "PRIVATE KEY") {
		testContext.Fatalf("sealed key missing or plaintext: %v", getError)
	}
}

func TestApplyEndpointRejectsInlineGateKeys(testContext *testing.T) {
	keyDirectory := testContext.TempDir()
	certificatePath := filepath.Join(keyDirectory, "cert.pem")
	keyPath := filepath.Join(keyDirectory, "key.pem")
	_ = os.WriteFile(certificatePath, []byte("-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"), 0o600)
	_ = os.WriteFile(keyPath, []byte("-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n"), 0o600)
	apiServer := NewServer(store.NewMemoryStore())
	dslConfig := fmt.Sprintf("gate web {\n    host \"a.example.com\"\n    service web port 80\n    tls {\n        cert %q\n        key %q\n    }\n}\n", certificatePath, keyPath)
	responseRecorder := httptest.NewRecorder()
	apiServer.applyDirect(responseRecorder, context.Background(), dslConfig)
	if responseRecorder.Code != http.StatusBadRequest || !strings.Contains(responseRecorder.Body.String(), "does not read local files") {
		testContext.Fatalf("status = %d, body %s", responseRecorder.Code, responseRecorder.Body.String())
	}
	if facts, _ := apiServer.factStore.Scan(context.Background(), types.ScanDesiredRoutes); len(facts) != 0 {
		testContext.Fatalf("facts written despite rejection: %d", len(facts))
	}
}

func TestSecretAPIProtectsSystemOwnedGateKeys(testContext *testing.T) {
	rawStore := store.NewMemoryStore()
	masterKey, _ := security.GenerateMasterKey()
	secretStore, _ := security.NewSecretStoreWithMasterKey(rawStore, masterKey)
	ctx := context.Background()
	_ = secretStore.PutSecret(ctx, types.SecretNameRouteTLSKey("web"), []byte("-----BEGIN EC PRIVATE KEY-----"))
	_ = secretStore.PutSecret(ctx, "db.password", []byte("s3cret"))
	apiServer := NewServer(rawStore)
	apiServer.SetSecretStore(secretStore)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	adminPrincipal := security.Principal{Kind: security.PrincipalKindUser, Name: "admin"}

	sendSecretRequest := func(method string, target string, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request = request.WithContext(security.WithPrincipalStruct(request.Context(), adminPrincipal))
		responseRecorder := httptest.NewRecorder()
		apiServer.handleSecret(responseRecorder, request)
		return responseRecorder
	}
	systemKeyTarget := "/api/secret?name=" + types.SecretNameRouteTLSKey("web")
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if response := sendSecretRequest(method, systemKeyTarget, "x"); response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "PRIVATE KEY") {
			testContext.Errorf("%s system key: status = %d", method, response.Code)
		}
	}
	listResponse := sendSecretRequest(http.MethodGet, "/api/secret", "")
	if listResponse.Code != http.StatusOK || strings.Contains(listResponse.Body.String(), "route/") || !strings.Contains(listResponse.Body.String(), "db.password") {
		testContext.Errorf("list: status = %d, body %s", listResponse.Code, listResponse.Body.String())
	}
	if stillSealed, getError := secretStore.GetSecret(ctx, types.SecretNameRouteTLSKey("web")); getError != nil || len(stillSealed) == 0 {
		testContext.Error("system key was modified or deleted")
	}
}
