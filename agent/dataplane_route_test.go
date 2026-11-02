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

func TestParseRouteFactsTLSFromDesired(t *testing.T) {
	desiredFacts := []store.Fact{
		{Key: types.KeyDesiredRoute("internal-api"), Value: []byte("")},
		{Key: types.KeyDesiredRouteService("internal-api"), Value: []byte("api")},
		{Key: types.KeyDesiredRouteHost("internal-api"), Value: []byte("api.internal")},
		{Key: types.KeyDesiredRouteTLSMode("internal-api"), Value: []byte("manual")},
		{Key: types.KeyDesiredRouteTLSCert("internal-api"), Value: []byte("cert-pem-data")},
		{Key: types.KeyDesiredRouteTLSKey("internal-api"), Value: []byte("key-pem-data")},
	}

	entries := parseRouteFactsForDataPlane(desiredFacts, nil)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].tlsCertPEM != "cert-pem-data" {
		t.Errorf("tlsCertPEM: got %q, want %q", entries[0].tlsCertPEM, "cert-pem-data")
	}
	if entries[0].tlsKeyPEM != "key-pem-data" {
		t.Errorf("tlsKeyPEM: got %q, want %q", entries[0].tlsKeyPEM, "key-pem-data")
	}
}

func TestParseRouteFactsTLSFromDerived(t *testing.T) {
	desiredFacts := []store.Fact{
		{Key: types.KeyDesiredRoute("web-public"), Value: []byte("")},
		{Key: types.KeyDesiredRouteService("web-public"), Value: []byte("web")},
		{Key: types.KeyDesiredRouteHost("web-public"), Value: []byte("app.example.com")},
		{Key: types.KeyDesiredRouteTLSMode("web-public"), Value: []byte("auto")},
	}

	derivedFacts := []store.Fact{
		{Key: types.KeyDerivedRouteBackends("web-public"), Value: []byte("10.0.1.4:8080")},
		{Key: types.KeyDerivedRouteTLSCert("web-public"), Value: []byte("derived-cert")},
		{Key: types.KeyDerivedRouteTLSKey("web-public"), Value: []byte("derived-key")},
	}

	entries := parseRouteFactsForDataPlane(desiredFacts, derivedFacts)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].tlsCertPEM != "derived-cert" {
		t.Errorf("tlsCertPEM: got %q, want %q", entries[0].tlsCertPEM, "derived-cert")
	}
	if entries[0].tlsKeyPEM != "derived-key" {
		t.Errorf("tlsKeyPEM: got %q, want %q", entries[0].tlsKeyPEM, "derived-key")
	}
}

func TestParseRouteFactsTLSDesiredTakesPrecedence(t *testing.T) {
	desiredFacts := []store.Fact{
		{Key: types.KeyDesiredRoute("mixed"), Value: []byte("")},
		{Key: types.KeyDesiredRouteService("mixed"), Value: []byte("web")},
		{Key: types.KeyDesiredRouteTLSCert("mixed"), Value: []byte("desired-cert")},
		{Key: types.KeyDesiredRouteTLSKey("mixed"), Value: []byte("desired-key")},
	}

	derivedFacts := []store.Fact{
		{Key: types.KeyDerivedRouteTLSCert("mixed"), Value: []byte("derived-cert")},
		{Key: types.KeyDerivedRouteTLSKey("mixed"), Value: []byte("derived-key")},
	}

	entries := parseRouteFactsForDataPlane(desiredFacts, derivedFacts)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].tlsCertPEM != "desired-cert" {
		t.Errorf("desired cert should take precedence: got %q, want %q", entries[0].tlsCertPEM, "desired-cert")
	}
}

func TestBuildRouteConfigFromEntryWithTLS(t *testing.T) {
	entry := dataPlaneRouteEntry{
		routeName:        "tls-gate",
		serviceName:      "web",
		backendAddresses: []string{"10.0.1.4:8080"},
		tlsCertPEM:       "cert-data",
		tlsKeyPEM:        "key-data",
	}
	vipConfig := network.ServiceVIPConfig{
		ServiceName: "web",
		VirtualIP:   "10.200.0.1",
		Port:        443,
	}

	routeConfig := buildRouteConfigFromEntry(entry, vipConfig)

	if routeConfig.TLSCertPEM != "cert-data" {
		t.Errorf("TLSCertPEM: got %q, want %q", routeConfig.TLSCertPEM, "cert-data")
	}
	if routeConfig.TLSKeyPEM != "key-data" {
		t.Errorf("TLSKeyPEM: got %q, want %q", routeConfig.TLSKeyPEM, "key-data")
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
