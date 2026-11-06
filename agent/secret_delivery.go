// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// secretDeliveryRetryInitial is the first retry delay after the API server
// could not be reached.
const secretDeliveryRetryInitial = 2 * time.Second

// secretDeliveryRetryMax caps the retry delay while the API is unreachable.
const secretDeliveryRetryMax = 30 * time.Second

// secretDeliveryRefusedRetry is the fallback retry interval after a refusal;
// a change of the service's grants, connections or pull secret retries at once.
const secretDeliveryRefusedRetry = 5 * time.Minute

// secretDeliveryProtocolVersion is reported in observed/node/{id}/secret_delivery.
const secretDeliveryProtocolVersion = "v2"

// LegacySecretMasterKeyPath is the old per-node key file. Agents no longer use
// it; they only report whether it is still present (migration gate, M176).
const LegacySecretMasterKeyPath = "/etc/ccattler/secret-master.key" //nolint:gosec // G101: file path, not a credential

// secretDeliveryWaitEntry tracks why and until when an instance waits for secrets.
type secretDeliveryWaitEntry struct {
	reason              SecretWaitReason // reason is the last wait reason.
	secretNames         []string         // secretNames are the refused or missing secrets.
	attempts            int              // attempts counts consecutive failed deliveries.
	retryAfter          time.Time        // retryAfter is the earliest next delivery attempt.
	requirementsVersion string           // requirementsVersion fingerprints grants, connections and pull secret at refusal time.
}

// secretWaitStatus is the JSON value of observed/instance/{id}/secret-wait.
type secretWaitStatus struct {
	Reason    string   `json:"reason"`            // Reason is the typed wait reason.
	Secrets   []string `json:"secrets,omitempty"` // Secrets lists secret names (never values).
	NextRetry string   `json:"next_retry"`        // NextRetry is the RFC3339 time of the next attempt.
}

// serviceSecretRequirements describes which secrets a service needs and where
// they are mounted. It is built from desired facts only, never from secrets/.
type serviceSecretRequirements struct {
	grantMountPaths       map[string]string // grantMountPaths maps granted secret name to its explicit mount path ("" if none).
	referencedSecretNames map[string]bool   // referencedSecretNames are connection and image pull secrets; each must also be granted.
	needsSecrets          bool              // needsSecrets is true when any grant, connection secret or pull secret exists.
	requirementsVersion   string            // requirementsVersion changes whenever any input above changes.
}

// SetSecretDelivery configures how the agent obtains secret material: client
// requests it from the control plane, materialStore keeps it on tmpfs. A nil
// client leaves instances that need secrets in waiting-for-secrets.
func (nodeAgent *Agent) SetSecretDelivery(client SecretDeliveryClient, materialStore *SecretMaterialStore) {
	nodeAgent.secretDeliveryClient = client
	nodeAgent.secretMaterialStore = materialStore
}

// loadServiceSecretRequirements reads the service's grants, connection
// references and image pull secret. A store error is returned so the caller
// treats the instance as not ready instead of as needing no secrets.
func (nodeAgent *Agent) loadServiceSecretRequirements(ctx context.Context, serviceName string) (serviceSecretRequirements, error) {
	requirements := serviceSecretRequirements{grantMountPaths: make(map[string]string), referencedSecretNames: make(map[string]bool)}
	var versionParts []string
	grantPrefix := types.ScanDesiredServiceSecrets(serviceName)
	grantFacts, scanError := nodeAgent.store.Scan(ctx, grantPrefix)
	if scanError != nil {
		return requirements, fmt.Errorf("read grants of %s: %w", serviceName, scanError)
	}
	for _, grantFact := range grantFacts {
		secretName := strings.TrimPrefix(grantFact.Key, grantPrefix)
		if types.ValidateSecretName(secretName) != nil {
			continue
		}
		requirements.grantMountPaths[secretName] = string(grantFact.Value)
		versionParts = append(versionParts, "grant="+secretName+"@"+string(grantFact.Value))
	}
	connectionVersionParts, connectionError := nodeAgent.collectConnectionSecretNames(ctx, serviceName, requirements.referencedSecretNames)
	if connectionError != nil {
		return requirements, connectionError
	}
	versionParts = append(versionParts, connectionVersionParts...)
	pullSecretFact, pullError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceImagePullSecret(serviceName))
	if pullError != nil && !errors.Is(pullError, store.ErrKeyNotFound) {
		return requirements, fmt.Errorf("read image pull secret of %s: %w", serviceName, pullError)
	}
	if pullError == nil && len(pullSecretFact.Value) > 0 {
		requirements.referencedSecretNames[string(pullSecretFact.Value)] = true
		versionParts = append(versionParts, "pull="+string(pullSecretFact.Value))
	}
	requirements.needsSecrets = len(requirements.grantMountPaths) > 0 || len(requirements.referencedSecretNames) > 0
	sort.Strings(versionParts)
	requirements.requirementsVersion = strings.Join(versionParts, ";")
	return requirements, nil
}

