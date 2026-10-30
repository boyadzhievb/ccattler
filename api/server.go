// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Package api implements the CCattler HTTP API server. It provides endpoints
// for reading and querying facts, applying DSL configurations, watching for
// changes via Server-Sent Events, and managing cluster state.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/metrics"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tenant"
	"github.com/boyadzhievb/ccattler/tracing"
	"github.com/boyadzhievb/ccattler/types"
)

var (
	apiRequestsTotal = metrics.DefaultRegistry.RegisterCounter(
		"ccattler_api_requests_total",
		"Total API requests by endpoint and status",
		"endpoint", "method", "status",
	)
	apiRequestDuration = metrics.DefaultRegistry.RegisterHistogram(
		"ccattler_api_request_duration_seconds",
		"API request latency by endpoint",
		metrics.DurationBuckets(),
		"endpoint",
	)
	activeWatches = metrics.DefaultRegistry.RegisterGauge(
		"ccattler_active_watches",
		"Number of active SSE watch/event stream connections",
	)
	instancesByState = metrics.DefaultRegistry.RegisterGauge(
		"ccattler_instances",
		"Current instance count by state",
		"state",
	)
	serviceCount = metrics.DefaultRegistry.RegisterGauge(
		"ccattler_services",
		"Total number of registered services",
	)
	nodeCount = metrics.DefaultRegistry.RegisterGauge(
		"ccattler_nodes",
		"Total number of registered nodes",
	)
)

// defaultStatusCacheTTL is how long the /api/status JSON response is cached
// before being recomputed from the fact store.
const defaultStatusCacheTTL = 2 * time.Second

// maxConfigRequestBodyBytes is the maximum size of a request body for DSL config
// endpoints (/api/apply, /api/diff, /api/secret). Limits memory consumption from
// oversized or malicious payloads.
const maxConfigRequestBodyBytes = 1 << 20 // 1 MiB

// maxSmallRequestBodyBytes is the maximum size of a request body for small JSON
// command endpoints (/api/scale, /api/node/drain, /api/node/disable, /api/node/enable,
// /api/enroll). These payloads are short JSON objects.
const maxSmallRequestBodyBytes = 64 << 10 // 64 KiB

// maxConcurrentWatchConnections is the maximum number of simultaneous SSE watch
// and event stream connections the server will accept. Connections beyond this
// limit receive HTTP 503.
const maxConcurrentWatchConnections = 1000

// ServerMode controls which components the API server expects to be available,
// affecting health checks and operational behavior.
type ServerMode string

const (
	// ServerModeFull indicates both API and controllers run in this process.
	// Health checks verify store connectivity and controller leader lease.
	ServerModeFull ServerMode = "full"
	// ServerModeAPIOnly indicates this process runs only the stateless API.
	// Health checks verify store connectivity only — no controller lease check.
	ServerModeAPIOnly ServerMode = "api-only"
)

// Server is the CCattler HTTP API server that provides endpoints for reading,
// querying, and modifying the fact store.
type Server struct {
	factStore                  store.StateStore
	eventLog                   *types.EventLog // eventLog is the optional event log for the /api/logs endpoint.
	enrollmentService          *security.EnrollmentService
	workloadTokenIssuer        *security.WorkloadTokenIssuer // workloadTokenIssuer signs workload identity JWTs and serves OIDC discovery.
	userTokenIssuer            *security.UserTokenIssuer     // userTokenIssuer mints user authentication JWTs for the login flow.
	watchMultiplexer           *WatchMultiplexer
	secretStore                *security.SecretStore        // secretStore handles encrypted secret CRUD.
	policyGate                 *tenant.PolicyGate           // policyGate is the optional admission pipeline for /api/apply.
	authenticatorChain         *security.AuthenticatorChain // authenticatorChain maps requests to principals.
	apiAuthorizer              *security.APIAuthorizer      // apiAuthorizer checks capability-based API permissions.
	tenantAuditView            *tenant.TenantAuditView      // tenantAuditView provides scoped audit log views per tenant.
	auditStore                 store.StateStore             // auditStore is the raw store for querying persisted audit entries.
	serverMode                 ServerMode
	secretMasterKey            []byte // secretMasterKey is the 32-byte key distributed to joining nodes.
	requirePrincipal           bool   // requirePrincipal enables 401 on requests without a principal in context.
	mux                        *http.ServeMux
	rateLimiter                *RateLimiter
	statusCache                *ResponseCache
	listener                   net.Listener
	activeWatchConnectionCount atomic.Int64             // activeWatchConnectionCount tracks concurrent SSE connections.
	externalOIDCConfig         *ExternalOIDCLoginConfig // externalOIDCConfig holds external IdP parameters for browser login.
}

// ExternalOIDCLoginConfig holds the external identity provider parameters that
// the CLI needs to initiate an OIDC browser login flow.
type ExternalOIDCLoginConfig struct {
	IssuerURL string // IdP issuer URL for OIDC discovery
	ClientID  string // OAuth2 client ID for PKCE flow
}

// SetEventLog attaches an event log to the server, enabling the /api/logs endpoint.
func (apiServer *Server) SetEventLog(eventLog *types.EventLog) {
	apiServer.eventLog = eventLog
}

// SetSecretStore attaches a secret store to the server, enabling the
// /api/secret endpoint for encrypted secret CRUD.
func (apiServer *Server) SetSecretStore(secretStore *security.SecretStore) {
	apiServer.secretStore = secretStore
}

// SetSecretMasterKey stores the 32-byte master key so the enrollment
// endpoint can distribute it to joining nodes.
func (apiServer *Server) SetSecretMasterKey(masterKey []byte) {
	apiServer.secretMasterKey = masterKey
}

// SetTenantAuditView attaches a tenant-scoped audit view to the server.
func (apiServer *Server) SetTenantAuditView(auditView *tenant.TenantAuditView) {
	apiServer.tenantAuditView = auditView
}

// SetAuditStore attaches the raw fact store for querying persisted audit
// entries via the /api/audit endpoint.
func (apiServer *Server) SetAuditStore(auditStore store.StateStore) {
	apiServer.auditStore = auditStore
}

// SetEnrollmentService attaches the enrollment service to the server, enabling
// the POST /api/enroll endpoint for node enrollment via join tokens.
func (apiServer *Server) SetEnrollmentService(enrollmentService *security.EnrollmentService) {
	apiServer.enrollmentService = enrollmentService
	apiServer.mux.HandleFunc("/api/enroll", apiServer.handleEnroll)
}

// SetWorkloadTokenIssuer attaches the OIDC workload token issuer to the server,
// enabling the /.well-known/openid-configuration and /oidc/jwks endpoints.
func (apiServer *Server) SetWorkloadTokenIssuer(workloadTokenIssuer *security.WorkloadTokenIssuer) {
	apiServer.workloadTokenIssuer = workloadTokenIssuer
	apiServer.mux.HandleFunc("/.well-known/openid-configuration", apiServer.handleOIDCDiscovery)
	apiServer.mux.HandleFunc("/oidc/jwks", apiServer.handleOIDCJWKS)
}

// SetUserTokenIssuer attaches the user token issuer to the server, enabling
// the user management endpoints for minting user authentication JWTs.
func (apiServer *Server) SetUserTokenIssuer(userTokenIssuer *security.UserTokenIssuer) {
	apiServer.userTokenIssuer = userTokenIssuer
}

// SetServerMode configures the operational mode, which affects health check
// behavior. API-only replicas skip the controller lease check.
func (apiServer *Server) SetServerMode(mode ServerMode) {
	apiServer.serverMode = mode
}

// SetWatchMultiplexer attaches a watch multiplexer to the server. When set,
// watch and event-stream endpoints share underlying store watches instead of
// each opening their own, reducing etcd load as API replicas scale.
func (apiServer *Server) SetWatchMultiplexer(multiplexer *WatchMultiplexer) {
	apiServer.watchMultiplexer = multiplexer
}

// SetExternalOIDCConfig stores the external identity provider parameters so
// the /api/auth/oidc-config endpoint can advertise them to the CLI for
// browser-based login.
func (apiServer *Server) SetExternalOIDCConfig(issuerURL string, clientID string) {
	apiServer.externalOIDCConfig = &ExternalOIDCLoginConfig{
		IssuerURL: issuerURL,
		ClientID:  clientID,
	}
}

// SetRequirePrincipal enables principal enforcement. When enabled, API requests
// (except /healthz, /metrics, /api/enroll) that lack a principal in context are
// rejected with 401. Used by the server command; local run/demo modes leave it off.
func (apiServer *Server) SetRequirePrincipal(enabled bool) {
	apiServer.requirePrincipal = enabled
}

// SetPolicyGate attaches the admission pipeline to the server. When set,
// /api/apply routes through syntax validation, schema validation, authorization,
// and quota checks before committing facts to the store.
func (apiServer *Server) SetPolicyGate(policyGate *tenant.PolicyGate) {
	apiServer.policyGate = policyGate
}

