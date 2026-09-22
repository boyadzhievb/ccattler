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
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/metrics"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
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
	factStore            store.StateStore
	eventLog             *types.EventLog // eventLog is the optional event log for the /api/logs endpoint.
	enrollmentService    *security.EnrollmentService
	workloadTokenIssuer  *security.WorkloadTokenIssuer
	watchMultiplexer     *WatchMultiplexer
	serverMode           ServerMode
	mux                  *http.ServeMux
	rateLimiter          *RateLimiter
	statusCache          *ResponseCache
	listener             net.Listener
}

// SetEventLog attaches an event log to the server, enabling the /api/logs endpoint.
func (apiServer *Server) SetEventLog(eventLog *types.EventLog) {
	apiServer.eventLog = eventLog
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

// NewServer creates a new API server backed by the given fact store.
func NewServer(factStore store.StateStore) *Server {
	apiServer := &Server{
		factStore:   factStore,
		serverMode:  ServerModeFull,
		mux:         http.NewServeMux(),
		rateLimiter: NewRateLimiter(),
		statusCache: NewResponseCache(2 * time.Second),
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
	go func() {
		if serveError := http.Serve(listener, apiServer.Handler()); serveError != nil && !errors.Is(serveError, net.ErrClosed) {
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
// The returned handler includes tracing and rate limiting middleware.
func (apiServer *Server) Handler() http.Handler {
	return tracing.Middleware(apiServer.rateLimiter.Wrap(apiServer.mux))
}

// registerRoutes sets up all API endpoint handlers.
func (apiServer *Server) registerRoutes() {
	apiServer.mux.HandleFunc("/api/state", apiServer.instrumentedHandler("state", apiServer.handleState))
	apiServer.mux.HandleFunc("/api/apply", apiServer.instrumentedHandler("apply", apiServer.handleApply))
	apiServer.mux.HandleFunc("/api/watch", apiServer.handleWatch)
	apiServer.mux.HandleFunc("/api/scale", apiServer.instrumentedHandler("scale", apiServer.handleScale))
	apiServer.mux.HandleFunc("/api/status", apiServer.instrumentedHandler("status", apiServer.handleStatus))
	apiServer.mux.HandleFunc("/api/logs", apiServer.instrumentedHandler("logs", apiServer.handleLogs))
	apiServer.mux.HandleFunc("/api/describe", apiServer.instrumentedHandler("describe", apiServer.handleDescribe))
	apiServer.mux.HandleFunc("/api/events/stream", apiServer.handleEventStream)
	apiServer.mux.HandleFunc("/api/diff", apiServer.instrumentedHandler("diff", apiServer.handleDiff))
	apiServer.mux.HandleFunc("/api/metric", apiServer.instrumentedHandler("metric", apiServer.handleMetric))
	apiServer.mux.HandleFunc("/api/activate", apiServer.instrumentedHandler("activate", apiServer.handleActivate))
	apiServer.mux.HandleFunc("/healthz", apiServer.handleHealthz)
	apiServer.mux.HandleFunc("/metrics", apiServer.handleMetrics)
}

// handleMetrics serves Prometheus-format metrics. Before emitting the
// registry, it snapshots cluster state into gauges so scrapers see current
// instance/service/node counts without a separate status query.
func (apiServer *Server) handleMetrics(responseWriter http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	status := buildStatusFromStore(ctx, apiServer.factStore)

	instancesByState.Set(0, "pending")
	instancesByState.Set(0, "running")
	instancesByState.Set(0, "failed")
	instancesByState.Set(0, "stopped")
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
	json.NewEncoder(responseWriter).Encode(result)
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
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	if singleKey := request.URL.Query().Get("key"); singleKey != "" {
		if isSensitivePrefix(singleKey) {
			http.Error(responseWriter, "access denied: sensitive prefix", http.StatusForbidden)
			return
		}
		fact, err := apiServer.factStore.Get(requestContext, singleKey)
		if err != nil {
			http.Error(responseWriter, fmt.Sprintf(`{"error":"key not found: %s"}`, singleKey), http.StatusNotFound)
			return
		}
		json.NewEncoder(responseWriter).Encode(factResponse{
			Key: fact.Key, Value: string(fact.Value), Revision: fact.Revision,
		})
		return
	}

	prefix := request.URL.Query().Get("prefix")
	if prefix == "" {
		http.Error(responseWriter, `{"error":"prefix or key parameter required"}`, http.StatusBadRequest)
		return
	}

	if isSensitivePrefix(prefix) {
		http.Error(responseWriter, "access denied: sensitive prefix", http.StatusForbidden)
		return
	}

	facts, err := apiServer.factStore.Scan(requestContext, prefix)
	if err != nil {
		http.Error(responseWriter, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	result := make([]factResponse, 0, len(facts))
	for _, fact := range facts {
		result = append(result, factResponse{
			Key: fact.Key, Value: string(fact.Value), Revision: fact.Revision,
		})
	}
	json.NewEncoder(responseWriter).Encode(result)
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
		http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var dslInput string
	contentType := request.Header.Get("Content-Type")

	if strings.Contains(contentType, "application/json") {
		var body applyRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			json.NewEncoder(responseWriter).Encode(applyResponse{Error: "invalid JSON: " + err.Error()})
			return
		}
		dslInput = body.Config
	} else {
		rawBody, err := io.ReadAll(request.Body)
		if err != nil {
			json.NewEncoder(responseWriter).Encode(applyResponse{Error: "read body: " + err.Error()})
			return
		}
		dslInput = string(rawBody)
	}

	if dslInput == "" {
		json.NewEncoder(responseWriter).Encode(applyResponse{Error: "empty config"})
		return
	}

	file, err := lang.Parse(dslInput)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(applyResponse{Error: "parse: " + err.Error()})
		return
	}

	facts, err := lang.Compile(file)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(applyResponse{Error: "compile: " + err.Error()})
		return
	}

	for _, fact := range facts {
		if _, err := apiServer.factStore.Put(requestContext, fact.Key, []byte(fact.Value)); err != nil {
			responseWriter.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(responseWriter).Encode(applyResponse{Error: "store: " + err.Error()})
			return
		}
	}

	json.NewEncoder(responseWriter).Encode(applyResponse{OK: true, FactsSet: len(facts)})
}

// handleWatch serves GET /api/watch?prefix=... as a Server-Sent Events stream.
// Each store change under the prefix is sent as a JSON-encoded SSE data line.
func (apiServer *Server) handleWatch(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	prefix := request.URL.Query().Get("prefix")
	if prefix == "" {
		http.Error(responseWriter, "prefix parameter required", http.StatusBadRequest)
		return
	}

	if isSensitivePrefix(prefix) {
		http.Error(responseWriter, "access denied: sensitive prefix", http.StatusForbidden)
		return
	}

	flusher, ok := responseWriter.(http.Flusher)
	if !ok {
		http.Error(responseWriter, "streaming not supported", http.StatusInternalServerError)
		return
	}

	watchContext, cancelWatch := context.WithCancel(request.Context())
	defer cancelWatch()

	var eventChannel <-chan store.Event
	var watchError error

	if apiServer.watchMultiplexer != nil {
		eventChannel, watchError = apiServer.watchMultiplexer.Subscribe(watchContext, prefix)
	} else {
		eventChannel, watchError = apiServer.factStore.Watch(watchContext, prefix, store.WatchOption{Prefix: true})
	}
	if watchError != nil {
		http.Error(responseWriter, "watch: "+watchError.Error(), http.StatusInternalServerError)
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
			fmt.Fprintf(responseWriter, "data: %s\n\n", eventBytes)
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
		http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	var body scaleRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": "invalid JSON"})
		return
	}

	if body.Service == "" || body.Instances < 0 {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": "service and non-negative instances required"})
		return
	}
	if validateError := types.ValidateResourceName(body.Service); validateError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": validateError.Error()})
		return
	}

	instancesStr := strconv.Itoa(body.Instances)
	apiServer.factStore.Put(requestContext, types.KeyDesiredServiceInstances(body.Service), []byte(instancesStr))
	apiServer.factStore.Put(requestContext, types.KeyIntentUserServiceInstances(body.Service), []byte(instancesStr))

	json.NewEncoder(responseWriter).Encode(map[string]any{
		"ok":        true,
		"service":   body.Service,
		"instances": body.Instances,
	})
}

// handleStatus serves GET /api/status returning the full cluster status as JSON.
func (apiServer *Server) handleStatus(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")

	if cached := apiServer.statusCache.Get("status"); cached != nil {
		responseWriter.Write(cached)
		return
	}

	requestContext := request.Context()
	status := buildStatusFromStore(requestContext, apiServer.factStore)
	encoded, encodeError := json.Marshal(status)
	if encodeError != nil {
		json.NewEncoder(responseWriter).Encode(status)
		return
	}

	apiServer.statusCache.Set("status", encoded)
	responseWriter.Write(encoded)
}

// handleLogs serves GET /api/logs to query the cluster event log. Supports
// optional query parameters: target (filter by affected resource), kind (filter
// by event kind), and limit (maximum number of events, default 50).
func (apiServer *Server) handleLogs(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")

	if apiServer.eventLog == nil {
		json.NewEncoder(responseWriter).Encode([]types.SystemEvent{})
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

	if targetFilter != "" {
		events, queryError = apiServer.eventLog.ForTarget(requestContext, targetFilter, eventLimit)
	} else if kindFilter != "" {
		events, queryError = apiServer.eventLog.Query(requestContext, kindFilter, eventLimit)
	} else {
		events, queryError = apiServer.eventLog.Query(requestContext, "", eventLimit)
	}

	if queryError != nil {
		http.Error(responseWriter, fmt.Sprintf(`{"error":"%s"}`, queryError), http.StatusInternalServerError)
		return
	}

	if events == nil {
		events = []types.SystemEvent{}
	}
	json.NewEncoder(responseWriter).Encode(events)
}

// handleDescribe serves GET /api/describe returning a detailed single-resource
// view aggregating all related facts, health state, placement, and recent events.
// Query parameters: type (service, node, instance) and name (resource identifier).
func (apiServer *Server) handleDescribe(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	requestContext := request.Context()

	resourceType := request.URL.Query().Get("type")
	resourceName := request.URL.Query().Get("name")
	if resourceType == "" || resourceName == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": "type and name query parameters required"})
		return
	}
	if validateError := types.ValidateResourceName(resourceName); validateError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": validateError.Error()})
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
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": "type must be service, node, or instance"})
		return
	}

	if describeError != nil {
		responseWriter.WriteHeader(http.StatusNotFound)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": describeError.Error()})
		return
	}

	json.NewEncoder(responseWriter).Encode(result)
}

// handleEventStream serves GET /api/events/stream as a Server-Sent Events
// stream of cluster events. New events written to the event log are sent as
// JSON-encoded SSE data lines in real time. Supports optional service query
// parameter to filter events whose target contains the service name.
func (apiServer *Server) handleEventStream(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := responseWriter.(http.Flusher)
	if !ok {
		http.Error(responseWriter, "streaming not supported", http.StatusInternalServerError)
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
		http.Error(responseWriter, "watch: "+watchError.Error(), http.StatusInternalServerError)
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
			fmt.Fprintf(responseWriter, "data: %s\n\n", eventBytes)
			flusher.Flush()
		}
	}
}

// handleDiff serves POST /api/diff, accepting DSL configuration and returning
// the list of facts that would be added, modified, or unchanged — without
// writing anything to the store.
func (apiServer *Server) handleDiff(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}

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
			json.NewEncoder(responseWriter).Encode(map[string]string{"error": "invalid JSON"})
			return
		}
		dslContent = requestBody.Config
	} else {
		rawBody, err := io.ReadAll(request.Body)
		if err != nil {
			responseWriter.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(responseWriter).Encode(map[string]string{"error": "read body: " + err.Error()})
			return
		}
		dslContent = string(rawBody)
	}

	if dslContent == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": "empty config"})
		return
	}

	changes, diffError := lang.Diff(requestContext, apiServer.factStore, dslContent)
	if diffError != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(map[string]string{"error": diffError.Error()})
		return
	}

	json.NewEncoder(responseWriter).Encode(changes)
}

