// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
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
// certificates via the cluster CA for gates with tls mode "auto". The
// certificate and expiry are plain derived facts; the private key is sealed
// at secrets/route/{gate}/tls/key (Flow 28) and never written as a fact.
type RouteTLSController struct {
	certificateAuthority *security.CertificateAuthority // certificateAuthority issues gate certificates.
	envelopeCodec        security.SecretEnvelopeCodec   // envelopeCodec seals gate private keys bound to their store key (shared with the secret store).
}

// NewRouteTLSController creates a controller that auto-issues TLS certificates
// for gates using the given cluster certificate authority and seals their
// private keys with the server's keyring envelope codec (Flow 29).
func NewRouteTLSController(certificateAuthority *security.CertificateAuthority, envelopeCodec security.SecretEnvelopeCodec) *RouteTLSController {
	return &RouteTLSController{certificateAuthority: certificateAuthority, envelopeCodec: envelopeCodec}
}

// Name returns "route-tls", identifying this controller in logs.
func (routeTLSController *RouteTLSController) Name() string { return "route-tls" }

// Watch returns the fact prefixes the controller monitors: desired route
// definitions (for TLS mode and host), derived route facts (for existing cert
// expiry and staleness detection) and the sealed gate keys, so a renewal's
// put of an existing key carries its revision guard instead of create-only.
func (routeTLSController *RouteTLSController) Watch() []string {
	return []string{
		types.ScanDesiredRoutes,
		types.ScanDerivedRoutes,
		security.SecretStorePrefix + types.SecretNameRouteTLSKeyPrefix,
	}
}

// Reconcile scans desired routes for tls/mode=auto, checks derived cert
// expiry, issues new certs when missing or near expiry, cleans up stale
// derived TLS facts for routes that no longer request auto TLS, and deletes
// sealed system keys no gate needs any more (Flow 28 step 4).
func (routeTLSController *RouteTLSController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	autoTLSRoutes := parseAutoTLSRoutes(facts)
	existingDerivedTLS := parseExistingDerivedTLSFacts(facts)
	sealedGates := collectSealedGateKeyNames(facts)

	var changes []Change
	desiredDerivedTLSKeys := make(map[string]bool)

	sortedRouteNames := make([]string, 0, len(autoTLSRoutes))
	for routeName := range autoTLSRoutes {
		sortedRouteNames = append(sortedRouteNames, routeName)
	}
	sort.Strings(sortedRouteNames)

	for _, routeName := range sortedRouteNames {
		notAfterKey := types.KeyDerivedRouteTLSNotAfter(routeName)
		desiredDerivedTLSKeys[types.KeyDerivedRouteTLSCert(routeName)] = true
		desiredDerivedTLSKeys[notAfterKey] = true
		// A plaintext key fact left from before M176 stays while the gate is
		// auto: agents without gate-key delivery still read it, and only the
		// gate-key-migration controller deletes it (Flow 28 step 7).
		desiredDerivedTLSKeys[types.KeyDerivedRouteTLSKey(routeName)] = true

		// A valid certificate whose sealed key is gone (and no pre-M176 plaintext
		// key waits for migration) can never be delivered: issue a new pair.
		sealedKeyMissing := !sealedGates[routeName] && existingDerivedTLS[types.KeyDerivedRouteTLSKey(routeName)] == ""
		if !needsCertificateIssuance(existingDerivedTLS[notAfterKey]) && !sealedKeyMissing {
			continue
		}

		issuanceChanges, issuanceError := routeTLSController.issueSealedGateCertificate(ctx, routeName, autoTLSRoutes[routeName])
		if issuanceError != nil {
			logging.Default().Error("route-tls: failed to issue certificate",
				"route", routeName, "host", autoTLSRoutes[routeName], "error", issuanceError.Error())
			continue
		}
		changes = append(changes, issuanceChanges...)
	}

	changes = append(changes, cleanupStaleDerivedTLSFacts(existingDerivedTLS, desiredDerivedTLSKeys)...)
	changes = append(changes, cleanupUnneededSealedGateKeys(facts, parseSystemKeyGateNames(facts))...)

	return changes, nil
}

// routeTLSSealedKeySuffix ends the store key of a sealed system gate key,
// secrets/route/{gate}/tls/key.
const routeTLSSealedKeySuffix = "/tls/key"

