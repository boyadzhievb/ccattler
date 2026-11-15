// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// Per-principal stream cap tests (M225, design/plans/plan-per-principal-stream-cap.md).

// streamSlotTestAdmin is the cluster admin principal of these tests.
const streamSlotTestAdmin = "user:slot-admin"

// bearerPrincipalAuthenticator authenticates "Bearer <kind:name>" as that
// principal in group platform (like local-token callers, so audit-follow is
// allowed); tokenAliases maps other tokens to a principal (shared identity).
type bearerPrincipalAuthenticator struct {
	tokenAliases map[string]string // tokenAliases maps a token to the principal it authenticates as.
}

// Authenticate returns the principal named by the bearer token.
func (bearerAuthenticator bearerPrincipalAuthenticator) Authenticate(request *http.Request) (*security.AuthenticationResult, error) {
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if aliasedPrincipal, isAlias := bearerAuthenticator.tokenAliases[token]; isAlias {
		token = aliasedPrincipal
	}
	if !strings.Contains(token, ":") {
		return nil, nil
	}
	return &security.AuthenticationResult{Principal: token, PrincipalKind: security.PrincipalKindUser, Groups: []string{"platform"}, AuthMethod: "local-token"}, nil
}

// newStreamSlotHarness serves a server where alice, bob and local-admin hold
// cluster workload.read and slot-admin holds cluster.admin.
func newStreamSlotHarness(t *testing.T, tokenAliases map[string]string) *streamTestHarness {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	apiAuthorizer := security.NewAPIAuthorizer()
	for _, readerPrincipal := range []string{"user:alice", "user:bob", "user:local-admin"} {
		apiAuthorizer.Grant(readerPrincipal, security.CapabilityWorkloadRead, security.ScopeCluster)
	}
	apiAuthorizer.Grant(streamSlotTestAdmin, security.CapabilityClusterAdmin, security.ScopeCluster)
	apiServer := NewServer(factStore)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(bearerPrincipalAuthenticator{tokenAliases: tokenAliases}))
	apiServer.SetRequirePrincipal(true)
	apiServer.SetAuditStore(factStore)
	apiServer.streamReauthorizationInterval = streamTestInterval
	httpServer := httptest.NewServer(apiServer.Handler())
	t.Cleanup(httpServer.Close)
	return &streamTestHarness{t: t, apiServer: apiServer, apiAuthorizer: apiAuthorizer, httpServer: httpServer}
}

