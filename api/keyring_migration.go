// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
)

// maxMigrateKeysRequestBytes caps the POST /api/secret/migrate-keys body.
const maxMigrateKeysRequestBytes = 4096

// KeyringAdministration checks and runs the legacy key migration and
// retirement (implemented by security.KeyringEnvelopeCodec, Flow 29 steps 5 and 7).
type KeyringAdministration interface {
	// CheckLegacyMigrationGate returns every unmet migration gate condition.
	CheckLegacyMigrationGate(ctx context.Context, gateRequest security.LegacyMigrationGateRequest, buildVersion string) ([]string, error)
	// StartLegacyMigration records the intent and switches writes to v2.
	StartLegacyMigration(ctx context.Context, requestedBy string) error
	// CheckLegacyRetirementGate returns every unmet retirement gate condition.
	CheckLegacyRetirementGate(ctx context.Context) ([]string, error)
	// RetireLegacyKey retires the legacy key; v1 envelopes are refused afterwards.
	RetireLegacyKey(ctx context.Context) error
	// CheckKeyringRotationGate returns every unmet KEK rotation condition (Flow 30).
	CheckKeyringRotationGate(ctx context.Context, buildVersion string) ([]string, error)
	// StartKeyringRotation records the rotation intent; returns the target KEK version.
	StartKeyringRotation(ctx context.Context, requestedBy string) (int, error)
}

// SetKeyringAdministration enables POST /api/secret/migrate-keys and
// /api/secret/retire-legacy-key; buildVersion is the version every replica
// report must match.
func (apiServer *Server) SetKeyringAdministration(keyringAdministration KeyringAdministration, buildVersion string) {
	apiServer.keyringAdministration = keyringAdministration
	apiServer.buildVersion = buildVersion
}

// keyringGateRefusal is the 409 body: every unmet gate condition.
type keyringGateRefusal struct {
	Error string   `json:"error"` // Error summarizes the refusal.
	Unmet []string `json:"unmet"` // Unmet lists each condition that is not satisfied.
}

// writeKeyringGateRefusal answers 409 with every unmet gate condition.
func writeKeyringGateRefusal(responseWriter http.ResponseWriter, gateName string, unmetConditions []string) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(responseWriter).Encode(keyringGateRefusal{Error: gateName + " gate not passed", Unmet: unmetConditions})
}

// beginKeyringAdministration checks method, configuration and capability
// shared by the legacy key endpoints. Both are irreversible, so they require
// cluster.admin at cluster scope rather than secret.write (security finding F9).
func (apiServer *Server) beginKeyringAdministration(responseWriter http.ResponseWriter, request *http.Request) bool {
	return apiServer.beginKeyringRequest(responseWriter, request, security.CapabilityClusterAdmin)
}

// beginKeyringRequest checks the POST method, that the keyring is configured
// and that the caller holds requiredCapability at cluster scope.
func (apiServer *Server) beginKeyringRequest(responseWriter http.ResponseWriter, request *http.Request, requiredCapability security.Capability) bool {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return false
	}
	if apiServer.keyringAdministration == nil {
		writeJSONError(responseWriter, "keyring not configured", http.StatusServiceUnavailable)
		return false
	}
	return apiServer.requireCapability(responseWriter, request, requiredCapability, security.ScopeCluster)
}

// handleSecretRotate implements POST /api/secret/rotate (Flow 30 step 1): 409
// with every unmet condition, otherwise intent/keyring/rotation is recorded and
// the keyring-rotation controller does the work. Rotation destroys nothing, so
// secret.write at cluster scope suffices.
func (apiServer *Server) handleSecretRotate(responseWriter http.ResponseWriter, request *http.Request) {
	if !apiServer.beginKeyringRequest(responseWriter, request, security.CapabilitySecretWrite) {
		return
	}
	requestContext := request.Context()
	unmetConditions, checkError := apiServer.keyringAdministration.CheckKeyringRotationGate(requestContext, apiServer.buildVersion)
	if checkError != nil {
		writeJSONError(responseWriter, "check rotation gate: "+checkError.Error(), http.StatusInternalServerError)
		return
	}
	if len(unmetConditions) > 0 {
		writeKeyringGateRefusal(responseWriter, "rotation", unmetConditions)
		return
	}
	requestedBy := security.PrincipalFromContext(requestContext)
	targetVersion, startError := apiServer.keyringAdministration.StartKeyringRotation(requestContext, requestedBy)
	if startError != nil {
		writeJSONError(responseWriter, startError.Error(), http.StatusConflict)
		return
	}
	logging.Default().Info("KEK rotation started", "principal", requestedBy, "target_version", strconv.Itoa(targetVersion))
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]any{"state": security.KeyringRotationInProgress, "target_version": targetVersion})
}

// handleSecretRetireLegacyKey implements POST /api/secret/retire-legacy-key:
// 409 with every unmet retirement condition, otherwise the legacy key is
// retired (irreversible: v1 envelopes are refused from then on).
func (apiServer *Server) handleSecretRetireLegacyKey(responseWriter http.ResponseWriter, request *http.Request) {
	if !apiServer.beginKeyringAdministration(responseWriter, request) {
		return
	}
	requestContext := request.Context()
	unmetConditions, checkError := apiServer.keyringAdministration.CheckLegacyRetirementGate(requestContext)
	if checkError != nil {
		writeJSONError(responseWriter, "check retirement gate: "+checkError.Error(), http.StatusInternalServerError)
		return
	}
	if len(unmetConditions) > 0 {
		writeKeyringGateRefusal(responseWriter, "retirement", unmetConditions)
		return
	}
	if retireError := apiServer.keyringAdministration.RetireLegacyKey(requestContext); retireError != nil {
		writeJSONError(responseWriter, retireError.Error(), http.StatusConflict)
		return
	}
	logging.Default().Info("legacy secret key retired", "principal", security.PrincipalFromContext(requestContext))
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{"legacy": string(security.LegacyKeyRetired)})
}

// handleSecretMigrateKeys implements POST /api/secret/migrate-keys
// {"confirm_backup": true}: 409 with every unmet condition, otherwise the
// migration starts (writes become v2, the keyring controller re-encrypts).
func (apiServer *Server) handleSecretMigrateKeys(responseWriter http.ResponseWriter, request *http.Request) {
	if !apiServer.beginKeyringAdministration(responseWriter, request) {
		return
	}
	var gateRequest security.LegacyMigrationGateRequest
	if decodeError := json.NewDecoder(http.MaxBytesReader(responseWriter, request.Body, maxMigrateKeysRequestBytes)).Decode(&gateRequest); decodeError != nil {
		writeJSONError(responseWriter, "body must be JSON {\"confirm_backup\": true}", http.StatusBadRequest)
		return
	}
	requestContext := request.Context()
	unmetConditions, checkError := apiServer.keyringAdministration.CheckLegacyMigrationGate(requestContext, gateRequest, apiServer.buildVersion)
	if checkError != nil {
		writeJSONError(responseWriter, "check migration gate: "+checkError.Error(), http.StatusInternalServerError)
		return
	}
	if len(unmetConditions) > 0 {
		writeKeyringGateRefusal(responseWriter, "migration", unmetConditions)
		return
	}
	requestedBy := security.PrincipalFromContext(requestContext)
	if startError := apiServer.keyringAdministration.StartLegacyMigration(requestContext, requestedBy); startError != nil {
		writeJSONError(responseWriter, startError.Error(), http.StatusConflict)
		return
	}
	logging.Default().Info("legacy key migration started", "principal", requestedBy)
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{"state": string(security.KeyringMigrationInProgress)})
}
