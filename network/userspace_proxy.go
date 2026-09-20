package network

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// UserSpaceProxy is an HTTP reverse proxy that routes requests to service
// backends using round-robin load balancing. It resolves service names from
// the Host header (stripping the ".ccattler.local" suffix if present) and
// forwards requests to healthy endpoints read from the store.
//
// When warm-zero is enabled for a service (min=0 with idle_timeout configured),
// the proxy triggers activation on cold requests and buffers connections until
// a backend becomes available.
type UserSpaceProxy struct {
	// resolver provides the current set of endpoints for each service.
	resolver ServiceResolver
	// factStore provides direct access to the store for reading warm-zero
	// configuration and writing activation state + last request timestamps.
	factStore store.StateStore
	// listenAddress is the host:port the proxy listens on.
	listenAddress string
	// roundRobinCounters tracks the next endpoint index per service.
	roundRobinCounters map[string]*atomic.Uint64
	// countersMutex protects the roundRobinCounters map.
	countersMutex sync.Mutex
	// activationChannels holds per-service channels for coordinating concurrent
	// cold-activation requests. The channel is closed when endpoints become
	// available, unblocking all waiting goroutines simultaneously.
	activationChannels map[string]chan struct{}
	// activationMutex protects the activationChannels map.
	activationMutex sync.Mutex
	// lastRequestWriteTime tracks the last time we wrote a last_request_time
	// fact per service, to throttle store writes to at most once per second.
	lastRequestWriteTime map[string]time.Time
	// lastRequestMutex protects lastRequestWriteTime.
	lastRequestMutex sync.Mutex
	// timeNow is injectable for testing.
	timeNow func() time.Time
}