// collectConnectionSecretNames adds the password secret of every connection
// the service uses to secretNames and returns their version parts.
func (nodeAgent *Agent) collectConnectionSecretNames(ctx context.Context, serviceName string, secretNames map[string]bool) ([]string, error) {
	connectionPrefix := types.ScanDesiredServiceConnections(serviceName)
	connectionRefs, scanError := nodeAgent.store.Scan(ctx, connectionPrefix)
	if scanError != nil {
		return nil, fmt.Errorf("read connections of %s: %w", serviceName, scanError)
	}
	var versionParts []string
	for _, connectionRef := range connectionRefs {
		connectionName := strings.TrimPrefix(connectionRef.Key, connectionPrefix)
		connectionFact, getError := nodeAgent.store.Get(ctx, types.KeyDesiredConnection(connectionName))
		if errors.Is(getError, store.ErrKeyNotFound) {
			continue
		}
		if getError != nil {
			return nil, fmt.Errorf("read connection %s: %w", connectionName, getError)
		}
		if connectionSecret := parseConnectionJSON(string(connectionFact.Value)).secret; connectionSecret != "" {
			secretNames[connectionSecret] = true
			versionParts = append(versionParts, "connection="+connectionName+"@"+connectionSecret)
		}
	}
	return versionParts, nil
}

// prepareInstanceSecretMaterial makes sure the instance's secrets are on tmpfs
// before its image is pulled (Flow 27 step 5). Returns the values and true when
// the instance may proceed; false keeps it waiting (or retries a store error).
func (nodeAgent *Agent) prepareInstanceSecretMaterial(ctx context.Context, instanceInfo placedInstanceInfo) (map[string][]byte, bool) {
	requirements, requirementsError := nodeAgent.loadServiceSecretRequirements(ctx, instanceInfo.service)
	if requirementsError != nil {
		logging.Default().Warn("cannot read secret requirements, retrying next cycle", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", requirementsError.Error())
		return nil, false
	}
	if !requirements.needsSecrets {
		nodeAgent.clearSecretDeliveryWait(ctx, instanceInfo.id)
		return nil, true
	}
	if nodeAgent.secretMaterialStore != nil {
		if existingValues, hasMaterial := nodeAgent.secretMaterialStore.ReadInstanceSecrets(instanceInfo.id); hasMaterial && materialMatchesRequirements(existingValues, requirements) {
			nodeAgent.clearSecretDeliveryWait(ctx, instanceInfo.id)
			return existingValues, true
		}
	}
	if waitEntry := nodeAgent.secretDeliveryWaits[instanceInfo.id]; waitEntry != nil && time.Now().Before(waitEntry.retryAfter) {
		requirementsChanged := waitEntry.reason != SecretWaitAPIUnreachable && waitEntry.requirementsVersion != requirements.requirementsVersion
		if !requirementsChanged {
			return nil, false
		}
	}
	if nodeAgent.secretDeliveryClient == nil || nodeAgent.secretMaterialStore == nil {
		nodeAgent.recordSecretDeliveryWait(ctx, instanceInfo, requirements, &SecretDeliveryError{Reason: SecretWaitDeliveryNotConfigured, Detail: "agent has no mTLS identity or API server address"})
		return nil, false
	}
	deliveredValues, deliveryError := nodeAgent.secretDeliveryClient.FetchInstanceSecrets(ctx, instanceInfo.id)
	if deliveryError == nil {
		deliveryError = nodeAgent.secretMaterialStore.WriteInstanceSecrets(instanceInfo.id, deliveredValues)
	}
	if deliveryError != nil {
		nodeAgent.recordSecretDeliveryWait(ctx, instanceInfo, requirements, deliveryError)
		return nil, false
	}
	if _, wasWaiting := nodeAgent.secretDeliveryWaits[instanceInfo.id]; wasWaiting {
		// Leave waiting-for-secrets; with a cached image nothing else would change the state before start.
		nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstancePending, "")
	}
	nodeAgent.clearSecretDeliveryWait(ctx, instanceInfo.id)
	logging.Default().Info("secrets delivered", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "count", strconv.Itoa(len(deliveredValues)))
	return deliveredValues, true
}

