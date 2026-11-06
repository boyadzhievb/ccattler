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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultSecretAPIReadTimeout is the HTTP read timeout for the secret API.
const defaultSecretAPIReadTimeout = 10 * time.Second

// defaultSecretAPIWriteTimeout is the HTTP write timeout for the secret API.
const defaultSecretAPIWriteTimeout = 10 * time.Second

// secretTokenLength is the byte length of generated bearer tokens (32 bytes →
// 64 hex characters). Tokens are compared in constant time.
const secretTokenLength = 32

// SecretAPIServer is the deprecated per-instance secret API (decision C, M174):
// only instances of services with legacy_secret_api true get a token. It answers
// from the instance's delivered material on tmpfs; the agent holds no key and
// never decrypts. Removed in the release after M174.
//
// Endpoints:
//   - GET /v1/secrets/{name} — returns a single secret value (JSON)
//   - GET /v1/secrets        — lists granted secret names for the caller's service
type SecretAPIServer struct {
	nodeID         string                   // nodeID identifies this node in log messages.
	materialStore  *SecretMaterialStore     // materialStore holds each instance's delivered secrets.
	httpServer     *http.Server             // httpServer is the underlying HTTP server.
	tokenMutex     sync.RWMutex             // tokenMutex protects the instanceTokens map.
	instanceTokens map[string]tokenMetadata // instanceTokens maps hex-encoded bearer token → instance metadata.
	warnedServices map[string]bool          // warnedServices records services whose deprecation warning was logged.
}

// tokenMetadata stores the instance and service associated with a bearer token.
type tokenMetadata struct {
	instanceID  string // instanceID is the instance this token was issued for.
	serviceName string // serviceName is the service this instance belongs to.
}

// NewSecretAPIServer creates a SecretAPIServer bound to the given address.
// The server is not started until Start is called.
func NewSecretAPIServer(nodeID string, listenAddress string, materialStore *SecretMaterialStore) *SecretAPIServer {
	secretAPIServer := &SecretAPIServer{
		nodeID:         nodeID,
		materialStore:  materialStore,
		instanceTokens: make(map[string]tokenMetadata),
		warnedServices: make(map[string]bool),
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
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), defaultHTTPShutdownTimeout)
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

	plaintext, retrievalError := secretAPIServer.materialStore.ReadInstanceSecret(callerMetadata.instanceID, secretName)
	if retrievalError != nil {
		writeDebugJSONError(responseWriter, "forbidden", http.StatusForbidden)
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

	grantedSecretNames := secretAPIServer.listDeliveredSecretNames(callerMetadata.instanceID)

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(secretListResponse{
		Service: callerMetadata.serviceName,
		Secrets: grantedSecretNames,
	})
}

// listDeliveredSecretNames returns the sorted names of the secrets delivered
// to the instance (exactly its granted secrets), never values.
func (secretAPIServer *SecretAPIServer) listDeliveredSecretNames(instanceID string) []string {
	secretValues, _ := secretAPIServer.materialStore.ReadInstanceSecrets(instanceID)
	secretNames := make([]string, 0, len(secretValues))
	for secretName := range secretValues {
		secretNames = append(secretNames, secretName)
	}
	sort.Strings(secretNames)
	return secretNames
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

// registerLegacySecretAPIToken issues a per-instance token only when the
// service sets legacy_secret_api true and the secret API runs. It logs the
// deprecation once per service. Returns the token and API base URL, or "".
func (nodeAgent *Agent) registerLegacySecretAPIToken(ctx context.Context, instanceInfo placedInstanceInfo) (string, string) {
	if nodeAgent.secretAPIServer == nil {
		return "", ""
	}
	optInFact, getError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceLegacySecretAPI(instanceInfo.service))
	if getError != nil || string(optInFact.Value) != "true" {
		return "", ""
	}
	nodeAgent.secretAPIServer.warnDeprecatedOncePerService(instanceInfo.service)
	secretAPIToken, registerError := nodeAgent.secretAPIServer.RegisterInstanceToken(instanceInfo.id, instanceInfo.service)
	if registerError != nil {
		logging.Default().Error("failed to register secret token", "agent", nodeAgent.nodeID, "instance", instanceInfo.id, "error", registerError.Error())
		return "", ""
	}
	return secretAPIToken, nodeAgent.resolveSecretAPIBaseURL()
}

// warnDeprecatedOncePerService logs the legacy secret API deprecation the first
// time a service's instance receives a token on this agent.
func (secretAPIServer *SecretAPIServer) warnDeprecatedOncePerService(serviceName string) {
	secretAPIServer.tokenMutex.Lock()
	defer secretAPIServer.tokenMutex.Unlock()
	if secretAPIServer.warnedServices[serviceName] {
		return
	}
	secretAPIServer.warnedServices[serviceName] = true
	logging.Default().Warn("legacy_secret_api is deprecated and will be removed in the next release; read secrets from the mounted files instead",
		"node", secretAPIServer.nodeID, "service", serviceName)
}
