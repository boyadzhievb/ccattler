package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newTestServer creates an API server backed by a memory store, starts it on
// a random port, and returns the base URL and a cleanup function.
func newTestServer(t *testing.T) (string, store.StateStore, func()) {
	t.Helper()
	factStore := store.NewMemoryStore()
	apiServer := NewServer(factStore)
	address, err := apiServer.Start(":0")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	baseURL := "http://" + address
	cleanup := func() {
		apiServer.Close()
		factStore.Close()
	}
	return baseURL, factStore, cleanup
}

func TestGetStateByKey(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))

	resp, err := http.Get(baseURL + "/api/state?key=" + types.KeyDesiredServiceImage("web"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var fact factResponse
	json.NewDecoder(resp.Body).Decode(&fact)
	if fact.Value != "nginx:1.28" {
		t.Fatalf("expected nginx:1.28, got %s", fact.Value)
	}
}

func TestGetStateByPrefix(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("golang:1.22"))

	resp, err := http.Get(baseURL + "/api/state?prefix=" + types.ScanDesiredServices)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var facts []factResponse
	json.NewDecoder(resp.Body).Decode(&facts)
	if len(facts) < 3 {
		t.Fatalf("expected at least 3 facts, got %d", len(facts))
	}
}

func TestGetStateMissingKey(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(baseURL + "/api/state?key=/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetStateRequiresParameter(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(baseURL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestApplyDSLConfigJSON(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	dslConfig := "service web {\n  image nginx:1.28\n  instances 3\n  expose 8080\n}"
	body := fmt.Sprintf(`{"config": %q}`, dslConfig)
	resp, err := http.Post(baseURL+"/api/apply", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result applyResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		t.Fatalf("apply failed: %s", result.Error)
	}
	if result.FactsSet == 0 {
		t.Fatal("expected facts_set > 0")
	}

	ctx := context.Background()
	imageFact, err := factStore.Get(ctx, types.KeyDesiredServiceImage("web"))
	if err != nil {
		t.Fatal("image fact not written")
	}
	if string(imageFact.Value) != "nginx:1.28" {
		t.Fatalf("expected nginx:1.28, got %s", string(imageFact.Value))
	}
}

func TestApplyDSLConfigPlainText(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	dslConfig := "service api {\n  image golang:1.22\n  instances 2\n}"
	resp, err := http.Post(baseURL+"/api/apply", "text/plain", strings.NewReader(dslConfig))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result applyResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		t.Fatalf("apply failed: %s", result.Error)
	}

	ctx := context.Background()
	instancesFact, err := factStore.Get(ctx, types.KeyDesiredServiceInstances("api"))
	if err != nil {
		t.Fatal("instances fact not written")
	}
	if string(instancesFact.Value) != "2" {
		t.Fatalf("expected 2, got %s", string(instancesFact.Value))
	}
}