// NewUserSpaceProxy creates an HTTP reverse proxy that routes requests to
// service backends resolved via the provided ServiceResolver. If factStore
// is non-nil, warm-zero activation support is enabled.
func NewUserSpaceProxy(resolver ServiceResolver, listenAddress string, factStore store.StateStore) *UserSpaceProxy {
	return &UserSpaceProxy{
		resolver:             resolver,
		factStore:            factStore,
		listenAddress:        listenAddress,
		roundRobinCounters:   make(map[string]*atomic.Uint64),
		activationChannels:   make(map[string]chan struct{}),
		lastRequestWriteTime: make(map[string]time.Time),
		timeNow:              time.Now,
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
// and port from the Host header. If no endpoints are available and the
// service has warm-zero enabled, the proxy triggers activation and waits
// for a backend to become ready.
func (userSpaceProxy *UserSpaceProxy) ServeHTTP(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
	serviceName := extractServiceName(incomingRequest.Host)
	if serviceName == "" {
		http.Error(responseWriter, "missing Host header", http.StatusBadRequest)
		return
	}

	availableEndpoints, resolveError := userSpaceProxy.resolver.ResolveEndpoints(
		incomingRequest.Context(), serviceName)
	if resolveError != nil {
		http.Error(responseWriter, fmt.Sprintf("no backends for service %q", serviceName),
			http.StatusBadGateway)
		return
	}

	if len(availableEndpoints) == 0 {
		if userSpaceProxy.isWarmZeroEnabled(incomingRequest.Context(), serviceName) {
			userSpaceProxy.handleColdActivation(responseWriter, incomingRequest, serviceName)
			return
		}
		http.Error(responseWriter, fmt.Sprintf("no backends for service %q", serviceName),
			http.StatusBadGateway)
		return
	}

	userSpaceProxy.recordLastRequestTime(incomingRequest.Context(), serviceName)
	userSpaceProxy.forwardToBackend(responseWriter, incomingRequest, serviceName, availableEndpoints)
}

// isWarmZeroEnabled checks whether the given service has warm-zero configured
// by looking for the idle_timeout fact in the store.
func (userSpaceProxy *UserSpaceProxy) isWarmZeroEnabled(ctx context.Context, serviceName string) bool {
	if userSpaceProxy.factStore == nil {
		return false
	}
	idleTimeoutFact, getError := userSpaceProxy.factStore.Get(ctx, types.KeyDesiredServiceScaleIdleTimeout(serviceName))
	if getError != nil || idleTimeoutFact == nil {
		return false
	}
	return len(idleTimeoutFact.Value) > 0
}

// handleColdActivation triggers activation for a scaled-to-zero service and
// waits for endpoints to appear. Concurrent requests share a single activation
// channel — the first request triggers the state write and all subsequent
// requests block on the same channel until endpoints are ready.
func (userSpaceProxy *UserSpaceProxy) handleColdActivation(
	responseWriter http.ResponseWriter,
	incomingRequest *http.Request,
	serviceName string,
) {
	activationTimeout := userSpaceProxy.getActivationTimeout(incomingRequest.Context(), serviceName)

	activationChannel := userSpaceProxy.getOrCreateActivationChannel(serviceName)

	userSpaceProxy.writeActivatingState(incomingRequest.Context(), serviceName)

	pollTicker := time.NewTicker(500 * time.Millisecond)
	defer pollTicker.Stop()

	timeoutTimer := time.NewTimer(activationTimeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-activationChannel:
			endpoints, resolveError := userSpaceProxy.resolver.ResolveEndpoints(
				incomingRequest.Context(), serviceName)
			if resolveError != nil || len(endpoints) == 0 {
				http.Error(responseWriter,
					fmt.Sprintf("service %q activation completed but no endpoints found", serviceName),
					http.StatusServiceUnavailable)
				return
			}
			userSpaceProxy.recordLastRequestTime(incomingRequest.Context(), serviceName)
			userSpaceProxy.forwardToBackend(responseWriter, incomingRequest, serviceName, endpoints)
			return

		case <-pollTicker.C:
			endpoints, resolveError := userSpaceProxy.resolver.ResolveEndpoints(
				incomingRequest.Context(), serviceName)
			if resolveError == nil && len(endpoints) > 0 {
				userSpaceProxy.signalActivationReady(serviceName)
				userSpaceProxy.recordLastRequestTime(incomingRequest.Context(), serviceName)
				userSpaceProxy.forwardToBackend(responseWriter, incomingRequest, serviceName, endpoints)
				return
			}

		case <-timeoutTimer.C:
			http.Error(responseWriter,
				fmt.Sprintf("service %q is activating — not ready within %s", serviceName, activationTimeout),
				http.StatusServiceUnavailable)
			userSpaceProxy.clearActivationChannel(serviceName)
			return

		case <-incomingRequest.Context().Done():
			return
		}
	}
}

// getActivationTimeout reads the activation_timeout fact for the service and
// returns it as a time.Duration. Defaults to 30 seconds if not configured.
func (userSpaceProxy *UserSpaceProxy) getActivationTimeout(ctx context.Context, serviceName string) time.Duration {
	if userSpaceProxy.factStore == nil {
		return 30 * time.Second
	}
	timeoutFact, getError := userSpaceProxy.factStore.Get(ctx, types.KeyDesiredServiceScaleActivationTimeout(serviceName))
	if getError != nil || timeoutFact == nil {
		return 30 * time.Second
	}
	seconds := parseDurationSecondsFromString(string(timeoutFact.Value))
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 30 * time.Second
}

// parseDurationSecondsFromString parses a duration string like "60s" or "5m"
// into seconds. Returns 0 if the string is not a recognized format.
func parseDurationSecondsFromString(durationValue string) int {
	if strings.HasSuffix(durationValue, "s") {
		parsedValue, parseError := strconv.Atoi(strings.TrimSuffix(durationValue, "s"))
		if parseError == nil {
			return parsedValue
		}
	}
	if strings.HasSuffix(durationValue, "m") {
		parsedValue, parseError := strconv.Atoi(strings.TrimSuffix(durationValue, "m"))
		if parseError == nil {
			return parsedValue * 60
		}
	}
	plainValue, parseError := strconv.Atoi(durationValue)
	if parseError == nil {
		return plainValue
	}
	return 0
}

// writeActivatingState writes the activation state fact for a service if it's
// not already activating or active.
func (userSpaceProxy *UserSpaceProxy) writeActivatingState(ctx context.Context, serviceName string) {
	if userSpaceProxy.factStore == nil {
		return
	}
	currentState, getError := userSpaceProxy.factStore.Get(ctx, types.KeyDerivedServiceActivationState(serviceName))
	if getError == nil && currentState != nil {
		stateValue := string(currentState.Value)
		if stateValue == "activating" || stateValue == "active" {
			return
		}
	}
	userSpaceProxy.factStore.Put(ctx, types.KeyDerivedServiceActivationState(serviceName), []byte("activating"))
}

// recordLastRequestTime writes the current timestamp to the last_request_time
// fact for the service. Throttled to at most one store write per second per
// service to avoid excessive store load under high request rates.
func (userSpaceProxy *UserSpaceProxy) recordLastRequestTime(ctx context.Context, serviceName string) {
	if userSpaceProxy.factStore == nil {
		return
	}

	currentTime := userSpaceProxy.timeNow()

	userSpaceProxy.lastRequestMutex.Lock()
	lastWrite := userSpaceProxy.lastRequestWriteTime[serviceName]
	if currentTime.Sub(lastWrite) < time.Second {
		userSpaceProxy.lastRequestMutex.Unlock()
		return
	}
	userSpaceProxy.lastRequestWriteTime[serviceName] = currentTime
	userSpaceProxy.lastRequestMutex.Unlock()

	timestampMillis := strconv.FormatInt(currentTime.UnixMilli(), 10)
	userSpaceProxy.factStore.Put(ctx, types.KeyObservedServiceLastRequestTime(serviceName), []byte(timestampMillis))
}

// getOrCreateActivationChannel returns the shared activation channel for a
// service, creating one if it doesn't exist.
func (userSpaceProxy *UserSpaceProxy) getOrCreateActivationChannel(serviceName string) chan struct{} {
	userSpaceProxy.activationMutex.Lock()
	defer userSpaceProxy.activationMutex.Unlock()

	if channel, exists := userSpaceProxy.activationChannels[serviceName]; exists {
		return channel
	}
	channel := make(chan struct{})
	userSpaceProxy.activationChannels[serviceName] = channel
	return channel
}

// signalActivationReady closes the activation channel for a service, unblocking
// all waiting goroutines, and removes it from the map so future activations
// get a fresh channel.
func (userSpaceProxy *UserSpaceProxy) signalActivationReady(serviceName string) {
	userSpaceProxy.activationMutex.Lock()
	defer userSpaceProxy.activationMutex.Unlock()

	if channel, exists := userSpaceProxy.activationChannels[serviceName]; exists {
		select {
		case <-channel:
			// already closed
		default:
			close(channel)
		}
		delete(userSpaceProxy.activationChannels, serviceName)
	}
}

// clearActivationChannel removes the activation channel on timeout so that
// subsequent requests start a fresh activation attempt.
func (userSpaceProxy *UserSpaceProxy) clearActivationChannel(serviceName string) {
	userSpaceProxy.activationMutex.Lock()
	defer userSpaceProxy.activationMutex.Unlock()

	if channel, exists := userSpaceProxy.activationChannels[serviceName]; exists {
		select {
		case <-channel:
		default:
			close(channel)
		}
		delete(userSpaceProxy.activationChannels, serviceName)
	}
}

// forwardToBackend selects a backend via round-robin and proxies the request.
func (userSpaceProxy *UserSpaceProxy) forwardToBackend(
	responseWriter http.ResponseWriter,
	incomingRequest *http.Request,
	serviceName string,
	availableEndpoints []types.Endpoint,
) {
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
