package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterAllowsRequestsWithinLimit(t *testing.T) {
	limiter := NewRateLimiter()

	for requestIndex := 0; requestIndex < 50; requestIndex++ {
		if !limiter.allow("10.0.0.1") {
			t.Fatalf("expected request %d to be allowed within burst", requestIndex)
		}
	}
}

func TestRateLimiterRejectsExcessRequests(t *testing.T) {
	limiter := NewRateLimiter()

	for requestIndex := 0; requestIndex < limiter.config.burstSize; requestIndex++ {
		limiter.allow("10.0.0.1")
	}

	if limiter.allow("10.0.0.1") {
		t.Error("expected request beyond burst to be rejected")
	}
}

func TestRateLimiterRefillsTokensOverTime(t *testing.T) {
	currentTime := time.Now()
	limiter := NewRateLimiter()
	limiter.timeNow = func() time.Time { return currentTime }

	for requestIndex := 0; requestIndex < limiter.config.burstSize; requestIndex++ {
		limiter.allow("10.0.0.1")
	}

	currentTime = currentTime.Add(time.Second)

	if !limiter.allow("10.0.0.1") {
		t.Error("expected request to be allowed after token refill")
	}
}

func TestRateLimiterIndependentPerClient(t *testing.T) {
	limiter := NewRateLimiter()

	for requestIndex := 0; requestIndex < limiter.config.burstSize; requestIndex++ {
		limiter.allow("10.0.0.1")
	}

	if !limiter.allow("10.0.0.2") {
		t.Error("expected different client to have its own bucket")
	}
}

func TestRateLimiterWrapReturns429(t *testing.T) {
	limiter := NewRateLimiter()
	limiter.config.burstSize = 2
	limiter.config.requestsPerSecond = 1

	inner := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	})

	handler := limiter.Wrap(inner)

	for requestIndex := 0; requestIndex < 2; requestIndex++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("GET", "/api/status", nil)
		request.RemoteAddr = "10.0.0.1:12345"
		handler.ServeHTTP(recorder, request)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/status", nil)
	request.RemoteAddr = "10.0.0.1:12345"
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", recorder.Code)
	}

	retryAfter := recorder.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Error("expected Retry-After header on 429 response")
	}
}

func TestRateLimiterWrapExemptsHealthzAndMetrics(t *testing.T) {
	limiter := NewRateLimiter()
	limiter.config.burstSize = 0

	inner := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	})

	handler := limiter.Wrap(inner)

	exemptPaths := []string{"/healthz", "/metrics"}
	for _, path := range exemptPaths {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("GET", path, nil)
		request.RemoteAddr = "10.0.0.1:12345"
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Errorf("path %s: expected 200, got %d", path, recorder.Code)
		}
	}
}

func TestRateLimiterCleanupStale(t *testing.T) {
	currentTime := time.Now()
	limiter := NewRateLimiter()
	limiter.timeNow = func() time.Time { return currentTime }

	limiter.allow("10.0.0.1")
	limiter.allow("10.0.0.2")

	currentTime = currentTime.Add(2 * limiter.config.cleanupInterval)

	limiter.CleanupStale()

	limiter.mutex.Lock()
	bucketCount := len(limiter.buckets)
	limiter.mutex.Unlock()

	if bucketCount != 0 {
		t.Errorf("expected 0 buckets after cleanup, got %d", bucketCount)
	}
}

func TestExtractClientIP(t *testing.T) {
	testCases := []struct {
		remoteAddress    string
		expectedClientIP string
	}{
		{"10.0.0.1:12345", "10.0.0.1"},
		{"[::1]:8080", "::1"},
		{"10.0.0.1", "10.0.0.1"},
	}

	for _, testCase := range testCases {
		request := &http.Request{RemoteAddr: testCase.remoteAddress}
		result := extractClientIP(request)
		if result != testCase.expectedClientIP {
			t.Errorf("extractClientIP(%q): got %q, want %q",
				testCase.remoteAddress, result, testCase.expectedClientIP)
		}
	}
}
