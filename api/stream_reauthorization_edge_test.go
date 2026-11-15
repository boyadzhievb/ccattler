// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// Edge cases of stream re-authorization found by the M216 review: the open
// path, credential deadlines, the lifetime cap, a client that stops reading
// and a narrowed audit tenant filter. The authenticator is scripted per call.

const (
	// streamTestNeverRecheck is a re-check interval no test reaches, so only
	// a deadline or a write failure can end the stream.
	streamTestNeverRecheck = time.Hour
	// streamTestShortDeadline is a credential or lifetime deadline a test waits out.
	streamTestShortDeadline = 300 * time.Millisecond
	// streamTestFloodFactBytes is the size of each fact written to fill a
	// non-reading client's socket buffers.
	streamTestFloodFactBytes = 256 << 10
	// streamTestFloodFactCount is how many such facts are written.
	streamTestFloodFactCount = 100
)

// scriptedStreamAuthenticator answers the n-th Authenticate call (counted
// from 1) with answer(n), so a test controls the open and every re-check.
type scriptedStreamAuthenticator struct {
	calls  atomic.Int64                                                   // calls counts Authenticate calls.
	answer func(callNumber int64) (*security.AuthenticationResult, error) // answer returns the call's result.
}

// Authenticate returns the scripted answer of this call.
func (scriptedAuthenticator *scriptedStreamAuthenticator) Authenticate(_ *http.Request) (*security.AuthenticationResult, error) {
	return scriptedAuthenticator.answer(scriptedAuthenticator.calls.Add(1))
}

// bobAuthentication is bob's authentication by authMethod expiring at expiresAt.
func bobAuthentication(authMethod string, expiresAt time.Time) *security.AuthenticationResult {
	return &security.AuthenticationResult{Principal: "user:bob", PrincipalKind: security.PrincipalKindUser, AuthMethod: authMethod, ExpiresAt: expiresAt}
}

// alwaysBob authenticates every call as bob with a local token.
func alwaysBob(int64) (*security.AuthenticationResult, error) {
	return bobAuthentication("local-token", time.Time{}), nil
}

// newScriptedStreamHarness serves a server whose only authenticator is
// scripted; bob holds cluster workload.read. configure adjusts the server
// before it starts serving.
func newScriptedStreamHarness(t *testing.T, answer func(int64) (*security.AuthenticationResult, error), configure func(*Server)) (*streamTestHarness, store.StateStore) {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:bob", security.CapabilityWorkloadRead, security.ScopeCluster)
	apiServer := NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(&scriptedStreamAuthenticator{answer: answer}))
	apiServer.SetRequirePrincipal(true)
	apiServer.SetAuditStore(factStore)
	apiServer.streamReauthorizationInterval = streamTestInterval
	if configure != nil {
		configure(apiServer)
	}
	httpServer := httptest.NewServer(apiServer.Handler())
	t.Cleanup(httpServer.Close)
	return &streamTestHarness{t: t, apiServer: apiServer, apiAuthorizer: apiAuthorizer, httpServer: httpServer}, factStore
}

// TestStreamOpenedWhileReauthenticationFailsIsStillRechecked (review CR-1/SA-1):
// the middleware authenticates, every later call is unavailable. The stream
// must still be re-checked and end after the tolerated outage, not run
// unguarded because a second authentication at open failed.
func TestStreamOpenedWhileReauthenticationFailsIsStillRechecked(t *testing.T) {
	harness, _ := newScriptedStreamHarness(t, func(callNumber int64) (*security.AuthenticationResult, error) {
		if callNumber == 1 {
			return bobAuthentication("local-token", time.Time{}), nil
		}
		return nil, security.ErrAuthenticationUnavailable
	}, nil)
	stream := harness.openStream(streamRoutes[0], "scripted")
	requireClosedWithReason(t, streamRoutes[0], stream, streamClosedUnavailable)
}

// TestStreamOpenAuthenticatesOnce (review CR-2): opening a stream runs the
// authenticator chain once (the middleware), not again in the wrapper.
func TestStreamOpenAuthenticatesOnce(t *testing.T) {
	scriptedAuthenticator := &scriptedStreamAuthenticator{answer: alwaysBob}
	harness, _ := newScriptedStreamHarness(t, alwaysBob, func(apiServer *Server) {
		apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(scriptedAuthenticator))
		apiServer.streamReauthorizationInterval = streamTestNeverRecheck
	})
	harness.openStream(streamRoutes[0], "scripted")
	if calls := scriptedAuthenticator.calls.Load(); calls != 1 {
		t.Errorf("opening a stream authenticated %d times, want 1", calls)
	}
}

// TestStreamWithoutAuthenticationResultRefused: with a chain but no
// authentication result (no credentials, principal not required), the
// stream route refuses with 401 instead of streaming unguarded.
func TestStreamWithoutAuthenticationResultRefused(t *testing.T) {
	harness, _ := newScriptedStreamHarness(t, func(int64) (*security.AuthenticationResult, error) { return nil, nil },
		func(apiServer *Server) { apiServer.SetRequirePrincipal(false) })
	for _, routePath := range streamRoutes {
		response, sendError := http.Get(harness.httpServer.URL + routePath)
		if sendError != nil {
			t.Fatal(sendError)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without authentication: HTTP %d, want 401", routePath, response.StatusCode)
		}
	}
}

