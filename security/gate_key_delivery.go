// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// GateKeyDeliveryAuditAction is the audit action recorded for every gate-key
// delivery decision (allowed or refused).
const GateKeyDeliveryAuditAction = "gate-key.deliver"

// GateKeyStoreAuditAction is the audit action recorded when an inline gate key
// is sealed through PUT /api/gate-key (or rejected).
const GateKeyStoreAuditAction = "gate-key.store"

// GateKeyRejection is returned when an inline gate key is not accepted: the
// gate has no manual TLS, uses a key secret, or the key does not match its
// certificate. It never contains key material.
type GateKeyRejection struct {
	Detail string // Detail explains why the key was rejected.
}

// Error formats the rejection.
func (rejection *GateKeyRejection) Error() string {
	return "gate key rejected: " + rejection.Detail
}

// Gate attributes read by gate-key delivery, relative to {desired,derived}/route/{gate}/.
const (
	gateAttributeHost      = "host"           // gateAttributeHost is the gate's Host match.
	gateAttributeTLSMode   = "tls/mode"       // gateAttributeTLSMode is the TLS mode (auto or manual).
	gateAttributeTLSCert   = "tls/cert"       // gateAttributeTLSCert is the certificate PEM.
	gateAttributeKeySecret = "tls/key_secret" // gateAttributeKeySecret names the user secret holding a manual key.
)

// DeliveredGateKey is one gate's TLS pair, certificate and key delivered
// together and checked to match.
type DeliveredGateKey struct {
	Gate           string `json:"gate"`            // Gate is the gate name.
	Host           string `json:"host"`            // Host is the gate's declared host (the SNI it serves).
	CertificatePEM string `json:"certificate_pem"` // CertificatePEM is the gate certificate.
	PrivateKeyPEM  string `json:"private_key_pem"` // PrivateKeyPEM is the matching private key.
}

// DeliveredGateKeys is the response of a gate-key delivery: every TLS gate
// whose pair is available, plus the TLS gates still waiting for one.
type DeliveredGateKeys struct {
	Gates   []DeliveredGateKey `json:"gates"`   // Gates are the available pairs, sorted by gate name.
	Pending []string           `json:"pending"` // Pending lists TLS gates without a valid pair, sorted.
}

// GateKeyDeliveryService authorizes and serves POST /api/node/gate-keys
// (Flow 28). It reads through a store bound to SecretDeliveryPrincipal and
// opens envelopes with the API server's SecretStore key provider.
type GateKeyDeliveryService struct {
	deliveryStore       store.StateStore     // deliveryStore is the AuthorizedStore bound to SecretDeliveryPrincipal.
	secretStore         *SecretStore         // secretStore opens envelopes read through deliveryStore.
	auditLog            AuditLogger          // auditLog records every decision; nil disables audit.
	refusalAuditLimiter *refusalAuditLimiter // refusalAuditLimiter rate-limits refusal audits per node|reason.
}

// NewGateKeyDeliveryService creates a gate-key delivery service. deliveryStore
// must be an AuthorizedStore with SecretDeliveryPrincipal as its principal.
func NewGateKeyDeliveryService(deliveryStore store.StateStore, secretStore *SecretStore, auditLog AuditLogger) *GateKeyDeliveryService {
	return &GateKeyDeliveryService{
		deliveryStore:       deliveryStore,
		secretStore:         secretStore,
		auditLog:            auditLog,
		refusalAuditLimiter: newRefusalAuditLimiter(),
	}
}