// SetAuthenticatorChain attaches the authentication chain to the server. When
// set, the authentication middleware extracts credentials from each request via
// the chain and injects the resulting principal into the request context. This
// replaces the earlier approach of requiring an external middleware to set the
// principal before the request reaches the API server.
func (apiServer *Server) SetAuthenticatorChain(chain *security.AuthenticatorChain) {
	apiServer.authenticatorChain = chain
}

// SetAPIAuthorizer attaches the capability-based authorizer to the server.
// When set, each handler checks the caller's capabilities before proceeding.
func (apiServer *Server) SetAPIAuthorizer(apiAuthorizer *security.APIAuthorizer) {
	apiServer.apiAuthorizer = apiAuthorizer
}

// NewServer creates a new API server backed by the given fact store.
func NewServer(factStore store.StateStore) *Server {
	apiServer := &Server{
		factStore:   factStore,
		serverMode:  ServerModeFull,
		mux:         http.NewServeMux(),
		rateLimiter: NewRateLimiter(),
		statusCache: NewResponseCache(defaultStatusCacheTTL),
	}
	apiServer.registerRoutes()
	return apiServer
}

// Start begins listening on the given address. It returns the actual address
// the server bound to, which is useful when binding to ":0" for tests.
func (apiServer *Server) Start(listenAddress string) (string, error) {
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return "", fmt.Errorf("listen %s: %w", listenAddress, err)
	}
	apiServer.listener = listener
	httpServer := &http.Server{
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: types.DefaultReadHeaderTimeout,
	}
	go func() {
		if serveError := httpServer.Serve(listener); serveError != nil && !errors.Is(serveError, net.ErrClosed) {
			logging.Default().Error("api server error", "error", serveError.Error())
		}
	}()
	return listener.Addr().String(), nil
}

// Close shuts down the API server.
func (apiServer *Server) Close() error {
	if apiServer.listener != nil {
		return apiServer.listener.Close()
	}
	return nil
}

// Handler returns the HTTP handler for use in tests or custom servers.
// The returned handler includes tracing, rate limiting, optional authentication
// via the authenticator chain, and optional principal enforcement middleware.
func (apiServer *Server) Handler() http.Handler {
	var handler http.Handler = apiServer.mux
	if apiServer.requirePrincipal {
		handler = requirePrincipalMiddleware(handler)
	}
	if apiServer.authenticatorChain != nil {
		handler = authenticationMiddleware(apiServer.authenticatorChain, handler)
	}
	return tracing.Middleware(apiServer.rateLimiter.Wrap(handler))
}

// writeJSONError writes a JSON error response with the given status code.
// All API errors use this instead of http.Error to ensure clients can always
// JSON-decode the response body.
func writeJSONError(responseWriter http.ResponseWriter, message string, statusCode int) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": message})
}

// requirePrincipalMiddleware rejects requests that lack a principal in context
// with 401 Unauthorized. Exempt paths (/healthz, /metrics, /api/enroll) are
// passed through without checking.
func requirePrincipalMiddleware(wrappedHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz", "/metrics", "/api/enroll":
			wrappedHandler.ServeHTTP(responseWriter, request)
			return
		}
		principal := security.PrincipalFromContext(request.Context())
		if principal == "" {
			writeJSONError(responseWriter, "authentication required", http.StatusUnauthorized)
			return
		}
		wrappedHandler.ServeHTTP(responseWriter, request)
	})
}

// authenticationMiddleware runs the authenticator chain on each request and
// injects the resulting principal into the context. Exempt paths (/healthz,
// /metrics, /api/enroll) are passed through without authentication. If the
// chain returns nil (no credentials recognised), the request proceeds without
// a principal — the downstream requirePrincipalMiddleware will reject it if
// enforcement is enabled.
func authenticationMiddleware(chain *security.AuthenticatorChain, wrappedHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz", "/metrics", "/api/enroll":
			wrappedHandler.ServeHTTP(responseWriter, request)
			return
		}
		result, authError := chain.Authenticate(request)
		if authError != nil {
			writeJSONError(responseWriter, "authentication failed: "+authError.Error(), http.StatusUnauthorized)
			return
		}
		if result != nil {
			enrichedContext := security.WithPrincipal(request.Context(), result.Principal)
			enrichedContext = security.WithAuthMethod(enrichedContext, result.AuthMethod)
			principalStruct := security.Principal{
				Kind:       result.PrincipalKind,
				Name:       security.PrincipalFromKindAndName(result.Principal).Name,
				Groups:     result.Groups,
				Attributes: result.Attributes,
			}
			enrichedContext = security.WithPrincipalStruct(enrichedContext, principalStruct)
			request = request.WithContext(enrichedContext)
		}
		wrappedHandler.ServeHTTP(responseWriter, request)
	})
}

// requireCapability checks whether the request's principal has the given
// capability at the given scope. Returns true if the check passes (caller may
// proceed). Returns false and writes a 403/500 response if the caller lacks
// the capability or the authorizer is not configured.
func (apiServer *Server) requireCapability(responseWriter http.ResponseWriter, request *http.Request, capability security.Capability, scope security.Scope) bool {
	if apiServer.apiAuthorizer == nil {
		writeJSONError(responseWriter, "authorization not configured", http.StatusInternalServerError)
		return false
	}
	principal := security.PrincipalStructFromContext(request.Context())
	if authError := apiServer.apiAuthorizer.AuthorizeAPI(principal, capability, scope, nil); authError != nil {
		writeJSONError(responseWriter, "forbidden: "+authError.Error(), http.StatusForbidden)
		return false
	}
	return true
}

// registerRoutes sets up all API endpoint handlers.
func (apiServer *Server) registerRoutes() {
	apiServer.mux.HandleFunc("/api/state", apiServer.instrumentedHandler("state", apiServer.handleState))
	apiServer.mux.HandleFunc("/api/apply", apiServer.instrumentedHandler("apply", apiServer.handleApply))
	apiServer.mux.HandleFunc("/api/watch", apiServer.handleWatch)
	apiServer.mux.HandleFunc("/api/scale", apiServer.instrumentedHandler("scale", apiServer.handleScale))
	apiServer.mux.HandleFunc("/api/restart", apiServer.instrumentedHandler("restart", apiServer.handleRestart))
	apiServer.mux.HandleFunc("/api/rollout/restart", apiServer.instrumentedHandler("rollout-restart", apiServer.handleRolloutRestart))
	apiServer.mux.HandleFunc("/api/status", apiServer.instrumentedHandler("status", apiServer.handleStatus))
	apiServer.mux.HandleFunc("/api/logs", apiServer.instrumentedHandler("logs", apiServer.handleLogs))
	apiServer.mux.HandleFunc("/api/describe", apiServer.instrumentedHandler("describe", apiServer.handleDescribe))
	apiServer.mux.HandleFunc("/api/events/stream", apiServer.handleEventStream)
	apiServer.mux.HandleFunc("/api/diff", apiServer.instrumentedHandler("diff", apiServer.handleDiff))
	apiServer.mux.HandleFunc("/api/metric", apiServer.instrumentedHandler("metric", apiServer.handleMetric))
	apiServer.mux.HandleFunc("/api/activate", apiServer.instrumentedHandler("activate", apiServer.handleActivate))
	apiServer.mux.HandleFunc("/api/secret/rotate", apiServer.instrumentedHandler("secret-rotate", apiServer.handleSecretRotate))
	apiServer.mux.HandleFunc("/api/secret", apiServer.instrumentedHandler("secret", apiServer.handleSecret))
	apiServer.mux.HandleFunc("/api/node/drain", apiServer.instrumentedHandler("node-drain", apiServer.handleNodeDrain))
	apiServer.mux.HandleFunc("/api/node/disable", apiServer.instrumentedHandler("node-disable", apiServer.handleNodeDisable))
	apiServer.mux.HandleFunc("/api/node/enable", apiServer.instrumentedHandler("node-enable", apiServer.handleNodeEnable))
	apiServer.mux.HandleFunc("/api/user", apiServer.instrumentedHandler("user", apiServer.handleUser))
	apiServer.mux.HandleFunc("/api/user/token", apiServer.instrumentedHandler("user-token", apiServer.handleUserToken))
	apiServer.mux.HandleFunc("/api/role", apiServer.instrumentedHandler("role", apiServer.handleRole))
	apiServer.mux.HandleFunc("/api/role/check", apiServer.instrumentedHandler("role-check", apiServer.handleRoleCheck))
	apiServer.mux.HandleFunc("/api/gc", apiServer.instrumentedHandler("gc", apiServer.handleGC))
	apiServer.mux.HandleFunc("/api/audit", apiServer.handleAudit)
	apiServer.mux.HandleFunc("/api/auth/whoami", apiServer.instrumentedHandler("whoami", apiServer.handleAuthWhoami))
	apiServer.mux.HandleFunc("/api/auth/oidc-config", apiServer.instrumentedHandler("oidc-config", apiServer.handleAuthOIDCConfig))
	apiServer.mux.HandleFunc("/healthz", apiServer.handleHealthz)
	apiServer.mux.HandleFunc("/metrics", apiServer.handleMetrics)
}