// enrollmentRequestBody is the JSON body for POST /api/enroll.
type enrollmentRequestBody struct {
	Token       string   `json:"token"`
	NodeID      string   `json:"node_id"`
	IPAddresses []string `json:"ip_addresses"`
}

// enrollmentResponseBody is the JSON response for POST /api/enroll.
type enrollmentResponseBody struct {
	CertificatePEM string `json:"certificate_pem,omitempty"`
	PrivateKeyPEM  string `json:"private_key_pem,omitempty"`
	CACertPEM      string `json:"ca_cert_pem,omitempty"`
	Principal      string `json:"principal,omitempty"`
	Error          string `json:"error,omitempty"`
}

// handleEnroll serves POST /api/enroll for node enrollment. The joining node
// presents a join token and node ID; the server validates the token, issues a
// certificate, binds the node-agent RBAC role, and returns the credentials.
func (apiServer *Server) handleEnroll(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")

	if apiServer.enrollmentService == nil {
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: "enrollment not available"})
		return
	}

	var requestBody enrollmentRequestBody
	if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: "invalid JSON"})
		return
	}

	if requestBody.Token == "" || requestBody.NodeID == "" {
		responseWriter.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: "token and node_id required"})
		return
	}

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
	})
	if enrollmentError != nil {
		responseWriter.WriteHeader(http.StatusForbidden)
		json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{Error: enrollmentError.Error()})
		return
	}

	json.NewEncoder(responseWriter).Encode(enrollmentResponseBody{
		CertificatePEM: string(enrollmentResponse.CertificatePEM),
		PrivateKeyPEM:  string(enrollmentResponse.PrivateKeyPEM),
		CACertPEM:      string(enrollmentResponse.CACertPEM),
		Principal:      enrollmentResponse.Principal,
	})
}

