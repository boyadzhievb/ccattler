// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// handler_join_token.go serves /api/node/join-token: node join tokens are
// created, listed and revoked through the API (cluster.admin, audited), so
// cca token needs no etcd access (M219).
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// maxJoinTokenTTL is the longest lifetime a join token may have; a token is
// a bootstrap credential, so long-lived ones are refused (review SEC-L2).
const maxJoinTokenTTL = 7 * 24 * time.Hour

// JoinTokenPath is the API route for node join tokens.
const JoinTokenPath = "/api/node/join-token" //nolint:gosec // URL path, not a credential

// joinTokenAuditAction is the audit action of every join token operation;
// the method is appended (create, list, revoke).
const joinTokenAuditAction = "join-token."

// joinTokenDisplayPrefixLength is how many characters of a token value an
// audit entry names; the value itself is never logged.
const joinTokenDisplayPrefixLength = 8

// JoinTokenCreateRequest is the JSON body of POST /api/node/join-token.
type JoinTokenCreateRequest struct {
	NodeID string `json:"node_id,omitempty"` // NodeID binds the token to one node; empty means any node.
	TTL    string `json:"ttl"`               // TTL is a Go duration such as "15m".
}

// JoinTokenCreateResponse is the answer to a create; the only time the token
// value leaves the server.
type JoinTokenCreateResponse struct {
	Token     string    `json:"token"`             // Token is "<value>.<ca-fingerprint>", as cca join takes it.
	NodeID    string    `json:"node_id,omitempty"` // NodeID is the node the token is bound to, if any.
	ExpiresAt time.Time `json:"expires_at"`        // ExpiresAt is when the token stops working.
}

// JoinTokenListEntry describes one active token without its value.
type JoinTokenListEntry struct {
	ID        string    `json:"id"`                // ID is the token's display prefix.
	NodeID    string    `json:"node_id,omitempty"` // NodeID is the node the token is bound to, if any.
	ExpiresAt time.Time `json:"expires_at"`        // ExpiresAt is when the token stops working.
}

// JoinTokenRevokeRequest is the JSON body of DELETE /api/node/join-token. The
// value travels in the body, never the query string, so it is not logged.
type JoinTokenRevokeRequest struct {
	Token string `json:"token"` // Token is the value, with or without ".<ca-fingerprint>".
}

