// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"testing"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestParseRouteFactsForDataPlane(t *testing.T) {
	desiredFacts := []store.Fact{
		{Key: types.KeyDesiredRoute("web-public"), Value: []byte("")},
		{Key: types.KeyDesiredRouteService("web-public"), Value: []byte("web")},
		{Key: types.KeyDesiredRoutePort("web-public"), Value: []byte("8080")},
		{Key: types.KeyDesiredRouteSticky("web-public"), Value: []byte("source-ip")},
	}

	derivedFacts := []store.Fact{
		{Key: types.KeyDerivedRouteBackends("web-public"), Value: []byte("10.0.1.4:8080,10.0.1.5:8080")},
	}

	entries := parseRouteFactsForDataPlane(desiredFacts, derivedFacts)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.routeName != "web-public" {
		t.Errorf("routeName: got %s, want web-public", entry.routeName)
	}
	if entry.serviceName != "web" {
		t.Errorf("serviceName: got %s, want web", entry.serviceName)
	}
	if entry.servicePort != 8080 {
		t.Errorf("servicePort: got %d, want 8080", entry.servicePort)
	}
	if entry.stickyMethod != "source-ip" {
		t.Errorf("stickyMethod: got %s, want source-ip", entry.stickyMethod)
	}
	if len(entry.backendAddresses) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(entry.backendAddresses))
	}
	if entry.backendAddresses[0] != "10.0.1.4:8080" {
		t.Errorf("backend[0]: got %s, want 10.0.1.4:8080", entry.backendAddresses[0])
	}
}

func TestParseRouteFactsCookieSettings(t *testing.T) {
	desiredFacts := []store.Fact{
		{Key: types.KeyDesiredRoute("api-sticky"), Value: []byte("")},
		{Key: types.KeyDesiredRouteService("api-sticky"), Value: []byte("api")},
		{Key: types.KeyDesiredRouteSticky("api-sticky"), Value: []byte("cookie")},
		{Key: types.KeyDesiredRouteCookieName("api-sticky"), Value: []byte("session")},
		{Key: types.KeyDesiredRouteCookieSecure("api-sticky"), Value: []byte("true")},
		{Key: types.KeyDesiredRouteCookieHTTPOnly("api-sticky"), Value: []byte("true")},
		{Key: types.KeyDesiredRouteCookieSameSite("api-sticky"), Value: []byte("strict")},
	}

	entries := parseRouteFactsForDataPlane(desiredFacts, nil)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.stickyMethod != "cookie" {
		t.Errorf("stickyMethod: got %s, want cookie", entry.stickyMethod)
	}
	if entry.cookieName != "session" {
		t.Errorf("cookieName: got %s, want session", entry.cookieName)
	}
	if !entry.cookieSecure {
		t.Error("expected cookieSecure=true")
	}
	if !entry.cookieHTTPOnly {
		t.Error("expected cookieHTTPOnly=true")
	}
	if entry.cookieSameSite != "strict" {
		t.Errorf("cookieSameSite: got %s, want strict", entry.cookieSameSite)
	}
}

func TestParseRouteFactsNoService(t *testing.T) {
	desiredFacts := []store.Fact{
		{Key: types.KeyDesiredRoute("orphan"), Value: []byte("")},
	}

	entries := parseRouteFactsForDataPlane(desiredFacts, nil)

	if len(entries) != 0 {
		t.Errorf("expected 0 entries for route without service, got %d", len(entries))
	}
}

func TestBuildRouteConfigFromEntry(t *testing.T) {
	entry := dataPlaneRouteEntry{
		routeName:        "web-route",
		serviceName:      "web",
		servicePort:      0,
		stickyMethod:     "cookie",
		cookieName:       "sess",
		cookieSecure:     true,
		backendAddresses: []string{"10.0.1.4:8080", "10.0.1.5:8080"},
	}
	vipConfig := network.ServiceVIPConfig{
		ServiceName: "web",
		VirtualIP:   "10.200.0.1",
		Port:        8080,
	}

	routeConfig := buildRouteConfigFromEntry(entry, vipConfig)

	if routeConfig.RouteName != "web-route" {
		t.Errorf("RouteName: got %s, want web-route", routeConfig.RouteName)
	}
	if routeConfig.VirtualIP != "10.200.0.1" {
		t.Errorf("VirtualIP: got %s, want 10.200.0.1", routeConfig.VirtualIP)
	}
	if routeConfig.Port != 8080 {
		t.Errorf("Port: got %d, want 8080 (inherited from VIP)", routeConfig.Port)
	}
	if len(routeConfig.Backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(routeConfig.Backends))
	}
	if routeConfig.CookieConfig == nil {
		t.Fatal("expected CookieConfig for cookie sticky")
	}
	if routeConfig.CookieConfig.Name != "sess" {
		t.Errorf("CookieConfig.Name: got %s, want sess", routeConfig.CookieConfig.Name)
	}
	if !routeConfig.CookieConfig.Secure {
		t.Error("expected Secure=true")
	}
}

func TestParseBackendAddressList(t *testing.T) {
	backends := parseBackendAddressList([]string{
		"10.0.1.4:8080",
		"badaddr",
		"10.0.1.5:9090",
	})

	if len(backends) != 2 {
		t.Fatalf("expected 2 valid backends, got %d", len(backends))
	}
	if backends[0].Address != "10.0.1.4" || backends[0].Port != 8080 {
		t.Errorf("backend[0]: got %s:%d", backends[0].Address, backends[0].Port)
	}
	if backends[1].Address != "10.0.1.5" || backends[1].Port != 9090 {
		t.Errorf("backend[1]: got %s:%d", backends[1].Address, backends[1].Port)
	}
}