// handleMetrics serves Prometheus-format metrics. Before emitting the
// registry, it snapshots cluster state into gauges so scrapers see current
// instance/service/node counts without a separate status query.
func (apiServer *Server) handleMetrics(responseWriter http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	status := buildStatusFromStore(ctx, apiServer.factStore, nil, false)

	instancesByState.Set(0, string(types.InstancePending))
	instancesByState.Set(0, string(types.InstanceRunning))
	instancesByState.Set(0, string(types.InstanceFailed))
	instancesByState.Set(0, string(types.InstanceStopped))
	for _, instance := range status.Instances {
		instancesByState.Inc(instance.State)
	}
	serviceCount.Set(int64(len(status.Services)))
	nodeCount.Set(int64(len(status.Nodes)))

	metrics.DefaultRegistry.Handler().ServeHTTP(responseWriter, request)
}

// healthCheckResult describes the health status of a single component.
type healthCheckResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// handleHealthz returns the health of the control plane. In full mode it
// checks store connectivity and controller leader presence. In api-only mode
// it checks only store connectivity — the critical signal for load-balancer
// readiness. Returns 200 when healthy, 503 when any critical check fails.
func (apiServer *Server) handleHealthz(responseWriter http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	checks := []healthCheckResult{}
	healthy := true

	_, revisionError := apiServer.factStore.Revision(ctx)
	if revisionError != nil {
		checks = append(checks, healthCheckResult{
			Name:    "store",
			Status:  "unhealthy",
			Message: revisionError.Error(),
		})
		healthy = false
	} else {
		checks = append(checks, healthCheckResult{
			Name:   "store",
			Status: "healthy",
		})
	}

	if apiServer.serverMode == ServerModeFull {
		controllerFacts, _ := apiServer.factStore.Scan(ctx, "leader/")
		if len(controllerFacts) > 0 {
			checks = append(checks, healthCheckResult{
				Name:   "controllers",
				Status: "healthy",
			})
		} else {
			checks = append(checks, healthCheckResult{
				Name:    "controllers",
				Status:  "unknown",
				Message: "no leader lease found",
			})
		}
	}

	serverModeLabel := string(apiServer.serverMode)
	responseWriter.Header().Set("Content-Type", "application/json")
	if !healthy {
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
	}
	result := struct {
		Status string              `json:"status"`
		Mode   string              `json:"mode"`
		Checks []healthCheckResult `json:"checks"`
	}{
		Mode:   serverModeLabel,
		Checks: checks,
	}
	if healthy {
		result.Status = "healthy"
	} else {
		result.Status = "unhealthy"
	}
	_ = json.NewEncoder(responseWriter).Encode(result)
}

// instrumentedHandler wraps an HTTP handler to record request count and duration
// metrics. Watch and event stream endpoints are not wrapped because they are
// long-lived SSE connections.
func (apiServer *Server) instrumentedHandler(endpointName string, handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		startTime := metrics.Timer()
		wrappedWriter := &statusCapturingWriter{ResponseWriter: responseWriter, statusCode: 200}
		handler(wrappedWriter, request)
		apiRequestDuration.ObserveSince(startTime, endpointName)
		apiRequestsTotal.Inc(endpointName, request.Method, strconv.Itoa(wrappedWriter.statusCode))
	}
}

// statusCapturingWriter wraps http.ResponseWriter to capture the status code
// for metrics recording.
type statusCapturingWriter struct {
	http.ResponseWriter
	statusCode int
}

func (writer *statusCapturingWriter) WriteHeader(statusCode int) {
	writer.statusCode = statusCode
	writer.ResponseWriter.WriteHeader(statusCode)
}

// factResponse is a single fact in JSON API responses.
type factResponse struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Revision int64  `json:"revision"`
}

