package network

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// mockResolver implements ServiceResolver for testing.
type mockResolver struct {
	endpoints map[string][]types.Endpoint
	vips      map[string]string
}

func (mockResolverInstance *mockResolver) ResolveEndpoints(_ context.Context, serviceName string) ([]types.Endpoint, error) {
	endpoints, exists := mockResolverInstance.endpoints[serviceName]
	if !exists {
		return nil, nil
	}
	return endpoints, nil
}

func (mockResolverInstance *mockResolver) ResolveVIP(_ context.Context, serviceName string) (string, error) {
	vip, exists := mockResolverInstance.vips[serviceName]
	if !exists {
		return "", fmt.Errorf("no VIP for %s", serviceName)
	}
	return vip, nil
}

func TestUserSpaceProxyRoutesToBackend(t *testing.T) {
	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		responseWriter.Write([]byte("backend-response"))
	}))
	defer backendServer.Close()

	backendHost, backendPort := parseHostPort(backendServer.URL)

	resolver := &mockResolver{
		endpoints: map[string][]types.Endpoint{
			"web": {{Service: "web", InstanceID: "aaa", IP: backendHost, Port: backendPort}},
		},
	}

	proxy := NewUserSpaceProxy(resolver, ":0", nil)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	request, _ := http.NewRequest("GET", proxyServer.URL+"/hello", nil)
	request.Host = "web.ccattler.local"

	response, requestError := http.DefaultClient.Do(request)
	if requestError != nil {
		t.Fatal(requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", response.StatusCode)
	}
}

func TestUserSpaceProxyRoundRobin(t *testing.T) {
	hitCounts := make(map[string]int)

	backendA := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		hitCounts["A"]++
		responseWriter.Write([]byte("A"))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		hitCounts["B"]++
		responseWriter.Write([]byte("B"))
	}))
	defer backendB.Close()

	hostA, portA := parseHostPort(backendA.URL)
	hostB, portB := parseHostPort(backendB.URL)

	resolver := &mockResolver{
		endpoints: map[string][]types.Endpoint{
			"web": {
				{Service: "web", InstanceID: "aaa", IP: hostA, Port: portA},
				{Service: "web", InstanceID: "bbb", IP: hostB, Port: portB},
			},
		},
	}

	proxy := NewUserSpaceProxy(resolver, ":0", nil)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	for requestIndex := 0; requestIndex < 10; requestIndex++ {
		request, _ := http.NewRequest("GET", proxyServer.URL, nil)
		request.Host = "web"
		response, requestError := http.DefaultClient.Do(request)
		if requestError != nil {
			t.Fatal(requestError)
		}
		response.Body.Close()
	}

	if hitCounts["A"] != 5 || hitCounts["B"] != 5 {
		t.Errorf("round-robin distribution: A=%d B=%d, want A=5 B=5", hitCounts["A"], hitCounts["B"])
	}
}

func TestUserSpaceProxyNoBackends(t *testing.T) {
	resolver := &mockResolver{
		endpoints: map[string][]types.Endpoint{},
	}

	proxy := NewUserSpaceProxy(resolver, ":0", nil)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	request, _ := http.NewRequest("GET", proxyServer.URL, nil)
	request.Host = "unknown-service"

	response, requestError := http.DefaultClient.Do(request)
	if requestError != nil {
		t.Fatal(requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status: got %d, want 502", response.StatusCode)
	}
}

func TestUserSpaceProxyMissingHost(t *testing.T) {
	resolver := &mockResolver{endpoints: map[string][]types.Endpoint{}}
	proxy := NewUserSpaceProxy(resolver, ":0", nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)
	request.Host = ""

	proxy.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", recorder.Code)
	}
}