// tryOpenStream opens routePath with token and returns the status and, on a
// refusal, the body. An admitted stream stays open until the test ends.
func (harness *streamTestHarness) tryOpenStream(routePath string, token string) (int, string) {
	harness.t.Helper()
	streamContext, cancelStream := context.WithCancel(context.Background())
	harness.t.Cleanup(cancelStream)
	request, _ := http.NewRequestWithContext(streamContext, http.MethodGet, harness.httpServer.URL+routePath, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, sendError := http.DefaultClient.Do(request)
	if sendError != nil {
		harness.t.Fatal(sendError)
	}
	if response.StatusCode == http.StatusOK {
		harness.t.Cleanup(func() { _ = response.Body.Close() })
		return response.StatusCode, ""
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(responseBody)
}

// openStreamsOf returns principal's open streams and the total.
func (harness *streamTestHarness) openStreamsOf(principal string) (int, int) {
	snapshot := harness.apiServer.streamSlots.snapshot()
	for _, principalCount := range snapshot.Principals {
		if principalCount.Principal == principal {
			return principalCount.OpenStreams, snapshot.Total
		}
	}
	return 0, snapshot.Total
}

// waitForOpenStreams waits until principal holds wantPrincipal streams and
// the server wantTotal, and fails with the last counts otherwise.
func (harness *streamTestHarness) waitForOpenStreams(principal string, wantPrincipal int, wantTotal int) {
	harness.t.Helper()
	deadline := time.Now().Add(streamTestCloseWait)
	for {
		principalOpen, totalOpen := harness.openStreamsOf(principal)
		if principalOpen == wantPrincipal && totalOpen == wantTotal {
			return
		}
		if time.Now().After(deadline) {
			harness.t.Fatalf("%s holds %d streams of %d open, want %d of %d", principal, principalOpen, totalOpen, wantPrincipal, wantTotal)
		}
		time.Sleep(streamTestInterval / 4)
	}
}

// TestStreamCapRefusesPrincipalOverLimit (rough user): one principal opens
// one stream more than the limit; it gets 429 naming the limit, and another
// user still opens a stream.
func TestStreamCapRefusesPrincipalOverLimit(t *testing.T) {
	harness := newStreamSlotHarness(t, nil)
	for streamIndex := range maxStreamsPerPrincipal {
		if statusCode, refusal := harness.tryOpenStream(streamRoutes[streamIndex%len(streamRoutes)], "user:alice"); statusCode != http.StatusOK {
			t.Fatalf("stream %d of the limit refused: HTTP %d %s", streamIndex+1, statusCode, refusal)
		}
	}
	statusCode, refusal := harness.tryOpenStream(streamRoutes[0], "user:alice")
	if statusCode != http.StatusTooManyRequests || !strings.Contains(refusal, "too many open streams for user:alice (limit 32)") {
		t.Errorf("stream over the limit: HTTP %d %s, want 429 naming the principal and limit", statusCode, refusal)
	}
	if statusCode, refusal := harness.tryOpenStream(streamRoutes[0], "user:bob"); statusCode != http.StatusOK {
		t.Errorf("another user refused while alice is at her limit: HTTP %d %s", statusCode, refusal)
	}
}

// TestStreamCapAdmitsExactLimitUnderConcurrency (rough user, parallel): 64
// opens at once by one principal admit exactly the limit.
func TestStreamCapAdmitsExactLimitUnderConcurrency(t *testing.T) {
	harness := newStreamSlotHarness(t, nil)
	var admittedStreams atomic.Int64
	var openers sync.WaitGroup
	for range 2 * maxStreamsPerPrincipal {
		openers.Add(1)
		go func() {
			defer openers.Done()
			streamContext, cancelStream := context.WithCancel(context.Background())
			t.Cleanup(cancelStream)
			request, _ := http.NewRequestWithContext(streamContext, http.MethodGet, harness.httpServer.URL+streamRoutes[0], nil)
			request.Header.Set("Authorization", "Bearer user:alice")
			response, sendError := http.DefaultClient.Do(request)
			if sendError != nil {
				return
			}
			t.Cleanup(func() { _ = response.Body.Close() })
			if response.StatusCode == http.StatusOK {
				admittedStreams.Add(1)
			}
		}()
	}
	openers.Wait()
	if admitted := admittedStreams.Load(); admitted != maxStreamsPerPrincipal {
		t.Errorf("%d parallel opens admitted %d streams, want exactly %d", 2*maxStreamsPerPrincipal, admitted, maxStreamsPerPrincipal)
	}
}

// TestStreamCapSharedIdentityShareOneAllowance (careless operator): two
// operators on the control node's local token are both user:local-admin and
// share one allowance; the admin view shows who holds it.
func TestStreamCapSharedIdentityShareOneAllowance(t *testing.T) {
	harness := newStreamSlotHarness(t, map[string]string{"operator-1": "user:local-admin", "operator-2": "user:local-admin"})
	for streamIndex := range maxStreamsPerPrincipal {
		operatorToken := []string{"operator-1", "operator-2"}[streamIndex%2]
		if statusCode, refusal := harness.tryOpenStream(streamRoutes[0], operatorToken); statusCode != http.StatusOK {
			t.Fatalf("stream %d refused: HTTP %d %s", streamIndex+1, statusCode, refusal)
		}
	}
	if statusCode, _ := harness.tryOpenStream(streamRoutes[2], "operator-1"); statusCode != http.StatusTooManyRequests {
		t.Errorf("audit-follow on the shared local token at the limit: HTTP %d, want 429", statusCode)
	}
	if principalOpen, _ := harness.openStreamsOf("user:local-admin"); principalOpen != maxStreamsPerPrincipal {
		t.Errorf("user:local-admin holds %d streams, want %d", principalOpen, maxStreamsPerPrincipal)
	}
}

// TestStreamSlotsFreedAfterGrantRevoked (admin remedy): revoking the grant
// of a user at the limit ends the streams and frees every slot.
func TestStreamSlotsFreedAfterGrantRevoked(t *testing.T) {
	harness := newStreamSlotHarness(t, nil)
	for range maxStreamsPerPrincipal {
		harness.tryOpenStream(streamRoutes[0], "user:alice")
	}
	harness.waitForOpenStreams("user:alice", maxStreamsPerPrincipal, maxStreamsPerPrincipal)
	harness.apiAuthorizer.ReplaceGrants(map[string][]security.CapabilityGrant{
		"user:bob": {{Capability: security.CapabilityWorkloadRead, Scope: security.ScopeCluster}},
	})
	harness.waitForOpenStreams("user:alice", 0, 0)
	if statusCode, refusal := harness.tryOpenStream(streamRoutes[0], "user:bob"); statusCode != http.StatusOK {
		t.Errorf("bob refused after alice's slots were freed: HTTP %d %s", statusCode, refusal)
	}
}

// TestStreamRefusalReleasesSlot: every way a stream open is refused after its
// slot was taken leaves no slot behind.
func TestStreamRefusalReleasesSlot(t *testing.T) {
	harness := newStreamSlotHarness(t, nil)
	refusedOpens := map[string]int{
		"/api/watch":                    http.StatusBadRequest, // prefix missing
		"/api/watch?prefix=auth/user/x": http.StatusForbidden,  // prefix not readable
	}
	for routePath, wantStatus := range refusedOpens {
		if statusCode, refusal := harness.tryOpenStream(routePath, "user:alice"); statusCode != wantStatus {
			t.Errorf("%s: HTTP %d %s, want %d", routePath, statusCode, refusal, wantStatus)
		}
	}
	harness.apiServer.streamSlots.setTotalOpenForTest(maxConcurrentWatchConnections)
	for _, routePath := range streamRoutes {
		if statusCode, _ := harness.tryOpenStream(routePath, "user:alice"); statusCode != http.StatusServiceUnavailable {
			t.Errorf("%s with every slot taken: HTTP %d, want 503", routePath, statusCode)
		}
	}
	harness.apiServer.streamSlots.setTotalOpenForTest(0)
	if snapshot := harness.apiServer.streamSlots.snapshot(); snapshot.Total != 0 || len(snapshot.Principals) != 0 {
		t.Errorf("refused opens left slots behind: %+v", snapshot)
	}
}

// TestStreamSlotReleaseIsIdempotent: a release called twice frees one slot.
func TestStreamSlotReleaseIsIdempotent(t *testing.T) {
	registry := newStreamSlotRegistry()
	releaseFirst, firstRefusal := registry.reserve("user:alice")
	_, secondRefusal := registry.reserve("user:alice")
	if firstRefusal != "" || secondRefusal != "" {
		t.Fatalf("reserve refused: %q %q", firstRefusal, secondRefusal)
	}
	releaseFirst()
	releaseFirst()
	if snapshot := registry.snapshot(); snapshot.Total != 1 || snapshot.Principals[0].OpenStreams != 1 {
		t.Errorf("after one release called twice: %+v, want alice holding 1 of 1", snapshot)
	}
}

// TestStreamSlotRefusesMissingPrincipal: without a principal (a server
// without an authenticator chain) the stream is refused, not uncounted.
func TestStreamSlotRefusesMissingPrincipal(t *testing.T) {
	apiServer := NewServer(nil)
	recorder := httptest.NewRecorder()
	if _, reserved := apiServer.reserveStreamSlot(recorder, httptest.NewRequest(http.MethodGet, "/api/watch?prefix=desired", nil)); reserved {
		t.Fatal("a stream without a principal was given a slot")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("HTTP %d, want 401", recorder.Code)
	}
}

// TestStreamsEndpointRequiresClusterAdmin: /api/streams shows who holds the
// slots, only to cluster admins.
func TestStreamsEndpointRequiresClusterAdmin(t *testing.T) {
	harness := newStreamSlotHarness(t, nil)
	for range 3 {
		harness.tryOpenStream(streamRoutes[0], "user:alice")
	}
	for token, wantStatus := range map[string]int{"user:alice": http.StatusForbidden, streamSlotTestAdmin: http.StatusOK} {
		request, _ := http.NewRequest(http.MethodGet, harness.httpServer.URL+"/api/streams", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, sendError := http.DefaultClient.Do(request)
		if sendError != nil {
			t.Fatal(sendError)
		}
		responseBody, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != wantStatus {
			t.Errorf("GET /api/streams as %s: HTTP %d, want %d", token, response.StatusCode, wantStatus)
			continue
		}
		if wantStatus != http.StatusOK {
			continue
		}
		var snapshot streamSlotSnapshot
		if decodeError := json.Unmarshal(responseBody, &snapshot); decodeError != nil {
			t.Fatal(decodeError)
		}
		if snapshot.Total != 3 || len(snapshot.Principals) != 1 || snapshot.Principals[0] != (principalStreamCount{Principal: "user:alice", OpenStreams: 3}) {
			t.Errorf("GET /api/streams = %s, want alice holding 3 of 3", responseBody)
		}
	}
}

// setTotalOpenForTest sets the global open-stream count, as if other
// principals held that many slots.
func (registry *streamSlotRegistry) setTotalOpenForTest(totalOpen int) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	registry.totalOpen = totalOpen
}