// handleState serves GET /api/state?prefix=... to read facts by prefix, or
// GET /api/state?key=... to read a single fact.
func (apiServer *Server) handleState(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	if singleKey := request.URL.Query().Get("key"); singleKey != "" {
		if isSensitivePrefix(singleKey) {
			writeJSONError(responseWriter, "access denied: sensitive prefix", http.StatusForbidden)
			return
		}
		fact, err := apiServer.factStore.Get(requestContext, singleKey)
		if err != nil {
			writeJSONError(responseWriter, fmt.Sprintf("key not found: %s", singleKey), http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(responseWriter).Encode(factResponse{
			Key: fact.Key, Value: string(fact.Value), Revision: fact.Revision,
		})
		return
	}

	prefix := request.URL.Query().Get("prefix")
	if prefix == "" {
		writeJSONError(responseWriter, "prefix or key parameter required", http.StatusBadRequest)
		return
	}

	if isSensitivePrefix(prefix) {
		writeJSONError(responseWriter, "access denied: sensitive prefix", http.StatusForbidden)
		return
	}

	statePrincipal := security.PrincipalStructFromContext(requestContext)
	facts, err := tenant.ScopedScan(requestContext, apiServer.factStore, prefix, statePrincipal)
	if err != nil {
		writeJSONError(responseWriter, err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]factResponse, 0, len(facts))
	for _, fact := range facts {
		result = append(result, factResponse{
			Key: fact.Key, Value: string(fact.Value), Revision: fact.Revision,
		})
	}
	_ = json.NewEncoder(responseWriter).Encode(result)
}

// applyRequest is the JSON body for POST /api/apply.
type applyRequest struct {
	Config string `json:"config"`
}

// applyResponse is the JSON response for POST /api/apply.
type applyResponse struct {
	OK       bool   `json:"ok"`
	FactsSet int    `json:"facts_set,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleApply serves POST /api/apply to submit DSL configuration. The request
// body can be raw DSL text (Content-Type: text/plain) or JSON with a "config"
// field (Content-Type: application/json).
func (apiServer *Server) handleApply(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadCreate, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxConfigRequestBodyBytes)
	responseWriter.Header().Set("Content-Type", "application/json")

	dslInput, readError := readDSLRequestBody(request)
	if readError != "" {
		_ = json.NewEncoder(responseWriter).Encode(applyResponse{Error: readError})
		return
	}

	if apiServer.policyGate != nil {
		apiServer.applyViaPolicyGate(responseWriter, request.Context(), dslInput)
		return
	}

	apiServer.applyDirect(responseWriter, request.Context(), dslInput)
}

// readDSLRequestBody extracts DSL text from the request body. Returns the DSL
// string and an empty error string on success, or empty DSL and the error
// message on failure.
func readDSLRequestBody(request *http.Request) (string, string) {
	contentType := request.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var body applyRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return "", "invalid JSON: " + err.Error()
		}
		if body.Config == "" {
			return "", "empty config"
		}
		return body.Config, ""
	}
	rawBody, err := io.ReadAll(request.Body)
	if err != nil {
		return "", "read body: " + err.Error()
	}
	if len(rawBody) == 0 {
		return "", "empty config"
	}
	return string(rawBody), ""
}

// applyViaPolicyGate routes DSL through the full admission pipeline
// (syntax, schema, authorization, quota, commit).
func (apiServer *Server) applyViaPolicyGate(responseWriter http.ResponseWriter, requestContext context.Context, dslInput string) {
	principal := security.PrincipalFromContext(requestContext)
	result, evaluateError := apiServer.policyGate.EvaluateAndCommit(requestContext, principal, dslInput)
	if evaluateError != nil {
		responseWriter.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(responseWriter).Encode(applyResponse{Error: "policy: " + evaluateError.Error()})
		return
	}
	if !result.Allowed {
		responseWriter.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(responseWriter).Encode(applyResponse{Error: result.Stage + ": " + result.Reason})
		return
	}
	_ = json.NewEncoder(responseWriter).Encode(applyResponse{OK: true, FactsSet: len(result.Facts)})
}

// applyDirect parses, compiles, and writes facts without policy evaluation.
// Used in local/demo modes without a policy gate.
func (apiServer *Server) applyDirect(responseWriter http.ResponseWriter, requestContext context.Context, dslInput string) {
	file, err := lang.Parse(dslInput)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(applyResponse{Error: "parse: " + err.Error()})
		return
	}
	facts, err := lang.Compile(file)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(applyResponse{Error: "compile: " + err.Error()})
		return
	}
	for _, fact := range facts {
		if _, putErr := apiServer.factStore.Put(requestContext, fact.Key, []byte(fact.Value)); putErr != nil {
			responseWriter.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(responseWriter).Encode(applyResponse{Error: "store: " + putErr.Error()})
			return
		}
	}
	_ = json.NewEncoder(responseWriter).Encode(applyResponse{OK: true, FactsSet: len(facts)})
}

// handleWatch serves GET /api/watch?prefix=... as a Server-Sent Events stream.
// Each store change under the prefix is sent as a JSON-encoded SSE data line.
func (apiServer *Server) handleWatch(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}

	eventChannel, flusher, setupOK := apiServer.setupWatchStream(responseWriter, request)
	if !setupOK {
		return
	}

	activeWatches.Inc()
	defer activeWatches.Dec()

	responseWriter.Header().Set("Content-Type", "text/event-stream")
	responseWriter.Header().Set("Cache-Control", "no-cache")
	responseWriter.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	streamWatchEventsAsSSE(request.Context(), eventChannel, responseWriter, flusher)
}

// setupWatchStream validates the watch request, enforces connection limits, and
// subscribes to the store watch. Returns the event channel, flusher, and true
// on success. Writes an error response and returns false on failure.
func (apiServer *Server) setupWatchStream(responseWriter http.ResponseWriter, request *http.Request) (<-chan store.Event, http.Flusher, bool) {
	if apiServer.activeWatchConnectionCount.Load() >= maxConcurrentWatchConnections {
		writeJSONError(responseWriter, "too many concurrent watch connections", http.StatusServiceUnavailable)
		return nil, nil, false
	}
	apiServer.activeWatchConnectionCount.Add(1)

	prefix := request.URL.Query().Get("prefix")
	if prefix == "" {
		apiServer.activeWatchConnectionCount.Add(-1)
		writeJSONError(responseWriter, "prefix parameter required", http.StatusBadRequest)
		return nil, nil, false
	}
	if isSensitivePrefix(prefix) {
		apiServer.activeWatchConnectionCount.Add(-1)
		writeJSONError(responseWriter, "access denied: sensitive prefix", http.StatusForbidden)
		return nil, nil, false
	}

	flusher, ok := responseWriter.(http.Flusher)
	if !ok {
		apiServer.activeWatchConnectionCount.Add(-1)
		writeJSONError(responseWriter, "streaming not supported", http.StatusInternalServerError)
		return nil, nil, false
	}

	watchContext, cancelWatch := context.WithCancel(request.Context())
	var eventChannel <-chan store.Event
	var watchError error
	if apiServer.watchMultiplexer != nil {
		eventChannel, watchError = apiServer.watchMultiplexer.Subscribe(watchContext, prefix)
	} else {
		eventChannel, watchError = apiServer.factStore.Watch(watchContext, prefix, store.WatchOption{Prefix: true})
	}
	if watchError != nil {
		cancelWatch()
		apiServer.activeWatchConnectionCount.Add(-1)
		writeJSONError(responseWriter, "watch: "+watchError.Error(), http.StatusInternalServerError)
		return nil, nil, false
	}

	go func() {
		<-request.Context().Done()
		cancelWatch()
		apiServer.activeWatchConnectionCount.Add(-1)
	}()

	return eventChannel, flusher, true
}

// streamWatchEventsAsSSE reads events from the watch channel and writes them
// as Server-Sent Events until the channel closes or the request context ends.
func streamWatchEventsAsSSE(requestContext context.Context, eventChannel <-chan store.Event, responseWriter http.ResponseWriter, flusher http.Flusher) {
	for {
		select {
		case <-requestContext.Done():
			return
		case event, open := <-eventChannel:
			if !open {
				return
			}
			eventType := "put"
			if event.Type == store.EventDelete {
				eventType = "delete"
			}
			watchEvent := struct {
				Type     string `json:"type"`
				Key      string `json:"key"`
				Value    string `json:"value"`
				Revision int64  `json:"revision"`
			}{
				Type:     eventType,
				Key:      event.Fact.Key,
				Value:    string(event.Fact.Value),
				Revision: event.Fact.Revision,
			}
			eventBytes, _ := json.Marshal(watchEvent)
			_, _ = fmt.Fprintf(responseWriter, "data: %s\n\n", eventBytes)
			flusher.Flush()
		}
	}
}

// scaleRequest is the JSON body for POST /api/scale.
type scaleRequest struct {
	Service   string `json:"service"`
	Instances int    `json:"instances"`
}

// handleScale serves POST /api/scale to change the desired instance count
// for a service. It updates the user intent, desired instances, and effective
// instance count facts.
func (apiServer *Server) handleScale(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityScalingWrite, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var body scaleRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "invalid JSON"})
		return
	}

	if body.Service == "" || body.Instances < 0 {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "service and non-negative instances required"})
		return
	}
	if validateError := types.ValidateResourceName(body.Service); validateError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": validateError.Error()})
		return
	}

	instancesStr := strconv.Itoa(body.Instances)
	if _, putError := apiServer.factStore.Put(requestContext, types.KeyDesiredServiceInstances(body.Service), []byte(instancesStr)); putError != nil {
		logging.Default().Error("failed to store fact", "key", types.KeyDesiredServiceInstances(body.Service), "error", putError.Error())
	}
	if _, putError := apiServer.factStore.Put(requestContext, types.KeyIntentUserServiceInstances(body.Service), []byte(instancesStr)); putError != nil {
		logging.Default().Error("failed to store fact", "key", types.KeyIntentUserServiceInstances(body.Service), "error", putError.Error())
	}

	_ = json.NewEncoder(responseWriter).Encode(map[string]any{
		"ok":        true,
		"service":   body.Service,
		"instances": body.Instances,
	})
}

// restartInstanceRequest is the JSON body for POST /api/restart.
type restartInstanceRequest struct {
	InstanceID string `json:"instance_id"`
}

// handleRestart stops a single instance by setting its observed state to
// "stopped". The instance controller will detect the deficit and create a
// replacement to meet the desired count.
func (apiServer *Server) handleRestart(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadUpdate, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var body restartInstanceRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeJSONError(responseWriter, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.InstanceID == "" {
		writeJSONError(responseWriter, "instance_id required", http.StatusBadRequest)
		return
	}

	stateKey := types.KeyObservedInstanceState(body.InstanceID)
	currentState, getError := apiServer.factStore.Get(requestContext, stateKey)
	if getError != nil || currentState.Value == nil {
		writeJSONError(responseWriter, fmt.Sprintf("instance %q not found", body.InstanceID), http.StatusNotFound)
		return
	}

	if _, putError := apiServer.factStore.Put(requestContext, stateKey, []byte(string(types.InstanceStopped))); putError != nil {
		writeJSONError(responseWriter, "failed to stop instance", http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(responseWriter).Encode(map[string]any{
		"ok":         true,
		"instance":   body.InstanceID,
		"restarting": true,
	})
}

// rolloutRestartRequest is the JSON body for POST /api/rollout/restart.
type rolloutRestartRequest struct {
	Service string `json:"service"`
}

// handleRolloutRestart triggers a rolling restart of all instances belonging
// to a service. It sets each running instance's state to "stopped" so the
// instance controller recreates them to meet the desired count.
func (apiServer *Server) handleRolloutRestart(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadUpdate, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var body rolloutRestartRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeJSONError(responseWriter, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Service == "" {
		writeJSONError(responseWriter, "service required", http.StatusBadRequest)
		return
	}

	instanceFacts, scanError := apiServer.factStore.Scan(requestContext, types.ScanObservedInstances)
	if scanError != nil {
		writeJSONError(responseWriter, "failed to scan instances", http.StatusInternalServerError)
		return
	}

	serviceInstances := make(map[string]bool)
	instanceStates := make(map[string]string)
	for _, fact := range instanceFacts {
		factKey := string(fact.Key)
		if strings.HasSuffix(factKey, "/service") && string(fact.Value) == body.Service {
			instanceID := strings.TrimPrefix(factKey, types.ScanObservedInstances)
			instanceID = strings.TrimSuffix(instanceID, "/service")
			serviceInstances[instanceID] = true
		}
		if strings.HasSuffix(factKey, "/state") {
			instanceID := strings.TrimPrefix(factKey, types.ScanObservedInstances)
			instanceID = strings.TrimSuffix(instanceID, "/state")
			instanceStates[instanceID] = string(fact.Value)
		}
	}

	var restartedCount int
	for instanceID := range serviceInstances {
		if instanceStates[instanceID] == string(types.InstanceRunning) || instanceStates[instanceID] == string(types.InstanceStarting) {
			stateKey := types.KeyObservedInstanceState(instanceID)
			if _, putError := apiServer.factStore.Put(requestContext, stateKey, []byte(string(types.InstanceStopped))); putError == nil {
				restartedCount++
			}
		}
	}

	if restartedCount == 0 {
		writeJSONError(responseWriter, fmt.Sprintf("no running instances found for service %q", body.Service), http.StatusNotFound)
		return
	}

	_ = json.NewEncoder(responseWriter).Encode(map[string]any{
		"ok":        true,
		"service":   body.Service,
		"restarted": restartedCount,
	})
}

// nodeDrainRequest is the JSON body for POST /api/node/drain.
type nodeDrainRequest struct {
	// NodeID is the identifier of the node to drain.
	NodeID string `json:"node_id"`
	// GracePeriod is the drain grace period in seconds (0 uses default).
	GracePeriod int `json:"grace_period"`
}

// nodeDisableEnableRequest is the JSON body for POST /api/node/disable and /api/node/enable.
type nodeDisableEnableRequest struct {
	// NodeID is the identifier of the node to disable or enable.
	NodeID string `json:"node_id"`
}

// handleNodeDrain serves POST /api/node/drain to initiate graceful drain of a
// node. It sets the node state to draining and records drain metadata.
func (apiServer *Server) handleNodeDrain(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityNodeManage, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var body nodeDrainRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&body); decodeError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "invalid JSON"})
		return
	}
	if body.NodeID == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "node_id required"})
		return
	}

	if !apiServer.nodeExists(requestContext, body.NodeID) {
		responseWriter.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "node not found"})
		return
	}

	apiServer.writeNodeDrainFacts(requestContext, body.NodeID)
	_ = json.NewEncoder(responseWriter).Encode(map[string]any{"ok": true})
}

// handleNodeDisable serves POST /api/node/disable to exclude a node from new
// placements while keeping existing workloads running.
func (apiServer *Server) handleNodeDisable(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityNodeManage, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var body nodeDisableEnableRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&body); decodeError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "invalid JSON"})
		return
	}
	if body.NodeID == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "node_id required"})
		return
	}

	if !apiServer.nodeExists(requestContext, body.NodeID) {
		responseWriter.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "node not found"})
		return
	}

	if _, putError := apiServer.factStore.Put(requestContext, types.KeyObservedNodeState(body.NodeID), []byte(string(types.NodeDisabled))); putError != nil {
		logging.Default().Error("failed to disable node", "node", body.NodeID, "error", putError.Error())
	}
	_ = json.NewEncoder(responseWriter).Encode(map[string]any{"ok": true})
}

// handleNodeEnable serves POST /api/node/enable to return a disabled node
// to the alive state, making it eligible for new placements again.
func (apiServer *Server) handleNodeEnable(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityNodeManage, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var body nodeDisableEnableRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&body); decodeError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "invalid JSON"})
		return
	}
	if body.NodeID == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "node_id required"})
		return
	}

	if !apiServer.nodeExists(requestContext, body.NodeID) {
		responseWriter.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "node not found"})
		return
	}

	if _, putError := apiServer.factStore.Put(requestContext, types.KeyObservedNodeState(body.NodeID), []byte(string(types.NodeAlive))); putError != nil {
		logging.Default().Error("failed to enable node", "node", body.NodeID, "error", putError.Error())
	}
	_ = json.NewEncoder(responseWriter).Encode(map[string]any{"ok": true})
}

// nodeExists checks if a node's observed state key exists in the fact store.
// Returns true if the node is registered, false otherwise.
func (apiServer *Server) nodeExists(requestContext context.Context, nodeID string) bool {
	nodeStateFact, getError := apiServer.factStore.Get(requestContext, types.KeyObservedNodeState(nodeID))
	return getError == nil && nodeStateFact.Value != nil
}

// writeNodeDrainFacts writes the draining state and drain metadata facts for a
// node. Separated from the handler to keep the handler under the line limit.
func (apiServer *Server) writeNodeDrainFacts(requestContext context.Context, nodeID string) {
	drainStartedMillis := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if _, putError := apiServer.factStore.Put(requestContext, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeDraining))); putError != nil {
		logging.Default().Error("failed to set node draining", "node", nodeID, "error", putError.Error())
	}
	if _, putError := apiServer.factStore.Put(requestContext, types.KeyDerivedNodeDrainStarted(nodeID), []byte(drainStartedMillis)); putError != nil {
		logging.Default().Error("failed to write drain start", "node", nodeID, "error", putError.Error())
	}
	if _, putError := apiServer.factStore.Put(requestContext, types.KeyDerivedNodeDrainInitiator(nodeID), []byte("user:cli")); putError != nil {
		logging.Default().Error("failed to write drain initiator", "node", nodeID, "error", putError.Error())
	}
}

// handleStatus serves GET /api/status returning the full cluster status as JSON.
func (apiServer *Server) handleStatus(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")

	requestContext := request.Context()
	requestPrincipal := security.PrincipalStructFromContext(requestContext)
	hasPrincipal := requestPrincipal.Name != ""

	includeAllInstances := request.URL.Query().Get("all") == "true"
	isPlatform := !hasPrincipal || tenant.IsPlatformPrincipal(requestPrincipal)
	if isPlatform && !includeAllInstances {
		if cached := apiServer.statusCache.Get("status"); cached != nil {
			_, _ = responseWriter.Write(cached)
			return
		}
	}

	var principalPointer *security.Principal
	if hasPrincipal {
		principalPointer = &requestPrincipal
	}
	status := buildStatusFromStore(requestContext, apiServer.factStore, principalPointer, includeAllInstances)
	encoded, encodeError := json.Marshal(status)
	if encodeError != nil {
		_ = json.NewEncoder(responseWriter).Encode(status)
		return
	}

	if isPlatform {
		apiServer.statusCache.Set("status", encoded)
	}
	_, _ = responseWriter.Write(encoded)
}

// handleLogs serves GET /api/logs to query the cluster event log. Supports
// optional query parameters: target (filter by affected resource), kind (filter
// by event kind), and limit (maximum number of events, default 50).
func (apiServer *Server) handleLogs(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")

	if apiServer.eventLog == nil {
		_ = json.NewEncoder(responseWriter).Encode([]types.SystemEvent{})
		return
	}

	requestContext := request.Context()
	targetFilter := request.URL.Query().Get("target")
	kindFilter := request.URL.Query().Get("kind")
	limitParam := request.URL.Query().Get("limit")

	eventLimit := 50
	if limitParam != "" {
		if parsedLimit, err := strconv.Atoi(limitParam); err == nil && parsedLimit > 0 {
			eventLimit = parsedLimit
		}
	}

	var events []types.SystemEvent
	var queryError error

	switch {
	case targetFilter != "":
		events, queryError = apiServer.eventLog.ForTarget(requestContext, targetFilter, eventLimit)
	case kindFilter != "":
		events, queryError = apiServer.eventLog.Query(requestContext, kindFilter, eventLimit)
	default:
		events, queryError = apiServer.eventLog.Query(requestContext, "", eventLimit)
	}

	if queryError != nil {
		writeJSONError(responseWriter, queryError.Error(), http.StatusInternalServerError)
		return
	}

	if events == nil {
		events = []types.SystemEvent{}
	}
	_ = json.NewEncoder(responseWriter).Encode(events)
}

// handleAudit serves GET /api/audit returning persistent authorization audit
// entries from the store. Supports query parameters: principal, action, since
// (RFC3339), limit (default 100), denied (true/false), follow (true for SSE).
func (apiServer *Server) handleAudit(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}
	if apiServer.auditStore == nil {
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode([]security.AuditEntry{})
		return
	}

	queryParams := request.URL.Query()
	filterPrincipal := queryParams.Get("principal")
	filterAction := queryParams.Get("action")
	filterSince := queryParams.Get("since")
	filterDenied := queryParams.Get("denied") == "true"
	followMode := queryParams.Get("follow") == "true"
	limitString := queryParams.Get("limit")

	maxEntries := security.DefaultAuditQueryLimit
	if limitString != "" {
		if parsedLimit, parseErr := strconv.Atoi(limitString); parseErr == nil && parsedLimit > 0 {
			maxEntries = parsedLimit
		}
	}
	if maxEntries > security.MaxAuditQueryLimit {
		maxEntries = security.MaxAuditQueryLimit
	}

	var sinceTime time.Time
	if filterSince != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, filterSince); parseErr == nil {
			sinceTime = parsed
		}
	}

	if followMode {
		apiServer.handleAuditFollow(responseWriter, request, filterPrincipal, filterAction, sinceTime, filterDenied)
		return
	}

	facts, scanError := apiServer.auditStore.Scan(request.Context(), types.PrefixAudit+"/")
	if scanError != nil {
		writeJSONError(responseWriter, "scan audit log: "+scanError.Error(), http.StatusInternalServerError)
		return
	}

	tenantFilter := resolveAuditTenantFilter(request)

	allocationCapacity := min(len(facts), maxEntries)
	if allocationCapacity > security.MaxAuditQueryLimit {
		allocationCapacity = security.MaxAuditQueryLimit
	}
	filtered := make([]security.AuditEntry, 0, allocationCapacity)
	for entryIndex := len(facts) - 1; entryIndex >= 0 && len(filtered) < maxEntries; entryIndex-- {
		var entry security.AuditEntry
		if unmarshalErr := json.Unmarshal(facts[entryIndex].Value, &entry); unmarshalErr != nil {
			continue
		}
		if !matchesAuditFilter(entry, filterPrincipal, filterAction, sinceTime, filterDenied, tenantFilter) {
			continue
		}
		filtered = append(filtered, entry)
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(filtered)
}

// handleAuditFollow streams new audit entries as Server-Sent Events.
func (apiServer *Server) handleAuditFollow(responseWriter http.ResponseWriter, request *http.Request, filterPrincipal, filterAction string, sinceTime time.Time, filterDenied bool) {
	flusher, flusherOK := responseWriter.(http.Flusher)
	if !flusherOK {
		writeJSONError(responseWriter, "streaming not supported", http.StatusInternalServerError)
		return
	}

	watchContext, cancelWatch := context.WithCancel(request.Context())
	defer cancelWatch()

	auditPrefix := types.PrefixAudit + "/"
	eventChannel, watchError := apiServer.auditStore.Watch(watchContext, auditPrefix, store.WatchOption{Prefix: true})
	if watchError != nil {
		writeJSONError(responseWriter, "watch audit log: "+watchError.Error(), http.StatusInternalServerError)
		return
	}

	tenantFilter := resolveAuditTenantFilter(request)

	responseWriter.Header().Set("Content-Type", "text/event-stream")
	responseWriter.Header().Set("Cache-Control", "no-cache")
	responseWriter.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-eventChannel:
			if !open {
				return
			}
			if event.Type == store.EventDelete {
				continue
			}
			var entry security.AuditEntry
			if unmarshalErr := json.Unmarshal(event.Fact.Value, &entry); unmarshalErr != nil {
				continue
			}
			if !matchesAuditFilter(entry, filterPrincipal, filterAction, sinceTime, filterDenied, tenantFilter) {
				continue
			}
			entryBytes, _ := json.Marshal(entry)
			_, _ = fmt.Fprintf(responseWriter, "data: %s\n\n", entryBytes)
			flusher.Flush()
		}
	}
}

// matchesAuditFilter returns true when the entry passes all active filters.
// resolveAuditTenantFilter returns the tenant scope for the requesting principal,
// or empty string for platform principals who can see all entries.
func resolveAuditTenantFilter(request *http.Request) string {
	requestPrincipal := security.PrincipalStructFromContext(request.Context())
	if requestPrincipal.Name != "" && !tenant.IsPlatformPrincipal(requestPrincipal) {
		return tenant.ResolvePrincipalTenant(requestPrincipal)
	}
	return ""
}

func matchesAuditFilter(entry security.AuditEntry, filterPrincipal, filterAction string, sinceTime time.Time, filterDenied bool, tenantFilter string) bool {
	if filterPrincipal != "" && entry.Principal != filterPrincipal {
		return false
	}
	if filterAction != "" && entry.Action != filterAction {
		return false
	}
	if !sinceTime.IsZero() && entry.Timestamp.Before(sinceTime) {
		return false
	}
	if filterDenied && !strings.EqualFold(entry.Decision, security.AuditDecisionDeny) {
		return false
	}
	if tenantFilter != "" && !strings.HasPrefix(entry.Target, tenantFilter+"/") && !strings.Contains(entry.Target, "/"+tenantFilter+"/") && !strings.Contains(entry.Principal, tenantFilter) {
		return false
	}
	return true
}

// handleDescribe serves GET /api/describe returning a detailed single-resource
// view aggregating all related facts, health state, placement, and recent events.
// Query parameters: type (service, node, instance) and name (resource identifier).
func (apiServer *Server) handleDescribe(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	requestContext := request.Context()

	resourceType := request.URL.Query().Get("type")
	resourceName := request.URL.Query().Get("name")
	if resourceType == "" || resourceName == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "type and name query parameters required"})
		return
	}
	if validateError := types.ValidateResourceName(resourceName); validateError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": validateError.Error()})
		return
	}

	var result interface{}
	var describeError error

	switch resourceType {
	case "service", "svc":
		result, describeError = buildServiceDescribe(requestContext, apiServer.factStore, apiServer.eventLog, resourceName)
	case "node":
		result, describeError = buildNodeDescribe(requestContext, apiServer.factStore, apiServer.eventLog, resourceName)
	case "instance", "inst":
		result, describeError = buildInstanceDescribe(requestContext, apiServer.factStore, apiServer.eventLog, resourceName)
	default:
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "type must be service, node, or instance"})
		return
	}

	if describeError != nil {
		responseWriter.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": describeError.Error()})
		return
	}

	_ = json.NewEncoder(responseWriter).Encode(result)
}

// handleEventStream serves GET /api/events/stream as a Server-Sent Events
// stream of cluster events. New events written to the event log are sent as
// JSON-encoded SSE data lines in real time. Supports optional service query
// parameter to filter events whose target contains the service name.
func (apiServer *Server) handleEventStream(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}

	if apiServer.activeWatchConnectionCount.Load() >= maxConcurrentWatchConnections {
		writeJSONError(responseWriter, "too many concurrent watch connections", http.StatusServiceUnavailable)
		return
	}
	apiServer.activeWatchConnectionCount.Add(1)
	defer apiServer.activeWatchConnectionCount.Add(-1)

	flusher, ok := responseWriter.(http.Flusher)
	if !ok {
		writeJSONError(responseWriter, "streaming not supported", http.StatusInternalServerError)
		return
	}

	serviceFilter := request.URL.Query().Get("service")

	watchContext, cancelWatch := context.WithCancel(request.Context())
	defer cancelWatch()

	eventPrefix := types.PrefixEvent + "/"
	var eventChannel <-chan store.Event
	var watchError error

	if apiServer.watchMultiplexer != nil {
		eventChannel, watchError = apiServer.watchMultiplexer.Subscribe(watchContext, eventPrefix)
	} else {
		eventChannel, watchError = apiServer.factStore.Watch(watchContext, eventPrefix, store.WatchOption{Prefix: true})
	}
	if watchError != nil {
		writeJSONError(responseWriter, "watch: "+watchError.Error(), http.StatusInternalServerError)
		return
	}

	activeWatches.Inc()
	defer activeWatches.Dec()

	responseWriter.Header().Set("Content-Type", "text/event-stream")
	responseWriter.Header().Set("Cache-Control", "no-cache")
	responseWriter.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	for {
		select {
		case <-request.Context().Done():
			return
		case storeEvent, open := <-eventChannel:
			if !open {
				return
			}
			if storeEvent.Type == store.EventDelete {
				continue
			}
			var systemEvent types.SystemEvent
			if err := json.Unmarshal(storeEvent.Fact.Value, &systemEvent); err != nil {
				continue
			}
			if serviceFilter != "" && !strings.Contains(systemEvent.Target, serviceFilter) {
				continue
			}
			eventBytes, _ := json.Marshal(systemEvent)
			_, _ = fmt.Fprintf(responseWriter, "data: %s\n\n", eventBytes)
			flusher.Flush()
		}
	}
}

// handleDiff serves POST /api/diff, accepting DSL configuration and returning
// the list of facts that would be added, modified, or unchanged — without
// writing anything to the store.
func (apiServer *Server) handleDiff(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadRead, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxConfigRequestBodyBytes)

	responseWriter.Header().Set("Content-Type", "application/json")
	requestContext := request.Context()

	var dslContent string
	contentType := request.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var requestBody struct {
			Config string `json:"config"`
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			responseWriter.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "invalid JSON"})
			return
		}
		dslContent = requestBody.Config
	} else {
		rawBody, err := io.ReadAll(request.Body)
		if err != nil {
			responseWriter.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "read body: " + err.Error()})
			return
		}
		dslContent = string(rawBody)
	}

	if dslContent == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": "empty config"})
		return
	}

	changes, diffError := lang.Diff(requestContext, apiServer.factStore, dslContent)
	if diffError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"error": diffError.Error()})
		return
	}

	_ = json.NewEncoder(responseWriter).Encode(changes)
}

// enrollmentRequestBody is the JSON body for POST /api/enroll.
type enrollmentRequestBody struct {
	Token       string   `json:"token"`
	NodeID      string   `json:"node_id"`
	IPAddresses []string `json:"ip_addresses"`
	CSRPEM      string   `json:"csr_pem,omitempty"` // PEM-encoded CSR for CSR-based enrollment
}

// enrollmentResponseBody is the JSON response for POST /api/enroll.
type enrollmentResponseBody struct {
	CertificatePEM  string `json:"certificate_pem,omitempty"`
	PrivateKeyPEM   string `json:"private_key_pem,omitempty"`
	CACertPEM       string `json:"ca_cert_pem,omitempty"`
	SecretMasterKey string `json:"secret_master_key,omitempty"`
	Principal       string `json:"principal,omitempty"`
	Error           string `json:"error,omitempty"`
}

// handleEnroll serves POST /api/enroll for node enrollment. The joining node
// presents a join token and node ID; the server validates the token, issues a
// certificate, binds the node-agent RBAC role, and returns the credentials.
func (apiServer *Server) handleEnroll(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)

	responseWriter.Header().Set("Content-Type", "application/json")

	if apiServer.enrollmentService == nil {
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: "enrollment not available"})
		return
	}

	var requestBody enrollmentRequestBody
	if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: "invalid JSON"})
		return
	}

	if requestBody.Token == "" || requestBody.NodeID == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: "token and node_id required"})
		return
	}

	csrPresentString := "false"
	if requestBody.CSRPEM != "" {
		csrPresentString = "true"
	}
	logging.Default().Info("enrollment request",
		"node_id", requestBody.NodeID,
		"source_ip", request.RemoteAddr,
		"csr_present", csrPresentString,
	)

	var parsedIPAddresses []net.IP
	for _, ipString := range requestBody.IPAddresses {
		if parsedIP := net.ParseIP(ipString); parsedIP != nil {
			parsedIPAddresses = append(parsedIPAddresses, parsedIP)
		}
	}

	enrollmentResponse, enrollmentError := apiServer.enrollmentService.EnrollNode(request.Context(), security.EnrollmentRequest{
		Token:       requestBody.Token,
		NodeID:      requestBody.NodeID,
		IPAddresses: parsedIPAddresses,
		CSRPEM:      []byte(requestBody.CSRPEM),
	})
	if enrollmentError != nil {
		responseWriter.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: enrollmentError.Error()})
		return
	}

	secretMasterKeyHex := ""
	if len(apiServer.secretMasterKey) > 0 {
		secretMasterKeyHex = fmt.Sprintf("%x", apiServer.secretMasterKey)
	}

	_ = json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{
		CertificatePEM:  string(enrollmentResponse.CertificatePEM),
		PrivateKeyPEM:   string(enrollmentResponse.PrivateKeyPEM),
		CACertPEM:       string(enrollmentResponse.CACertPEM),
		SecretMasterKey: secretMasterKeyHex,
		Principal:       enrollmentResponse.Principal,
	})
}

// handleMetric serves POST /api/metric to inject a simulated metric value.
func (apiServer *Server) handleMetric(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadUpdate, security.ScopeCluster) {
		return
	}

	serviceName := request.URL.Query().Get("service")
	metricName := request.URL.Query().Get("metric")
	metricValue := request.URL.Query().Get("value")
	if serviceName == "" || metricName == "" || metricValue == "" {
		writeJSONError(responseWriter, "service, metric, and value required", http.StatusBadRequest)
		return
	}
	if validateError := types.ValidateResourceName(serviceName); validateError != nil {
		writeJSONError(responseWriter, validateError.Error(), http.StatusBadRequest)
		return
	}

	requestContext := request.Context()
	metricKey := types.KeyObservedMetric(serviceName, metricName)
	if _, putError := apiServer.factStore.Put(requestContext, metricKey, []byte(metricValue)); putError != nil {
		logging.Default().Error("failed to store fact", "key", metricKey, "error", putError.Error())
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"ok":      "true",
		"service": serviceName,
		"metric":  metricName,
		"value":   metricValue,
	})
}

// handleActivate serves POST /api/activate?service={name} to manually trigger
// activation for a warm-zero service. This enables non-HTTP activation signals
// (webhooks, CI pipelines, cron jobs) to wake a scaled-to-zero service.
func (apiServer *Server) handleActivate(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityWorkloadUpdate, security.ScopeCluster) {
		return
	}

	serviceName := request.URL.Query().Get("service")
	if serviceName == "" {
		writeJSONError(responseWriter, "service parameter required", http.StatusBadRequest)
		return
	}
	if validateError := types.ValidateResourceName(serviceName); validateError != nil {
		writeJSONError(responseWriter, validateError.Error(), http.StatusBadRequest)
		return
	}

	requestContext := request.Context()
	activationKey := types.KeyDerivedServiceActivationState(serviceName)
	if _, putError := apiServer.factStore.Put(requestContext, activationKey, []byte("activating")); putError != nil {
		logging.Default().Error("failed to store fact", "key", activationKey, "error", putError.Error())
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"ok":      "true",
		"service": serviceName,
		"state":   "activating",
	})
}

// handleOIDCDiscovery serves the OpenID Connect discovery document at
// /.well-known/openid-configuration. Cloud providers fetch this to locate
// the JWKS endpoint for verifying workload identity tokens.
func (apiServer *Server) handleOIDCDiscovery(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	discoveryDocument := apiServer.workloadTokenIssuer.OIDCDiscoveryDocument()
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(discoveryDocument)
}

// isSensitivePrefix returns true if the given prefix or key path refers to
// a sensitive store area that should not be exposed through the public API.
func isSensitivePrefix(prefix string) bool {
	sensitiveKeyPrefixes := []string{"secrets/", "credentials/", "enrollment/token/", "bootstrap/"}
	for _, sensitivePrefix := range sensitiveKeyPrefixes {
		if strings.HasPrefix(prefix, sensitivePrefix) || prefix == sensitivePrefix {
			return true
		}
	}
	return false
}

// handleOIDCJWKS serves the JSON Web Key Set at /oidc/jwks containing the
// public signing key. Cloud providers use this to verify workload token
// signatures during credential exchange.
func (apiServer *Server) handleOIDCJWKS(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	jwksDocument := apiServer.workloadTokenIssuer.JWKSDocument()
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(jwksDocument)
}

// handleSecret serves /api/secret for secret CRUD operations.
// GET /api/secret?name=X returns the decrypted value.
// POST /api/secret?name=X with body = plaintext stores the secret.
// DELETE /api/secret?name=X removes the secret.
// GET /api/secret (no name) lists all secret names.
func (apiServer *Server) handleSecret(responseWriter http.ResponseWriter, request *http.Request) {
	if apiServer.secretStore == nil {
		writeJSONError(responseWriter, "secret store not configured", http.StatusServiceUnavailable)
		return
	}

	secretName := request.URL.Query().Get("name")

	if secretName != "" {
		if validateError := types.ValidateSecretName(secretName); validateError != nil {
			writeJSONError(responseWriter, "invalid secret name: "+validateError.Error(), http.StatusBadRequest)
			return
		}
	}

	switch request.Method {
	case http.MethodGet:
		apiServer.handleSecretGet(responseWriter, request, secretName)
	case http.MethodPost:
		apiServer.handleSecretPut(responseWriter, request, secretName)
	case http.MethodDelete:
		apiServer.handleSecretDelete(responseWriter, request, secretName)
	default:
		writeJSONError(responseWriter, "GET, POST, or DELETE", http.StatusMethodNotAllowed)
	}
}

// handleSecretGet returns a secret value or lists secret names.
func (apiServer *Server) handleSecretGet(responseWriter http.ResponseWriter, request *http.Request, secretName string) {
	requestContext := request.Context()
	if secretName == "" {
		if !apiServer.requireCapability(responseWriter, request, security.CapabilitySecretMetadataRead, security.ScopeCluster) {
			return
		}
		secretNames, listErr := apiServer.secretStore.ListSecrets(requestContext)
		if listErr != nil {
			writeJSONError(responseWriter, listErr.Error(), http.StatusInternalServerError)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"secrets": secretNames})
		return
	}

	if !apiServer.requireCapability(responseWriter, request, security.CapabilitySecretRead, security.ScopeCluster) {
		return
	}
	plaintext, getErr := apiServer.secretStore.GetSecret(requestContext, secretName)
	if getErr != nil {
		writeJSONError(responseWriter, getErr.Error(), http.StatusNotFound)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"name":  secretName,
		"value": string(plaintext),
	})
}

// handleSecretPut stores an encrypted secret value.
func (apiServer *Server) handleSecretPut(responseWriter http.ResponseWriter, request *http.Request, secretName string) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilitySecretWrite, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxConfigRequestBodyBytes)
	if secretName == "" {
		writeJSONError(responseWriter, "name parameter required", http.StatusBadRequest)
		return
	}

	body, readErr := io.ReadAll(request.Body)
	if readErr != nil {
		writeJSONError(responseWriter, "read body", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		writeJSONError(responseWriter, "empty body", http.StatusBadRequest)
		return
	}

	requestContext := request.Context()
	if putErr := apiServer.secretStore.PutSecret(requestContext, secretName, body); putErr != nil {
		writeJSONError(responseWriter, putErr.Error(), http.StatusInternalServerError)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"ok":   "true",
		"name": secretName,
	})
}

// handleSecretDelete removes an encrypted secret.
func (apiServer *Server) handleSecretDelete(responseWriter http.ResponseWriter, request *http.Request, secretName string) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilitySecretWrite, security.ScopeCluster) {
		return
	}
	if secretName == "" {
		writeJSONError(responseWriter, "name parameter required", http.StatusBadRequest)
		return
	}

	requestContext := request.Context()
	if deleteErr := apiServer.secretStore.DeleteSecret(requestContext, secretName); deleteErr != nil {
		writeJSONError(responseWriter, deleteErr.Error(), http.StatusInternalServerError)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"ok":   "true",
		"name": secretName,
	})
}

// handleSecretRotate generates a new master key and re-wraps all stored
// secret DEKs under it. Running agents are not disrupted — they already
// decrypted secrets at startup. New node enrollments receive the new key.
func (apiServer *Server) handleSecretRotate(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if apiServer.secretStore == nil {
		writeJSONError(responseWriter, "secret store not configured", http.StatusServiceUnavailable)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilitySecretWrite, security.ScopeCluster) {
		return
	}

	requestContext := request.Context()

	secretNames, listErr := apiServer.secretStore.ListSecrets(requestContext)
	if listErr != nil {
		writeJSONError(responseWriter, "list secrets: "+listErr.Error(), http.StatusInternalServerError)
		return
	}

	newMasterKey, generateErr := security.GenerateMasterKey()
	if generateErr != nil {
		writeJSONError(responseWriter, "generate key: "+generateErr.Error(), http.StatusInternalServerError)
		return
	}

	newKeyProvider, providerErr := security.NewLocalKeyProvider(newMasterKey)
	if providerErr != nil {
		writeJSONError(responseWriter, "create key provider: "+providerErr.Error(), http.StatusInternalServerError)
		return
	}

	if rotateErr := apiServer.secretStore.RotateKeyProvider(requestContext, newKeyProvider); rotateErr != nil {
		writeJSONError(responseWriter, "rotate: "+rotateErr.Error(), http.StatusInternalServerError)
		return
	}

	apiServer.secretMasterKey = newMasterKey

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{
		"rotated": len(secretNames),
		"message": "master key rotated successfully",
	})
}

// handleUser dispatches /api/user requests for user account CRUD.
// POST creates a user, GET lists users, DELETE removes a user.
func (apiServer *Server) handleUser(responseWriter http.ResponseWriter, request *http.Request) {
	userName := request.URL.Query().Get("name")

	if userName != "" {
		if validateError := types.ValidateResourceName(userName); validateError != nil {
			writeJSONError(responseWriter, "invalid user name: "+validateError.Error(), http.StatusBadRequest)
			return
		}
	}

	switch request.Method {
	case http.MethodPost:
		apiServer.handleUserCreate(responseWriter, request, userName)
	case http.MethodGet:
		apiServer.handleUserList(responseWriter, request, userName)
	case http.MethodDelete:
		apiServer.handleUserDelete(responseWriter, request, userName)
	default:
		writeJSONError(responseWriter, "GET, POST, or DELETE", http.StatusMethodNotAllowed)
	}
}

// handleUserCreate stores a user account in the fact store.
func (apiServer *Server) handleUserCreate(responseWriter http.ResponseWriter, request *http.Request, userName string) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}
	if userName == "" {
		writeJSONError(responseWriter, "name parameter required", http.StatusBadRequest)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	body, readError := io.ReadAll(request.Body)
	if readError != nil {
		writeJSONError(responseWriter, "read body", http.StatusBadRequest)
		return
	}
	var userDefinition struct {
		Team   string   `json:"team"`   // team name for the user account
		Groups []string `json:"groups"` // group memberships for authorization
	}
	if len(body) > 0 {
		if unmarshalError := json.Unmarshal(body, &userDefinition); unmarshalError != nil {
			writeJSONError(responseWriter, "invalid JSON: "+unmarshalError.Error(), http.StatusBadRequest)
			return
		}
	}
	userRecord := map[string]interface{}{
		"team":    userDefinition.Team,
		"groups":  userDefinition.Groups,
		"created": time.Now().UTC().Format(time.RFC3339),
	}
	userJSON, marshalError := json.Marshal(userRecord)
	if marshalError != nil {
		writeJSONError(responseWriter, "marshal user: "+marshalError.Error(), http.StatusInternalServerError)
		return
	}
	requestContext := request.Context()
	if _, putError := apiServer.factStore.Put(requestContext, types.KeyAuthUser(userName), userJSON); putError != nil {
		writeJSONError(responseWriter, "store user: "+putError.Error(), http.StatusInternalServerError)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"ok":   "true",
		"user": userName,
	})
}

// handleUserList returns a single user or all user accounts.
func (apiServer *Server) handleUserList(responseWriter http.ResponseWriter, request *http.Request, userName string) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}
	requestContext := request.Context()
	if userName != "" {
		fact, getError := apiServer.factStore.Get(requestContext, types.KeyAuthUser(userName))
		if getError != nil {
			writeJSONError(responseWriter, "user not found: "+userName, http.StatusNotFound)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write(fact.Value) //nolint:gosec // fact.Value is JSON from the trusted fact store, not user input
		return
	}
	facts, scanError := apiServer.factStore.Scan(requestContext, types.ScanAuthUsers)
	if scanError != nil {
		writeJSONError(responseWriter, "scan users: "+scanError.Error(), http.StatusInternalServerError)
		return
	}
	userNames := make([]string, 0, len(facts))
	for _, fact := range facts {
		extractedName := strings.TrimPrefix(fact.Key, types.ScanAuthUsers)
		if extractedName != "" {
			userNames = append(userNames, extractedName)
		}
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"users": userNames})
}

// handleUserDelete removes a user account from the fact store.
func (apiServer *Server) handleUserDelete(responseWriter http.ResponseWriter, request *http.Request, userName string) {
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}
	if userName == "" {
		writeJSONError(responseWriter, "name parameter required", http.StatusBadRequest)
		return
	}
	requestContext := request.Context()
	if deleteError := apiServer.factStore.Delete(requestContext, types.KeyAuthUser(userName)); deleteError != nil {
		writeJSONError(responseWriter, "delete user: "+deleteError.Error(), http.StatusInternalServerError)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"ok":   "true",
		"user": userName,
	})
}

// handleUserToken mints a JWT for the named user. Requires cluster.admin.
func (apiServer *Server) handleUserToken(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}
	if apiServer.userTokenIssuer == nil {
		writeJSONError(responseWriter, "user token issuer not configured", http.StatusServiceUnavailable)
		return
	}

	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	body, readError := io.ReadAll(request.Body)
	if readError != nil {
		writeJSONError(responseWriter, "read body", http.StatusBadRequest)
		return
	}
	var tokenRequest struct {
		Username string `json:"username"` // name of the user to mint a token for
		TTL      string `json:"ttl"`      // token lifetime as a Go duration string (e.g. "8h")
	}
	if unmarshalError := json.Unmarshal(body, &tokenRequest); unmarshalError != nil {
		writeJSONError(responseWriter, "invalid JSON: "+unmarshalError.Error(), http.StatusBadRequest)
		return
	}
	if tokenRequest.Username == "" {
		writeJSONError(responseWriter, "username required", http.StatusBadRequest)
		return
	}

	requestContext := request.Context()
	userFact, getError := apiServer.factStore.Get(requestContext, types.KeyAuthUser(tokenRequest.Username))
	if getError != nil {
		writeJSONError(responseWriter, "user not found: "+tokenRequest.Username, http.StatusNotFound)
		return
	}

	var userRecord struct {
		Team   string   `json:"team"`   // team membership from user account
		Groups []string `json:"groups"` // group memberships from user account
	}
	_ = json.Unmarshal(userFact.Value, &userRecord)

	tokenTTL := time.Duration(0)
	if tokenRequest.TTL != "" {
		parsedTTL, parseError := time.ParseDuration(tokenRequest.TTL)
		if parseError != nil {
			writeJSONError(responseWriter, "invalid ttl: "+parseError.Error(), http.StatusBadRequest)
			return
		}
		tokenTTL = parsedTTL
	}

	tokenString, mintError := apiServer.userTokenIssuer.MintUserToken(
		tokenRequest.Username, userRecord.Team, userRecord.Groups, tokenTTL)
	if mintError != nil {
		writeJSONError(responseWriter, "mint token: "+mintError.Error(), http.StatusInternalServerError)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(map[string]string{
		"token":    tokenString,
		"username": tokenRequest.Username,
	})
}

// handleAuthWhoami returns the identity of the currently authenticated user.
func (apiServer *Server) handleAuthWhoami(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	principal := security.PrincipalFromContext(request.Context())
	if principal == "" {
		writeJSONError(responseWriter, "not authenticated", http.StatusUnauthorized)
		return
	}
	principalStruct := security.PrincipalStructFromContext(request.Context())
	result := map[string]interface{}{
		"principal": principal,
	}
	for _, attribute := range principalStruct.Attributes {
		if attribute.Key == "team" && attribute.Value != "" {
			result["team"] = attribute.Value
		}
	}
	if len(principalStruct.Groups) > 0 {
		result["groups"] = principalStruct.Groups
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(result)
}

// handleAuthOIDCConfig returns the server's OIDC login parameters for the CLI.
// This endpoint is unauthenticated so `cca login` can discover the IdP before
// the user has a token.
func (apiServer *Server) handleAuthOIDCConfig(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	result := map[string]interface{}{ //nolint:gosec // these are API path strings, not credentials
		"token_endpoint": "/api/user/token",
		"whoami":         "/api/auth/whoami",
	}
	if apiServer.workloadTokenIssuer != nil {
		result["issuer"] = apiServer.workloadTokenIssuer.IssuerURL()
	}
	if apiServer.externalOIDCConfig != nil {
		result["external_oidc_issuer"] = apiServer.externalOIDCConfig.IssuerURL
		result["external_oidc_client_id"] = apiServer.externalOIDCConfig.ClientID
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(result)
}
