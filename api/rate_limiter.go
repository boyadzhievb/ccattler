package api

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiterConfig holds the token-bucket parameters for rate limiting.
type rateLimiterConfig struct {
	requestsPerSecond float64
	burstSize         int
	cleanupInterval   time.Duration
}

// defaultRateLimiterConfig returns sensible defaults for the control plane API.
func defaultRateLimiterConfig() rateLimiterConfig {
	return rateLimiterConfig{
		requestsPerSecond: 50,
		burstSize:         100,
		cleanupInterval:   60 * time.Second,
	}
}

// tokenBucket implements the token bucket algorithm for a single client.
type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

// RateLimiter enforces per-client request rate limits using the token bucket
// algorithm. Clients are identified by their IP address. When a client exceeds
// their rate, the limiter returns 429 with a Retry-After header.
type RateLimiter struct {
	config  rateLimiterConfig
	buckets map[string]*tokenBucket
	mutex   sync.Mutex
	timeNow func() time.Time
}

// NewRateLimiter creates a rate limiter with default configuration.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		config:  defaultRateLimiterConfig(),
		buckets: make(map[string]*tokenBucket),
		timeNow: time.Now,
	}
}

// Wrap returns an http.Handler that enforces rate limits before delegating
// to the inner handler. Requests to /healthz and /metrics are exempt.
func (rateLimiter *RateLimiter) Wrap(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" || request.URL.Path == "/metrics" {
			inner.ServeHTTP(responseWriter, request)
			return
		}

		clientAddress := extractClientIP(request)
		if !rateLimiter.allow(clientAddress) {
			retryAfter := rateLimiter.retryAfterSeconds()
			responseWriter.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			http.Error(responseWriter,
				fmt.Sprintf("rate limit exceeded for %s — retry after %ds", clientAddress, retryAfter),
				http.StatusTooManyRequests)
			return
		}
		inner.ServeHTTP(responseWriter, request)
	})
}

// allow checks whether a request from the given client should proceed. It
// refills the client's token bucket based on elapsed time and consumes one
// token. Returns false if the bucket is empty.
func (rateLimiter *RateLimiter) allow(clientAddress string) bool {
	rateLimiter.mutex.Lock()
	defer rateLimiter.mutex.Unlock()

	currentTime := rateLimiter.timeNow()

	bucket, exists := rateLimiter.buckets[clientAddress]
	if !exists {
		bucket = &tokenBucket{
			tokens:     float64(rateLimiter.config.burstSize),
			lastRefill: currentTime,
		}
		rateLimiter.buckets[clientAddress] = bucket
	}

	elapsed := currentTime.Sub(bucket.lastRefill).Seconds()
	bucket.tokens += elapsed * rateLimiter.config.requestsPerSecond
	if bucket.tokens > float64(rateLimiter.config.burstSize) {
		bucket.tokens = float64(rateLimiter.config.burstSize)
	}
	bucket.lastRefill = currentTime

	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// retryAfterSeconds returns the suggested wait time for a rate-limited client.
func (rateLimiter *RateLimiter) retryAfterSeconds() int {
	secondsPerToken := 1.0 / rateLimiter.config.requestsPerSecond
	if secondsPerToken < 1 {
		return 1
	}
	return int(secondsPerToken) + 1
}

// CleanupStale removes token buckets for clients that haven't made requests
// in a while. Call periodically from a background goroutine.
func (rateLimiter *RateLimiter) CleanupStale() {
	rateLimiter.mutex.Lock()
	defer rateLimiter.mutex.Unlock()

	currentTime := rateLimiter.timeNow()
	for clientAddress, bucket := range rateLimiter.buckets {
		if currentTime.Sub(bucket.lastRefill) > rateLimiter.config.cleanupInterval {
			delete(rateLimiter.buckets, clientAddress)
		}
	}
}

// extractClientIP gets the client IP from RemoteAddr, stripping the port.
func extractClientIP(request *http.Request) string {
	host, _, splitError := net.SplitHostPort(request.RemoteAddr)
	if splitError != nil {
		return request.RemoteAddr
	}
	return host
}
