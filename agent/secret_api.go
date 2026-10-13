// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

// defaultSecretAPIReadTimeout is the HTTP read timeout for the secret API.
const defaultSecretAPIReadTimeout = 10 * time.Second

// defaultSecretAPIWriteTimeout is the HTTP write timeout for the secret API.
const defaultSecretAPIWriteTimeout = 10 * time.Second

// secretTokenLength is the byte length of generated bearer tokens (32 bytes →
// 64 hex characters). Tokens are compared in constant time.
const secretTokenLength = 32

// SecretAPIServer serves per-instance secret retrieval over HTTP. Containers
// fetch secrets at runtime from the local agent, authenticated by per-instance
// bearer tokens. No files on host disk, no bind-mounts, no permission issues.
//
// Endpoints:
//   - GET /v1/secrets/{name} — returns a single secret value (JSON)
//   - GET /v1/secrets        — lists granted secret names for the caller's service
type SecretAPIServer struct {
	nodeID         string                   // nodeID identifies this node in log messages.
	secretProvider SecretProvider           // secretProvider retrieves decrypted secrets.
	factStore      store.StateStore         // factStore is used to scan secret grants for listing.
	httpServer     *http.Server             // httpServer is the underlying HTTP server.
	tokenMutex     sync.RWMutex             // tokenMutex protects the instanceTokens map.
	instanceTokens map[string]tokenMetadata // instanceTokens maps hex-encoded bearer token → instance metadata.
}

// tokenMetadata stores the instance and service associated with a bearer token.
type tokenMetadata struct {
	instanceID  string // instanceID is the instance this token was issued for.
	serviceName string // serviceName is the service this instance belongs to.
}

// NewSecretAPIServer creates a SecretAPIServer bound to the given address.
// The server is not started until Start is called.
func NewSecretAPIServer(nodeID string, listenAddress string, secretProvider SecretProvider, factStore store.StateStore) *SecretAPIServer {
	secretAPIServer := &SecretAPIServer{
		nodeID:         nodeID,
		secretProvider: secretProvider,
		factStore:      factStore,
		instanceTokens: make(map[string]tokenMetadata),
	}

	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/v1/secrets/", secretAPIServer.handleGetSecret)
	serveMux.HandleFunc("/v1/secrets", secretAPIServer.handleListSecrets)

	secretAPIServer.httpServer = &http.Server{
		Addr:         listenAddress,
		Handler:      serveMux,
		ReadTimeout:  defaultSecretAPIReadTimeout,
		WriteTimeout: defaultSecretAPIWriteTimeout,
	}

	return secretAPIServer
}

// Start begins serving the secret API. It blocks until the server is shut down
// or the context is cancelled.
func (secretAPIServer *SecretAPIServer) Start(ctx context.Context) error {
	listener, listenError := net.Listen("tcp", secretAPIServer.httpServer.Addr)
	if listenError != nil {
		return listenError
	}

	go func() { //nolint:gosec // G118: shutdown goroutine needs context.Background — parent ctx is already done
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = secretAPIServer.httpServer.Shutdown(shutdownCtx)
	}()

	logging.Default().Info("secret API listening", "node", secretAPIServer.nodeID, "address", secretAPIServer.httpServer.Addr)
	if serveError := secretAPIServer.httpServer.Serve(listener); serveError != nil && serveError != http.ErrServerClosed {
		return serveError
	}
	return nil
}

// RegisterInstanceToken generates a cryptographically random bearer token for
// the given instance and service. Returns the hex-encoded token string that
// should be injected as CCA_SECRET_TOKEN into the container's environment.
func (secretAPIServer *SecretAPIServer) RegisterInstanceToken(instanceID string, serviceName string) (string, error) {
	tokenBytes := make([]byte, secretTokenLength)
	if _, readError := rand.Read(tokenBytes); readError != nil {
		return "", fmt.Errorf("generate secret token: %w", readError)
	}

	tokenHex := hex.EncodeToString(tokenBytes)

	secretAPIServer.tokenMutex.Lock()
	secretAPIServer.instanceTokens[tokenHex] = tokenMetadata{
		instanceID:  instanceID,
		serviceName: serviceName,
	}
	secretAPIServer.tokenMutex.Unlock()

	return tokenHex, nil
}

// RevokeInstanceToken removes the bearer token associated with an instance.
// Called when the instance stops or cleanup occurs.
func (secretAPIServer *SecretAPIServer) RevokeInstanceToken(instanceID string) {
	secretAPIServer.tokenMutex.Lock()
	defer secretAPIServer.tokenMutex.Unlock()

	for tokenHex, metadata := range secretAPIServer.instanceTokens {
		if metadata.instanceID == instanceID {
			delete(secretAPIServer.instanceTokens, tokenHex)
			return
		}
	}
}

