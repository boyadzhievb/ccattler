// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Gate fact attribute paths relative to desired/route/{name} or derived/route/{name}.
const (
	gateAttributeHost        = "host"          // gateAttributeHost is the hostname match.
	gateAttributePath        = "path"          // gateAttributePath is the URL path prefix match.
	gateAttributeService     = "service"       // gateAttributeService is the target service name.
	gateAttributePort        = "port"          // gateAttributePort is the target service port.
	gateAttributeSticky      = "sticky"        // gateAttributeSticky is the sticky session method.
	gateAttributeTLSMode     = "tls/mode"      // gateAttributeTLSMode is the TLS termination mode.
	gateAttributeTLSCert     = "tls/cert"      // gateAttributeTLSCert is the certificate PEM.
	gateAttributeTLSNotAfter = "tls/not-after" // gateAttributeTLSNotAfter is the CA-issued cert expiry.
	gateAttributeBackends    = "backends"      // gateAttributeBackends is the resolved backend list.
)

// GateStatus represents one gate (external HTTP route) in the cluster status.
type GateStatus struct {
	Name     string   `json:"name"`               // Name is the gate name from the DSL.
	Host     string   `json:"host,omitempty"`     // Host is the hostname match.
	Path     string   `json:"path,omitempty"`     // Path is the URL path prefix match.
	Service  string   `json:"service"`            // Service is the target service.
	Port     int      `json:"port,omitempty"`     // Port is the target service port.
	Sticky   string   `json:"sticky,omitempty"`   // Sticky is "cookie", "source-ip", or empty.
	TLS      *GateTLS `json:"tls,omitempty"`      // TLS is nil when the gate serves plain HTTP.
	Backends []string `json:"backends,omitempty"` // Backends are resolved host:port addresses.
}

// GateTLS describes a gate's TLS termination state without exposing key material.
type GateTLS struct {
	Mode            types.RouteTLSMode `json:"mode"`                       // Mode is auto or manual.
	CertificateSet  bool               `json:"certificate_set"`            // CertificateSet is true once cert material exists.
	NotAfter        string             `json:"not_after,omitempty"`        // NotAfter is the cert expiry in RFC 3339.
	CertificateHost string             `json:"certificate_host,omitempty"` // CertificateHost is the cert's first DNS SAN.
}

// gateFactAttributes maps gate name → attribute path → fact value.
type gateFactAttributes map[string]map[string]string

// collectGateStatusFromStore scans desired and derived route facts and
// assembles a sorted list of gates for display via `cca get gates`.
func collectGateStatusFromStore(ctx context.Context, factStore store.StateStore) []GateStatus {
	desiredAttributes := scanGateFactAttributes(ctx, factStore, types.ScanDesiredRoutes)
	derivedAttributes := scanGateFactAttributes(ctx, factStore, types.ScanDerivedRoutes)

	gateNames := make([]string, 0, len(desiredAttributes))
	for gateName := range desiredAttributes {
		gateNames = append(gateNames, gateName)
	}
	sort.Strings(gateNames)

	gateStatusList := make([]GateStatus, 0, len(gateNames))
	for _, gateName := range gateNames {
		gateStatusList = append(gateStatusList,
			buildGateStatus(gateName, desiredAttributes[gateName], derivedAttributes[gateName]))
	}
	return gateStatusList
}

// filterGatesByServiceNames returns only gates whose target service is in
// the visible set. Used for tenant visibility filtering.
func filterGatesByServiceNames(gateStatusList []GateStatus, visibleServiceNames []string) []GateStatus {
	visibleServiceSet := make(map[string]bool, len(visibleServiceNames))
	for _, serviceName := range visibleServiceNames {
		visibleServiceSet[serviceName] = true
	}
	var visibleGates []GateStatus
	for _, gateStatus := range gateStatusList {
		if visibleServiceSet[gateStatus.Service] {
			visibleGates = append(visibleGates, gateStatus)
		}
	}
	return visibleGates
}

