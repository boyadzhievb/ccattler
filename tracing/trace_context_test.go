package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewTraceContextGeneratesValidIDs(t *testing.T) {
	traceContext := NewTraceContext()

	if len(traceContext.TraceID) != 32 {
		t.Errorf("expected 32-char trace ID, got %d", len(traceContext.TraceID))
	}
	if len(traceContext.SpanID) != 16 {
		t.Errorf("expected 16-char span ID, got %d", len(traceContext.SpanID))
	}
	if !traceContext.Sampled {
		t.Error("expected new trace to be sampled")
	}
}

func TestNewTraceContextUniqueness(t *testing.T) {
	traceA := NewTraceContext()
	traceB := NewTraceContext()

	if traceA.TraceID == traceB.TraceID {
		t.Error("expected different trace IDs")
	}
	if traceA.SpanID == traceB.SpanID {
		t.Error("expected different span IDs")
	}
}

func TestTraceparentFormat(t *testing.T) {
	traceContext := TraceContext{
		TraceID: "0af7651916cd43dd8448eb211c80319c",
		SpanID:  "b7ad6b7169203331",
		Sampled: true,
	}

	expected := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	if traceContext.Traceparent() != expected {
		t.Errorf("expected %s, got %s", expected, traceContext.Traceparent())
	}
}

func TestTraceparentNotSampled(t *testing.T) {
	traceContext := TraceContext{
		TraceID: "0af7651916cd43dd8448eb211c80319c",
		SpanID:  "b7ad6b7169203331",
		Sampled: false,
	}

	expected := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"
	if traceContext.Traceparent() != expected {
		t.Errorf("expected %s, got %s", expected, traceContext.Traceparent())
	}
}

func TestParseTraceparentValid(t *testing.T) {
	traceContext, valid := ParseTraceparent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	if !valid {
		t.Fatal("expected valid parse")
	}
	if traceContext.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("wrong trace ID: %s", traceContext.TraceID)
	}
	if traceContext.SpanID != "b7ad6b7169203331" {
		t.Errorf("wrong span ID: %s", traceContext.SpanID)
	}
	if !traceContext.Sampled {
		t.Error("expected sampled=true")
	}
}

func TestParseTraceparentInvalid(t *testing.T) {
	invalidHeaders := []string{
		"",
		"invalid",
		"01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"00-short-b7ad6b7169203331-01",
		"00-0af7651916cd43dd8448eb211c80319c-short-01",
		"00-ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ-b7ad6b7169203331-01",
		"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-ZZ",
	}

	for _, header := range invalidHeaders {
		_, valid := ParseTraceparent(header)
		if valid {
			t.Errorf("expected invalid for %q", header)
		}
	}
}

func TestNewChildSpanPreservesTraceID(t *testing.T) {
	parent := NewTraceContext()
	child := parent.NewChildSpan()

	if child.TraceID != parent.TraceID {
		t.Error("expected child to share parent trace ID")
	}
	if child.SpanID == parent.SpanID {
		t.Error("expected child to have different span ID")
	}
	if child.ParentID != parent.SpanID {
		t.Error("expected child parent ID to be parent span ID")
	}
	if child.Sampled != parent.Sampled {
		t.Error("expected child to inherit sampled flag")
	}
}

func TestExtractFromRequestCreatesNew(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	traceContext := ExtractFromRequest(request)

	if traceContext.TraceID == "" {
		t.Error("expected non-empty trace ID")
	}
}

func TestExtractFromRequestPropagates(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set(TraceparentHeader, "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	traceContext := ExtractFromRequest(request)

	if traceContext.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("expected propagated trace ID, got %s", traceContext.TraceID)
	}
	if traceContext.ParentID != "b7ad6b7169203331" {
		t.Errorf("expected parent ID from incoming span, got %s", traceContext.ParentID)
	}
}

func TestInjectHeaders(t *testing.T) {
	traceContext := NewTraceContext()
	request := httptest.NewRequest("GET", "/", nil)

	traceContext.InjectHeaders(request)

	headerValue := request.Header.Get(TraceparentHeader)
	if headerValue == "" {
		t.Error("expected traceparent header to be set")
	}

	parsed, valid := ParseTraceparent(headerValue)
	if !valid {
		t.Fatal("expected valid traceparent header")
	}
	if parsed.TraceID != traceContext.TraceID {
		t.Error("expected injected trace ID to match")
	}
}

func TestContextRoundTrip(t *testing.T) {
	traceContext := NewTraceContext()
	ctx := ContextWithTrace(context.Background(), traceContext)

	recovered := TraceFromContext(ctx)
	if recovered.TraceID != traceContext.TraceID {
		t.Error("expected trace ID to survive context round-trip")
	}
}

func TestTraceFromContextEmpty(t *testing.T) {
	recovered := TraceFromContext(context.Background())
	if recovered.TraceID != "" {
		t.Error("expected empty trace from context without trace")
	}
}

func TestMiddlewareAddsTraceContext(t *testing.T) {
	var capturedTrace TraceContext
	inner := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedTrace = TraceFromContext(request.Context())
		responseWriter.WriteHeader(http.StatusOK)
	})

	handler := Middleware(inner)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)

	handler.ServeHTTP(recorder, request)

	if capturedTrace.TraceID == "" {
		t.Error("expected trace context in request")
	}

	responseHeader := recorder.Header().Get(TraceparentHeader)
	if responseHeader == "" {
		t.Error("expected traceparent in response headers")
	}
}

func TestMiddlewarePropagatesIncoming(t *testing.T) {
	var capturedTrace TraceContext
	inner := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedTrace = TraceFromContext(request.Context())
		responseWriter.WriteHeader(http.StatusOK)
	})

	handler := Middleware(inner)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set(TraceparentHeader, "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	handler.ServeHTTP(recorder, request)

	if capturedTrace.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("expected propagated trace ID, got %s", capturedTrace.TraceID)
	}
}
