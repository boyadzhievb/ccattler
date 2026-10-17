// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCookieStickyProxyPinsToBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		fmt.Fprint(responseWriter, "backend-1")
	}))
	defer backend.Close()

	backendAddress, backendPort := parseTestServerAddress(t, backend)

	cookieProxy := NewCookieStickyProxy()
	defer cookieProxy.Cleanup()

	cookieProxy.ReconcileRoutes([]RouteDataPlaneConfig{
		{
			RouteName:    "test-route",
			ServiceName:  "web",
			StickyMethod: "cookie",
			Backends: []DataPlaneBackend{
				{Address: backendAddress, Port: backendPort},
			},
			CookieConfig: &RouteCookieConfig{
				Name:     "sticky",
				Secure:   false,
				HTTPOnly: true,
				SameSite: "strict",
			},
		},
	})

	proxyPort := cookieProxy.ListenPort("test-route")
	if proxyPort == 0 {
		t.Fatal("expected non-zero proxy port")
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/", proxyPort)
	response, requestError := http.Get(proxyURL) //nolint:gosec // test-only URL from local test server
	if requestError != nil {
		t.Fatalf("request failed: %v", requestError)
	}
	defer response.Body.Close()

	responseBody, _ := io.ReadAll(response.Body)
	if string(responseBody) != "backend-1" {
		t.Errorf("expected 'backend-1', got %q", string(responseBody))
	}

	var stickyCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == "sticky" {
			stickyCookie = cookie
			break
		}
	}
	if stickyCookie == nil {
		t.Fatal("expected sticky cookie in response")
	}
	expectedValue := fmt.Sprintf("%s:%d", backendAddress, backendPort)
	if stickyCookie.Value != expectedValue {
		t.Errorf("cookie value: got %s, want %s", stickyCookie.Value, expectedValue)
	}
	if !stickyCookie.HttpOnly {
		t.Error("expected HttpOnly flag on cookie")
	}
}

func TestCookieStickyProxyRoundRobinWithoutCookie(t *testing.T) {
	callCount := 0
	backend1 := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		callCount++
		fmt.Fprint(responseWriter, "b1")
	}))
	defer backend1.Close()
	backend2 := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		callCount++
		fmt.Fprint(responseWriter, "b2")
	}))
	defer backend2.Close()

	address1, port1 := parseTestServerAddress(t, backend1)
	address2, port2 := parseTestServerAddress(t, backend2)

	cookieProxy := NewCookieStickyProxy()
	defer cookieProxy.Cleanup()

	cookieProxy.ReconcileRoutes([]RouteDataPlaneConfig{
		{
			RouteName:    "rr-route",
			ServiceName:  "api",
			StickyMethod: "cookie",
			Backends: []DataPlaneBackend{
				{Address: address1, Port: port1},
				{Address: address2, Port: port2},
			},
			CookieConfig: &RouteCookieConfig{Name: "sess"},
		},
	})

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/", cookieProxy.ListenPort("rr-route"))

	hitBackends := make(map[string]bool)
	for requestIndex := 0; requestIndex < 4; requestIndex++ {
		response, requestError := http.Get(proxyURL) //nolint:gosec // test-only URL from local test server
		if requestError != nil {
			t.Fatalf("request %d failed: %v", requestIndex, requestError)
		}
		responseBody, _ := io.ReadAll(response.Body)
		response.Body.Close()
		hitBackends[string(responseBody)] = true
	}

	if len(hitBackends) < 2 {
		t.Errorf("expected requests distributed across 2 backends, hit %d", len(hitBackends))
	}
}