// materialMatchesRequirements reports whether existing material can be reused:
// it holds exactly the granted secrets (a removed grant forces a new delivery)
// and every connection or pull secret is among them (else delivery is refused).
func materialMatchesRequirements(secretValues map[string][]byte, requirements serviceSecretRequirements) bool {
	if len(secretValues) != len(requirements.grantMountPaths) {
		return false
	}
	for secretName := range requirements.grantMountPaths {
		if _, present := secretValues[secretName]; !present {
			return false
		}
	}
	for secretName := range requirements.referencedSecretNames {
		if _, present := secretValues[secretName]; !present {
			return false
		}
	}
	return true
}

// recordSecretDeliveryWait sets the retry time for the failure, publishes
// waiting-for-secrets with its reason, and logs reason changes once.
func (nodeAgent *Agent) recordSecretDeliveryWait(ctx context.Context, instanceInfo placedInstanceInfo, requirements serviceSecretRequirements, failure error) {
	deliveryFailure := &SecretDeliveryError{Reason: SecretWaitAPIUnreachable, Detail: failure.Error()}
	var typedFailure *SecretDeliveryError
	if errors.As(failure, &typedFailure) {
		deliveryFailure = typedFailure
	}
	waitEntry := nodeAgent.secretDeliveryWaits[instanceInfo.id]
	if waitEntry == nil || waitEntry.reason != deliveryFailure.Reason {
		waitEntry = &secretDeliveryWaitEntry{}
		nodeAgent.secretDeliveryWaits[instanceInfo.id] = waitEntry
		logging.Default().Warn("instance waiting for secrets", "agent", nodeAgent.nodeID, "instance", instanceInfo.id,
			"service", instanceInfo.service, "reason", string(deliveryFailure.Reason), "detail", deliveryFailure.Error())
	}
	waitEntry.reason = deliveryFailure.Reason
	waitEntry.secretNames = deliveryFailure.SecretNames
	waitEntry.attempts++
	waitEntry.requirementsVersion = requirements.requirementsVersion
	waitEntry.retryAfter = time.Now().Add(secretDeliveryRetryDelay(deliveryFailure.Reason, waitEntry.attempts))

	nodeAgent.publishInstanceStateToStore(ctx, instanceInfo.id, instanceInfo.service, types.InstanceWaitingForSecrets, "")
	statusJSON, _ := json.Marshal(secretWaitStatus{Reason: string(waitEntry.reason), Secrets: waitEntry.secretNames, NextRetry: waitEntry.retryAfter.Format(time.RFC3339)})
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceSecretWait(instanceInfo.id), statusJSON); putError != nil {
		logging.Default().Error("failed to write secret wait status", "instance", instanceInfo.id, "error", putError.Error())
	}
	statusReason := "waiting for secrets: " + string(waitEntry.reason)
	if len(waitEntry.secretNames) > 0 {
		statusReason += " (" + strings.Join(waitEntry.secretNames, ", ") + ")"
	}
	nodeAgent.publishStatusReason(ctx, instanceInfo.id, statusReason)
}

// secretDeliveryRetryDelay returns exponential backoff (2s … 30s) for an
// unreachable API, a fixed 30s for a missing secret (the agent cannot watch
// secrets/, and setting the value after apply is common), and the 5-minute
// fallback for refusals.
func secretDeliveryRetryDelay(reason SecretWaitReason, attempts int) time.Duration {
	if reason == SecretWaitSecretMissing {
		return secretDeliveryRetryMax
	}
	if reason != SecretWaitAPIUnreachable {
		return secretDeliveryRefusedRetry
	}
	retryDelay := secretDeliveryRetryInitial
	for attempt := 1; attempt < attempts && retryDelay < secretDeliveryRetryMax; attempt++ {
		retryDelay *= 2
	}
	if retryDelay > secretDeliveryRetryMax {
		retryDelay = secretDeliveryRetryMax
	}
	return retryDelay
}

