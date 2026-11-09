// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"

	"github.com/boyadzhievb/ccattler/security"
)

// NodeGateKeyDeliveryPath is the endpoint agents running the gate proxy call
// to receive every gate's certificate and private key (Flow 28).
const NodeGateKeyDeliveryPath = "/api/node/gate-keys"

// SetGateKeyDeliveryService enables POST /api/node/gate-keys.
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
	if apiServer.gateKeyDeliveryService == nil {
		writeJSONError(responseWriter, "gate-key delivery not configured", http.StatusServiceUnavailable)
		return
	}
	callerPrincipal, isNodeCaller := requireNodeMTLSCaller(responseWriter, request)
	if !isNodeCaller {
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
