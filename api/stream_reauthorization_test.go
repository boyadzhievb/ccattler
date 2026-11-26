// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// Stream re-authorization tests (M216, design/plans/plan-stream-reauthorization.md).
// Each test is a persona row of the plan; the re-check interval is shortened.

const (
	// streamTestInterval replaces the 30 s re-check interval in these tests.
	streamTestInterval = 100 * time.Millisecond
	// streamTestCloseWait is how long a stream may take to close after a change.
	streamTestCloseWait = 3 * time.Second
	// streamTestStayOpen is how long a stream must stay open when nothing changes.
	streamTestStayOpen = 6 * streamTestInterval
)

// streamRoutes are the three streaming routes with the query each needs.
var streamRoutes = []string{"/api/watch?prefix=desired", "/api/events/stream", "/api/audit?follow=true"}

// flakyAuthStore fails Get of auth/user/ keys while failing is set, to
// simulate an etcd leader election under the user-record check.
type flakyAuthStore struct {
	store.StateStore               // StateStore is the real store.
	failing          atomic.Bool   // failing makes user-record reads fail.
	failedRead       chan struct{} // failedRead receives one signal per read refused while failing (never blocks).
}

// Get fails user-record reads while failing is set.
func (flakyStore *flakyAuthStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	if flakyStore.failing.Load() && strings.HasPrefix(key, "auth/user/") {
		select {
		case flakyStore.failedRead <- struct{}{}:
		default:
		}
		return nil, errors.New("etcdserver: leader changed")
	}
	return flakyStore.StateStore.Get(ctx, key)
}

// streamTestAdminAuthenticator authenticates requests without a bearer token
// as the admin, like the operator's local token.
type streamTestAdminAuthenticator struct{}

// Authenticate returns the admin principal for requests without a bearer token.
func (streamTestAdminAuthenticator) Authenticate(request *http.Request) (*security.AuthenticationResult, error) {
	if request.Header.Get("Authorization") != "" {
		return nil, nil
	}
	return &security.AuthenticationResult{Principal: "user:stream-admin", PrincipalKind: security.PrincipalKindUser, AuthMethod: "local-token"}, nil
}

// streamTestHarness is a real HTTP server with user tokens, the user-record
// check and an authorizer whose store grants a test can replace.
type streamTestHarness struct {
	t             *testing.T
	flakyStore    *flakyAuthStore         // flakyStore is what the user-record check reads.
	apiServer     *Server                 // apiServer is the server under test.
	apiAuthorizer *security.APIAuthorizer // apiAuthorizer holds the test grants.
	httpServer    *httptest.Server        // httpServer serves apiServer over a real connection.
}

// newStreamTestHarness builds the harness with the shortened re-check interval.
func newStreamTestHarness(t *testing.T) *streamTestHarness {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	flakyStore := &flakyAuthStore{StateStore: factStore, failedRead: make(chan struct{}, streamMaxConsecutiveUnavailable)}
	signingKey, keyError := security.GenerateOIDCKeyPair()
	if keyError != nil {
		t.Fatal(keyError)
	}
	workloadIssuer := security.NewWorkloadTokenIssuerWithKey("https://cca.stream.test", signingKey)
	oidcConfig := security.OIDCConfig{Issuer: workloadIssuer.IssuerURL(), Audience: "ccattler", ClaimMapping: security.ClaimMapping{PrincipalClaim: "sub", TeamClaim: "team"}}
	userTokens := security.NewLocalUserRecordAuthenticator(security.NewBearerTokenAuthenticator(security.NewOIDCAuthenticator(oidcConfig, workloadIssuer.PublicKey())), flakyStore)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.GrantRole("user:stream-admin", "cluster-admin")
	apiServer := NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(userTokens, streamTestAdminAuthenticator{}))
	apiServer.SetRequirePrincipal(true)
	apiServer.SetUserTokenIssuer(security.NewUserTokenIssuerWithKey(workloadIssuer.IssuerURL(), signingKey))
	apiServer.SetAuditStore(factStore)
	apiServer.streamReauthorizationInterval = streamTestInterval
	httpServer := httptest.NewServer(apiServer.Handler())
	t.Cleanup(httpServer.Close)
	return &streamTestHarness{t: t, flakyStore: flakyStore, apiServer: apiServer, apiAuthorizer: apiAuthorizer, httpServer: httpServer}
}

