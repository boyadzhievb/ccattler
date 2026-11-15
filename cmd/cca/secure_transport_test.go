// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestRequireSecureCredentialTransport: https to any host and http only to
// loopback are allowed; remote names, private-network IPs, other schemes and
// malformed addresses are refused.
func TestRequireSecureCredentialTransport(t *testing.T) {
	testCases := map[string]bool{
		"https://api.example.com:9770": true,
		"https://10.0.0.5:9770":        true,
		"https://[2001:db8::1]:9770":   true,
		"http://127.0.0.1:9770":        true,
		"http://127.10.20.30:9770":     true,
		"http://localhost:9770":        true,
		"http://[::1]:9770":            true,
		"http://api.example.com:9770":  false,
		"http://10.0.0.5:9770":         false,
		"http://192.168.124.10:9770":   false,
		"http://172.16.0.1:9770":       false,
		"http://[fd00::1]:9770":        false,
		"http://localhost.evil.com":    false,
		"ftp://127.0.0.1:9770":         false,
		"127.0.0.1:9770":               false,
		"http://":                      false,
	}
	for rawURL, allowed := range testCases {
		transportError := requireSecureCredentialTransportForBaseURL(rawURL)
		if (transportError == nil) != allowed {
			t.Errorf("%s: allowed=%v, got error %v", rawURL, allowed, transportError)
		}
		if transportError != nil && !errors.Is(transportError, errInsecureCredentialTransport) {
			t.Errorf("%s: error %v does not wrap errInsecureCredentialTransport", rawURL, transportError)
		}
	}
}

// recordingRoundTripper counts requests that reach the network layer.
type recordingRoundTripper struct {
	calls int // calls is the number of requests passed on.
}

// RoundTrip records the call and answers 200.
func (recorder *recordingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	recorder.calls++
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

// TestTokenRoundTripperRefusesBeforeSending: a bearer-token request to a
// remote http:// host never reaches the transport and carries no header.
func TestTokenRoundTripperRefusesBeforeSending(t *testing.T) {
	recorder := &recordingRoundTripper{}
	tokenClient := &http.Client{Transport: &localTokenRoundTripper{baseTransport: recorder, bearerToken: "secret-token"}}
	request, _ := http.NewRequest(http.MethodGet, "http://192.168.124.10:9770/api/status", nil)
	_, sendError := tokenClient.Do(request)
	if !errors.Is(sendError, errInsecureCredentialTransport) || recorder.calls != 0 || request.Header.Get("Authorization") != "" {
		t.Errorf("remote http: error %v, %d calls, header %q; want refusal before sending", sendError, recorder.calls, request.Header.Get("Authorization"))
	}
	loopbackRequest, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:9770/api/status", nil)
	if _, loopbackError := tokenClient.Do(loopbackRequest); loopbackError != nil || recorder.calls != 1 {
		t.Errorf("loopback http: error %v, %d calls; want one request sent", loopbackError, recorder.calls)
	}
}

// TestTokenRoundTripperHTTPSWithAndWithoutCustomCA: https to a server with a
// private CA works when that CA is trusted and fails verification otherwise;
// it is never retried over http.
func TestTokenRoundTripperHTTPSWithAndWithoutCustomCA(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-token" {
			responseWriter.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer tlsServer.Close()

	trustingTransport := tlsServer.Client().Transport
	trustingClient := &http.Client{Transport: &localTokenRoundTripper{baseTransport: trustingTransport, bearerToken: "secret-token"}}
	if response, sendError := trustingClient.Get(tlsServer.URL); sendError != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("https with the custom CA trusted: %v %v", response, sendError)
	}

	untrustingTransport := &http.Transport{}
	untrustingClient := &http.Client{Transport: &localTokenRoundTripper{baseTransport: untrustingTransport, bearerToken: "secret-token"}}
	_, untrustedError := untrustingClient.Get(tlsServer.URL)
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(untrustedError, &unknownAuthority) {
		t.Errorf("https without the custom CA: error %v, want a certificate verification failure", untrustedError)
	}
}

// TestResolveStatusAPIEndpointKeepsExplicitScheme: an explicit scheme in
// CCA_API is used as given (https is never downgraded).
func TestResolveStatusAPIEndpointKeepsExplicitScheme(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for configuredAddress, wantURL := range map[string]string{
		"https://10.0.0.5:9770": "https://10.0.0.5:9770",
		"http://127.0.0.1:9770": "http://127.0.0.1:9770",
	} {
		t.Setenv("CCA_API", configuredAddress)
		scheme, hostPort := resolveStatusAPIEndpoint()
		if resolved := (&url.URL{Scheme: scheme, Host: hostPort}).String(); resolved != wantURL {
			t.Errorf("CCA_API=%s resolved to %s, want %s", configuredAddress, resolved, wantURL)
		}
	}
}
