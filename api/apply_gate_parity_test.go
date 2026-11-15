// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// gateParityServiceBlock is the service every gate in these tests targets.
const gateParityServiceBlock = "service web {\n    image nginx:1.27\n}\n"

// newApplyPathServer builds the server for one apply path as production does:
// with the policy gate, as cca server (API reads through the api-server role,
// the gate commits to the raw store); without it, as cca run and the demo
// modes (the API uses the raw store directly).
func newApplyPathServer(t *testing.T, withPolicyGate bool) (*Server, store.StateStore) {
	t.Helper()
	if withPolicyGate {
		apiServer, apiAuthorizer, rawStore := newServicePrincipalServer(t, true)
		apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)
		apiAuthorizer.GrantRole("user:writer", "api-writer")
		return apiServer, rawStore
	}
	rawStore := store.NewMemoryStore()
	t.Cleanup(func() { rawStore.Close() })
	apiServer := NewServer(rawStore)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)
	apiAuthorizer.GrantRole("user:writer", "api-writer")
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	return apiServer, rawStore
}

// TestServerApplyPruneNeedsCapabilityOfRemovedFacts: an admin binds a gate's
// key secret; a writer (workload.create, no secret.use) re-applies the gate
// without tls. Both apply paths must refuse with 403 and keep tls/key_secret
// (review SEC-M2: prune deletes were not authorized).
func TestServerApplyPruneNeedsCapabilityOfRemovedFacts(t *testing.T) {
	withKeySecret := gateParityServiceBlock + "gate web-tls {\n    host \"web.example.com\"\n    service web port 8080\n    tls {\n        cert \"c.pem\"\n        key secret \"web-tls-key\"\n    }\n}\n"
	withoutTLS := gateParityServiceBlock + "gate web-tls {\n    host \"web.example.com\"\n    service web port 8080\n}\n"
	for _, withPolicyGate := range []bool{false, true} {
		apiServer, rawStore := newApplyPathServer(t, withPolicyGate)
		adminResponse := postConfigurationAsAdmin(apiServer, "/api/apply", map[string]any{"config": withKeySecret, "files": map[string]string{"c.pem": "certificate-placeholder"}})
		if adminResponse.Code != http.StatusOK {
			t.Fatalf("policy gate %v: admin apply: HTTP %d %s", withPolicyGate, adminResponse.Code, adminResponse.Body.String())
		}
		writerResponse := postConfigurationAsUser(apiServer, "writer", "/api/apply", map[string]any{"config": withoutTLS})
		if writerResponse.Code != http.StatusForbidden {
			t.Errorf("policy gate %v: writer pruning tls/key_secret: HTTP %d %s, want 403", withPolicyGate, writerResponse.Code, writerResponse.Body.String())
		}
		if _, getError := rawStore.Get(context.Background(), types.KeyDesiredRouteTLSKeySecret("web-tls")); getError != nil {
			t.Errorf("policy gate %v: tls/key_secret was deleted by a writer without secret.use: %v", withPolicyGate, getError)
		}
		if _, getError := rawStore.Get(context.Background(), types.KeyDesiredRouteTLSCert("web-tls")); getError != nil {
			t.Errorf("policy gate %v: refused apply still pruned the gate (tls/cert gone): %v", withPolicyGate, getError)
		}
	}
}

// TestServerApplyPrunesRemovedGateSettings re-applies a gate without its tls
// block through POST /api/apply, direct and through the policy gate: the tls
// facts must be deleted, as cca apply has done since M170 (e2e T14).
func TestServerApplyPrunesRemovedGateSettings(t *testing.T) {
	withTLS := gateParityServiceBlock + "gate web-tls {\n    host \"web.example.com\"\n    service web port 8080\n    tls auto\n}\n"
	withoutTLS := gateParityServiceBlock + "gate web-tls {\n    host \"web.example.com\"\n    service web port 8080\n}\n"
	for _, withPolicyGate := range []bool{false, true} {
		apiServer, rawStore := newApplyPathServer(t, withPolicyGate)
		for _, configuration := range []string{withTLS, withoutTLS} {
			if response := postConfigurationAsAdmin(apiServer, "/api/apply", map[string]any{"config": configuration}); response.Code != http.StatusOK {
				t.Fatalf("policy gate %v: apply: HTTP %d %s", withPolicyGate, response.Code, response.Body.String())
			}
		}
		if modeFact, getError := rawStore.Get(context.Background(), types.KeyDesiredRouteTLSMode("web-tls")); getError == nil {
			t.Errorf("policy gate %v: tls removed from the gate but %s = %q is still stored", withPolicyGate, modeFact.Key, modeFact.Value)
		}
		if _, getError := rawStore.Get(context.Background(), types.KeyDesiredRoute("web-tls")); getError != nil {
			t.Errorf("policy gate %v: the gate itself was deleted: %v", withPolicyGate, getError)
		}
	}
}