// DeliverGateKeys returns the TLS pair of every gate to an enrolled, enabled
// node running the gate proxy (decision B: every such node serves every gate),
// or a *SecretDeliveryRefusal. Store errors are returned as plain errors.
func (gateKeyDeliveryService *GateKeyDeliveryService) DeliverGateKeys(ctx context.Context, nodeID string) (*DeliveredGateKeys, error) {
	if refusalError := gateKeyDeliveryService.authorizeGateProxyNode(ctx, nodeID); refusalError != nil {
		gateKeyDeliveryService.auditRefusal(nodeID, refusalError)
		return nil, refusalError
	}
	desiredGates, desiredError := gateKeyDeliveryService.scanGateAttributes(ctx, types.ScanDesiredRoutes)
	if desiredError != nil {
		return nil, desiredError
	}
	derivedGates, derivedError := gateKeyDeliveryService.scanGateAttributes(ctx, types.ScanDerivedRoutes)
	if derivedError != nil {
		return nil, derivedError
	}
	delivered := &DeliveredGateKeys{Gates: []DeliveredGateKey{}, Pending: []string{}}
	for _, gateName := range sortedGateNames(desiredGates) {
		gateAttributes := desiredGates[gateName]
		if gateAttributes[gateAttributeTLSMode] == "" || gateAttributes[gateAttributeHost] == "" {
			continue
		}
		gatePair, pairError := gateKeyDeliveryService.resolveGatePair(ctx, gateName, gateAttributes, derivedGates[gateName])
		if pairError != nil {
			return nil, pairError
		}
		if gatePair == nil {
			delivered.Pending = append(delivered.Pending, gateName)
			continue
		}
		delivered.Gates = append(delivered.Gates, *gatePair)
	}
	gateKeyDeliveryService.auditAllowed(nodeID, delivered)
	return delivered, nil
}

// authorizeGateProxyNode refuses nodes that are not enrolled, are disabled, or
// do not report observed/node/{id}/gate_proxy = enabled.
func (gateKeyDeliveryService *GateKeyDeliveryService) authorizeGateProxyNode(ctx context.Context, nodeID string) error {
	if nodeError := checkDeliveryNode(ctx, gateKeyDeliveryService.deliveryStore, nodeID); nodeError != nil {
		return nodeError
	}
	gateProxyFact, gateProxyError := getOptionalDeliveryFact(ctx, gateKeyDeliveryService.deliveryStore, types.KeyObservedNodeGateProxy(nodeID))
	if gateProxyError != nil {
		return gateProxyError
	}
	if gateProxyFact == nil || types.GateProxyState(gateProxyFact.Value) != types.GateProxyEnabled {
		return &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "node does not run the gate proxy"}
	}
	return nil
}

// scanGateAttributes scans route facts under scanPrefix and groups them by
// gate name into attribute path → value.
func (gateKeyDeliveryService *GateKeyDeliveryService) scanGateAttributes(ctx context.Context, scanPrefix string) (map[string]map[string]string, error) {
	routeFacts, scanError := gateKeyDeliveryService.deliveryStore.Scan(ctx, scanPrefix)
	if scanError != nil {
		return nil, fmt.Errorf("read %s: %w", scanPrefix, scanError)
	}
	gateAttributes := make(map[string]map[string]string)
	for _, routeFact := range routeFacts {
		gateName, attributePath, hasAttribute := strings.Cut(strings.TrimPrefix(routeFact.Key, scanPrefix), "/")
		if gateName == "" || !hasAttribute {
			continue
		}
		if gateAttributes[gateName] == nil {
			gateAttributes[gateName] = make(map[string]string)
		}
		gateAttributes[gateName][attributePath] = string(routeFact.Value)
	}
	return gateAttributes, nil
}

// sortedGateNames returns the gate names of a grouped attribute map, sorted.
func sortedGateNames(gateAttributes map[string]map[string]string) []string {
	gateNames := make([]string, 0, len(gateAttributes))
	for gateName := range gateAttributes {
		gateNames = append(gateNames, gateName)
	}
	sort.Strings(gateNames)
	return gateNames
}

