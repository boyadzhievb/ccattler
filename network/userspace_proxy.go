package network

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

// UserSpaceProxy is an HTTP reverse proxy that routes requests to service
// backends using round-robin load balancing. It resolves service names from
// the Host header (stripping the ".ccattler.local" suffix if present) and
// forwards requests to healthy endpoints read from the store.
type UserSpaceProxy struct {
	// resolver provides the current set of endpoints for each service.
	resolver ServiceResolver
	// listenAddress is the host:port the proxy listens on.
	listenAddress string
	// roundRobinCounters tracks the next endpoint index per service.
	roundRobinCounters map[string]*atomic.Uint64
	// countersMutex protects the roundRobinCounters map.
	countersMutex sync.Mutex
}

// NewUserSpaceProxy creates an HTTP reverse proxy that routes requests to
// service backends resolved via the provided ServiceResolver.
func NewUserSpaceProxy(resolver ServiceResolver, listenAddress string) *UserSpaceProxy {
	return &UserSpaceProxy{
		resolver:           resolver,
		listenAddress:      listenAddress,
		roundRobinCounters: make(map[string]*atomic.Uint64),
	}
}

// Start begins listening for HTTP requests and forwarding them to service
// backends. It blocks until the context is cancelled.
func (userSpaceProxy *UserSpaceProxy) Start(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:    userSpaceProxy.listenAddress,
		Handler: userSpaceProxy,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	go func() {
		<-ctx.Done()
		httpServer.Close()
	}()

	if listenError := httpServer.ListenAndServe(); listenError != nil && listenError != http.ErrServerClosed {
		return fmt.Errorf("proxy listen error: %w", listenError)
	}
	return nil
}

// ServeHTTP routes an incoming request to a backend endpoint based on the
// Host header. The service name is extracted by stripping the domain suffix
// and port from the Host header.
func (userSpaceProxy *UserSpaceProxy) ServeHTTP(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
	serviceName := extractServiceName(incomingRequest.Host)
	if serviceName == "" {
		http.Error(responseWriter, "missing Host header", http.StatusBadRequest)
		return
	}

	availableEndpoints, resolveError := userSpaceProxy.resolver.ResolveEndpoints(
		incomingRequest.Context(), serviceName)
	if resolveError != nil || len(availableEndpoints) == 0 {
		http.Error(responseWriter, fmt.Sprintf("no backends for service %q", serviceName),
			http.StatusBadGateway)
		return
	}

	roundRobinCounter := userSpaceProxy.getOrCreateCounter(serviceName)
	currentIndex := roundRobinCounter.Add(1) - 1
	selectedEndpoint := availableEndpoints[int(currentIndex)%len(availableEndpoints)]

	backendURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", selectedEndpoint.IP, selectedEndpoint.Port),
	}

	reverseProxy := &httputil.ReverseProxy{
		Director: func(proxyRequest *http.Request) {
			proxyRequest.URL.Scheme = backendURL.Scheme
			proxyRequest.URL.Host = backendURL.Host
			proxyRequest.Host = incomingRequest.Host
		},
	}

	reverseProxy.ServeHTTP(responseWriter, incomingRequest)
}

// extractServiceName derives the service name from the Host header value.
// It strips the port and the ".ccattler.local" domain suffix if present.
func extractServiceName(hostHeader string) string {
	hostname := hostHeader
	if colonIndex := strings.LastIndex(hostname, ":"); colonIndex >= 0 {
		hostname = hostname[:colonIndex]
	}
	hostname = strings.TrimSuffix(hostname, "."+DefaultDNSDomain)
	return strings.TrimSpace(hostname)
}

// getOrCreateCounter returns the round-robin counter for the named service,
// creating one if it doesn't exist yet.
func (userSpaceProxy *UserSpaceProxy) getOrCreateCounter(serviceName string) *atomic.Uint64 {
	userSpaceProxy.countersMutex.Lock()
	defer userSpaceProxy.countersMutex.Unlock()

	if counter, exists := userSpaceProxy.roundRobinCounters[serviceName]; exists {
		return counter
	}
	newCounter := &atomic.Uint64{}
	userSpaceProxy.roundRobinCounters[serviceName] = newCounter
	return newCounter
}