func TestExtractServiceName(t *testing.T) {
	testCases := []struct {
		hostHeader          string
		expectedServiceName string
	}{
		{"web.ccattler.local", "web"},
		{"web.ccattler.local:8080", "web"},
		{"web", "web"},
		{"web:80", "web"},
		{"java-webapp.ccattler.local", "java-webapp"},
		{"api", "api"},
	}

	for _, testCase := range testCases {
		extractedName := extractServiceName(testCase.hostHeader)
		if extractedName != testCase.expectedServiceName {
			t.Errorf("extractServiceName(%q): got %q, want %q",
				testCase.hostHeader, extractedName, testCase.expectedServiceName)
		}
	}
}

func TestProxyRejectsPathTraversalHost(t *testing.T) {
	resolver := &mockResolver{endpoints: map[string][]types.Endpoint{}}
	proxy := NewUserSpaceProxy(resolver, ":0", nil)

	maliciousHosts := []string{
		"../admin",
		"../../desired/service/web",
		"web/../../etc/passwd",
		"Web", // uppercase
		"web service", // space
	}

	for _, hostValue := range maliciousHosts {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("GET", "/", nil)
		request.Host = hostValue

		proxy.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("Host %q: got %d, want 400", hostValue, recorder.Code)
		}
	}
}

func TestProxyWarmZeroColdActivation(t *testing.T) {
	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		responseWriter.Write([]byte("activated-response"))
	}))
	defer backendServer.Close()

	backendHost, backendPort := parseHostPort(backendServer.URL)

	endpointsMutex := &sync.Mutex{}
	endpointsAvailable := false

	resolver := &dynamicMockResolver{
		resolveFunc: func(ctx context.Context, serviceName string) ([]types.Endpoint, error) {
			endpointsMutex.Lock()
			defer endpointsMutex.Unlock()
			if endpointsAvailable {
				return []types.Endpoint{
					{Service: "api", InstanceID: "inst-1", IP: backendHost, Port: backendPort},
				}, nil
			}
			return nil, nil
		},
	}

	factStore := store.NewMemoryStore()
	factStore.Put(context.Background(), types.KeyDesiredServiceScaleIdleTimeout("api"), []byte("5m"))
	factStore.Put(context.Background(), types.KeyDesiredServiceScaleActivationTimeout("api"), []byte("5s"))

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	go func() {
		time.Sleep(600 * time.Millisecond)
		endpointsMutex.Lock()
		endpointsAvailable = true
		endpointsMutex.Unlock()
	}()

	request, _ := http.NewRequest("GET", proxyServer.URL+"/hello", nil)
	request.Host = "api.ccattler.local"

	response, requestError := http.DefaultClient.Do(request)
	if requestError != nil {
		t.Fatal(requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", response.StatusCode)
	}

	activationFact, _ := factStore.Get(context.Background(), types.KeyDerivedServiceActivationState("api"))
	if activationFact == nil || string(activationFact.Value) != "activating" {
		t.Errorf("expected activation state to be 'activating', got %v", activationFact)
	}

	lastRequestFact, _ := factStore.Get(context.Background(), types.KeyObservedServiceLastRequestTime("api"))
	if lastRequestFact == nil {
		t.Error("expected last_request_time to be set")
	}
}