// handleMetric serves POST /api/metric to inject a simulated metric value.
func (apiServer *Server) handleMetric(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}

	serviceName := request.URL.Query().Get("service")
	metricName := request.URL.Query().Get("metric")
	metricValue := request.URL.Query().Get("value")
	if serviceName == "" || metricName == "" || metricValue == "" {
		http.Error(responseWriter, `{"error":"service, metric, and value required"}`, http.StatusBadRequest)
		return
	}
	if validateError := types.ValidateResourceName(serviceName); validateError != nil {
		http.Error(responseWriter, fmt.Sprintf(`{"error":%q}`, validateError.Error()), http.StatusBadRequest)
		return
	}

	requestContext := request.Context()
	metricKey := types.KeyObservedMetric(serviceName, metricName)
	apiServer.factStore.Put(requestContext, metricKey, []byte(metricValue))

	responseWriter.Header().Set("Content-Type", "application/json")
	json.NewEncoder(responseWriter).Encode(map[string]string{
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
		http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}

	serviceName := request.URL.Query().Get("service")
	if serviceName == "" {
		http.Error(responseWriter, `{"error":"service parameter required"}`, http.StatusBadRequest)
		return
	}
	if validateError := types.ValidateResourceName(serviceName); validateError != nil {
		http.Error(responseWriter, fmt.Sprintf(`{"error":%q}`, validateError.Error()), http.StatusBadRequest)
		return
	}

	requestContext := request.Context()
	activationKey := types.KeyDerivedServiceActivationState(serviceName)
	apiServer.factStore.Put(requestContext, activationKey, []byte("activating"))

	responseWriter.Header().Set("Content-Type", "application/json")
	json.NewEncoder(responseWriter).Encode(map[string]string{
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
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	discoveryDocument := apiServer.workloadTokenIssuer.OIDCDiscoveryDocument()
	responseWriter.Header().Set("Content-Type", "application/json")
	json.NewEncoder(responseWriter).Encode(discoveryDocument)
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
		http.Error(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}

	jwksDocument := apiServer.workloadTokenIssuer.JWKSDocument()
	responseWriter.Header().Set("Content-Type", "application/json")
	json.NewEncoder(responseWriter).Encode(jwksDocument)
}
