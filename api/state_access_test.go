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
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

func TestIsReadableStatePrefix(t *testing.T) {
	for _, readablePrefix := range []string{"desired", "desired/", "desired/route/", "observed/node/worker-1/debug_address", "network/node/", "derived/route/", "event/"} {
		if !isReadableStatePrefix(readablePrefix) {
			t.Errorf("prefix %q should be readable", readablePrefix)
		}
	}
	// Short prefixes would make a store prefix scan match sensitive areas.
	for _, unreadablePrefix := range []string{"", "s", "secret", "secrets/", "cred", "credentials/", "enroll", "enrollment/", "bootstrap/", "auth/", "audit/", "leader/", "desiredx/", "/desired/"} {
		if isReadableStatePrefix(unreadablePrefix) {
			t.Errorf("prefix %q must not be readable", unreadablePrefix)
		}
	}
}

func TestStateAPIRefusesPrefixesThatMatchSensitiveAreas(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()
	_, _ = factStore.Put(context.Background(), "secrets/db-password", []byte("ciphertext"))
	_, _ = factStore.Put(context.Background(), "enrollment/node/worker-1", []byte("{}"))

	for _, bypassPrefix := range []string{"secret", "s", "enroll", "auth/"} {
		resp, err := http.Get(baseURL + "/api/state?prefix=" + bypassPrefix)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("prefix %q: got HTTP %d (%s), want 403", bypassPrefix, resp.StatusCode, body)
		}
	}
}

func TestStateAPIHidesTLSPrivateKeys(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = factStore.Put(ctx, types.KeyDesiredRouteTLSKey("web"), []byte("-----BEGIN EC PRIVATE KEY-----"))
	_, _ = factStore.Put(ctx, types.KeyDesiredRouteTLSCert("web"), []byte("-----BEGIN CERTIFICATE-----"))
	_, _ = factStore.Put(ctx, types.KeyDerivedRouteTLSKey("web"), []byte("-----BEGIN EC PRIVATE KEY-----"))

	resp, err := http.Get(baseURL + "/api/state?prefix=desired/route/")
	if err != nil {
		t.Fatal(err)
	}
	var facts []factResponse
	_ = json.NewDecoder(resp.Body).Decode(&facts)
	resp.Body.Close()
	if len(facts) != 1 || facts[0].Key != types.KeyDesiredRouteTLSCert("web") {
		t.Fatalf("scan must return the certificate but not the private key, got %+v", facts)
	}

	resp, err = http.Get(baseURL + "/api/state?key=" + types.KeyDerivedRouteTLSKey("web"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("single private key read: got HTTP %d, want 403", resp.StatusCode)
	}
}

func TestWatchAPIDropsTLSPrivateKeyEvents(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/watch?prefix=desired/route/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = factStore.Put(context.Background(), types.KeyDesiredRouteTLSKey("web"), []byte("PRIVATE-KEY-MATERIAL"))
		_, _ = factStore.Put(context.Background(), types.KeyDesiredRouteTLSMode("web"), []byte("auto"))
	}()

	buffer := make([]byte, 4096)
	readCount, readError := resp.Body.Read(buffer)
	if readError != nil && readError != io.EOF {
		t.Fatal(readError)
	}
	firstEvent := string(buffer[:readCount])
	if strings.Contains(firstEvent, "PRIVATE-KEY-MATERIAL") || !strings.Contains(firstEvent, "/tls/mode") {
		t.Fatalf("first watch event must be the TLS mode, not the private key: %s", firstEvent)
	}
}

func TestReserveWatchConnectionEnforcesLimit(t *testing.T) {
	apiServer := NewServer(nil)
	apiServer.activeWatchConnectionCount.Store(maxConcurrentWatchConnections)

	recorder := httptest.NewRecorder()
	if apiServer.reserveWatchConnection(recorder) {
		t.Fatal("connection over the limit must be refused")
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("got HTTP %d, want 503", recorder.Code)
	}
	if apiServer.activeWatchConnectionCount.Load() != maxConcurrentWatchConnections {
		t.Fatal("a refused connection must not change the count")
	}

	apiServer.activeWatchConnectionCount.Store(maxConcurrentWatchConnections - 1)
	if !apiServer.reserveWatchConnection(httptest.NewRecorder()) {
		t.Fatal("connection at the limit must be accepted")
	}
}
