package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
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

func TestDescribeService(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	types.WriteService(ctx, factStore, types.Service{
		Name: "web", Image: "nginx:1.28", Instances: 3, Ports: []int{8080},
		CPU: "500m", Memory: "512Mi",
	})
	types.WriteInstance(ctx, factStore, types.Instance{
		ID: "inst-1", Service: "web", State: types.InstanceRunning, IP: "10.0.1.4",
	})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "inst-1", NodeID: "node-a"})
	types.WriteEndpoint(ctx, factStore, types.Endpoint{
		Service: "web", InstanceID: "inst-1", IP: "10.0.1.4", Port: 8080,
	})
	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{Service: "web", VIP: "10.200.0.1", Port: 80})

	resp, err := http.Get(baseURL + "/api/describe?type=service&name=web")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var serviceDetail ServiceDescribe
	json.NewDecoder(resp.Body).Decode(&serviceDetail)

	if serviceDetail.Name != "web" {
		t.Fatalf("expected web, got %s", serviceDetail.Name)
	}
	if serviceDetail.Image != "nginx:1.28" {
		t.Fatalf("expected nginx:1.28, got %s", serviceDetail.Image)
	}
	if serviceDetail.DesiredInstances != 3 {
		t.Fatalf("expected 3 desired, got %d", serviceDetail.DesiredInstances)
	}
	if serviceDetail.RunningInstances != 1 {
		t.Fatalf("expected 1 running, got %d", serviceDetail.RunningInstances)
	}
	if len(serviceDetail.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(serviceDetail.Instances))
	}
	if serviceDetail.Instances[0].ID != "inst-1" {
		t.Fatalf("expected inst-1, got %s", serviceDetail.Instances[0].ID)
	}
	if serviceDetail.Resources == nil || serviceDetail.Resources.CPU != "500m" {
		t.Fatal("expected resources with CPU 500m")
	}
	if serviceDetail.Networking == nil || serviceDetail.Networking.VIP != "10.200.0.1" {
		t.Fatal("expected networking with VIP 10.200.0.1")
	}
	if len(serviceDetail.Endpoints) != 1 || serviceDetail.Endpoints[0] != "10.0.1.4:8080" {
		t.Fatalf("expected endpoint 10.0.1.4:8080, got %v", serviceDetail.Endpoints)
	}
}

func TestDescribeNode(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-a", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 2000, AvailableMemory: 4096,
		Architecture: "amd64", Zone: "us-east-1a",
	})
	factStore.Put(ctx, types.KeyObservedNodeAddress("node-a"), []byte("192.168.1.10"))

	types.WriteInstance(ctx, factStore, types.Instance{
		ID: "inst-1", Service: "web", State: types.InstanceRunning, IP: "10.0.1.4",
	})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "inst-1", NodeID: "node-a"})

	resp, err := http.Get(baseURL + "/api/describe?type=node&name=node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var nodeDetail NodeDescribe
	json.NewDecoder(resp.Body).Decode(&nodeDetail)

	if nodeDetail.ID != "node-a" {
		t.Fatalf("expected node-a, got %s", nodeDetail.ID)
	}
	if nodeDetail.State != "alive" {
		t.Fatalf("expected alive, got %s", nodeDetail.State)
	}
	if nodeDetail.Address != "192.168.1.10" {
		t.Fatalf("expected 192.168.1.10, got %s", nodeDetail.Address)
	}
	if nodeDetail.Architecture != "amd64" {
		t.Fatalf("expected amd64, got %s", nodeDetail.Architecture)
	}
	if nodeDetail.CapacityCPU != 4000 {
		t.Fatalf("expected 4000 cpu, got %d", nodeDetail.CapacityCPU)
	}
	if len(nodeDetail.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(nodeDetail.Instances))
	}
	if nodeDetail.Instances[0].Service != "web" {
		t.Fatalf("expected service web, got %s", nodeDetail.Instances[0].Service)
	}
}

