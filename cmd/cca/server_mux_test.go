// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// serverMuxTestTimeout bounds each request against the test server.
const serverMuxTestTimeout = 5 * time.Second

// noGrantsTestAuthenticator authenticates every request as a user without grants.
type noGrantsTestAuthenticator struct{}

// Authenticate returns the ungranted test user.
func (authenticator *noGrantsTestAuthenticator) Authenticate(_ *http.Request) (*security.AuthenticationResult, error) {
	return &security.AuthenticationResult{Principal: "user:nogrants", PrincipalKind: security.PrincipalKindUser}, nil
}

// reserveLocalAddress returns a free 127.0.0.1 address for the server to bind.
func reserveLocalAddress(t *testing.T) string {
	t.Helper()
	probeListener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		t.Fatal(listenError)
	}
	address := probeListener.Addr().String()
	_ = probeListener.Close()
	return address
}

// TestServerMuxServesOnlyAPIServerRoutes starts the server's HTTP mux with a
// user without grants: /status must not exist (404, no cluster data) and every
// forwarded path must be answered by the API server's own authorization
// (spec api-route-authorization, "No legacy status endpoint").
func TestServerMuxServesOnlyAPIServerRoutes(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	address := reserveLocalAddress(t)
	apiServer := launchStatusAPIServer(factStore, security.NewAuthenticatorChain(&noGrantsTestAuthenticator{}), address, nil, false)
	defer apiServer.Close()
	// cca server sets the authorizer after launch (configureAndStartAPIServer).
	apiServer.SetAPIAuthorizer(security.NewAPIAuthorizer())

	expectations := []struct {
		path       string // path is the request path.
		wantStatus int    // wantStatus is the expected HTTP status.
	}{
		{"/status", http.StatusNotFound},
		{"/api/status", http.StatusForbidden},
		{"/healthz", http.StatusOK},
		{"/no-such-route", http.StatusNotFound},
	}
	client := &http.Client{Timeout: serverMuxTestTimeout}
	for _, expectation := range expectations {
		response, requestError := client.Get("http://" + address + expectation.path)
		if requestError != nil {
			t.Fatalf("GET %s: %v", expectation.path, requestError)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != expectation.wantStatus {
			t.Errorf("GET %s = %d, want %d (body %.80q)", expectation.path, response.StatusCode, expectation.wantStatus, body)
		}
		if expectation.path == "/status" && strings.Contains(string(body), "CLUSTER STATUS") {
			t.Errorf("GET /status returned cluster data to a user without grants")
		}
	}
}
