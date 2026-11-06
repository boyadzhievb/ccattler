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
	"sync"
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

// secretDeliveryRefusalAuditInterval is the minimum time between two refusal
// audit records for the same (node, instance, reason), so a waiting instance
// that retries does not flood the audit log.
const secretDeliveryRefusalAuditInterval = time.Minute

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
	deliveryStore         store.StateStore     // deliveryStore is the AuthorizedStore bound to SecretDeliveryPrincipal.
	secretStore           *SecretStore         // secretStore is the API server's secret store; it decrypts envelopes read through deliveryStore.
	auditLog              AuditLogger          // auditLog records every decision; nil disables audit.
	refusalAuditMutex     sync.Mutex           // refusalAuditMutex guards lastRefusalAuditTimes.
	lastRefusalAuditTimes map[string]time.Time // lastRefusalAuditTimes maps node|instance|reason to the last refusal audit time.
}

// NewSecretDeliveryService creates a delivery service. deliveryStore must be
// an AuthorizedStore with SecretDeliveryPrincipal as its service principal;
// secretStore supplies the current KeyProvider (shared with rotation).
func NewSecretDeliveryService(deliveryStore store.StateStore, secretStore *SecretStore, auditLog AuditLogger) *SecretDeliveryService {
	return &SecretDeliveryService{
		deliveryStore:         deliveryStore,
		secretStore:           secretStore,
		auditLog:              auditLog,
		lastRefusalAuditTimes: make(map[string]time.Time),
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
		if readError != nil {
			missingSecretNames = append(missingSecretNames, secretName)
			continue
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

// authorizeNodeForInstance checks that the node is enrolled and not disabled
// and that the instance is placed on it. Returns the instance's service.
func (deliveryService *SecretDeliveryService) authorizeNodeForInstance(ctx context.Context, nodeID string, instanceID string) (string, error) {
	if nodeID == "" || instanceID == "" || strings.Contains(instanceID, "/") {
		return "", &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "invalid node or instance id"}
	}
	if _, enrolledError := deliveryService.deliveryStore.Get(ctx, EnrolledNodePrefix+nodeID); enrolledError != nil {
		return "", &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "node is not enrolled"}
	}
	if nodeStateFact, stateError := deliveryService.deliveryStore.Get(ctx, types.KeyObservedNodeState(nodeID)); stateError == nil &&
		types.NodeState(nodeStateFact.Value) == types.NodeDisabled {
		return "", &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "node is disabled"}
	}
	placementFact, placementError := deliveryService.deliveryStore.Get(ctx, types.KeyPlacementInstance(instanceID))
	if placementError != nil || string(placementFact.Value) != nodeID {
		return "", &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "instance is not placed on this node"}
	}
	serviceFact, serviceError := deliveryService.deliveryStore.Get(ctx, types.KeyObservedInstanceService(instanceID))
	if serviceError != nil || len(serviceFact.Value) == 0 {
		return "", &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "instance has no service"}
	}
	return string(serviceFact.Value), nil
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
		if secretName := strings.TrimPrefix(grantFact.Key, grantPrefix); secretName != "" && !strings.Contains(secretName, "/") {
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
	if len(ungrantedSecretNames) > 0 {
		sort.Strings(ungrantedSecretNames)
		return nil, &SecretDeliveryRefusal{Reason: SecretDeliveryNotAuthorized, Detail: "service has no grant", SecretNames: ungrantedSecretNames}
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
// secretDeliveryRefusalAuditInterval for the same node, instance and reason.
func (deliveryService *SecretDeliveryService) auditRefusal(nodeID string, instanceID string, serviceName string, refusalError error) {
	reason := string(SecretDeliveryNotAuthorized)
	var secretNames []string
	var refusal *SecretDeliveryRefusal
	if errors.As(refusalError, &refusal) {
		reason = string(refusal.Reason) + ": " + refusal.Detail
		secretNames = refusal.SecretNames
	}
	rateLimitKey := nodeID + "|" + instanceID + "|" + reason
	now := time.Now()
	deliveryService.refusalAuditMutex.Lock()
	lastAuditTime, audited := deliveryService.lastRefusalAuditTimes[rateLimitKey]
	if audited && now.Sub(lastAuditTime) < secretDeliveryRefusalAuditInterval {
		deliveryService.refusalAuditMutex.Unlock()
		return
	}
	deliveryService.lastRefusalAuditTimes[rateLimitKey] = now
	deliveryService.pruneRefusalAuditTimes(now)
	deliveryService.refusalAuditMutex.Unlock()
	deliveryService.auditDecision(nodeID, instanceID, serviceName, secretNames, AuditDecisionDeny, reason)
}

// pruneRefusalAuditTimes drops rate-limit entries older than the interval so
// the map stays bounded. Caller holds refusalAuditMutex.
func (deliveryService *SecretDeliveryService) pruneRefusalAuditTimes(now time.Time) {
	for rateLimitKey, lastAuditTime := range deliveryService.lastRefusalAuditTimes {
		if now.Sub(lastAuditTime) >= secretDeliveryRefusalAuditInterval {
			delete(deliveryService.lastRefusalAuditTimes, rateLimitKey)
		}
	}
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
// access to the secrets, grants, connections, placements and node facts that
// delivery checks. It writes nothing through the store (audit is separate).
func secretDeliveryRole() Role {
	readOnly := []Permission{PermissionRead}
	return Role{
		Name: "secret-delivery",
		Rules: []Rule{
			{KeyPrefix: SecretStorePrefix, Operations: readOnly},
			{KeyPrefix: "desired/service/", Operations: readOnly},
			{KeyPrefix: "desired/connection/", Operations: readOnly},
			{KeyPrefix: "placement/instance/", Operations: readOnly},
			{KeyPrefix: "observed/instance/", Operations: readOnly},
			{KeyPrefix: "observed/node/", Operations: readOnly},
			{KeyPrefix: EnrolledNodePrefix, Operations: readOnly},
		},
	}
}