func TestProxyWarmZeroTimeout503(t *testing.T) {
	resolver := &dynamicMockResolver{
		resolveFunc: func(ctx context.Context, serviceName string) ([]types.Endpoint, error) {
			return nil, nil
		},
	}

	factStore := store.NewMemoryStore()
	factStore.Put(context.Background(), types.KeyDesiredServiceScaleIdleTimeout("api"), []byte("5m"))
	factStore.Put(context.Background(), types.KeyDesiredServiceScaleActivationTimeout("api"), []byte("2s"))

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	request, _ := http.NewRequest("GET", proxyServer.URL, nil)
	request.Host = "api"

	response, requestError := http.DefaultClient.Do(request)
	if requestError != nil {
		t.Fatal(requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", response.StatusCode)
	}
}

func TestProxyNonWarmZeroReturns502(t *testing.T) {
	resolver := &dynamicMockResolver{
		resolveFunc: func(ctx context.Context, serviceName string) ([]types.Endpoint, error) {
			return nil, nil
		},
	}

	factStore := store.NewMemoryStore()

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	request, _ := http.NewRequest("GET", proxyServer.URL, nil)
	request.Host = "regular-service"

	response, requestError := http.DefaultClient.Do(request)
	if requestError != nil {
		t.Fatal(requestError)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status: got %d, want 502", response.StatusCode)
	}
}

func TestProxyConcurrentActivation(t *testing.T) {
	backendServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		responseWriter.Write([]byte("ok"))
	}))
	defer backendServer.Close()

	backendHost, backendPort := parseHostPort(backendServer.URL)

	endpointsMutex := &sync.Mutex{}
	endpointsAvailable := false

	resolver := &dynamicMockResolver{
		resolveFunc: func(ctx context.Context, serviceName string) ([]types.Endpoint, error) {
			endpointsMutex.Lock()
			defer endpointsMutex.Unlock()
			if endpointsAvailable {
				return []types.Endpoint{
					{Service: "api", InstanceID: "inst-1", IP: backendHost, Port: backendPort},
				}, nil
			}
			return nil, nil
		},
	}

	factStore := store.NewMemoryStore()
	factStore.Put(context.Background(), types.KeyDesiredServiceScaleIdleTimeout("api"), []byte("5m"))
	factStore.Put(context.Background(), types.KeyDesiredServiceScaleActivationTimeout("api"), []byte("5s"))

	proxy := NewUserSpaceProxy(resolver, ":0", factStore)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	go func() {
		time.Sleep(800 * time.Millisecond)
		endpointsMutex.Lock()
		endpointsAvailable = true
		endpointsMutex.Unlock()
	}()

	concurrentRequestCount := 5
	responseChannel := make(chan int, concurrentRequestCount)

	for requestIndex := 0; requestIndex < concurrentRequestCount; requestIndex++ {
		go func() {
			request, _ := http.NewRequest("GET", proxyServer.URL, nil)
			request.Host = "api"
			response, requestError := http.DefaultClient.Do(request)
			if requestError != nil {
				responseChannel <- 0
				return
			}
			response.Body.Close()
			responseChannel <- response.StatusCode
		}()
	}

	successCount := 0
	for requestIndex := 0; requestIndex < concurrentRequestCount; requestIndex++ {
		statusCode := <-responseChannel
		if statusCode == http.StatusOK {
			successCount++
		}
	}

	if successCount != concurrentRequestCount {
		t.Errorf("expected all %d concurrent requests to succeed, got %d", concurrentRequestCount, successCount)
	}
}

func TestParseDurationSecondsFromString(t *testing.T) {
	testCases := []struct {
		input    string
		expected int
	}{
		{"60s", 60},
		{"5m", 300},
		{"120s", 120},
		{"30", 30},
		{"invalid", 0},
		{"", 0},
	}

	for _, testCase := range testCases {
		result := parseDurationSecondsFromString(testCase.input)
		if result != testCase.expected {
			t.Errorf("parseDurationSecondsFromString(%q): got %d, want %d",
				testCase.input, result, testCase.expected)
		}
	}
}

// dynamicMockResolver implements ServiceResolver with a custom resolve function.
type dynamicMockResolver struct {
	resolveFunc func(ctx context.Context, serviceName string) ([]types.Endpoint, error)
}

func (dynamicResolver *dynamicMockResolver) ResolveEndpoints(ctx context.Context, serviceName string) ([]types.Endpoint, error) {
	return dynamicResolver.resolveFunc(ctx, serviceName)
}

func (dynamicResolver *dynamicMockResolver) ResolveVIP(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// parseHostPort splits an httptest.Server URL into host and port.
func parseHostPort(serverURL string) (string, int) {
	// serverURL is like "http://127.0.0.1:12345"
	trimmed := serverURL[len("http://"):]
	colonIndex := len(trimmed) - 1
	for colonIndex >= 0 && trimmed[colonIndex] != ':' {
		colonIndex--
	}
	host := trimmed[:colonIndex]
	port := 0
	for _, character := range trimmed[colonIndex+1:] {
		port = port*10 + int(character-'0')
	}
	return host, port
}
