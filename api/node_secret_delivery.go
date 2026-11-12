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

// NodeSecretDeliveryPath is the endpoint agents call to receive the secrets of
// an instance placed on them (Flow 27).
const NodeSecretDeliveryPath = "/api/node/secrets"

// NodeSecretDeliveryRequest is the body of POST /api/node/secrets.
type NodeSecretDeliveryRequest struct {
	InstanceID string `json:"instance_id"` // InstanceID is the instance whose secrets are requested.
}

// NodeSecretDeliveryRefusalResponse is the body of a refused delivery. It
// names the failed secrets, never their values.
type NodeSecretDeliveryRefusalResponse struct {
	Error   string   `json:"error"`             // Error is the human-readable refusal.
	Reason  string   `json:"reason"`            // Reason is the typed refusal reason (not_authorized, secret_missing).
	Secrets []string `json:"secrets,omitempty"` // Secrets lists the secret names that failed.
}

// SetSecretDeliveryService enables POST /api/node/secrets.
func (apiServer *Server) SetSecretDeliveryService(deliveryService *security.SecretDeliveryService) {
	apiServer.secretDeliveryService = deliveryService
}

// handleNodeSecretDelivery serves POST /api/node/secrets. Only node principals
// authenticated by mTLS are accepted; users and tokens are refused. The node
// ID comes from the certificate, never from the request body.
func (apiServer *Server) handleNodeSecretDelivery(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	callerPrincipal, isNodeCaller := requireNodeMTLSCaller(responseWriter, request)
	if !isNodeCaller {
		return
	}
	if apiServer.secretDeliveryService == nil {
		writeJSONError(responseWriter, "secret delivery not configured", http.StatusServiceUnavailable)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	var deliveryRequest NodeSecretDeliveryRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&deliveryRequest); decodeError != nil || deliveryRequest.InstanceID == "" {
		writeJSONError(responseWriter, "instance_id required", http.StatusBadRequest)
		return
	}
	delivered, deliveryError := apiServer.secretDeliveryService.DeliverInstanceSecrets(request.Context(), callerPrincipal.Name, deliveryRequest.InstanceID)
	if deliveryError != nil {
		writeSecretDeliveryRefusal(responseWriter, callerPrincipal.Name, "instance/"+deliveryRequest.InstanceID, deliveryError)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(responseWriter).Encode(delivered)
}

// requireNodeMTLSCaller accepts only node principals authenticated by mTLS
// (users and tokens are refused with 403). The node ID is the certificate's
// principal name, never request input. Shared by Flows 27 and 28.
func requireNodeMTLSCaller(responseWriter http.ResponseWriter, request *http.Request) (security.Principal, bool) {
	callerPrincipal := security.PrincipalStructFromContext(request.Context())
	if callerPrincipal.Kind != security.PrincipalKindNode || security.AuthMethodFromContext(request.Context()) != "mtls" || request.TLS == nil {
		writeJSONError(responseWriter, "forbidden: node mTLS certificate required", http.StatusForbidden)
		return security.Principal{}, false
	}
	return callerPrincipal, true
}

// writeSecretDeliveryRefusal maps a refusal to 403 (not authorized) or 409
// (granted secret missing); any other error is a 500 without detail.
// deliveryTarget names what was requested (instance/{id} or gates) for the log.
func writeSecretDeliveryRefusal(responseWriter http.ResponseWriter, nodeID string, deliveryTarget string, deliveryError error) {
	var refusal *security.SecretDeliveryRefusal
	if !errors.As(deliveryError, &refusal) {
		logging.Default().Error("delivery failed", "node", nodeID, "target", deliveryTarget, "error", deliveryError.Error())
		writeJSONError(responseWriter, "delivery failed", http.StatusInternalServerError)
		return
	}
	statusCode := http.StatusForbidden
	if refusal.Reason == security.SecretDeliverySecretMissing {
		statusCode = http.StatusConflict
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(NodeSecretDeliveryRefusalResponse{
		Error:   refusal.Error(),
		Reason:  string(refusal.Reason),
		Secrets: refusal.SecretNames,
	})
}
