// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
)

// maxMigrateKeysRequestBytes caps the POST /api/secret/migrate-keys body.
const maxMigrateKeysRequestBytes = 4096

// KeyringMigrationGate checks and starts the legacy key migration
// (implemented by security.KeyringEnvelopeCodec, Flow 29 step 5).
type KeyringMigrationGate interface {
	// CheckLegacyMigrationGate returns every unmet gate condition.
	CheckLegacyMigrationGate(ctx context.Context, gateRequest security.LegacyMigrationGateRequest, buildVersion string) ([]string, error)
	// StartLegacyMigration records the intent and switches writes to v2.
	StartLegacyMigration(ctx context.Context, requestedBy string) error
}

// SetKeyringMigrationGate enables POST /api/secret/migrate-keys; buildVersion
// is the version every replica report must match.
func (apiServer *Server) SetKeyringMigrationGate(keyringMigrationGate KeyringMigrationGate, buildVersion string) {
	apiServer.keyringMigrationGate = keyringMigrationGate
	apiServer.buildVersion = buildVersion
}

// migrateKeysRefusal is the 409 body: every unmet gate condition.
type migrateKeysRefusal struct {
	Error string   `json:"error"` // Error summarizes the refusal.
	Unmet []string `json:"unmet"` // Unmet lists each condition that is not satisfied.
}

// handleSecretMigrateKeys implements POST /api/secret/migrate-keys
// {"confirm_backup": true}: 409 with every unmet condition, otherwise the
// migration starts (writes become v2, the keyring controller re-encrypts).
func (apiServer *Server) handleSecretMigrateKeys(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if apiServer.keyringMigrationGate == nil {
		writeJSONError(responseWriter, "keyring not configured", http.StatusServiceUnavailable)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilitySecretWrite, security.ScopeCluster) {
		return
	}
	var gateRequest security.LegacyMigrationGateRequest
	if decodeError := json.NewDecoder(http.MaxBytesReader(responseWriter, request.Body, maxMigrateKeysRequestBytes)).Decode(&gateRequest); decodeError != nil {
		writeJSONError(responseWriter, "body must be JSON {\"confirm_backup\": true}", http.StatusBadRequest)
		return
	}
	requestContext := request.Context()
	unmetConditions, checkError := apiServer.keyringMigrationGate.CheckLegacyMigrationGate(requestContext, gateRequest, apiServer.buildVersion)
	if checkError != nil {
		writeJSONError(responseWriter, "check migration gate: "+checkError.Error(), http.StatusInternalServerError)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	if len(unmetConditions) > 0 {
		responseWriter.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(responseWriter).Encode(migrateKeysRefusal{Error: "migration gate not passed", Unmet: unmetConditions})
		return
	}
	requestedBy := security.PrincipalFromContext(requestContext)
	if startError := apiServer.keyringMigrationGate.StartLegacyMigration(requestContext, requestedBy); startError != nil {
		writeJSONError(responseWriter, startError.Error(), http.StatusConflict)
		return
	}
	logging.Default().Info("legacy key migration started", "principal", requestedBy)
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{"state": "in_progress"})
}
