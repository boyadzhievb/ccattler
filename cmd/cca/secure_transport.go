// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// secure_transport.go refuses to send CLI credentials (bearer tokens, join
// tokens, gate keys, file contents) over plain HTTP to another host (M219,
// review finding SEC-L1). There is no override.
package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
)

// errInsecureCredentialTransport marks a request refused because it would
// carry credentials over plain HTTP to a non-loopback host.
var errInsecureCredentialTransport = errors.New("refusing to send credentials over plain HTTP to a non-loopback host; configure HTTPS with a trusted certificate")

// insecureTransportNoticeOnce prints the refusal once per process, because
// many commands report any request error as "cannot connect".
var insecureTransportNoticeOnce sync.Once

// requireSecureCredentialTransport allows https:// to any host and http://
// only to localhost, 127.0.0.0/8 or ::1. Anything else, including an
// unparsable URL, an empty host or another scheme, is refused (fail closed).
func requireSecureCredentialTransport(requestURL *url.URL) error {
	if requestURL == nil || requestURL.Hostname() == "" {
		return fmt.Errorf("%w (invalid API address)", errInsecureCredentialTransport)
	}
	switch requestURL.Scheme {
	case "https":
		return nil
	case "http":
		hostName := requestURL.Hostname()
		if hostIP := net.ParseIP(hostName); hostName == "localhost" || (hostIP != nil && hostIP.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("%w (API address %s)", errInsecureCredentialTransport, requestURL.Host)
	default:
		return fmt.Errorf("%w (unsupported scheme %q)", errInsecureCredentialTransport, requestURL.Scheme)
	}
}

// requireSecureCredentialTransportForBaseURL parses apiBaseURL and applies
// requireSecureCredentialTransport; a parse error is a refusal.
func requireSecureCredentialTransportForBaseURL(apiBaseURL string) error {
	parsedURL, parseError := url.Parse(apiBaseURL)
	if parseError != nil {
		return fmt.Errorf("%w (invalid API address %q)", errInsecureCredentialTransport, apiBaseURL)
	}
	return requireSecureCredentialTransport(parsedURL)
}

// refuseInsecureCredentialRequest checks a request about to carry the bearer
// token (redirects included) and prints the refusal once to stderr.
func refuseInsecureCredentialRequest(request *http.Request) error {
	transportError := requireSecureCredentialTransport(request.URL)
	if transportError != nil {
		insecureTransportNoticeOnce.Do(func() { fmt.Fprintf(os.Stderr, "error: %v\n", transportError) })
	}
	return transportError
}