// handleJoinToken serves POST (create), GET (list) and DELETE (revoke) of
// node join tokens. Every method requires cluster.admin at cluster scope.
func (apiServer *Server) handleJoinToken(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost && request.Method != http.MethodGet && request.Method != http.MethodDelete {
		writeJSONError(responseWriter, "GET, POST or DELETE only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}
	if apiServer.enrollmentService == nil {
		apiServer.failJoinTokenOperation(responseWriter, request, strings.ToLower(request.Method), "", "enrollment not available", http.StatusServiceUnavailable)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	responseWriter.Header().Set("Content-Type", "application/json")
	switch request.Method {
	case http.MethodPost:
		apiServer.createJoinToken(responseWriter, request)
	case http.MethodGet:
		apiServer.listJoinTokens(responseWriter, request)
	default:
		apiServer.revokeJoinToken(responseWriter, request)
	}
}

// createJoinToken generates a token and returns its value once; the audit
// entry names only its display prefix.
func (apiServer *Server) createJoinToken(responseWriter http.ResponseWriter, request *http.Request) {
	var createRequest JoinTokenCreateRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&createRequest); decodeError != nil {
		apiServer.failJoinTokenOperation(responseWriter, request, "create", "", "invalid JSON: "+decodeError.Error(), http.StatusBadRequest)
		return
	}
	tokenTTL, durationError := time.ParseDuration(createRequest.TTL)
	if durationError != nil || tokenTTL <= 0 || tokenTTL > maxJoinTokenTTL {
		apiServer.failJoinTokenOperation(responseWriter, request, "create", "",
			fmt.Sprintf("ttl must be a positive duration up to %s, such as \"15m\"", maxJoinTokenTTL), http.StatusBadRequest)
		return
	}
	if createRequest.NodeID != "" {
		if nodeIDError := types.ValidateNodeID(createRequest.NodeID); nodeIDError != nil {
			apiServer.failJoinTokenOperation(responseWriter, request, "create", "", "node_id: "+nodeIDError.Error(), http.StatusBadRequest)
			return
		}
	}
	joinToken, createError := apiServer.enrollmentService.GenerateJoinToken(request.Context(), createRequest.NodeID, tokenTTL)
	if createError != nil {
		apiServer.failJoinTokenOperation(responseWriter, request, "create", "", "create join token: "+createError.Error(), http.StatusInternalServerError)
		return
	}
	// The value is shown once; keep it out of caches.
	responseWriter.Header().Set("Cache-Control", "no-store")
	apiServer.auditJoinTokenOperation(request, "create", joinTokenDisplayID(joinToken.Token))
	_ = json.NewEncoder(responseWriter).Encode(JoinTokenCreateResponse{
		Token: joinToken.CompositeTokenString(), NodeID: joinToken.NodeID, ExpiresAt: joinToken.ExpiresAt,
	})
}

// listJoinTokens returns the active tokens by display prefix and expiry.
func (apiServer *Server) listJoinTokens(responseWriter http.ResponseWriter, request *http.Request) {
	activeTokens, listError := apiServer.enrollmentService.ListJoinTokens(request.Context())
	if listError != nil {
		apiServer.failJoinTokenOperation(responseWriter, request, "list", "", "list join tokens: "+listError.Error(), http.StatusInternalServerError)
		return
	}
	listEntries := make([]JoinTokenListEntry, 0, len(activeTokens))
	for _, activeToken := range activeTokens {
		listEntries = append(listEntries, JoinTokenListEntry{
			ID: joinTokenDisplayID(activeToken.Token), NodeID: activeToken.NodeID, ExpiresAt: activeToken.ExpiresAt,
		})
	}
	apiServer.auditJoinTokenOperation(request, "list", "")
	_ = json.NewEncoder(responseWriter).Encode(listEntries)
}

// revokeJoinToken deletes the token named in the body; an unknown token is 404.
func (apiServer *Server) revokeJoinToken(responseWriter http.ResponseWriter, request *http.Request) {
	var revokeRequest JoinTokenRevokeRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&revokeRequest); decodeError != nil {
		apiServer.failJoinTokenOperation(responseWriter, request, "revoke", "", "invalid JSON: "+decodeError.Error(), http.StatusBadRequest)
		return
	}
	tokenValue, _, _ := strings.Cut(revokeRequest.Token, ".")
	if tokenValue == "" {
		apiServer.failJoinTokenOperation(responseWriter, request, "revoke", "", "token required", http.StatusBadRequest)
		return
	}
	revokeError := apiServer.enrollmentService.RevokeJoinToken(request.Context(), tokenValue)
	if errors.Is(revokeError, store.ErrKeyNotFound) {
		// Audited: repeated 404s are a guessing probe.
		apiServer.failJoinTokenOperation(responseWriter, request, "revoke", joinTokenDisplayID(tokenValue), "no such join token", http.StatusNotFound)
		return
	}
	if revokeError != nil {
		apiServer.failJoinTokenOperation(responseWriter, request, "revoke", joinTokenDisplayID(tokenValue), "revoke join token: "+revokeError.Error(), http.StatusInternalServerError)
		return
	}
	apiServer.auditJoinTokenOperation(request, "revoke", joinTokenDisplayID(tokenValue))
	_ = json.NewEncoder(responseWriter).Encode(map[string]bool{"ok": true})
}

// auditJoinTokenOperation records an allowed join token operation by the
// request's principal; tokenID is a display prefix, never a token value.
func (apiServer *Server) auditJoinTokenOperation(request *http.Request, operation string, tokenID string) {
	apiServer.recordJoinTokenAudit(request, operation, tokenID, security.AuditDecisionAllow, "")
}

// failJoinTokenOperation answers an error and audits it as a denied
// operation with the HTTP status as outcome (review SEC-L3).
func (apiServer *Server) failJoinTokenOperation(responseWriter http.ResponseWriter, request *http.Request, operation string, tokenID string, message string, statusCode int) {
	apiServer.recordJoinTokenAudit(request, operation, tokenID, security.AuditDecisionDeny, fmt.Sprintf("http_%d", statusCode))
	writeJSONError(responseWriter, message, statusCode)
}

// recordJoinTokenAudit writes one join-token audit entry; outcome goes to
// the entry's Policy field (empty for success).
func (apiServer *Server) recordJoinTokenAudit(request *http.Request, operation string, tokenID string, decision string, outcome string) {
	apiServer.apiAuthorizer.RecordAuditEntry(security.AuditEntry{
		Timestamp:  time.Now(),
		Principal:  security.PrincipalStructFromContext(request.Context()).String(),
		Action:     joinTokenAuditAction + operation,
		Target:     tokenID,
		Decision:   decision,
		AuthMethod: security.AuthMethodFromContext(request.Context()),
		Policy:     outcome,
	})
}

// joinTokenDisplayID returns the first characters of a token value (or of a
// stored display prefix), which identify a token without revealing it.
func joinTokenDisplayID(tokenValue string) string {
	if len(tokenValue) > joinTokenDisplayPrefixLength {
		return tokenValue[:joinTokenDisplayPrefixLength]
	}
	return tokenValue
}