// TestStreamEndsAtCredentialDeadline (review CR-4): an mTLS certificate's
// NotAfter ends the stream although no re-check runs.
func TestStreamEndsAtCredentialDeadline(t *testing.T) {
	certificateNotAfter := time.Now().Add(streamTestShortDeadline)
	harness, _ := newScriptedStreamHarness(t, func(int64) (*security.AuthenticationResult, error) {
		return bobAuthentication("mtls", certificateNotAfter), nil
	}, func(apiServer *Server) { apiServer.streamReauthorizationInterval = streamTestNeverRecheck })
	stream := harness.openStream(streamRoutes[0], "scripted")
	requireClosedWithReason(t, streamRoutes[0], stream, streamClosedCredentials)
}

// TestStreamEndsAtMaximumLifetime (review CR-4): a credential without a real
// expiry (local token) is capped by the stream lifetime.
func TestStreamEndsAtMaximumLifetime(t *testing.T) {
	harness, _ := newScriptedStreamHarness(t, alwaysBob, func(apiServer *Server) {
		apiServer.streamReauthorizationInterval = streamTestNeverRecheck
		apiServer.streamMaximumLifetime = streamTestShortDeadline
	})
	stream := harness.openStream(streamRoutes[0], "scripted")
	requireClosedWithReason(t, streamRoutes[0], stream, streamClosedLifetimeReached)
}

// TestStreamEndsWhenPrincipalChanges (review CR-4): the same credential now
// authenticates as someone else; the stream ends.
func TestStreamEndsWhenPrincipalChanges(t *testing.T) {
	harness, _ := newScriptedStreamHarness(t, func(callNumber int64) (*security.AuthenticationResult, error) {
		if callNumber == 1 {
			return bobAuthentication("local-token", time.Time{}), nil
		}
		return &security.AuthenticationResult{Principal: "user:mallory", PrincipalKind: security.PrincipalKindUser, AuthMethod: "local-token"}, nil
	}, nil)
	stream := harness.openStream(streamRoutes[0], "scripted")
	requireClosedWithReason(t, streamRoutes[0], stream, streamClosedCredentials)
}

// TestAuditFollowEndsWhenTenantFilterNarrows (review CR-3, cluster admin
// demoted): a cluster admin follows the whole audit log, then keeps only
// cluster workload.read. The capability check still passes, the tenant
// filter changes, the stream ends, and the reconnect gets team a's filter.
func TestAuditFollowEndsWhenTenantFilterNarrows(t *testing.T) {
	harness := newStreamTestHarness(t)
	token := harness.createUserWithToken("1h")
	harness.apiAuthorizer.Grant("user:alice", security.CapabilityClusterAdmin, security.ScopeCluster)
	auditFollowRoute := streamRoutes[2]
	stream := harness.openStream(auditFollowRoute, token)
	harness.apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"user:alice": {{Capability: security.CapabilityWorkloadRead, Scope: security.ScopeCluster}},
	})
	requireClosedWithReason(t, auditFollowRoute, stream, streamClosedAccess)
	reconnected := harness.openStream(auditFollowRoute, token)
	if sent, ended := reconnected.waitClosed(streamTestStayOpen); ended {
		t.Errorf("reconnect with team a's filter ended; sent:\n%s", sent)
	}
}

// TestStreamEndsWhenClientStopsReading (review SA-3, slowloris): a client
// that stops reading must free its stream slot after one write timeout, not
// hold it until the stream lifetime.
func TestStreamEndsWhenClientStopsReading(t *testing.T) {
	harness, factStore := newScriptedStreamHarness(t, alwaysBob, func(apiServer *Server) {
		apiServer.streamReauthorizationInterval = streamTestNeverRecheck
		apiServer.streamWriteTimeout = streamTestShortDeadline
	})
	connection := openNonReadingStream(t, harness.httpServer.Listener.Addr().String(), streamRoutes[0])
	defer func() { _ = connection.Close() }()
	floodValue := []byte(strings.Repeat("x", streamTestFloodFactBytes))
	for factIndex := range streamTestFloodFactCount {
		if _, putError := factStore.Put(context.Background(), fmt.Sprintf("desired/flood/%03d", factIndex), floodValue); putError != nil {
			t.Fatal(putError)
		}
	}
	slotDeadline := time.Now().Add(streamTestCloseWait)
	for harness.apiServer.streamSlots.snapshot().Total != 0 {
		if time.Now().After(slotDeadline) {
			t.Fatalf("a client that stopped reading still holds its stream slot %s later", streamTestCloseWait)
		}
		time.Sleep(streamTestInterval / 4)
	}
}

// openNonReadingStream opens routePath on a raw connection, waits for the
// response headers and then never reads again.
func openNonReadingStream(t *testing.T, serverAddress string, routePath string) net.Conn {
	t.Helper()
	connection, dialError := net.Dial("tcp", serverAddress)
	if dialError != nil {
		t.Fatal(dialError)
	}
	_, _ = connection.Write([]byte("GET " + routePath + " HTTP/1.1\r\nHost: stream.test\r\nAuthorization: Bearer scripted\r\n\r\n"))
	statusLine, readError := bufio.NewReader(connection).ReadString('\n')
	if readError != nil || !strings.Contains(statusLine, "200") {
		t.Fatalf("open %s: %q %v", routePath, statusLine, readError)
	}
	return connection
}