// resolveGatePair returns the gate's certificate (manual: desired, auto:
// derived) with its key (key_secret if set, else the system key), or nil when
// either is missing or they do not match. Only store failures are errors.
func (gateKeyDeliveryService *GateKeyDeliveryService) resolveGatePair(ctx context.Context, gateName string, desiredAttributes, derivedAttributes map[string]string) (*DeliveredGateKey, error) {
	certificatePEM := derivedAttributes[gateAttributeTLSCert]
	if types.RouteTLSMode(desiredAttributes[gateAttributeTLSMode]) == types.RouteTLSModeManual {
		certificatePEM = desiredAttributes[gateAttributeTLSCert]
	}
	if certificatePEM == "" {
		return nil, nil
	}
	keySecretName := GateKeySecretName(gateName, desiredAttributes[gateAttributeKeySecret])
	if types.ValidateSecretName(keySecretName) != nil {
		logging.Default().Warn("gate-key delivery: invalid key secret name", "gate", gateName)
		return nil, nil
	}
	privateKeyPEM, readError := gateKeyDeliveryService.secretStore.GetSecretThroughStore(ctx, gateKeyDeliveryService.deliveryStore, keySecretName)
	if errors.Is(readError, store.ErrKeyNotFound) {
		return nil, nil
	}
	if readError != nil {
		return nil, fmt.Errorf("read key of gate %s: %w", gateName, readError)
	}
	if _, pairError := tls.X509KeyPair([]byte(certificatePEM), privateKeyPEM); pairError != nil {
		logging.Default().Warn("gate-key delivery: certificate and key do not match", "gate", gateName)
		return nil, nil
	}
	return &DeliveredGateKey{
		Gate:           gateName,
		Host:           desiredAttributes[gateAttributeHost],
		CertificatePEM: certificatePEM,
		PrivateKeyPEM:  string(privateKeyPEM),
	}, nil
}

// StoreInlineGateKey seals a manual gate's inline private key at
// secrets/route/{gate}/tls/key after checking the gate exists with manual TLS
// and no key secret, and that the key matches desired/route/{gate}/tls/cert.
// Returns *GateKeyRejection for invalid requests; the caller authorizes.
func (gateKeyDeliveryService *GateKeyDeliveryService) StoreInlineGateKey(ctx context.Context, principalName string, gateName string, privateKeyPEM []byte) error {
	rejection, readError := gateKeyDeliveryService.checkInlineGateKey(ctx, gateName, privateKeyPEM)
	if readError != nil {
		return readError
	}
	if rejection != nil {
		gateKeyDeliveryService.writeStoreAuditEntry(principalName, gateName, AuditDecisionDeny, rejection.Detail)
		return rejection
	}
	if putError := gateKeyDeliveryService.secretStore.PutSecret(ctx, types.SecretNameRouteTLSKey(gateName), privateKeyPEM); putError != nil {
		return fmt.Errorf("seal key of gate %s: %w", gateName, putError)
	}
	gateKeyDeliveryService.writeStoreAuditEntry(principalName, gateName, AuditDecisionAllow, "")
	return nil
}

