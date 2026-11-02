// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package network

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Gate route attributes read by the gate proxy, relative to
// desired/route/{name}/ or derived/route/{name}/.
const (
	gateFactHost           = "host"            // gateFactHost is the gate's Host match.
	gateFactService        = "service"         // gateFactService is the target service.
	gateFactSticky         = "sticky"          // gateFactSticky is the sticky method.
	gateFactCookieName     = "cookie/name"     // gateFactCookieName is the sticky cookie name.
	gateFactCookieSecure   = "cookie/secure"   // gateFactCookieSecure is the Secure flag.
	gateFactCookieHTTPOnly = "cookie/httponly" // gateFactCookieHTTPOnly is the HttpOnly flag.
	gateFactCookieSameSite = "cookie/samesite" // gateFactCookieSameSite is the SameSite value.
	gateFactTLSMode        = "tls/mode"        // gateFactTLSMode is the TLS mode (auto or manual).
	gateFactTLSCert        = "tls/cert"        // gateFactTLSCert is the certificate PEM.
	gateFactTLSKey         = "tls/key"         // gateFactTLSKey is the private key PEM.
)

// stickyMethodCookie is the gate sticky method that pins clients by cookie.
const stickyMethodCookie = "cookie"

// errNoGateCertificate is returned during the TLS handshake when the client's
// SNI does not name a gate with an available certificate.
var errNoGateCertificate = errors.New("no gate certificate for server name")

// gateHostEntry is the gate proxy's view of one gate, keyed by its host.
type gateHostEntry struct {
	hostname     string             // hostname is the gate's declared host.
	routeName    string             // routeName is the gate name.
	serviceName  string             // serviceName is the gate's target service.
	cookieConfig *RouteCookieConfig // cookieConfig is non-nil for sticky cookie gates.
	tlsEnabled   bool               // tlsEnabled is true when the gate declares tls.
	certificate  *tls.Certificate   // certificate is the parsed gate cert; nil until available.
}

// buildGateHostEntries converts desired and derived route facts into gate
// entries keyed by host. A desired (manual) certificate takes precedence over
// a derived (auto) one. Gates without a host or service are skipped.
func buildGateHostEntries(desiredFacts, derivedFacts []store.Fact) map[string]*gateHostEntry {
	desiredAttributes := groupRouteFactAttributes(desiredFacts, types.ScanDesiredRoutes)
	derivedAttributes := groupRouteFactAttributes(derivedFacts, types.ScanDerivedRoutes)

	routeNames := make([]string, 0, len(desiredAttributes))
	for routeName := range desiredAttributes {
		routeNames = append(routeNames, routeName)
	}
	sort.Strings(routeNames)

	gateHosts := make(map[string]*gateHostEntry, len(routeNames))
	for _, routeName := range routeNames {
		attributes := desiredAttributes[routeName]
		hostname, serviceName := attributes[gateFactHost], attributes[gateFactService]
		if hostname == "" || serviceName == "" {
			continue
		}
		gateHosts[hostname] = &gateHostEntry{
			hostname:     hostname,
			routeName:    routeName,
			serviceName:  serviceName,
			cookieConfig: buildGateCookieConfig(attributes),
			tlsEnabled:   attributes[gateFactTLSMode] != "",
			certificate:  parseGateCertificate(routeName, attributes, derivedAttributes[routeName]),
		}
	}
	return gateHosts
}

// groupRouteFactAttributes groups route facts under scanPrefix by route name,
// mapping each route to its attribute path → value.
func groupRouteFactAttributes(routeFacts []store.Fact, scanPrefix string) map[string]map[string]string {
	groupedAttributes := make(map[string]map[string]string)
	for _, routeFact := range routeFacts {
		routeName, attributePath, hasAttribute := strings.Cut(strings.TrimPrefix(routeFact.Key, scanPrefix), "/")
		if routeName == "" {
			continue
		}
		if groupedAttributes[routeName] == nil {
			groupedAttributes[routeName] = make(map[string]string)
		}
		if hasAttribute {
			groupedAttributes[routeName][attributePath] = string(routeFact.Value)
		}
	}
	return groupedAttributes
}

// buildGateCookieConfig returns the sticky cookie settings for a gate, or nil
// when the gate does not use cookie stickiness.
func buildGateCookieConfig(attributes map[string]string) *RouteCookieConfig {
	if attributes[gateFactSticky] != stickyMethodCookie {
		return nil
	}
	return &RouteCookieConfig{
		Name:     attributes[gateFactCookieName],
		Secure:   attributes[gateFactCookieSecure] == "true",
		HTTPOnly: attributes[gateFactCookieHTTPOnly] == "true",
		SameSite: attributes[gateFactCookieSameSite],
	}
}