func TestApplyInvalidDSL(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Post(baseURL+"/api/apply", "text/plain", strings.NewReader("invalid {{ config"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestScaleService(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	body := `{"service": "web", "instances": 5}`
	resp, err := http.Post(baseURL+"/api/scale", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ctx := context.Background()
	desiredFact, err := factStore.Get(ctx, types.KeyDesiredServiceInstances("web"))
	if err != nil {
		t.Fatal("desired instances not written")
	}
	if string(desiredFact.Value) != "5" {
		t.Fatalf("expected 5, got %s", string(desiredFact.Value))
	}

	intentFact, err := factStore.Get(ctx, types.KeyIntentUserServiceInstances("web"))
	if err != nil {
		t.Fatal("intent not written")
	}
	if string(intentFact.Value) != "5" {
		t.Fatalf("expected 5, got %s", string(intentFact.Value))
	}
}

func TestScaleValidation(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	body := `{"service": "", "instances": 5}`
	resp, err := http.Post(baseURL+"/api/scale", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMetricInjection(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Post(baseURL+"/api/metric?service=web&metric=cpu&value=85", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ctx := context.Background()
	metricFact, err := factStore.Get(ctx, types.KeyObservedMetric("web", "cpu"))
	if err != nil {
		t.Fatal("metric not written")
	}
	if string(metricFact.Value) != "85" {
		t.Fatalf("expected 85, got %s", string(metricFact.Value))
	}
}

func TestStatusEndpoint(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("3"))

	resp, err := http.Get(baseURL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var status ClusterStatus
	json.NewDecoder(resp.Body).Decode(&status)
	if len(status.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(status.Services))
	}
	if status.Services[0].Name != "web" {
		t.Fatalf("expected web, got %s", status.Services[0].Name)
	}
}

func TestWatchSSE(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/watch?prefix="+types.ScanDesiredServices, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}

	// Write a fact in the background to trigger an SSE event.
	go func() {
		time.Sleep(100 * time.Millisecond)
		factStore.Put(context.Background(), types.KeyDesiredServiceImage("web"), []byte("nginx:1.29"))
	}()

	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	eventData := string(buf[:n])
	if !strings.Contains(eventData, "nginx:1.29") {
		t.Fatalf("expected SSE event with nginx:1.29, got: %s", eventData)
	}
	if !strings.HasPrefix(eventData, "data: ") {
		t.Fatalf("expected SSE data: prefix, got: %s", eventData)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/state"},
		{http.MethodGet, "/api/apply"},
		{http.MethodGet, "/api/scale"},
		{http.MethodGet, "/api/metric"},
		{http.MethodPost, "/api/status"},
		{http.MethodPost, "/api/watch"},
	}

	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, baseURL+tt.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tt.method, tt.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 405 {
			t.Errorf("%s %s: expected 405, got %d", tt.method, tt.path, resp.StatusCode)
		}
	}
}

// newTestServerWithEnrollment creates an API server with a CA and enrollment
// service enabled, returns the base URL, a join token, and a cleanup function.
func newTestServerWithEnrollment(t *testing.T) (string, string, func()) {
	t.Helper()
	factStore := store.NewMemoryStore()
	apiServer := NewServer(factStore)

	certificateAuthority, err := security.NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	enrollmentService := security.NewEnrollmentService(factStore, certificateAuthority, nil, 1*time.Hour)
	apiServer.SetEnrollmentService(enrollmentService)

	joinToken, err := enrollmentService.GenerateJoinToken(context.Background(), "", 15*time.Minute)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	address, err := apiServer.Start(":0")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}

	baseURL := "http://" + address
	cleanup := func() {
		apiServer.Close()
		factStore.Close()
	}
	return baseURL, joinToken.Token, cleanup
}

func TestEnrollNodeSuccess(t *testing.T) {
	baseURL, tokenValue, cleanup := newTestServerWithEnrollment(t)
	defer cleanup()

	requestBody := fmt.Sprintf(`{"token":"%s","node_id":"worker-1","ip_addresses":["192.168.1.10"]}`, tokenValue)
	resp, err := http.Post(baseURL+"/api/enroll", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var enrollResponse enrollmentResponseBody
	json.NewDecoder(resp.Body).Decode(&enrollResponse)

	if enrollResponse.Error != "" {
		t.Fatalf("unexpected error: %s", enrollResponse.Error)
	}
	if enrollResponse.Principal != "node:worker-1" {
		t.Fatalf("expected principal node:worker-1, got %s", enrollResponse.Principal)
	}
	if enrollResponse.CertificatePEM == "" {
		t.Fatal("expected certificate PEM, got empty")
	}
	if enrollResponse.PrivateKeyPEM == "" {
		t.Fatal("expected private key PEM, got empty")
	}
	if enrollResponse.CACertPEM == "" {
		t.Fatal("expected CA cert PEM, got empty")
	}
}

func TestEnrollNodeInvalidToken(t *testing.T) {
	baseURL, _, cleanup := newTestServerWithEnrollment(t)
	defer cleanup()

	requestBody := `{"token":"invalid-token-value","node_id":"worker-1"}`
	resp, err := http.Post(baseURL+"/api/enroll", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	var enrollResponse enrollmentResponseBody
	json.NewDecoder(resp.Body).Decode(&enrollResponse)
	if enrollResponse.Error == "" {
		t.Fatal("expected error message, got empty")
	}
}

func TestEnrollNodeTokenConsumed(t *testing.T) {
	baseURL, tokenValue, cleanup := newTestServerWithEnrollment(t)
	defer cleanup()

	requestBody := fmt.Sprintf(`{"token":"%s","node_id":"worker-1"}`, tokenValue)

	resp, err := http.Post(baseURL+"/api/enroll", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first enrollment expected 200, got %d", resp.StatusCode)
	}

	resp2, err := http.Post(baseURL+"/api/enroll", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 403 {
		t.Fatalf("second enrollment expected 403 (token consumed), got %d", resp2.StatusCode)
	}
}

func TestEnrollNodeMissingFields(t *testing.T) {
	baseURL, _, cleanup := newTestServerWithEnrollment(t)
	defer cleanup()

	resp, err := http.Post(baseURL+"/api/enroll", "application/json", strings.NewReader(`{"token":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for missing node_id, got %d", resp.StatusCode)
	}
}

func TestEnrollEndpointMethodNotAllowed(t *testing.T) {
	baseURL, _, cleanup := newTestServerWithEnrollment(t)
	defer cleanup()

	resp, err := http.Get(baseURL + "/api/enroll")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 405 {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}