// buildGateDescribe aggregates all facts related to a single gate. Returns an
// error when no desired route facts exist for the name.
func buildGateDescribe(ctx context.Context, factStore store.StateStore, gateName string) (*GateStatus, error) {
	desiredAttributes := scanGateFactAttributes(ctx, factStore, types.ScanDesiredRoutes+gateName)
	if _, exists := desiredAttributes[gateName]; !exists {
		return nil, fmt.Errorf("gate %q not found", gateName)
	}
	derivedAttributes := scanGateFactAttributes(ctx, factStore, types.ScanDerivedRoutes+gateName)
	gateStatus := buildGateStatus(gateName, desiredAttributes[gateName], derivedAttributes[gateName])
	return &gateStatus, nil
}

// scanGateFactAttributes scans route facts under scanPrefix and groups them by
// gate name. The root marker key (desired/route/{name}) maps to attribute "".
func scanGateFactAttributes(ctx context.Context, factStore store.StateStore, scanPrefix string) gateFactAttributes {
	groupedAttributes := make(gateFactAttributes)
	routeFacts, scanError := factStore.Scan(ctx, scanPrefix)
	if scanError != nil {
		return groupedAttributes
	}
	routeRootPrefix := types.ScanDesiredRoutes
	if strings.HasPrefix(scanPrefix, types.ScanDerivedRoutes) {
		routeRootPrefix = types.ScanDerivedRoutes
	}
	for _, routeFact := range routeFacts {
		relativePath := strings.TrimPrefix(routeFact.Key, routeRootPrefix)
		gateName, attributePath, _ := strings.Cut(relativePath, "/")
		if gateName == "" {
			continue
		}
		if groupedAttributes[gateName] == nil {
			groupedAttributes[gateName] = make(map[string]string)
		}
		groupedAttributes[gateName][attributePath] = string(routeFact.Value)
	}
	return groupedAttributes
}

// buildGateStatus converts grouped desired and derived attributes for one gate
// into a GateStatus. Desired TLS cert takes precedence over the CA-issued one,
// matching the agent data plane.
func buildGateStatus(gateName string, desiredAttributes, derivedAttributes map[string]string) GateStatus {
	gateStatus := GateStatus{
		Name:    gateName,
		Host:    desiredAttributes[gateAttributeHost],
		Path:    desiredAttributes[gateAttributePath],
		Service: desiredAttributes[gateAttributeService],
		Sticky:  desiredAttributes[gateAttributeSticky],
	}
	if portNumber, parseError := strconv.Atoi(desiredAttributes[gateAttributePort]); parseError == nil {
		gateStatus.Port = portNumber
	}
	if backendList := derivedAttributes[gateAttributeBackends]; backendList != "" {
		gateStatus.Backends = strings.Split(backendList, ",")
	}
	if tlsMode := desiredAttributes[gateAttributeTLSMode]; tlsMode != "" {
		gateStatus.TLS = buildGateTLS(types.RouteTLSMode(tlsMode), desiredAttributes, derivedAttributes)
	}
	return gateStatus
}

// buildGateTLS resolves the effective certificate for a gate and extracts its
// expiry and hostname. Key material is never included.
func buildGateTLS(tlsMode types.RouteTLSMode, desiredAttributes, derivedAttributes map[string]string) *GateTLS {
	gateTLS := &GateTLS{Mode: tlsMode}
	certificatePEM := desiredAttributes[gateAttributeTLSCert]
	if certificatePEM == "" {
		certificatePEM = derivedAttributes[gateAttributeTLSCert]
	}
	if certificatePEM == "" {
		return gateTLS
	}
	gateTLS.CertificateSet = true
	gateTLS.NotAfter = derivedAttributes[gateAttributeTLSNotAfter]

	pemBlock, _ := pem.Decode([]byte(certificatePEM))
	if pemBlock == nil {
		return gateTLS
	}
	parsedCertificate, parseError := x509.ParseCertificate(pemBlock.Bytes)
	if parseError != nil {
		return gateTLS
	}
	gateTLS.NotAfter = parsedCertificate.NotAfter.UTC().Format(time.RFC3339)
	if len(parsedCertificate.DNSNames) > 0 {
		gateTLS.CertificateHost = parsedCertificate.DNSNames[0]
	}
	return gateTLS
}
