package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCounterIncrement(t *testing.T) {
	registry := NewRegistry()
	counter := registry.RegisterCounter("test_requests_total", "Total requests", "method")

	counter.Inc("GET")
	counter.Inc("GET")
	counter.Inc("POST")

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()

	if !strings.Contains(body, "# TYPE test_requests_total counter") {
		t.Error("missing TYPE declaration")
	}
	if !strings.Contains(body, `test_requests_total{method="GET"} 2`) {
		t.Errorf("expected GET=2, got:\n%s", body)
	}
	if !strings.Contains(body, `test_requests_total{method="POST"} 1`) {
		t.Errorf("expected POST=1, got:\n%s", body)
	}
}

func TestGaugeSetAndIncDec(t *testing.T) {
	registry := NewRegistry()
	gauge := registry.RegisterGauge("test_active_watches", "Active watches")

	gauge.Set(5)
	gauge.Inc()
	gauge.Dec()
	gauge.Dec()

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()

	if !strings.Contains(body, "# TYPE test_active_watches gauge") {
		t.Error("missing TYPE declaration")
	}
	if !strings.Contains(body, "test_active_watches 4") {
		t.Errorf("expected value 4, got:\n%s", body)
	}
}

func TestHistogramObserve(t *testing.T) {
	registry := NewRegistry()
	histogram := registry.RegisterHistogram("test_duration_seconds", "Duration", []float64{0.01, 0.1, 1.0}, "controller")

	histogram.ObserveDuration(5*time.Millisecond, "instance")
	histogram.ObserveDuration(50*time.Millisecond, "instance")
	histogram.ObserveDuration(500*time.Millisecond, "instance")

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()

	if !strings.Contains(body, "# TYPE test_duration_seconds histogram") {
		t.Error("missing TYPE declaration")
	}
	if !strings.Contains(body, `test_duration_seconds_bucket{controller="instance",le="0.01"} 1`) {
		t.Errorf("expected 1 in 0.01 bucket, got:\n%s", body)
	}
	if !strings.Contains(body, `test_duration_seconds_bucket{controller="instance",le="0.1"} 2`) {
		t.Errorf("expected 2 in 0.1 bucket, got:\n%s", body)
	}
	if !strings.Contains(body, `test_duration_seconds_bucket{controller="instance",le="1.0"} 3`) {
		t.Errorf("expected 3 in 1.0 bucket, got:\n%s", body)
	}
	if !strings.Contains(body, `test_duration_seconds_bucket{controller="instance",le="+Inf"} 3`) {
		t.Errorf("expected 3 in +Inf bucket, got:\n%s", body)
	}
	if !strings.Contains(body, `test_duration_seconds_count{controller="instance"} 3`) {
		t.Errorf("expected count=3, got:\n%s", body)
	}
}

func TestCounterWithoutLabels(t *testing.T) {
	registry := NewRegistry()
	counter := registry.RegisterCounter("test_simple_total", "Simple counter")

	counter.Inc()
	counter.Inc()

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()

	if !strings.Contains(body, "test_simple_total 2") {
		t.Errorf("expected 2, got:\n%s", body)
	}
}

func TestContentTypeHeader(t *testing.T) {
	registry := NewRegistry()
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))

	contentType := recorder.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("expected text/plain content type, got: %s", contentType)
	}
}

func TestHelpText(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCounter("test_counter", "A helpful description")

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()

	if !strings.Contains(body, "# HELP test_counter A helpful description") {
		t.Errorf("missing HELP text, got:\n%s", body)
	}
}

func TestHandlerServesHTTP(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCounter("http_test_total", "Test")
	handler := registry.Handler()

	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		t.Errorf("expected 200, got %d", response.StatusCode)
	}
}

func TestObserveSince(t *testing.T) {
	registry := NewRegistry()
	histogram := registry.RegisterHistogram("test_latency", "Latency", DurationBuckets())

	startTime := Timer()
	time.Sleep(2 * time.Millisecond)
	histogram.ObserveSince(startTime)

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()

	if !strings.Contains(body, "test_latency_count 1") {
		t.Errorf("expected count=1, got:\n%s", body)
	}
}