// parseGateCertificate returns the gate's effective TLS certificate: the
// desired (manual) pair if present, otherwise the derived (auto) pair. Invalid
// PEM is logged by gate name only, never by content, and yields nil.
func parseGateCertificate(routeName string, desiredAttributes, derivedAttributes map[string]string) *tls.Certificate {
	certificatePEM, keyPEM := desiredAttributes[gateFactTLSCert], desiredAttributes[gateFactTLSKey]
	if certificatePEM == "" || keyPEM == "" {
		certificatePEM, keyPEM = derivedAttributes[gateFactTLSCert], derivedAttributes[gateFactTLSKey]
	}
	if certificatePEM == "" || keyPEM == "" {
		return nil
	}
	certificate, parseError := tls.X509KeyPair([]byte(certificatePEM), []byte(keyPEM))
	if parseError != nil {
		logging.Default().Warn("gate proxy: invalid TLS certificate", "gate", routeName)
		return nil
	}
	return &certificate
}

// getGateCertificate is the tls.Config GetCertificate callback: it returns the
// certificate of the gate named by the client's SNI. There is no default
// certificate, so unknown or missing SNI fails the handshake.
func (userSpaceProxy *UserSpaceProxy) getGateCertificate(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	gateEntry := userSpaceProxy.lookupGateHost(clientHello.Context(), clientHello.ServerName)
	if gateEntry == nil || gateEntry.certificate == nil {
		return nil, errNoGateCertificate
	}
	return gateEntry.certificate, nil
}

// StartTLS begins serving HTTPS for gates on tlsListenAddress, selecting each
// gate's certificate by SNI and routing with the same handler as Start. It
// blocks until the context is cancelled.
func (userSpaceProxy *UserSpaceProxy) StartTLS(ctx context.Context, tlsListenAddress string) error {
	httpsServer := &http.Server{
		Addr:              tlsListenAddress,
		Handler:           userSpaceProxy,
		ReadHeaderTimeout: types.DefaultReadHeaderTimeout,
		TLSConfig: &tls.Config{
			GetCertificate: userSpaceProxy.getGateCertificate,
			MinVersion:     tls.VersionTLS12,
		},
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	go func() { //nolint:gosec // parent context is already cancelled; fresh context needed for shutdown timeout
		<-ctx.Done()
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), defaultProxyShutdownTimeout)
		defer cancelShutdown()
		_ = httpsServer.Shutdown(shutdownContext)
	}()

	if listenError := httpsServer.ListenAndServeTLS("", ""); listenError != nil && listenError != http.ErrServerClosed {
		return fmt.Errorf("gate HTTPS listen error: %w", listenError)
	}
	return nil
}

// handleGateRequestPolicy applies gate TLS rules before routing. Plain-HTTP
// requests for a gate serving TLS get a 308 redirect to https; HTTPS requests
// whose Host is not the SNI's gate get 421. Returns true when it responded.
func (userSpaceProxy *UserSpaceProxy) handleGateRequestPolicy(
	responseWriter http.ResponseWriter, incomingRequest *http.Request, gateEntry *gateHostEntry,
) bool {
	if incomingRequest.TLS == nil {
		if gateEntry != nil && gateEntry.certificate != nil {
			// The target host is the gate's declared host, never the request's
			// Host header, so the redirect cannot point anywhere but the gate.
			redirectURL := url.URL{Scheme: "https", Host: gateEntry.hostname, Path: incomingRequest.URL.Path, RawQuery: incomingRequest.URL.RawQuery}
			http.Redirect(responseWriter, incomingRequest, redirectURL.String(), http.StatusPermanentRedirect) //nolint:gosec // host is the declared gate host, not request input
			return true
		}
		return false
	}
	sniGate := userSpaceProxy.lookupGateHost(incomingRequest.Context(), incomingRequest.TLS.ServerName)
	if gateEntry == nil || sniGate == nil || gateEntry.routeName != sniGate.routeName {
		http.Error(responseWriter, "host does not match TLS server name", http.StatusMisdirectedRequest)
		return true
	}
	return false
}

// hostWithoutPort strips an optional :port suffix from a Host header value.
func hostWithoutPort(hostHeader string) string {
	if hostname, _, splitError := net.SplitHostPort(hostHeader); splitError == nil {
		return hostname
	}
	return hostHeader
}
