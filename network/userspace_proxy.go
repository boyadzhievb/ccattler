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

	"github.com/boyadzhievb/ccattler/metrics"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

var (
	activationTotal = metrics.DefaultRegistry.RegisterCounter(
		"ccattler_activation_total",
		"Total number of warm-zero activations by outcome",
		"service", "outcome",
	)
	activationDuration = metrics.DefaultRegistry.RegisterHistogram(
		"ccattler_activation_duration_seconds",
		"Cold-start activation latency in seconds",
		[]float64{0.5, 1, 2, 5, 10, 30, 60},
		"service",
	)
	activationWaitingGauge = metrics.DefaultRegistry.RegisterGauge(
		"ccattler_activation_waiting_requests",
		"Number of requests currently waiting for cold activation",
		"service",
	)
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
	// activationWaiters tracks how many goroutines are currently waiting for
	// cold activation per service. Used to cap concurrent waits and prevent DoS.
	activationWaiters map[string]*atomic.Int64
	// waitersMutex protects the activationWaiters map.
	waitersMutex sync.Mutex
	// warmZeroCache caches per-service warm-zero enabled status to avoid
	// hitting the store on every request. Entries expire after warmZeroCacheTTL.
	warmZeroCache map[string]warmZeroCacheEntry
	// warmZeroCacheMutex protects warmZeroCache.
	warmZeroCacheMutex sync.RWMutex
	// timeNow is injectable for testing.
	timeNow func() time.Time
}

// warmZeroCacheEntry holds a cached warm-zero status with its expiration.
type warmZeroCacheEntry struct {
	enabled   bool
	expiresAt time.Time
}

// warmZeroCacheTTL is how long warm-zero enabled status is cached per service.
const warmZeroCacheTTL = 5 * time.Second

// maxActivationWaiters is the maximum number of concurrent requests that can
// wait for a single service's cold activation. Beyond this, requests get 503.
const maxActivationWaiters = 100

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
		activationWaiters:    make(map[string]*atomic.Int64),
		warmZeroCache:        make(map[string]warmZeroCacheEntry),
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
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()
		httpServer.Shutdown(shutdownContext)
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
	if types.ValidateResourceName(serviceName) != nil {
		http.Error(responseWriter, "invalid service name in Host header", http.StatusBadRequest)
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

	currentTime := userSpaceProxy.timeNow()

	userSpaceProxy.warmZeroCacheMutex.RLock()
	cached, cacheHit := userSpaceProxy.warmZeroCache[serviceName]
	userSpaceProxy.warmZeroCacheMutex.RUnlock()

	if cacheHit && currentTime.Before(cached.expiresAt) {
		return cached.enabled
	}

	idleTimeoutFact, getError := userSpaceProxy.factStore.Get(ctx, types.KeyDesiredServiceScaleIdleTimeout(serviceName))
	enabled := getError == nil && idleTimeoutFact != nil && len(idleTimeoutFact.Value) > 0

	userSpaceProxy.warmZeroCacheMutex.Lock()
	userSpaceProxy.warmZeroCache[serviceName] = warmZeroCacheEntry{
		enabled:   enabled,
		expiresAt: currentTime.Add(warmZeroCacheTTL),
	}
	userSpaceProxy.warmZeroCacheMutex.Unlock()

	return enabled
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
	waiterCount := userSpaceProxy.getOrCreateWaiterCounter(serviceName)
	currentWaiters := waiterCount.Add(1)
	defer waiterCount.Add(-1)

	activationWaitingGauge.Inc(serviceName)
	defer activationWaitingGauge.Dec(serviceName)

	if currentWaiters > maxActivationWaiters {
		activationTotal.Inc(serviceName, "overloaded")
		http.Error(responseWriter,
			fmt.Sprintf("service %q activation overloaded — %d requests waiting, max %d",
				serviceName, currentWaiters, maxActivationWaiters),
			http.StatusServiceUnavailable)
		return
	}

	activationStartTime := userSpaceProxy.timeNow()

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
				activationTotal.Inc(serviceName, "error")
				http.Error(responseWriter,
					fmt.Sprintf("service %q activation completed but no endpoints found", serviceName),
					http.StatusServiceUnavailable)
				return
			}
			activationTotal.Inc(serviceName, "success")
			activationDuration.ObserveSince(activationStartTime, serviceName)
			userSpaceProxy.recordLastRequestTime(incomingRequest.Context(), serviceName)
			userSpaceProxy.forwardToBackend(responseWriter, incomingRequest, serviceName, endpoints)
			return

		case <-pollTicker.C:
			endpoints, resolveError := userSpaceProxy.resolver.ResolveEndpoints(
				incomingRequest.Context(), serviceName)
			if resolveError == nil && len(endpoints) > 0 {
				userSpaceProxy.signalActivationReady(serviceName)
				activationTotal.Inc(serviceName, "success")
				activationDuration.ObserveSince(activationStartTime, serviceName)
				userSpaceProxy.recordLastRequestTime(incomingRequest.Context(), serviceName)
				userSpaceProxy.forwardToBackend(responseWriter, incomingRequest, serviceName, endpoints)
				return
			}

		case <-timeoutTimer.C:
			activationTotal.Inc(serviceName, "timeout")
			activationDuration.ObserveSince(activationStartTime, serviceName)
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

// parseDurationSecondsFromString delegates to types.ParseDurationSeconds.
func parseDurationSecondsFromString(durationValue string) int {
	return types.ParseDurationSeconds(durationValue)
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

// getOrCreateWaiterCounter returns the atomic counter tracking how many
// goroutines are waiting for cold activation of the named service.
func (userSpaceProxy *UserSpaceProxy) getOrCreateWaiterCounter(serviceName string) *atomic.Int64 {
	userSpaceProxy.waitersMutex.Lock()
	defer userSpaceProxy.waitersMutex.Unlock()

	if counter, exists := userSpaceProxy.activationWaiters[serviceName]; exists {
		return counter
	}
	counter := &atomic.Int64{}
	userSpaceProxy.activationWaiters[serviceName] = counter
	return counter
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