// clearSecretDeliveryWait drops the wait entry and its status fact after a
// successful delivery or when the instance no longer needs secrets.
func (nodeAgent *Agent) clearSecretDeliveryWait(ctx context.Context, instanceID string) {
	if _, waiting := nodeAgent.secretDeliveryWaits[instanceID]; !waiting {
		return
	}
	delete(nodeAgent.secretDeliveryWaits, instanceID)
	if deleteError := nodeAgent.store.Delete(ctx, types.KeyObservedInstanceSecretWait(instanceID)); deleteError != nil && !errors.Is(deleteError, store.ErrKeyNotFound) {
		logging.Default().Error("failed to delete secret wait status", "instance", instanceID, "error", deleteError.Error())
	}
}

// buildInstanceSecretMounts writes connection files and returns the read-only
// secret mounts (container path → tmpfs host path) for the instance. Every
// granted secret appears at /run/cca-secrets/{name} and at its explicit mount.
// Names are processed in sorted order so a path collision resolves the same
// way every time. Any error means the instance must not start this cycle.
func (nodeAgent *Agent) buildInstanceSecretMounts(ctx context.Context, instanceInfo placedInstanceInfo, secretValues map[string][]byte) (map[string]string, error) {
	if nodeAgent.secretMaterialStore == nil || len(secretValues) == 0 {
		return nil, nil
	}
	requirements, requirementsError := nodeAgent.loadServiceSecretRequirements(ctx, instanceInfo.service)
	if requirementsError != nil {
		return nil, requirementsError
	}
	grantedSecretNames := make([]string, 0, len(requirements.grantMountPaths))
	for secretName := range requirements.grantMountPaths {
		grantedSecretNames = append(grantedSecretNames, secretName)
	}
	sort.Strings(grantedSecretNames)
	secretMounts := make(map[string]string)
	for _, secretName := range grantedSecretNames {
		if _, delivered := secretValues[secretName]; !delivered {
			continue
		}
		hostPath := nodeAgent.secretMaterialStore.SecretValuePath(instanceInfo.id, secretName)
		secretMounts[ccaSecretMountPrefix+secretName] = hostPath
		if explicitMountPath, usable := resolveExplicitSecretMountPath(requirements.grantMountPaths[secretName]); usable {
			secretMounts[explicitMountPath] = hostPath
		}
	}
	if connectionError := nodeAgent.addConnectionFileMounts(ctx, instanceInfo, secretValues, secretMounts); connectionError != nil {
		return nil, connectionError
	}
	return secretMounts, nil
}

// forbiddenSecretMountPrefixes are container paths an explicit secret mount
// may not cover: kernel filesystems and the cca-secret helper directory.
var forbiddenSecretMountPrefixes = []string{"/proc", "/sys", "/dev", strings.TrimSuffix(ccaSecretMountPrefix, "/")}

// resolveExplicitSecretMountPath cleans a grant's mount path and reports
// whether it can be used: absolute and outside forbiddenSecretMountPrefixes.
func resolveExplicitSecretMountPath(mountPath string) (string, bool) {
	if !strings.HasPrefix(mountPath, "/") {
		return "", false
	}
	cleanedPath := filepath.Clean(mountPath)
	if cleanedPath == "/" {
		return "", false
	}
	for _, forbiddenPrefix := range forbiddenSecretMountPrefixes {
		if cleanedPath == forbiddenPrefix || strings.HasPrefix(cleanedPath, forbiddenPrefix+"/") {
			return "", false
		}
	}
	return cleanedPath, true
}