func TestDescribeInstance(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	types.WriteInstance(ctx, factStore, types.Instance{
		ID: "inst-1", Service: "web", State: types.InstanceRunning,
		Image: "nginx:1.28", IP: "10.0.1.4", Health: types.HealthHealthy,
	})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "inst-1", NodeID: "node-a"})
	factStore.Put(ctx, types.KeyObservedInstanceCPU("inst-1"), []byte("120"))
	factStore.Put(ctx, types.KeyObservedInstanceMemory("inst-1"), []byte("64Mi"))
	factStore.Put(ctx, types.KeyObservedInstanceRestarts("inst-1"), []byte("2"))
	types.WriteEndpoint(ctx, factStore, types.Endpoint{
		Service: "web", InstanceID: "inst-1", IP: "10.0.1.4", Port: 8080,
	})

	resp, err := http.Get(baseURL + "/api/describe?type=instance&name=inst-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var instanceDetail InstanceDescribe
	json.NewDecoder(resp.Body).Decode(&instanceDetail)

	if instanceDetail.ID != "inst-1" {
		t.Fatalf("expected inst-1, got %s", instanceDetail.ID)
	}
	if instanceDetail.ServiceName != "web" {
		t.Fatalf("expected web, got %s", instanceDetail.ServiceName)
	}
	if instanceDetail.NodeID != "node-a" {
		t.Fatalf("expected node-a, got %s", instanceDetail.NodeID)
	}
	if instanceDetail.State != "running" {
		t.Fatalf("expected running, got %s", instanceDetail.State)
	}
	if instanceDetail.CPUMillis != "120" {
		t.Fatalf("expected 120, got %s", instanceDetail.CPUMillis)
	}
	if instanceDetail.Restarts != "2" {
		t.Fatalf("expected 2, got %s", instanceDetail.Restarts)
	}
	if instanceDetail.Endpoint != "10.0.1.4:8080" {
		t.Fatalf("expected 10.0.1.4:8080, got %s", instanceDetail.Endpoint)
	}
}

func TestDescribeNotFound(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(baseURL + "/api/describe?type=service&name=nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDescribeMissingParams(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(baseURL + "/api/describe?type=service")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDescribeInvalidType(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(baseURL + "/api/describe?type=unknown&name=foo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDescribeServiceWithEvents(t *testing.T) {
	factStore := store.NewMemoryStore()
	apiServer := NewServer(factStore)
	eventLog := types.NewEventLog(factStore, 100)
	apiServer.SetEventLog(eventLog)

	address, err := apiServer.Start(":0")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer apiServer.Close()
	defer factStore.Close()

	ctx := context.Background()
	types.WriteService(ctx, factStore, types.Service{
		Name: "web", Image: "nginx:1.28", Instances: 1,
	})
	eventLog.Emit(ctx, "service.created", "web", "service web created", "cli")

	baseURL := "http://" + address
	resp, err := http.Get(baseURL + "/api/describe?type=service&name=web")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var serviceDetail ServiceDescribe
	json.NewDecoder(resp.Body).Decode(&serviceDetail)

	if len(serviceDetail.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(serviceDetail.Events))
	}
	if serviceDetail.Events[0].Kind != "service.created" {
		t.Fatalf("expected service.created, got %s", serviceDetail.Events[0].Kind)
	}
}

func TestEventStreamSSE(t *testing.T) {
	factStore := store.NewMemoryStore()
	apiServer := NewServer(factStore)
	eventLog := types.NewEventLog(factStore, 100)
	apiServer.SetEventLog(eventLog)

	address, err := apiServer.Start(":0")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer apiServer.Close()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	baseURL := "http://" + address
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/events/stream", nil)
	httpResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResponse.Body.Close()

	if httpResponse.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", httpResponse.Header.Get("Content-Type"))
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		eventLog.Emit(context.Background(), "instance.created", "service/web", "instance inst-1 created", "instance-controller")
	}()

	scanner := bufio.NewScanner(httpResponse.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		eventJSON := strings.TrimPrefix(line, "data: ")
		var receivedEvent types.SystemEvent
		if err := json.Unmarshal([]byte(eventJSON), &receivedEvent); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if receivedEvent.Kind != "instance.created" {
			t.Fatalf("expected instance.created, got %s", receivedEvent.Kind)
		}
		if receivedEvent.Target != "service/web" {
			t.Fatalf("expected service/web, got %s", receivedEvent.Target)
		}
		return
	}
	t.Fatal("no SSE event received")
}