// TestServerApplyAcceptsKeyFileWithoutReadingIt applies a manual-tls gate
// that names a key file: the server stores the certificate the client sent,
// never reads or stores the key (cca apply seals it through PUT /api/gate-key
// afterwards), and refuses a key file whose contents were sent in "files".
func TestServerApplyAcceptsKeyFileWithoutReadingIt(t *testing.T) {
	manualGate := gateParityServiceBlock + "gate web-tls {\n    host \"web.example.com\"\n    service web port 8080\n    tls {\n        cert \"c.pem\"\n        key \"k.pem\"\n    }\n}\n"
	for _, withPolicyGate := range []bool{false, true} {
		apiServer, rawStore := newApplyPathServer(t, withPolicyGate)

		accepted := postConfigurationAsAdmin(apiServer, "/api/apply", map[string]any{"config": manualGate, "files": map[string]string{"c.pem": "certificate-placeholder"}})
		if accepted.Code != http.StatusOK {
			t.Fatalf("policy gate %v: apply of a gate naming a key file: HTTP %d %s", withPolicyGate, accepted.Code, accepted.Body.String())
		}
		if certFact, getError := rawStore.Get(context.Background(), types.KeyDesiredRouteTLSCert("web-tls")); getError != nil || string(certFact.Value) != "certificate-placeholder" {
			t.Errorf("policy gate %v: tls cert fact = %v %v, want the certificate sent", withPolicyGate, certFact, getError)
		}
		if storedFacts, _ := rawStore.Scan(context.Background(), "desired/route/web-tls/"); len(storedFacts) > 0 {
			for _, storedFact := range storedFacts {
				if strings.HasSuffix(storedFact.Key, "/tls/key") {
					t.Errorf("policy gate %v: a key fact was stored: %s", withPolicyGate, storedFact.Key)
				}
			}
		}

		keySent := postConfigurationAsAdmin(apiServer, "/api/apply", map[string]any{"config": manualGate, "files": map[string]string{"c.pem": "certificate-placeholder", "k.pem": "key-placeholder"}})
		if keySent.Code != http.StatusBadRequest && keySent.Code != http.StatusForbidden || !strings.Contains(keySent.Body.String(), "PUT /api/gate-key") {
			t.Errorf("policy gate %v: key file contents sent in the apply body: HTTP %d %s, want a refusal pointing to PUT /api/gate-key", withPolicyGate, keySent.Code, keySent.Body.String())
		}
	}
}

// TestServerApplyRefusesCombinedCertAndKeyFile: one file named as both tls
// cert and key would put the private key into the cert fact; the server
// refuses it with an actionable message (review CR-F1, rejected as a bug).
func TestServerApplyRefusesCombinedCertAndKeyFile(t *testing.T) {
	combinedGate := gateParityServiceBlock + "gate web-tls {\n    host \"web.example.com\"\n    service web port 8080\n    tls {\n        cert \"g.pem\"\n        key \"g.pem\"\n    }\n}\n"
	apiServer, _ := newApplyPathServer(t, false)
	response := postConfigurationAsAdmin(apiServer, "/api/apply", map[string]any{"config": combinedGate, "files": map[string]string{"g.pem": "combined-placeholder"}})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "separate cert and key files") {
		t.Errorf("combined cert/key file: HTTP %d %s, want 400 naming separate files", response.Code, response.Body.String())
	}
}

// TestServerApplyBodyOverLimitIs413: a body over maxConfigRequestBodyBytes is
// answered 413 naming the limit, not as invalid JSON (review CR-F3).
func TestServerApplyBodyOverLimitIs413(t *testing.T) {
	apiServer, _ := newApplyPathServer(t, false)
	oversizedFile := strings.Repeat("x", maxConfigRequestBodyBytes)
	for _, routePath := range []string{"/api/apply", "/api/diff"} {
		response := postConfigurationAsAdmin(apiServer, routePath, map[string]any{"config": gateParityServiceBlock, "files": map[string]string{"big.conf": oversizedFile}})
		if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "request limit") {
			t.Errorf("%s with a body over the limit: HTTP %d %s, want 413", routePath, response.Code, response.Body.String())
		}
	}
}