// adminRequest sends a request as the admin and fails on a non-200.
func (harness *streamTestHarness) adminRequest(method string, path string, body string) []byte {
	harness.t.Helper()
	request, _ := http.NewRequest(method, harness.httpServer.URL+path, bytes.NewBufferString(body))
	response, sendError := http.DefaultClient.Do(request)
	if sendError != nil {
		harness.t.Fatal(sendError)
	}
	defer func() { _ = response.Body.Close() }()
	var responseBody bytes.Buffer
	_, _ = responseBody.ReadFrom(response.Body)
	if response.StatusCode != http.StatusOK {
		harness.t.Fatalf("%s %s: HTTP %d %s", method, path, response.StatusCode, responseBody.String())
	}
	return responseBody.Bytes()
}

// createUserWithToken creates alice in team a with a cluster-wide workload.read
// grant and returns a user token with the given lifetime.
func (harness *streamTestHarness) createUserWithToken(tokenTTL string) string {
	harness.t.Helper()
	harness.adminRequest(http.MethodPost, "/api/user?name=alice", `{"team":"a"}`)
	harness.apiAuthorizer.Grant("user:alice", security.CapabilityWorkloadRead, security.ScopeCluster)
	var minted struct {
		Token string `json:"token"` // Token is the user JWT.
	}
	_ = json.Unmarshal(harness.adminRequest(http.MethodPost, "/api/user/token", `{"username":"alice","ttl":"`+tokenTTL+`"}`), &minted)
	if minted.Token == "" {
		harness.t.Fatal("no token minted")
	}
	return minted.Token
}

// openedStream is a running stream: the lines it sent and when it ended.
type openedStream struct {
	lines  chan string     // lines receives every line the server sends.
	closed chan struct{}   // closed is closed when the response body ends.
	text   strings.Builder // text collects the lines read so far.
}

// openStream opens routePath with token and reads it in the background.
func (harness *streamTestHarness) openStream(routePath string, token string) *openedStream {
	harness.t.Helper()
	// Cancelled at cleanup (before the server closes, cleanups run last-in
	// first-out), so a stream the server never ends cannot stall shutdown.
	streamContext, cancelStream := context.WithCancel(context.Background())
	harness.t.Cleanup(cancelStream)
	request, _ := http.NewRequestWithContext(streamContext, http.MethodGet, harness.httpServer.URL+routePath, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, sendError := http.DefaultClient.Do(request)
	if sendError != nil {
		harness.t.Fatal(sendError)
	}
	if response.StatusCode != http.StatusOK {
		harness.t.Fatalf("open %s: HTTP %d", routePath, response.StatusCode)
	}
	stream := &openedStream{lines: make(chan string, 1024), closed: make(chan struct{})}
	harness.t.Cleanup(func() { _ = response.Body.Close() })
	go func() {
		defer close(stream.closed)
		lineScanner := bufio.NewScanner(response.Body)
		for lineScanner.Scan() {
			stream.lines <- lineScanner.Text()
		}
	}()
	return stream
}

// waitClosed waits up to within for the stream to end and returns what it
// sent and whether it ended.
func (stream *openedStream) waitClosed(within time.Duration) (string, bool) {
	deadline := time.After(within)
	for {
		select {
		case line := <-stream.lines:
			stream.text.WriteString(line + "\n")
		case <-stream.closed:
			for {
				select {
				case line := <-stream.lines:
					stream.text.WriteString(line + "\n")
				default:
					return stream.text.String(), true
				}
			}
		case <-deadline:
			return stream.text.String(), false
		}
	}
}

// requireClosedWithReason fails unless the stream ends within the wait with
// the close frame carrying wantReason.
func requireClosedWithReason(t *testing.T, routePath string, stream *openedStream, wantReason streamCloseReason) {
	t.Helper()
	sent, ended := stream.waitClosed(streamTestCloseWait)
	if !ended {
		t.Fatalf("%s: stream still open %s after the change", routePath, streamTestCloseWait)
	}
	if !strings.Contains(sent, "event: closed\ndata: {\"reason\":\""+string(wantReason)+"\"}") {
		t.Errorf("%s: stream ended without the close frame %q; sent:\n%s", routePath, wantReason, sent)
	}
}

// TestStreamEndsAfterUserDeleted (careless user): the account is deleted
// while a forgotten window streams; every route closes with the frame.
func TestStreamEndsAfterUserDeleted(t *testing.T) {
	for _, routePath := range streamRoutes {
		t.Run(routePath, func(t *testing.T) {
			harness := newStreamTestHarness(t)
			stream := harness.openStream(routePath, harness.createUserWithToken("1h"))
			harness.adminRequest(http.MethodDelete, "/api/user?name=alice", "")
			requireClosedWithReason(t, routePath, stream, streamClosedCredentials)
		})
	}
}

// TestStreamEndsAfterGrantRevoked (rough user): the admin revokes the read
// grant while the user streams.
func TestStreamEndsAfterGrantRevoked(t *testing.T) {
	for _, routePath := range streamRoutes {
		t.Run(routePath, func(t *testing.T) {
			harness := newStreamTestHarness(t)
			stream := harness.openStream(routePath, harness.createUserWithToken("1h"))
			harness.apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{})
			requireClosedWithReason(t, routePath, stream, streamClosedAccess)
		})
	}
}

