// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// handler_join_token.go serves /api/node/join-token: node join tokens are
// created, listed and revoked through the API (cluster.admin, audited), so
// cca token needs no etcd access (M219).
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

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
		writeJSONError(responseWriter, "enrollment not available", http.StatusServiceUnavailable)
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
		writeJSONError(responseWriter, "invalid JSON: "+decodeError.Error(), http.StatusBadRequest)
		return
	}
	tokenTTL, durationError := time.ParseDuration(createRequest.TTL)
	if durationError != nil || tokenTTL <= 0 {
		writeJSONError(responseWriter, "ttl must be a positive duration such as \"15m\"", http.StatusBadRequest)
		return
	}
	joinToken, createError := apiServer.enrollmentService.GenerateJoinToken(request.Context(), createRequest.NodeID, tokenTTL)
	if createError != nil {
		writeJSONError(responseWriter, "create join token: "+createError.Error(), http.StatusInternalServerError)
		return
	}
	apiServer.auditJoinTokenOperation(request, "create", joinTokenDisplayID(joinToken.Token))
	_ = json.NewEncoder(responseWriter).Encode(JoinTokenCreateResponse{
		Token: joinToken.CompositeTokenString(), NodeID: joinToken.NodeID, ExpiresAt: joinToken.ExpiresAt,
	})
}

// listJoinTokens returns the active tokens by display prefix and expiry.
func (apiServer *Server) listJoinTokens(responseWriter http.ResponseWriter, request *http.Request) {
	activeTokens, listError := apiServer.enrollmentService.ListJoinTokens(request.Context())
	if listError != nil {
		writeJSONError(responseWriter, "list join tokens: "+listError.Error(), http.StatusInternalServerError)
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
		writeJSONError(responseWriter, "invalid JSON: "+decodeError.Error(), http.StatusBadRequest)
		return
	}
	tokenValue, _, _ := strings.Cut(revokeRequest.Token, ".")
	if tokenValue == "" {
		writeJSONError(responseWriter, "token required", http.StatusBadRequest)
		return
	}
	revokeError := apiServer.enrollmentService.RevokeJoinToken(request.Context(), tokenValue)
	if errors.Is(revokeError, store.ErrKeyNotFound) {
		writeJSONError(responseWriter, "no such join token", http.StatusNotFound)
		return
	}
	if revokeError != nil {
		writeJSONError(responseWriter, "revoke join token: "+revokeError.Error(), http.StatusInternalServerError)
		return
	}
	apiServer.auditJoinTokenOperation(request, "revoke", joinTokenDisplayID(tokenValue))
	_ = json.NewEncoder(responseWriter).Encode(map[string]bool{"ok": true})
}

// auditJoinTokenOperation records an allowed join token operation by the
// request's principal; tokenID is a display prefix, never a token value.
func (apiServer *Server) auditJoinTokenOperation(request *http.Request, operation string, tokenID string) {
	apiServer.apiAuthorizer.RecordAuditEntry(security.AuditEntry{
		Timestamp:  time.Now(),
		Principal:  security.PrincipalStructFromContext(request.Context()).String(),
		Action:     joinTokenAuditAction + operation,
		Target:     tokenID,
		Decision:   security.AuditDecisionAllow,
		AuthMethod: security.AuthMethodFromContext(request.Context()),
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
