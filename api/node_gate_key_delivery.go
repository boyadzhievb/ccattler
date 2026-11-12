// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
)

// NodeGateKeyDeliveryPath is the endpoint agents running the gate proxy call
// to receive every gate's certificate and private key (Flow 28).
const NodeGateKeyDeliveryPath = "/api/node/gate-keys"

// SetGateKeyDeliveryService enables POST /api/node/gate-keys and PUT /api/gate-key.
func (apiServer *Server) SetGateKeyDeliveryService(gateKeyDeliveryService *security.GateKeyDeliveryService) {
	apiServer.gateKeyDeliveryService = gateKeyDeliveryService
}

// handleNodeGateKeyDelivery serves POST /api/node/gate-keys (no body). Only
// node principals authenticated by mTLS are accepted; the node ID comes from
// the certificate. Refusals are 403, store failures 500.
func (apiServer *Server) handleNodeGateKeyDelivery(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	callerPrincipal, isNodeCaller := requireNodeMTLSCaller(responseWriter, request)
	if !isNodeCaller {
		return
	}
	if apiServer.gateKeyDeliveryService == nil {
		writeJSONError(responseWriter, "gate-key delivery not configured", http.StatusServiceUnavailable)
		return
	}
	delivered, deliveryError := apiServer.gateKeyDeliveryService.DeliverGateKeys(request.Context(), callerPrincipal.Name)
	if deliveryError != nil {
		writeSecretDeliveryRefusal(responseWriter, callerPrincipal.Name, "gates", deliveryError)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(responseWriter).Encode(delivered)
}

// GateKeyPath is the endpoint cca apply calls to seal an inline gate private
// key after writing the gate facts (Flow 28 step 2).
const GateKeyPath = "/api/gate-key"

// GateKeyRequest is the body of PUT /api/gate-key.
type GateKeyRequest struct {
	Gate          string `json:"gate"`            // Gate is the gate name.
	PrivateKeyPEM string `json:"private_key_pem"` // PrivateKeyPEM is the inline key, matching desired/route/{gate}/tls/cert.
}

// handleGateKey serves PUT /api/gate-key: requires secret.write at cluster
// scope, then seals the key at secrets/route/{gate}/tls/key. A rejected key
// (gate not manual TLS, key secret in use, mismatch) is 409; store errors 500.
func (apiServer *Server) handleGateKey(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		writeJSONError(responseWriter, "PUT only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilitySecretWrite, security.ScopeCluster) {
		return
	}
	if apiServer.gateKeyDeliveryService == nil {
		writeJSONError(responseWriter, "gate-key store not configured", http.StatusServiceUnavailable)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	var gateKeyRequest GateKeyRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&gateKeyRequest); decodeError != nil || gateKeyRequest.Gate == "" || gateKeyRequest.PrivateKeyPEM == "" {
		writeJSONError(responseWriter, "gate and private_key_pem required", http.StatusBadRequest)
		return
	}
	principal := security.PrincipalStructFromContext(request.Context())
	storeError := apiServer.gateKeyDeliveryService.StoreInlineGateKey(request.Context(), principal.String(), gateKeyRequest.Gate, []byte(gateKeyRequest.PrivateKeyPEM))
	var rejection *security.GateKeyRejection
	switch {
	case errors.As(storeError, &rejection):
		writeJSONError(responseWriter, rejection.Error(), http.StatusConflict)
	case storeError != nil:
		logging.Default().Error("gate key store failed", "gate", gateKeyRequest.Gate, "error", storeError.Error())
		writeJSONError(responseWriter, "gate key store failed", http.StatusInternalServerError)
	default:
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"ok": "true", "gate": gateKeyRequest.Gate})
	}
}