// parseSystemKeyGateNames returns the gates that need a system-owned sealed
// key: tls auto or manual, unless the gate uses a key secret
// (types.GateUsesKeySecret, the rule delivery uses too).
func parseSystemKeyGateNames(facts []store.Fact) map[string]bool {
	routeTLSModes := make(map[string]types.RouteTLSMode)
	routeKeySecrets := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredRoutes) {
		routeName, fieldPath, hasField := strings.Cut(strings.TrimPrefix(fact.Key, types.ScanDesiredRoutes), "/")
		switch {
		case !hasField:
		case fieldPath == "tls/mode":
			routeTLSModes[routeName] = types.RouteTLSMode(fact.Value)
		case fieldPath == "tls/key_secret":
			routeKeySecrets[routeName] = string(fact.Value)
		}
	}
	systemKeyGates := make(map[string]bool, len(routeTLSModes))
	for routeName, tlsMode := range routeTLSModes {
		isTLSMode := tlsMode == types.RouteTLSModeAuto || tlsMode == types.RouteTLSModeManual
		if isTLSMode && !types.GateUsesKeySecret(tlsMode, routeKeySecrets[routeName]) {
			systemKeyGates[routeName] = true
		}
	}
	return systemKeyGates
}

// cleanupUnneededSealedGateKeys deletes secrets/route/{gate}/tls/key when the
// gate is gone, has no tls, or uses a key secret. Sorted for determinism.
func cleanupUnneededSealedGateKeys(facts []store.Fact, systemKeyGates map[string]bool) []Change {
	sealedKeyPrefix := security.SecretStorePrefix + types.SecretNameRouteTLSKeyPrefix
	var deletedKeys []string
	for _, fact := range store.FactsWithPrefix(facts, sealedKeyPrefix) {
		gateName, isSealedKey := strings.CutSuffix(strings.TrimPrefix(fact.Key, sealedKeyPrefix), routeTLSSealedKeySuffix)
		if isSealedKey && !strings.Contains(gateName, "/") && !systemKeyGates[gateName] {
			deletedKeys = append(deletedKeys, fact.Key)
		}
	}
	sort.Strings(deletedKeys)
	changes := make([]Change, 0, len(deletedKeys))
	for _, deletedKey := range deletedKeys {
		logging.Default().Info("route-tls: removing sealed key of a gate that no longer needs it", "key", deletedKey)
		changes = append(changes, Change{Type: store.OpDelete, Key: deletedKey})
	}
	return changes
}

// issueSealedGateCertificate issues a certificate for the gate's host and
// returns one change group: the certificate and expiry facts plus the sealed
// private key at secrets/route/{gate}/tls/key, committed in one transaction.
func (routeTLSController *RouteTLSController) issueSealedGateCertificate(ctx context.Context, routeName string, hostname string) ([]Change, error) {
	issuedCertificate, issueError := routeTLSController.certificateAuthority.IssueCertificate(security.IssueCertificateRequest{
		CommonName: hostname,
		DNSNames:   []string{hostname},
		TTL:        gateCertificateTTL,
	})
	if issueError != nil {
		return nil, issueError
	}

	sealedKeyStoreKey := security.SecretStorePrefix + types.SecretNameRouteTLSKey(routeName)
	sealedPrivateKey, sealError := security.SealSecretValue(ctx, routeTLSController.envelopeCodec, sealedKeyStoreKey, issuedCertificate.PrivateKeyPEM)
	if sealError != nil {
		return nil, fmt.Errorf("seal private key: %w", sealError)
	}

	logging.Default().Info("route-tls: issued certificate",
		"route", routeName, "host", hostname,
		"notAfter", issuedCertificate.NotAfter.Format(time.RFC3339))

	return groupedChanges("route-tls-issue/"+routeName,
		Change{Type: store.OpPut, Key: types.KeyDerivedRouteTLSCert(routeName), Value: issuedCertificate.CertificatePEM},
		Change{Type: store.OpPut, Key: sealedKeyStoreKey, Value: sealedPrivateKey},
		Change{Type: store.OpPut, Key: types.KeyDerivedRouteTLSNotAfter(routeName), Value: []byte(issuedCertificate.NotAfter.Format(time.RFC3339))},
	), nil
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
