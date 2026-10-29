// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// roleGrantRequestBody is the JSON body for role grant and revoke operations.
type roleGrantRequestBody struct {
	Principal  string `json:"principal"`
	Role       string `json:"role"`
	Capability string `json:"capability"`
	Scope      string `json:"scope"`
}

// roleListResponseEntry represents a single role binding in the list response.
type roleListResponseEntry struct {
	Principal    string   `json:"principal"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Scope        string   `json:"scope"`
}

// handleRole dispatches /api/role requests. POST creates a grant, GET lists
// grants, DELETE revokes a grant.
func (apiServer *Server) handleRole(responseWriter http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost:
		apiServer.handleRoleGrant(responseWriter, request)
	case http.MethodGet:
		apiServer.handleRoleList(responseWriter, request)
	case http.MethodDelete:
		apiServer.handleRoleRevoke(responseWriter, request)
	default:
		writeJSONError(responseWriter, "GET, POST, or DELETE", http.StatusMethodNotAllowed)
	}
}

// handleRoleGrant writes auth/ facts to grant a role or direct capability to a
// principal. When --role is provided, it writes an auth/grant binding. When
// --capability is provided, it creates an inline role with the single
// capability and binds it to the principal.
func (apiServer *Server) handleRoleGrant(responseWriter http.ResponseWriter, request *http.Request) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}

	requestBody, parseError := parseRoleRequestBody(responseWriter, request)
	if parseError {
		return
	}

	principalKind, principalName, splitError := splitPrincipalString(requestBody.Principal)
	if splitError {
		writeJSONError(responseWriter, "invalid principal format — use kind:name (e.g. user:alice)", http.StatusBadRequest)
		return
	}

	requestContext := request.Context()

	if requestBody.Role != "" {
		grantKey := types.KeyAuthGrant(principalKind, principalName, requestBody.Role)
		if _, putError := apiServer.factStore.Put(requestContext, grantKey, []byte("true")); putError != nil {
			writeJSONError(responseWriter, "store write failed: "+putError.Error(), http.StatusInternalServerError)
			return
		}
		responseWriter.WriteHeader(http.StatusOK)
		return
	}

	if requestBody.Capability != "" {
		if !isValidCapability(requestBody.Capability) {
			writeJSONError(responseWriter, "unknown capability: "+requestBody.Capability, http.StatusBadRequest)
			return
		}

		scopePath := requestBody.Scope
		if scopePath == "" {
			scopePath = "cluster"
		}

		inlineRoleName := "inline-" + principalKind + "-" + principalName
		capabilityKey := types.KeyAuthRoleCapability(inlineRoleName, requestBody.Capability)
		scopeKey := types.KeyAuthRoleScope(inlineRoleName, scopePath)
		grantKey := types.KeyAuthGrant(principalKind, principalName, inlineRoleName)

		if _, putError := apiServer.factStore.Put(requestContext, capabilityKey, []byte("true")); putError != nil {
			writeJSONError(responseWriter, "store write failed: "+putError.Error(), http.StatusInternalServerError)
			return
		}
		if _, putError := apiServer.factStore.Put(requestContext, scopeKey, []byte("true")); putError != nil {
			writeJSONError(responseWriter, "store write failed: "+putError.Error(), http.StatusInternalServerError)
			return
		}
		if _, putError := apiServer.factStore.Put(requestContext, grantKey, []byte("true")); putError != nil {
			writeJSONError(responseWriter, "store write failed: "+putError.Error(), http.StatusInternalServerError)
			return
		}
		responseWriter.WriteHeader(http.StatusOK)
		return
	}

	writeJSONError(responseWriter, "either role or capability is required", http.StatusBadRequest)
}

// handleRoleRevoke deletes auth/ facts to remove a role or capability grant.
func (apiServer *Server) handleRoleRevoke(responseWriter http.ResponseWriter, request *http.Request) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}

	requestBody, parseError := parseRoleRequestBody(responseWriter, request)
	if parseError {
		return
	}

	principalKind, principalName, splitError := splitPrincipalString(requestBody.Principal)
	if splitError {
		writeJSONError(responseWriter, "invalid principal format — use kind:name (e.g. user:alice)", http.StatusBadRequest)
		return
	}

	requestContext := request.Context()

	if requestBody.Role != "" {
		grantKey := types.KeyAuthGrant(principalKind, principalName, requestBody.Role)
		if deleteError := apiServer.factStore.Delete(requestContext, grantKey); deleteError != nil {
			writeJSONError(responseWriter, "store delete failed: "+deleteError.Error(), http.StatusInternalServerError)
			return
		}
		responseWriter.WriteHeader(http.StatusOK)
		return
	}

	if requestBody.Capability != "" {
		inlineRoleName := "inline-" + principalKind + "-" + principalName
		capabilityKey := types.KeyAuthRoleCapability(inlineRoleName, requestBody.Capability)
		if deleteError := apiServer.factStore.Delete(requestContext, capabilityKey); deleteError != nil {
			writeJSONError(responseWriter, "store delete failed: "+deleteError.Error(), http.StatusInternalServerError)
			return
		}
		responseWriter.WriteHeader(http.StatusOK)
		return
	}

	writeJSONError(responseWriter, "either role or capability is required", http.StatusBadRequest)
}

// handleRoleList returns all role grants from the auth/ fact tree. Optionally
// filters by principal query parameter.
func (apiServer *Server) handleRoleList(responseWriter http.ResponseWriter, request *http.Request) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}

	principalFilter := request.URL.Query().Get("principal")
	requestContext := request.Context()

	grantFacts, scanError := apiServer.factStore.Scan(requestContext, types.ScanAuthGrants)
	if scanError != nil {
		writeJSONError(responseWriter, "store scan failed: "+scanError.Error(), http.StatusInternalServerError)
		return
	}

	roleFacts, scanError := apiServer.factStore.Scan(requestContext, types.ScanAuthRoles)
	if scanError != nil {
		writeJSONError(responseWriter, "store scan failed: "+scanError.Error(), http.StatusInternalServerError)
		return
	}

	roleCapabilities := buildRoleCapabilityMap(roleFacts)
	roleScopeMap := buildRoleScopeMap(roleFacts)

	var entries []roleListResponseEntry
	for _, grantFact := range grantFacts {
		relativePath := strings.TrimPrefix(grantFact.Key, types.ScanAuthGrants)
		pathParts := strings.SplitN(relativePath, "/", 4)
		if len(pathParts) != 4 || pathParts[2] != "role" {
			continue
		}

		principalKind := pathParts[0]
		principalName := pathParts[1]
		roleName := pathParts[3]
		principalString := principalKind + ":" + principalName

		if principalFilter != "" && principalString != principalFilter {
			continue
		}

		capabilities := roleCapabilities[roleName]
		capabilityStrings := make([]string, len(capabilities))
		for capIndex, capability := range capabilities {
			capabilityStrings[capIndex] = string(capability)
		}

		scopeDisplay := ""
		if scopes, hasScopes := roleScopeMap[roleName]; hasScopes && len(scopes) > 0 {
			scopeStrings := make([]string, len(scopes))
			for scopeIndex, scope := range scopes {
				scopeStrings[scopeIndex] = string(scope)
			}
			scopeDisplay = strings.Join(scopeStrings, ",")
		}

		entries = append(entries, roleListResponseEntry{
			Principal:    principalString,
			Role:         roleName,
			Capabilities: capabilityStrings,
			Scope:        scopeDisplay,
		})
	}

	sort.Slice(entries, func(first, second int) bool {
		return entries[first].Principal < entries[second].Principal
	})

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
		"grants": entries,
	})
}

// handleRoleCheck evaluates whether a principal currently has a capability at a
// scope. Uses the live APIAuthorizer (rebuilt by the auth controller).
func (apiServer *Server) handleRoleCheck(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}

	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}

	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	bodyBytes, readError := io.ReadAll(request.Body)
	if readError != nil {
		writeJSONError(responseWriter, "cannot read request body", http.StatusBadRequest)
		return
	}

	var checkRequest struct {
		Principal  string `json:"principal"`
		Capability string `json:"capability"`
		Scope      string `json:"scope"`
	}
	if unmarshalError := json.Unmarshal(bodyBytes, &checkRequest); unmarshalError != nil {
		writeJSONError(responseWriter, "invalid JSON", http.StatusBadRequest)
		return
	}

	if checkRequest.Principal == "" || checkRequest.Capability == "" {
		writeJSONError(responseWriter, "principal and capability are required", http.StatusBadRequest)
		return
	}

	principalKind, principalName, splitError := splitPrincipalString(checkRequest.Principal)
	if splitError {
		writeJSONError(responseWriter, "invalid principal format — use kind:name", http.StatusBadRequest)
		return
	}

	checkScope := security.Scope(checkRequest.Scope)
	if checkScope == "" {
		checkScope = security.ScopeCluster
	}

	testPrincipal := security.Principal{
		Kind: security.PrincipalKind(principalKind),
		Name: principalName,
	}

	authError := apiServer.apiAuthorizer.AuthorizeAPI(testPrincipal, security.Capability(checkRequest.Capability), checkScope, nil)

	responseWriter.Header().Set("Content-Type", "application/json")
	if authError == nil {
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
			"allowed": true,
		})
	} else {
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
			"allowed": false,
			"reason":  authError.Error(),
		})
	}
}

// parseRoleRequestBody reads and validates the role grant/revoke request body.
// Returns true if an error was written to the response.
func parseRoleRequestBody(responseWriter http.ResponseWriter, request *http.Request) (roleGrantRequestBody, bool) {
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	bodyBytes, readError := io.ReadAll(request.Body)
	if readError != nil {
		writeJSONError(responseWriter, "cannot read request body", http.StatusBadRequest)
		return roleGrantRequestBody{}, true
	}

	var requestBody roleGrantRequestBody
	if unmarshalError := json.Unmarshal(bodyBytes, &requestBody); unmarshalError != nil {
		writeJSONError(responseWriter, "invalid JSON", http.StatusBadRequest)
		return roleGrantRequestBody{}, true
	}

	if requestBody.Principal == "" {
		writeJSONError(responseWriter, "principal is required", http.StatusBadRequest)
		return roleGrantRequestBody{}, true
	}

	return requestBody, false
}

// splitPrincipalString splits "kind:name" into its components. Returns true on
// error (missing colon or empty parts).
func splitPrincipalString(principalString string) (string, string, bool) {
	colonIndex := strings.Index(principalString, ":")
	if colonIndex < 1 || colonIndex == len(principalString)-1 {
		return "", "", true
	}
	return principalString[:colonIndex], principalString[colonIndex+1:], false
}

// isValidCapability checks whether a capability name is in the AllCapabilities list.
func isValidCapability(capabilityName string) bool {
	for _, validCapability := range security.AllCapabilities {
		if string(validCapability) == capabilityName {
			return true
		}
	}
	return false
}

// buildRoleCapabilityMap extracts role→capabilities from auth/role/ facts.
func buildRoleCapabilityMap(roleFacts []store.Fact) map[string][]security.Capability {
	roleCapabilities := make(map[string][]security.Capability)
	for _, fact := range roleFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanAuthRoles)
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) == 3 && pathParts[1] == "capability" {
			roleName := pathParts[0]
			capabilityName := pathParts[2]
			roleCapabilities[roleName] = append(roleCapabilities[roleName], security.Capability(capabilityName))
		}
	}
	return roleCapabilities
}

// buildRoleScopeMap extracts role→scopes from auth/role/ facts.
func buildRoleScopeMap(roleFacts []store.Fact) map[string][]security.Scope {
	roleScopes := make(map[string][]security.Scope)
	for _, fact := range roleFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanAuthRoles)
		pathParts := strings.SplitN(relativePath, "/", 3)
		if len(pathParts) >= 3 && pathParts[1] == "scope" {
			roleName := pathParts[0]
			scopePath := pathParts[2]
			roleScopes[roleName] = append(roleScopes[roleName], security.Scope(scopePath))
		}
	}
	return roleScopes
}
