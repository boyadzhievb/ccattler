// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// SecretDeliveryPrincipal is the store-layer identity of node secret delivery
// (Flow 27). It runs inside the API server process but is not the generic
// api-server role, which keeps having no access to secrets/.
const SecretDeliveryPrincipal = "service:secret-delivery"

// SecretDeliveryAuditAction is the audit action recorded for every delivery
// decision (allowed or refused).
const SecretDeliveryAuditAction = "secret.deliver"

// SecretDeliveryRefusalReason is the typed reason a delivery was refused. The
// agent publishes it as the reason of the waiting-for-secrets state.
type SecretDeliveryRefusalReason string

const (
	// SecretDeliveryNotAuthorized means the caller, placement, node or a grant
	// check failed.
	SecretDeliveryNotAuthorized SecretDeliveryRefusalReason = "not_authorized"
	// SecretDeliverySecretMissing means every check passed but a granted secret
	// has no stored value.
	SecretDeliverySecretMissing SecretDeliveryRefusalReason = "secret_missing"
)

// SecretDeliveryRefusal is the error returned when a delivery is refused. It
// carries secret names, never values.
type SecretDeliveryRefusal struct {
	Reason      SecretDeliveryRefusalReason // Reason is the typed refusal reason.
	Detail      string                      // Detail explains which check failed.
	SecretNames []string                    // SecretNames lists the secrets that failed, sorted.
}

// Error formats the refusal with its reason, detail and secret names.
func (refusal *SecretDeliveryRefusal) Error() string {
	if len(refusal.SecretNames) == 0 {
		return fmt.Sprintf("%s: %s", refusal.Reason, refusal.Detail)
	}
	return fmt.Sprintf("%s: %s (%s)", refusal.Reason, refusal.Detail, strings.Join(refusal.SecretNames, ", "))
}

// DeliveredInstanceSecrets is the plaintext material for one instance: every
// secret its service mounts, uses in a connection, or needs to pull its image.
type DeliveredInstanceSecrets struct {
	InstanceID string            `json:"instance_id"` // InstanceID is the instance the material belongs to.
	Service    string            `json:"service"`     // Service is the instance's service.
	Secrets    map[string][]byte `json:"secrets"`     // Secrets maps secret name to plaintext value.
}

// SecretDeliveryService authorizes and serves node secret delivery requests.
// It reads through a store bound to SecretDeliveryPrincipal and decrypts with
// the API server's SecretStore key provider.
type SecretDeliveryService struct {
	deliveryStore       store.StateStore     // deliveryStore is the AuthorizedStore bound to SecretDeliveryPrincipal.
	secretStore         *SecretStore         // secretStore is the API server's secret store; it decrypts envelopes read through deliveryStore.
	auditLog            AuditLogger          // auditLog records every decision; nil disables audit.
	refusalAuditLimiter *refusalAuditLimiter // refusalAuditLimiter rate-limits refusal audits per node|instance|reason.
}

// NewSecretDeliveryService creates a delivery service. deliveryStore must be
// an AuthorizedStore with SecretDeliveryPrincipal as its service principal;
// secretStore supplies the current KeyProvider (shared with rotation).
func NewSecretDeliveryService(deliveryStore store.StateStore, secretStore *SecretStore, auditLog AuditLogger) *SecretDeliveryService {
	return &SecretDeliveryService{
		deliveryStore:       deliveryStore,
		secretStore:         secretStore,
		auditLog:            auditLog,
		refusalAuditLimiter: newRefusalAuditLimiter(),
	}
}

