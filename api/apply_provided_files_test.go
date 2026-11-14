// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/types"
)

// configWithFromFile returns a configuration whose config file is read from fromPath.
func configWithFromFile(fromPath string) string {
	return "service web {\n    image nginx:1.27\n    instances 1\n    config {\n        file \"/etc/web/app.conf\" from \"" + fromPath + "\"\n    }\n}\n"
}

// postConfigurationAsAdmin sends a JSON body to path as user:admin.
func postConfigurationAsAdmin(apiServer *Server, path string, body map[string]any) *httptest.ResponseRecorder {
	encodedBody, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encodedBody)))
	request.Header.Set("Content-Type", "application/json")
	adminContext := security.WithPrincipalStruct(request.Context(), security.Principal{Kind: security.PrincipalKindUser, Name: "admin"})
	adminContext = security.WithPrincipal(adminContext, "user:admin")
	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, request.WithContext(adminContext))
	return recorder
}

// TestApplyReadsReferencedFilesOnlyFromRequest applies a configuration with a
// config "from" file through POST /api/apply, direct and through the policy
// gate, and diffs it through POST /api/diff (spec api-route-authorization,
// "Configuration references a local file"): contents sent in "files" are used;
// a path that exists on the server's disk but was not sent is refused with
// 400, so the server never reads a client-named path.
func TestApplyReadsReferencedFilesOnlyFromRequest(t *testing.T) {
	serverLocalFile := filepath.Join(t.TempDir(), "server-only.conf")
	if writeError := os.WriteFile(serverLocalFile, []byte("read-from-server-disk"), 0o600); writeError != nil {
		t.Fatal(writeError)
	}
	configFileKey := types.KeyDesiredServiceConfigFile("web", "/etc/web/app.conf")

	for _, withPolicyGate := range []bool{false, true} {
		apiServer, apiAuthorizer, rawStore := newServicePrincipalServer(t, withPolicyGate)
		apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)

		accepted := postConfigurationAsAdmin(apiServer, "/api/apply", map[string]any{
			"config": configWithFromFile("app.conf"),
			"files":  map[string]string{"app.conf": "sent-by-client"},
		})
		if accepted.Code != http.StatusOK {
			t.Fatalf("policy gate %v: apply with the file sent: HTTP %d %s", withPolicyGate, accepted.Code, accepted.Body.String())
		}
		if storedFact, getError := rawStore.Get(context.Background(), configFileKey); getError != nil || string(storedFact.Value) != "sent-by-client" {
			t.Errorf("policy gate %v: config file fact = %v %v, want the contents sent in the request", withPolicyGate, storedFact, getError)
		}

		for _, routePath := range []string{"/api/apply", "/api/diff"} {
			for _, sentFiles := range []map[string]string{nil, {"other.conf": "x"}} {
				refused := postConfigurationAsAdmin(apiServer, routePath, map[string]any{"config": configWithFromFile(serverLocalFile), "files": sentFiles})
				if refused.Code != http.StatusBadRequest && refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "not sent with the request") {
					t.Errorf("policy gate %v, %s, files %v: HTTP %d %s, want a refusal naming the unsent file", withPolicyGate, routePath, sentFiles, refused.Code, refused.Body.String())
				}
				if strings.Contains(refused.Body.String(), "server-disk") {
					t.Errorf("policy gate %v, %s: the server read its own disk: %s", withPolicyGate, routePath, refused.Body.String())
				}
			}
		}
		// Gate tls: a cert file not sent is refused; key file contents in the
		// apply body are refused (keys go only to PUT /api/gate-key).
		gateCases := []struct {
			name      string            // name describes the case.
			tlsBlock  string            // tlsBlock is the gate's tls block.
			files     map[string]string // files is the "files" field sent.
			wantError string            // wantError is a fragment of the expected refusal.
		}{
			{"cert not sent", `tls { cert "` + serverLocalFile + `" key secret "web-key" }`, nil, "not sent with the request"},
			{"key file sent", `tls { cert "c.pem" key "k.pem" }`, map[string]string{"c.pem": "x", "k.pem": "y"}, "PUT /api/gate-key"},
		}
		for _, gateCase := range gateCases {
			gateConfiguration := "service web {\n    image nginx:1.27\n}\ngate web-tls {\n    host \"web.example.com\"\n    service web port 8080\n    " + gateCase.tlsBlock + "\n}\n"
			refused := postConfigurationAsAdmin(apiServer, "/api/apply", map[string]any{"config": gateConfiguration, "files": gateCase.files})
			if refused.Code != http.StatusBadRequest && refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), gateCase.wantError) {
				t.Errorf("policy gate %v, gate %s: HTTP %d %s, want a refusal containing %q", withPolicyGate, gateCase.name, refused.Code, refused.Body.String(), gateCase.wantError)
			}
		}
		if storedFact, _ := rawStore.Get(context.Background(), configFileKey); storedFact == nil || strings.Contains(string(storedFact.Value), "server-disk") {
			t.Errorf("policy gate %v: a refused request changed the config file fact: %v", withPolicyGate, storedFact)
		}
	}
}