func TestEventStreamServiceFilter(t *testing.T) {
	factStore := store.NewMemoryStore()
	apiServer := NewServer(factStore)
	eventLog := types.NewEventLog(factStore, 100)
	apiServer.SetEventLog(eventLog)

	address, err := apiServer.Start(":0")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer apiServer.Close()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	baseURL := "http://" + address
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/events/stream?service=web", nil)
	httpResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResponse.Body.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		eventLog.Emit(context.Background(), "instance.created", "service/api", "api instance created", "controller")
		time.Sleep(50 * time.Millisecond)
		eventLog.Emit(context.Background(), "instance.created", "service/web", "web instance created", "controller")
	}()

	scanner := bufio.NewScanner(httpResponse.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		eventJSON := strings.TrimPrefix(line, "data: ")
		var receivedEvent types.SystemEvent
		if err := json.Unmarshal([]byte(eventJSON), &receivedEvent); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if !strings.Contains(receivedEvent.Target, "web") {
			t.Fatalf("expected event for web, got target %s", receivedEvent.Target)
		}
		return
	}
	t.Fatal("no filtered SSE event received")
}

func TestEventStreamMethodNotAllowed(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/events/stream", nil)
	httpResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != 405 {
		t.Fatalf("expected 405, got %d", httpResponse.StatusCode)
	}
}

func TestDiffEndpointAllNew(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	dslConfig := "service web {\n  image nginx:1.28\n  instances 3\n}"
	resp, err := http.Post(baseURL+"/api/diff", "text/plain", strings.NewReader(dslConfig))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var changes []lang.FactChange
	json.NewDecoder(resp.Body).Decode(&changes)

	if len(changes) == 0 {
		t.Fatal("expected changes")
	}
	for _, change := range changes {
		if change.Type != "add" {
			t.Errorf("expected add, got %s for %s", change.Type, change.Key)
		}
	}
}

func TestDiffEndpointDetectsModified(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	types.WriteService(ctx, factStore, types.Service{
		Name: "web", Image: "nginx:1.27", Instances: 2,
	})

	dslConfig := "service web {\n  image nginx:1.28\n  instances 3\n}"
	resp, err := http.Post(baseURL+"/api/diff", "text/plain", strings.NewReader(dslConfig))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var changes []lang.FactChange
	json.NewDecoder(resp.Body).Decode(&changes)

	foundImageModify := false
	for _, change := range changes {
		if change.Key == types.KeyDesiredServiceImage("web") && change.Type == "modify" {
			foundImageModify = true
			if change.OldValue != "nginx:1.27" {
				t.Fatalf("expected old nginx:1.27, got %s", change.OldValue)
			}
		}
	}
	if !foundImageModify {
		t.Fatal("expected image modify change")
	}
}

func TestDiffEndpointInvalidDSL(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Post(baseURL+"/api/diff", "text/plain", strings.NewReader("invalid {{ config"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	baseURL, factStore, cleanup := newTestServer(t)
	defer cleanup()

	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyObservedInstanceState("inst-1"), []byte("running"))
	factStore.Put(ctx, types.KeyObservedInstanceState("inst-2"), []byte("running"))
	factStore.Put(ctx, types.KeyObservedInstanceState("inst-3"), []byte("pending"))

	metricsResponse, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer metricsResponse.Body.Close()

	if metricsResponse.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", metricsResponse.StatusCode)
	}

	metricsBody, _ := io.ReadAll(metricsResponse.Body)
	metricsText := string(metricsBody)

	if !strings.Contains(metricsText, "ccattler_services 1") {
		t.Errorf("expected ccattler_services 1, got:\n%s", metricsText)
	}
	if !strings.Contains(metricsText, "ccattler_instances") {
		t.Error("expected ccattler_instances gauge in output")
	}
	if !strings.Contains(metricsText, "# TYPE ccattler_api_requests_total counter") {
		t.Error("expected api request counter type declaration")
	}
	if !strings.Contains(metricsText, "# TYPE ccattler_api_request_duration_seconds histogram") {
		t.Error("expected API request duration histogram type declaration")
	}
}

func TestHealthzEndpointHealthy(t *testing.T) {
	baseURL, _, cleanup := newTestServer(t)
	defer cleanup()

	healthResponse, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer healthResponse.Body.Close()

	if healthResponse.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", healthResponse.StatusCode)
	}

	var result struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if decodeErr := json.NewDecoder(healthResponse.Body).Decode(&result); decodeErr != nil {
		t.Fatal(decodeErr)
	}

	if result.Status != "healthy" {
		t.Errorf("expected status=healthy, got %s", result.Status)
	}

	storeFound := false
	for _, check := range result.Checks {
		if check.Name == "store" {
			storeFound = true
			if check.Status != "healthy" {
				t.Errorf("expected store=healthy, got %s", check.Status)
			}
		}
	}
	if !storeFound {
		t.Error("expected store check in health response")
	}
}