// DeliverInstanceSecrets returns every secret the instance needs, or a
// *SecretDeliveryRefusal. All checks run on every request: enrolled and not
// disabled node, instance placed on that node, a grant for each secret. The
// result is all-or-nothing; partial material is never returned.
func (deliveryService *SecretDeliveryService) DeliverInstanceSecrets(ctx context.Context, nodeID string, instanceID string) (*DeliveredInstanceSecrets, error) {
	serviceName, checkError := deliveryService.authorizeNodeForInstance(ctx, nodeID, instanceID)
	if checkError != nil {
		deliveryService.auditRefusal(nodeID, instanceID, serviceName, checkError)
		return nil, checkError
	}
	requiredSecretNames, grantError := deliveryService.resolveRequiredSecretNames(ctx, serviceName)
	if grantError != nil {
		deliveryService.auditRefusal(nodeID, instanceID, serviceName, grantError)
		return nil, grantError
	}
	delivered := &DeliveredInstanceSecrets{InstanceID: instanceID, Service: serviceName, Secrets: make(map[string][]byte, len(requiredSecretNames))}
	var missingSecretNames []string
	for _, secretName := range requiredSecretNames {
		plaintext, readError := deliveryService.secretStore.GetSecretThroughStore(ctx, deliveryService.deliveryStore, secretName)
		if errors.Is(readError, store.ErrKeyNotFound) {
			missingSecretNames = append(missingSecretNames, secretName)
			continue
		}
		if readError != nil {
			return nil, fmt.Errorf("read secret %s: %w", secretName, readError)
		}
		delivered.Secrets[secretName] = plaintext
	}
	if len(missingSecretNames) > 0 {
		missingError := &SecretDeliveryRefusal{Reason: SecretDeliverySecretMissing, Detail: "granted secret has no value", SecretNames: missingSecretNames}
		deliveryService.auditRefusal(nodeID, instanceID, serviceName, missingError)
		return nil, missingError
	}
	deliveryService.auditDecision(nodeID, instanceID, serviceName, requiredSecretNames, AuditDecisionAllow, "")
	return delivered, nil
}

// getOptionalDeliveryFact reads one fact for a delivery check. A missing key
// returns (nil, nil) so callers refuse; any other store error is returned so
// the request fails with a server error instead of a refusal.
func getOptionalDeliveryFact(ctx context.Context, readStore store.StateStore, key string) (*store.Fact, error) {
	fact, getError := readStore.Get(ctx, key)
	if errors.Is(getError, store.ErrKeyNotFound) {
		return nil, nil
	}
	if getError != nil {
		return nil, fmt.Errorf("read %s: %w", key, getError)
	}
	return fact, nil
}

// authorizeNodeForInstance checks that the node is enrolled and not disabled
// and that the instance is placed on it. Returns the instance's service.
// Store errors are returned unwrapped from refusals (server error, retried).
func (deliveryService *SecretDeliveryService) authorizeNodeForInstance(ctx context.Context, nodeID string, instanceID string) (string, error) {
	if types.ValidateResourceName(instanceID) != nil {
		return "", &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "invalid node or instance id"}
	}
	if nodeError := checkDeliveryNode(ctx, deliveryService.deliveryStore, nodeID); nodeError != nil {
		return "", nodeError
	}
	placementFact, placementError := getOptionalDeliveryFact(ctx, deliveryService.deliveryStore, types.KeyPlacementInstance(instanceID))
	if placementError != nil || placementFact == nil || string(placementFact.Value) != nodeID {
		return "", firstError(placementError, &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "instance is not placed on this node"})
	}
	serviceFact, serviceError := getOptionalDeliveryFact(ctx, deliveryService.deliveryStore, types.KeyObservedInstanceService(instanceID))
	if serviceError != nil || serviceFact == nil || len(serviceFact.Value) == 0 {
		return "", firstError(serviceError, &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "instance has no service"})
	}
	serviceName := string(serviceFact.Value)
	if identityError := checkInstanceIsServiceInstance(ctx, deliveryService.deliveryStore, instanceID, serviceName); identityError != nil {
		return "", identityError
	}
	return serviceName, nil
}

// checkInstanceIsServiceInstance refuses job instances (a job records its own
// name as the instance's service, so a job named like a service would receive
// that service's secrets) and services without a declared root marker.
func checkInstanceIsServiceInstance(ctx context.Context, readStore store.StateStore, instanceID string, serviceName string) error {
	jobOwnerFact, jobOwnerError := getOptionalDeliveryFact(ctx, readStore, types.KeyDerivedJobInstanceOwner(instanceID))
	if jobOwnerError != nil || jobOwnerFact != nil {
		return firstError(jobOwnerError, &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "instance belongs to a job"})
	}
	serviceMarkerFact, markerError := getOptionalDeliveryFact(ctx, readStore, types.KeyDesiredService(serviceName))
	if markerError != nil || serviceMarkerFact == nil {
		return firstError(markerError, &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "service is not declared"})
	}
	return nil
}