func TestCookieStickyProxyNoBackends(t *testing.T) {
	cookieProxy := NewCookieStickyProxy()
	defer cookieProxy.Cleanup()

	cookieProxy.ReconcileRoutes([]RouteDataPlaneConfig{
		{
			RouteName:    "empty-route",
			ServiceName:  "web",
			StickyMethod: "cookie",
			Backends:     []DataPlaneBackend{},
			CookieConfig: &RouteCookieConfig{Name: "s"},
		},
	})

	proxyPort := cookieProxy.ListenPort("empty-route")
	if proxyPort == 0 {
		t.Fatal("expected non-zero proxy port")
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/", proxyPort)
	response, requestError := http.Get(proxyURL) //nolint:gosec // test-only URL from local test server
	if requestError != nil {
		t.Fatalf("request failed: %v", requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", response.StatusCode)
	}
}

func TestCookieStickyProxyCleanupStopsAll(t *testing.T) {
	cookieProxy := NewCookieStickyProxy()

	cookieProxy.ReconcileRoutes([]RouteDataPlaneConfig{
		{
			RouteName:    "cleanup-route",
			ServiceName:  "web",
			StickyMethod: "cookie",
			Backends:     []DataPlaneBackend{{Address: "127.0.0.1", Port: 9999}},
			CookieConfig: &RouteCookieConfig{Name: "s"},
		},
	})

	if cookieProxy.ListenPort("cleanup-route") == 0 {
		t.Fatal("expected running proxy")
	}

	cookieProxy.Cleanup()

	if cookieProxy.ListenPort("cleanup-route") != 0 {
		t.Error("expected proxy stopped after cleanup")
	}
}

func TestCookieStickyProxyRemovesStaleRoute(t *testing.T) {
	cookieProxy := NewCookieStickyProxy()
	defer cookieProxy.Cleanup()

	cookieProxy.ReconcileRoutes([]RouteDataPlaneConfig{
		{
			RouteName:    "stale-route",
			ServiceName:  "web",
			StickyMethod: "cookie",
			Backends:     []DataPlaneBackend{{Address: "127.0.0.1", Port: 9999}},
			CookieConfig: &RouteCookieConfig{Name: "s"},
		},
	})

	if cookieProxy.ListenPort("stale-route") == 0 {
		t.Fatal("expected running proxy")
	}

	cookieProxy.ReconcileRoutes([]RouteDataPlaneConfig{})

	if cookieProxy.ListenPort("stale-route") != 0 {
		t.Error("expected stale route proxy stopped")
	}
}

func TestBuildStickyCookieFormat(t *testing.T) {
	cookieString := buildStickyCookie(RouteCookieConfig{
		Name:     "session",
		Secure:   true,
		HTTPOnly: true,
		SameSite: "strict",
	}, "10.0.1.4:8080")

	if !strings.HasPrefix(cookieString, "session=10.0.1.4:8080") {
		t.Errorf("unexpected cookie start: %s", cookieString)
	}
	if !strings.Contains(cookieString, "; Secure") {
		t.Error("missing Secure flag")
	}
	if !strings.Contains(cookieString, "; HttpOnly") {
		t.Error("missing HttpOnly flag")
	}
	if !strings.Contains(cookieString, "; SameSite=strict") {
		t.Error("missing SameSite")
	}
	if !strings.Contains(cookieString, "; Path=/") {
		t.Error("missing Path")
	}
}

func TestBuildStickyCookieDefaultName(t *testing.T) {
	cookieString := buildStickyCookie(RouteCookieConfig{}, "10.0.1.4:8080")

	if !strings.HasPrefix(cookieString, "__CCA_STICKY=") {
		t.Errorf("expected default name __CCA_STICKY, got: %s", cookieString)
	}
}

func parseTestServerAddress(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()
	addressWithPort := strings.TrimPrefix(server.URL, "http://")
	colonIndex := strings.LastIndex(addressWithPort, ":")
	if colonIndex < 0 {
		t.Fatalf("invalid test server address: %s", server.URL)
	}
	address := addressWithPort[:colonIndex]
	portValue := 0
	for _, digit := range addressWithPort[colonIndex+1:] {
		portValue = portValue*10 + int(digit-'0')
	}
	return address, portValue
}
