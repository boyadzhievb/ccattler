// Package api implements the CCattler HTTP API server. It provides endpoints
// for reading and querying facts, applying DSL configurations, watching for
// changes via Server-Sent Events, and managing cluster state.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Server is the CCattler HTTP API server that provides endpoints for reading,
// querying, and modifying the fact store.
type Server struct {
	factStore store.StateStore
	mux       *http.ServeMux
	listener  net.Listener
}

// NewServer creates a new API server backed by the given fact store.
func NewServer(factStore store.StateStore) *Server {
	apiServer := &Server{
		factStore: factStore,
		mux:       http.NewServeMux(),
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
	go http.Serve(listener, apiServer.mux)
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
func (apiServer *Server) Handler() http.Handler {
	return apiServer.mux
}

// registerRoutes sets up all API endpoint handlers.
func (apiServer *Server) registerRoutes() {
	apiServer.mux.HandleFunc("/api/state", apiServer.handleState)
	apiServer.mux.HandleFunc("/api/apply", apiServer.handleApply)
	apiServer.mux.HandleFunc("/api/watch", apiServer.handleWatch)
	apiServer.mux.HandleFunc("/api/scale", apiServer.handleScale)
	apiServer.mux.HandleFunc("/api/status", apiServer.handleStatus)
	apiServer.mux.HandleFunc("/api/metric", apiServer.handleMetric)
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

	flusher, ok := responseWriter.(http.Flusher)
	if !ok {
		http.Error(responseWriter, "streaming not supported", http.StatusInternalServerError)
		return
	}

	watchContext, cancelWatch := context.WithCancel(request.Context())
	defer cancelWatch()

	eventChannel, err := apiServer.factStore.Watch(watchContext, prefix, store.WatchOption{Prefix: true})
	if err != nil {
		http.Error(responseWriter, "watch: "+err.Error(), http.StatusInternalServerError)
		return
	}

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

	requestContext := request.Context()
	responseWriter.Header().Set("Content-Type", "application/json")

	status := buildStatusFromStore(requestContext, apiServer.factStore)
	json.NewEncoder(responseWriter).Encode(status)
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