// checkDeliveryNode refuses a delivery to a node that is not enrolled or is
// disabled (Flows 27 and 28). Store errors are returned unwrapped so the
// request fails with a server error instead of a refusal.
func checkDeliveryNode(ctx context.Context, readStore store.StateStore, nodeID string) error {
	if nodeID == "" {
		return &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "invalid node or instance id"}
	}
	enrolledFact, enrolledError := getOptionalDeliveryFact(ctx, readStore, EnrolledNodePrefix+nodeID)
	if enrolledError != nil || enrolledFact == nil {
		return firstError(enrolledError, &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "node is not enrolled"})
	}
	nodeStateFact, stateError := getOptionalDeliveryFact(ctx, readStore, types.KeyObservedNodeState(nodeID))
	if stateError != nil {
		return stateError
	}
	if nodeStateFact != nil && types.NodeState(nodeStateFact.Value) == types.NodeDisabled {
		return &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "node is disabled"}
	}
	return nil
}

// firstError returns storeError when set, otherwise refusal.
func firstError(storeError error, refusal *SecretDeliveryRefusal) error {
	if storeError != nil {
		return storeError
	}
	return refusal
}

// resolveRequiredSecretNames returns the sorted names of every secret the
// service needs (mounted secrets, connection passwords, image pull secret).
// A connection or pull secret without a grant refuses the whole request.
func (deliveryService *SecretDeliveryService) resolveRequiredSecretNames(ctx context.Context, serviceName string) ([]string, error) {
	grantPrefix := types.ScanDesiredServiceSecrets(serviceName)
	grantFacts, scanError := deliveryService.deliveryStore.Scan(ctx, grantPrefix)
	if scanError != nil {
		return nil, fmt.Errorf("read grants of %s: %w", serviceName, scanError)
	}
	grantedSecretNames := make(map[string]bool, len(grantFacts))
	for _, grantFact := range grantFacts {
		if secretName := strings.TrimPrefix(grantFact.Key, grantPrefix); types.ValidateSecretName(secretName) == nil {
			grantedSecretNames[secretName] = true
		}
	}
	referencedSecretNames, referenceError := deliveryService.collectReferencedSecretNames(ctx, serviceName)
	if referenceError != nil {
		return nil, referenceError
	}
	var ungrantedSecretNames []string
	for secretName := range referencedSecretNames {
		if !grantedSecretNames[secretName] {
			ungrantedSecretNames = append(ungrantedSecretNames, secretName)
		}
	}
	// System-owned gate TLS keys (route/…) are never delivered to an
	// instance, whatever grants say (Flow 28): only gate-key delivery serves them.
	for secretName := range grantedSecretNames {
		if types.IsSystemOwnedSecretName(secretName) {
			ungrantedSecretNames = append(ungrantedSecretNames, secretName)
		}
	}
	if len(ungrantedSecretNames) > 0 {
		sort.Strings(ungrantedSecretNames)
		return nil, &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "service has no grant (or names a system-owned secret)", SecretNames: ungrantedSecretNames}
	}
	requiredSecretNames := make([]string, 0, len(grantedSecretNames))
	for secretName := range grantedSecretNames {
		requiredSecretNames = append(requiredSecretNames, secretName)
	}
	sort.Strings(requiredSecretNames)
	return requiredSecretNames, nil
}

// connectionSecretField is the part of a desired/connection/{name} value that
// delivery needs: the name of the password secret.
type connectionSecretField struct {
	Secret string `json:"secret"` // Secret is the password secret name; empty means no password.
}