// TestStreamEndsAfterGrantNarrowed (rough user): a cluster-wide reader is
// narrowed to team a while streaming. The watch filter was computed at open,
// so the stream must close and the reconnect gets the narrower filter; audit
// and events need cluster scope, so they close too.
func TestStreamEndsAfterGrantNarrowed(t *testing.T) {
	for _, routePath := range streamRoutes {
		t.Run(routePath, func(t *testing.T) {
			harness := newStreamTestHarness(t)
			stream := harness.openStream(routePath, harness.createUserWithToken("1h"))
			harness.apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
				"user:alice": {{Capability: security.CapabilityWorkloadRead, Scope: security.Scope("team/a")}},
			})
			requireClosedWithReason(t, routePath, stream, streamClosedAccess)
		})
	}
}

// TestStreamEndsAtTokenExpiry (careless developer): a watch left open ends at
// the token's real expiry. No re-check runs, so only the deadline can end it.
func TestStreamEndsAtTokenExpiry(t *testing.T) {
	harness := newStreamTestHarness(t)
	harness.apiServer.streamReauthorizationInterval = streamTestNeverRecheck
	stream := harness.openStream(streamRoutes[0], harness.createUserWithToken("2s"))
	requireClosedWithReason(t, streamRoutes[0], stream, streamClosedCredentials)
}

// TestStreamStaysOpenWhileAccessUnchanged is the positive control: nothing
// changes, the stream survives several re-checks and sends no close frame.
func TestStreamStaysOpenWhileAccessUnchanged(t *testing.T) {
	for _, routePath := range streamRoutes {
		t.Run(routePath, func(t *testing.T) {
			harness := newStreamTestHarness(t)
			stream := harness.openStream(routePath, harness.createUserWithToken("1h"))
			if sent, ended := stream.waitClosed(streamTestStayOpen); ended || strings.Contains(sent, "event: closed") {
				t.Errorf("%s: stream ended although nothing changed; sent:\n%s", routePath, sent)
			}
		})
	}
}

// TestStreamSurvivesShortStoreOutage (careless user, etcd leader election):
// two failed user-record checks in a row keep the stream open; an outage
// that lasts keeps failing and closes it.
func TestStreamSurvivesShortStoreOutage(t *testing.T) {
	harness := newStreamTestHarness(t)
	stream := harness.openStream(streamRoutes[0], harness.createUserWithToken("1h"))
	harness.flakyStore.failing.Store(true)
	for range 2 {
		select {
		case <-harness.flakyStore.failedRead:
		case <-time.After(streamTestCloseWait):
			t.Fatal("no user-record re-check ran during the outage (is the stream re-checked at all?)")
		}
	}
	harness.flakyStore.failing.Store(false)
	if sent, ended := stream.waitClosed(streamTestStayOpen); ended {
		t.Fatalf("a two-check store outage closed the stream; sent:\n%s", sent)
	}
	harness.flakyStore.failing.Store(true)
	requireClosedWithReason(t, streamRoutes[0], stream, streamClosedUnavailable)
}

// TestAuditFollowCountsTowardStreamCap (rough user): with every stream slot
// taken, audit-follow is refused like watch and events.
func TestAuditFollowCountsTowardStreamCap(t *testing.T) {
	harness := newStreamTestHarness(t)
	token := harness.createUserWithToken("1h")
	harness.apiServer.streamSlots.setTotalOpenForTest(maxConcurrentWatchConnections)
	request, _ := http.NewRequest(http.MethodGet, harness.httpServer.URL+"/api/audit?follow=true", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, sendError := http.DefaultClient.Do(request)
	if sendError != nil {
		t.Fatal(sendError)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("audit-follow with every stream slot taken: HTTP %d, want 503", response.StatusCode)
	}
}