// authenticateRequest extracts the bearer token from the Authorization header
// and looks up the associated instance and service. Returns the token metadata
// or writes an HTTP 401 response.
func (secretAPIServer *SecretAPIServer) authenticateRequest(responseWriter http.ResponseWriter, request *http.Request) (tokenMetadata, bool) {
	authorizationHeader := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorizationHeader, "Bearer ") {
		writeDebugJSONError(responseWriter, "missing or invalid Authorization header", http.StatusUnauthorized)
		return tokenMetadata{}, false
	}

	presentedToken := strings.TrimPrefix(authorizationHeader, "Bearer ")

	secretAPIServer.tokenMutex.RLock()
	defer secretAPIServer.tokenMutex.RUnlock()

	for storedToken, metadata := range secretAPIServer.instanceTokens {
		if subtle.ConstantTimeCompare([]byte(presentedToken), []byte(storedToken)) == 1 {
			return metadata, true
		}
	}

	writeDebugJSONError(responseWriter, "invalid token", http.StatusUnauthorized)
	return tokenMetadata{}, false
}

// handleGetSecret serves GET /v1/secrets/{name}. Authenticates the caller,
// checks the service grant, and returns the decrypted secret value. With
// ?raw=true the response is plain text (for curl in shell scripts); otherwise
// the response is JSON {"name","value"}.
func (secretAPIServer *SecretAPIServer) handleGetSecret(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	callerMetadata, authenticated := secretAPIServer.authenticateRequest(responseWriter, request)
	if !authenticated {
		return
	}

	secretName := strings.TrimPrefix(request.URL.Path, "/v1/secrets/")
	if secretName == "" {
		writeDebugJSONError(responseWriter, "missing secret name", http.StatusBadRequest)
		return
	}

	plaintext, _, retrievalError := secretAPIServer.secretProvider.GetSecretForService(request.Context(), callerMetadata.serviceName, secretName)
	if retrievalError != nil {
		if strings.Contains(retrievalError.Error(), "no grant") {
			writeDebugJSONError(responseWriter, "forbidden", http.StatusForbidden)
			return
		}
		logging.Default().Error("secret retrieval failed", "node", secretAPIServer.nodeID, "secret", secretName, "service", callerMetadata.serviceName, "error", retrievalError.Error())
		writeDebugJSONError(responseWriter, "internal error", http.StatusInternalServerError)
		return
	}

	if request.URL.Query().Get("raw") == "true" {
		responseWriter.Header().Set("Content-Type", "text/plain")
		_, _ = responseWriter.Write(plaintext) //nolint:gosec // G705: plaintext is a secret value, not user-controlled HTML
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(secretAPIResponse{
		Name:  secretName,
		Value: string(plaintext),
	})
}

// handleListSecrets serves GET /v1/secrets. Authenticates the caller and
// returns the names of secrets the caller's service is granted access to.
func (secretAPIServer *SecretAPIServer) handleListSecrets(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeDebugJSONError(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if request.URL.Path != "/v1/secrets" {
		secretAPIServer.handleGetSecret(responseWriter, request)
		return
	}

	callerMetadata, authenticated := secretAPIServer.authenticateRequest(responseWriter, request)
	if !authenticated {
		return
	}

	grantedSecretNames, scanError := secretAPIServer.listGrantedSecrets(request.Context(), callerMetadata.serviceName)
	if scanError != nil {
		logging.Default().Error("list secrets failed", "node", secretAPIServer.nodeID, "service", callerMetadata.serviceName, "error", scanError.Error())
		writeDebugJSONError(responseWriter, "internal error", http.StatusInternalServerError)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(secretListResponse{
		Service: callerMetadata.serviceName,
		Secrets: grantedSecretNames,
	})
}

// listGrantedSecrets scans the fact store for secret grants belonging to the
// given service and returns the secret names (without plaintext values).
func (secretAPIServer *SecretAPIServer) listGrantedSecrets(ctx context.Context, serviceName string) ([]string, error) {
	grantPrefix := fmt.Sprintf("desired/service/%s/secret/", serviceName)
	grantFacts, scanError := secretAPIServer.factStore.Scan(ctx, grantPrefix)
	if scanError != nil {
		return nil, scanError
	}

	secretNames := make([]string, 0, len(grantFacts))
	for _, grantFact := range grantFacts {
		secretName := strings.TrimPrefix(grantFact.Key, grantPrefix)
		secretNames = append(secretNames, secretName)
	}
	return secretNames, nil
}

// secretAPIResponse is the JSON response for GET /v1/secrets/{name}.
type secretAPIResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// secretListResponse is the JSON response for GET /v1/secrets.
type secretListResponse struct {
	Service string   `json:"service"`
	Secrets []string `json:"secrets"`
}