// collectReferencedSecretNames returns the secret names used by the service's
// connections and image pull secret. These must also be granted.
func (deliveryService *SecretDeliveryService) collectReferencedSecretNames(ctx context.Context, serviceName string) (map[string]bool, error) {
	referencedSecretNames := make(map[string]bool)
	connectionPrefix := types.ScanDesiredServiceConnections(serviceName)
	connectionRefs, scanError := deliveryService.deliveryStore.Scan(ctx, connectionPrefix)
	if scanError != nil {
		return nil, fmt.Errorf("read connections of %s: %w", serviceName, scanError)
	}
	for _, connectionRef := range connectionRefs {
		connectionName := strings.TrimPrefix(connectionRef.Key, connectionPrefix)
		connectionFact, getError := deliveryService.deliveryStore.Get(ctx, types.KeyDesiredConnection(connectionName))
		if getError != nil {
			continue
		}
		var connectionFields connectionSecretField
		if json.Unmarshal(connectionFact.Value, &connectionFields) == nil && connectionFields.Secret != "" {
			referencedSecretNames[connectionFields.Secret] = true
		}
	}
	pullSecretFact, pullError := deliveryService.deliveryStore.Get(ctx, types.KeyDesiredServiceImagePullSecret(serviceName))
	if pullError == nil && len(pullSecretFact.Value) > 0 {
		referencedSecretNames[string(pullSecretFact.Value)] = true
	} else if pullError != nil && !errors.Is(pullError, store.ErrKeyNotFound) {
		return nil, fmt.Errorf("read image pull secret of %s: %w", serviceName, pullError)
	}
	return referencedSecretNames, nil
}

// auditRefusal records a refused delivery at most once per
// refusalAuditInterval for the same node, instance and reason.
func (deliveryService *SecretDeliveryService) auditRefusal(nodeID string, instanceID string, serviceName string, refusalError error) {
	var refusal *SecretDeliveryRefusal
	if !errors.As(refusalError, &refusal) {
		return
	}
	reason := string(refusal.Reason) + ": " + refusal.Detail
	if !deliveryService.refusalAuditLimiter.shouldAudit(nodeID+"|"+instanceID+"|"+reason, time.Now()) {
		return
	}
	deliveryService.auditDecision(nodeID, instanceID, serviceName, refusal.SecretNames, AuditDecisionDeny, reason)
}

// auditDecision writes one audit entry with node, instance, service and secret
// names. Values are never part of an audit entry.
func (deliveryService *SecretDeliveryService) auditDecision(nodeID string, instanceID string, serviceName string, secretNames []string, decision string, reason string) {
	if deliveryService.auditLog == nil {
		return
	}
	deliveryService.auditLog.Log(AuditEntry{
		Principal:  string(PrincipalKindNode) + ":" + nodeID,
		Action:     SecretDeliveryAuditAction,
		Target:     fmt.Sprintf("instance/%s service=%s secrets=%s", instanceID, serviceName, strings.Join(secretNames, ",")),
		Decision:   decision,
		AuthMethod: "mtls",
		Policy:     reason,
	})
}

// secretDeliveryRole is the store role of SecretDeliveryPrincipal: read-only
// access to the secrets, grants, connections, gates, placements and node
// facts that secret and gate-key delivery check. It writes nothing through the store (audit is separate).
func secretDeliveryRole() Role {
	readOnly := []Permission{PermissionRead}
	return Role{
		Name: "secret-delivery",
		Rules: []Rule{
			{KeyPrefix: SecretStorePrefix, Operations: readOnly},
			{KeyPrefix: "desired/service/", Operations: readOnly},
			{KeyPrefix: "desired/connection/", Operations: readOnly},
			{KeyPrefix: types.ScanDesiredRoutes, Operations: readOnly},
			{KeyPrefix: types.ScanDerivedRoutes, Operations: readOnly},
			{KeyPrefix: "placement/instance/", Operations: readOnly},
			{KeyPrefix: "observed/instance/", Operations: readOnly},
			{KeyPrefix: "derived/instance/", Operations: readOnly},
			{KeyPrefix: "observed/node/", Operations: readOnly},
			{KeyPrefix: EnrolledNodePrefix, Operations: readOnly},
		},
	}
}