// addConnectionFileMounts assembles each connection URL from the delivered
// password and the (non-secret) definition, writes it to tmpfs and mounts it.
// Store and write errors are returned; an unusable definition is skipped (logged).
func (nodeAgent *Agent) addConnectionFileMounts(ctx context.Context, instanceInfo placedInstanceInfo, secretValues map[string][]byte, secretMounts map[string]string) error {
	connectionPrefix := types.ScanDesiredServiceConnections(instanceInfo.service)
	connectionRefs, scanError := nodeAgent.store.Scan(ctx, connectionPrefix)
	if scanError != nil {
		return fmt.Errorf("read connections of %s: %w", instanceInfo.service, scanError)
	}
	for _, connectionRef := range connectionRefs {
		connectionName := strings.TrimPrefix(connectionRef.Key, connectionPrefix)
		mountPath, usable := resolveExplicitSecretMountPath(string(connectionRef.Value))
		connectionURL := nodeAgent.assembleConnectionURL(ctx, connectionName, secretValues)
		if connectionName == "" || connectionURL == "" || !usable {
			continue
		}
		hostPath, writeError := nodeAgent.secretMaterialStore.WriteConnectionFile(instanceInfo.id, connectionName, connectionURL)
		if writeError != nil {
			return writeError
		}
		secretMounts[mountPath] = hostPath
	}
	return nil
}

// addSecretHelperIfMounted adds the cca-secret helper script when secrets are
// mounted under /run/cca-secrets/. The script itself is not secret.
func addSecretHelperIfMounted(secretMounts map[string]string, configFiles map[string]string) map[string]string {
	for containerPath := range secretMounts {
		if strings.HasPrefix(containerPath, ccaSecretMountPrefix) {
			if configFiles == nil {
				configFiles = make(map[string]string)
			}
			configFiles[ccaSecretHelperPath] = ccaSecretHelperScript
			return configFiles
		}
	}
	return configFiles
}

// sweepSecretMaterial removes material and wait state of instances that are
// neither placed on this node nor running here (agent restart, re-placement,
// stop). Material of running or placed instances is kept.
func (nodeAgent *Agent) sweepSecretMaterial(ctx context.Context, desiredByID map[string]bool, running []runtime.Status) {
	for instanceID := range nodeAgent.secretDeliveryWaits {
		if !desiredByID[instanceID] {
			nodeAgent.clearSecretDeliveryWait(ctx, instanceID)
		}
	}
	if nodeAgent.secretMaterialStore == nil {
		return
	}
	runningInstanceIDs := make(map[string]bool, len(running))
	for _, runtimeStatus := range running {
		if runtimeStatus.Running {
			runningInstanceIDs[runtimeStatus.ID] = true
		}
	}
	for _, instanceID := range nodeAgent.secretMaterialStore.ListInstances() {
		if !desiredByID[instanceID] && !runningInstanceIDs[instanceID] {
			nodeAgent.secretMaterialStore.RemoveInstance(instanceID)
			logging.Default().Info("removed secret material", "agent", nodeAgent.nodeID, "instance", instanceID)
		}
	}
}

// publishSecretDeliveryCapability reports that this agent uses control-plane
// delivery and whether the legacy key file is still on disk (Flow 27).
func (nodeAgent *Agent) publishSecretDeliveryCapability(ctx context.Context) {
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedNodeSecretDelivery(nodeAgent.nodeID), []byte(secretDeliveryProtocolVersion)); putError != nil {
		logging.Default().Error("failed to publish secret delivery version", "agent", nodeAgent.nodeID, "error", putError.Error())
	}
	legacyKeyFileState := "absent"
	if _, statError := os.Stat(nodeAgent.legacyKeyFilePath); statError == nil {
		legacyKeyFileState = "present"
		logging.Default().Warn("legacy secret key file is unused by this agent; remove it during the M176 migration", "agent", nodeAgent.nodeID, "path", nodeAgent.legacyKeyFilePath)
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedNodeLegacyKeyFile(nodeAgent.nodeID), []byte(legacyKeyFileState)); putError != nil {
		logging.Default().Error("failed to publish legacy key file state", "agent", nodeAgent.nodeID, "error", putError.Error())
	}
}

// readDeliveredSecretValues returns the instance's delivered material from
// tmpfs, or nil when there is none.
func (nodeAgent *Agent) readDeliveredSecretValues(instanceID string) map[string][]byte {
	if nodeAgent.secretMaterialStore == nil {
		return nil
	}
	secretValues, _ := nodeAgent.secretMaterialStore.ReadInstanceSecrets(instanceID)
	return secretValues
}
