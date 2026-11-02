// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// gateCertificateTTL is the validity period for auto-issued gate TLS certs.
	gateCertificateTTL = 90 * 24 * time.Hour

	// gateCertificateRenewalWindow is how far before expiry the controller
	// re-issues the certificate. A 30-day window matches Let's Encrypt convention.
	gateCertificateRenewalWindow = 30 * 24 * time.Hour
)

// RouteTLSController watches desired route TLS facts and issues or renews
// certificates via the cluster CA for gates with tls mode "auto". For manual
// mode, cert material is stored directly in desired facts by the compiler.
type RouteTLSController struct {
	certificateAuthority *security.CertificateAuthority
}

// NewRouteTLSController creates a controller that auto-issues TLS certificates
// for gates using the given cluster certificate authority.
func NewRouteTLSController(certificateAuthority *security.CertificateAuthority) *RouteTLSController {
	return &RouteTLSController{certificateAuthority: certificateAuthority}
}

// Name returns "route-tls", identifying this controller in logs.
func (routeTLSController *RouteTLSController) Name() string { return "route-tls" }

// Watch returns the fact prefixes the controller monitors: desired route
// definitions (for TLS mode and host) and derived route facts (for existing
// cert expiry and staleness detection).
func (routeTLSController *RouteTLSController) Watch() []string {
	return []string{
		types.ScanDesiredRoutes,
		types.ScanDerivedRoutes,
	}
}

// Reconcile scans desired routes for tls/mode=auto, checks derived cert
// expiry, issues new certs when missing or near expiry, and cleans up stale
// derived TLS facts for routes that no longer request auto TLS.
func (routeTLSController *RouteTLSController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	autoTLSRoutes := parseAutoTLSRoutes(facts)
	existingDerivedTLS := parseExistingDerivedTLSFacts(facts)

	var changes []Change
	desiredDerivedTLSKeys := make(map[string]bool)

	sortedRouteNames := make([]string, 0, len(autoTLSRoutes))
	for routeName := range autoTLSRoutes {
		sortedRouteNames = append(sortedRouteNames, routeName)
	}
	sort.Strings(sortedRouteNames)

	for _, routeName := range sortedRouteNames {
		hostname := autoTLSRoutes[routeName]

		certKey := types.KeyDerivedRouteTLSCert(routeName)
		keyKey := types.KeyDerivedRouteTLSKey(routeName)
		notAfterKey := types.KeyDerivedRouteTLSNotAfter(routeName)
		desiredDerivedTLSKeys[certKey] = true
		desiredDerivedTLSKeys[keyKey] = true
		desiredDerivedTLSKeys[notAfterKey] = true

		if !needsCertificateIssuance(existingDerivedTLS[notAfterKey]) {
			continue
		}

		issuedCertificate, issueError := routeTLSController.certificateAuthority.IssueCertificate(security.IssueCertificateRequest{
			CommonName: hostname,
			DNSNames:   []string{hostname},
			TTL:        gateCertificateTTL,
		})
		if issueError != nil {
			logging.Default().Error("route-tls: failed to issue certificate",
				"route", routeName, "host", hostname, "error", issueError.Error())
			continue
		}

		logging.Default().Info("route-tls: issued certificate",
			"route", routeName, "host", hostname,
			"notAfter", issuedCertificate.NotAfter.Format(time.RFC3339))

		changes = append(changes,
			Change{Type: store.OpPut, Key: certKey, Value: issuedCertificate.CertificatePEM},
			Change{Type: store.OpPut, Key: keyKey, Value: issuedCertificate.PrivateKeyPEM},
			Change{Type: store.OpPut, Key: notAfterKey, Value: []byte(issuedCertificate.NotAfter.Format(time.RFC3339))},
		)
	}

	changes = append(changes, cleanupStaleDerivedTLSFacts(existingDerivedTLS, desiredDerivedTLSKeys)...)

	return changes, nil
}

// autoTLSRoute maps route name to its hostname for routes with tls mode "auto".
type autoTLSRoute = string

// parseAutoTLSRoutes scans desired route facts and returns a map from route
// name to hostname for routes that have tls/mode=auto.
func parseAutoTLSRoutes(facts []store.Fact) map[string]autoTLSRoute {
	routeTLSModes := make(map[string]types.RouteTLSMode)
	routeHosts := make(map[string]string)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredRoutes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredRoutes)
		pathParts := strings.SplitN(relativePath, "/", 2)
		routeName := pathParts[0]

		if len(pathParts) < 2 {
			continue
		}

		fieldPath := pathParts[1]
		switch fieldPath {
		case "tls/mode":
			routeTLSModes[routeName] = types.RouteTLSMode(fact.Value)
		case "host":
			routeHosts[routeName] = string(fact.Value)
		}
	}

	autoRoutes := make(map[string]autoTLSRoute)
	for routeName, tlsMode := range routeTLSModes {
		if tlsMode == types.RouteTLSModeAuto {
			if hostname, hasHost := routeHosts[routeName]; hasHost && hostname != "" {
				autoRoutes[routeName] = hostname
			}
		}
	}
	return autoRoutes
}

// parseExistingDerivedTLSFacts scans derived/route/ facts and returns a map of
// TLS-related keys (cert, key, not-after) to their current values.
func parseExistingDerivedTLSFacts(facts []store.Fact) map[string]string {
	existingTLS := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedRoutes) {
		if strings.Contains(fact.Key, "/tls/") {
			existingTLS[fact.Key] = string(fact.Value)
		}
	}
	return existingTLS
}

// needsCertificateIssuance returns true if the certificate is missing or within
// the renewal window.
func needsCertificateIssuance(notAfterValue string) bool {
	if notAfterValue == "" {
		return true
	}
	notAfterTime, parseError := time.Parse(time.RFC3339, notAfterValue)
	if parseError != nil {
		return true
	}
	return time.Until(notAfterTime) < gateCertificateRenewalWindow
}

// cleanupStaleDerivedTLSFacts deletes derived TLS facts that are no longer
// backed by a desired route with tls mode "auto".
func cleanupStaleDerivedTLSFacts(existingDerivedTLS map[string]string, desiredKeys map[string]bool) []Change {
	var changes []Change

	sortedKeys := make([]string, 0, len(existingDerivedTLS))
	for existingKey := range existingDerivedTLS {
		sortedKeys = append(sortedKeys, existingKey)
	}
	sort.Strings(sortedKeys)

	for _, existingKey := range sortedKeys {
		if !desiredKeys[existingKey] {
			logging.Default().Info("route-tls: removing stale derived TLS fact", "key", existingKey)
			changes = append(changes, Change{Type: store.OpDelete, Key: existingKey})
		}
	}
	return changes
}