// checkInlineGateKey returns a rejection when the gate cannot take an inline
// key; store failures other than a missing key are returned as errors.
func (gateKeyDeliveryService *GateKeyDeliveryService) checkInlineGateKey(ctx context.Context, gateName string, privateKeyPEM []byte) (*GateKeyRejection, error) {
	if types.ValidateResourceName(gateName) != nil || strings.Contains(gateName, "/") {
		return &GateKeyRejection{Detail: "invalid gate name"}, nil
	}
	gateFacts := make(map[string]string, 3)
	for _, attributeKey := range []string{types.KeyDesiredRouteTLSMode(gateName), types.KeyDesiredRouteTLSKeySecret(gateName), types.KeyDesiredRouteTLSCert(gateName)} {
		attributeFact, getError := getOptionalDeliveryFact(ctx, gateKeyDeliveryService.deliveryStore, attributeKey)
		if getError != nil {
			return nil, getError
		}
		if attributeFact != nil {
			gateFacts[attributeKey] = string(attributeFact.Value)
		}
	}
	switch {
	case types.RouteTLSMode(gateFacts[types.KeyDesiredRouteTLSMode(gateName)]) != types.RouteTLSModeManual:
		return &GateKeyRejection{Detail: fmt.Sprintf("gate %s has no manual tls (apply the gate first)", gateName)}, nil
	case gateFacts[types.KeyDesiredRouteTLSKeySecret(gateName)] != "":
		return &GateKeyRejection{Detail: fmt.Sprintf("gate %s uses a key secret", gateName)}, nil
	}
	if _, pairError := tls.X509KeyPair([]byte(gateFacts[types.KeyDesiredRouteTLSCert(gateName)]), privateKeyPEM); pairError != nil {
		return &GateKeyRejection{Detail: fmt.Sprintf("key does not match the certificate of gate %s", gateName)}, nil
	}
	return nil, nil
}

// writeStoreAuditEntry records an inline gate-key store decision (gate and
// principal, never key material).
func (gateKeyDeliveryService *GateKeyDeliveryService) writeStoreAuditEntry(principalName string, gateName string, decision string, reason string) {
	if gateKeyDeliveryService.auditLog == nil {
		return
	}
	gateKeyDeliveryService.auditLog.Log(AuditEntry{
		Principal: principalName,
		Action:    GateKeyStoreAuditAction,
		Target:    "gate/" + gateName,
		Decision:  decision,
		Policy:    reason,
	})
}

// GateKeySecretName returns the secret name (relative to secrets/) holding a
// gate's private key: the user secret named by key_secret when set, otherwise
// the system key route/{gate}/tls/key. Ownership and delivery share this rule.
func GateKeySecretName(gateName string, keySecretName string) string {
	if keySecretName != "" {
		return keySecretName
	}
	return types.SecretNameRouteTLSKey(gateName)
}

// auditRefusal records a refused gate-key delivery at most once per
// refusalAuditInterval for the same node and reason.
func (gateKeyDeliveryService *GateKeyDeliveryService) auditRefusal(nodeID string, refusalError error) {
	var refusal *SecretDeliveryRefusal
	if !errors.As(refusalError, &refusal) {
		return
	}
	reason := string(refusal.Reason) + ": " + refusal.Detail
	if !gateKeyDeliveryService.refusalAuditLimiter.shouldAudit(nodeID+"|"+reason, time.Now()) {
		return
	}
	gateKeyDeliveryService.writeAuditEntry(nodeID, "gates", AuditDecisionDeny, reason)
}

// auditAllowed records an allowed delivery with gate names, never key material.
func (gateKeyDeliveryService *GateKeyDeliveryService) auditAllowed(nodeID string, delivered *DeliveredGateKeys) {
	deliveredGateNames := make([]string, 0, len(delivered.Gates))
	for _, deliveredGate := range delivered.Gates {
		deliveredGateNames = append(deliveredGateNames, deliveredGate.Gate)
	}
	target := fmt.Sprintf("gates=%s pending=%s", strings.Join(deliveredGateNames, ","), strings.Join(delivered.Pending, ","))
	gateKeyDeliveryService.writeAuditEntry(nodeID, target, AuditDecisionAllow, "")
}

// writeAuditEntry writes one gate-key delivery audit entry for the node.
func (gateKeyDeliveryService *GateKeyDeliveryService) writeAuditEntry(nodeID string, target string, decision string, reason string) {
	if gateKeyDeliveryService.auditLog == nil {
		return
	}
	gateKeyDeliveryService.auditLog.Log(AuditEntry{
		Principal:  string(PrincipalKindNode) + ":" + nodeID,
		Action:     GateKeyDeliveryAuditAction,
		Target:     target,
		Decision:   decision,
		AuthMethod: "mtls",
		Policy:     reason,
	})
}
