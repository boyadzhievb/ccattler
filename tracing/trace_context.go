package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// W3C Trace Context header names.
const (
	TraceparentHeader = "traceparent"
	TracestateHeader  = "tracestate"
)

type traceContextKey struct{}

// TraceContext carries a W3C Trace Context through the system.
type TraceContext struct {
	TraceID  string
	SpanID   string
	ParentID string
	Sampled  bool
}

// NewTraceContext generates a new trace with random trace ID and span ID.
func NewTraceContext() TraceContext {
	return TraceContext{
		TraceID: generateTraceID(),
		SpanID:  generateSpanID(),
		Sampled: true,
	}
}

// NewChildSpan creates a child span under the current trace context.
func (traceContext TraceContext) NewChildSpan() TraceContext {
	return TraceContext{
		TraceID:  traceContext.TraceID,
		SpanID:   generateSpanID(),
		ParentID: traceContext.SpanID,
		Sampled:  traceContext.Sampled,
	}
}

// Traceparent formats the trace context as a W3C traceparent header value.
func (traceContext TraceContext) Traceparent() string {
	flags := "00"
	if traceContext.Sampled {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", traceContext.TraceID, traceContext.SpanID, flags)
}

// ParseTraceparent parses a W3C traceparent header value into a TraceContext.
// Returns an empty TraceContext and false if the header is malformed.
func ParseTraceparent(headerValue string) (TraceContext, bool) {
	parts := strings.Split(headerValue, "-")
	if len(parts) != 4 {
		return TraceContext{}, false
	}

	version := parts[0]
	traceID := parts[1]
	spanID := parts[2]
	flags := parts[3]

	if version != "00" {
		return TraceContext{}, false
	}
	if len(traceID) != 32 || !isHex(traceID) {
		return TraceContext{}, false
	}
	if len(spanID) != 16 || !isHex(spanID) {
		return TraceContext{}, false
	}
	if len(flags) != 2 || !isHex(flags) {
		return TraceContext{}, false
	}

	return TraceContext{
		TraceID: traceID,
		SpanID:  spanID,
		Sampled: flags == "01",
	}, true
}

// InjectHeaders writes the trace context into HTTP request headers.
func (traceContext TraceContext) InjectHeaders(request *http.Request) {
	request.Header.Set(TraceparentHeader, traceContext.Traceparent())
}

// ExtractFromRequest reads a trace context from incoming HTTP request headers.
// If no traceparent header is present, creates a new trace context.
func ExtractFromRequest(request *http.Request) TraceContext {
	headerValue := request.Header.Get(TraceparentHeader)
	if headerValue == "" {
		return NewTraceContext()
	}
	traceContext, valid := ParseTraceparent(headerValue)
	if !valid {
		return NewTraceContext()
	}
	return traceContext.NewChildSpan()
}

// ContextWithTrace stores a TraceContext in a Go context.
func ContextWithTrace(ctx context.Context, traceContext TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, traceContext)
}

// TraceFromContext retrieves a TraceContext from a Go context.
// Returns an empty TraceContext if none is stored.
func TraceFromContext(ctx context.Context) TraceContext {
	traceContext, ok := ctx.Value(traceContextKey{}).(TraceContext)
	if !ok {
		return TraceContext{}
	}
	return traceContext
}

// Middleware returns an HTTP middleware that extracts or creates trace context
// for each request and stores it in the request context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		traceContext := ExtractFromRequest(request)
		ctx := ContextWithTrace(request.Context(), traceContext)
		responseWriter.Header().Set(TraceparentHeader, traceContext.Traceparent())
		next.ServeHTTP(responseWriter, request.WithContext(ctx))
	})
}

func generateTraceID() string {
	randomBytes := make([]byte, 16)
	rand.Read(randomBytes)
	return hex.EncodeToString(randomBytes)
}

func generateSpanID() string {
	randomBytes := make([]byte, 8)
	rand.Read(randomBytes)
	return hex.EncodeToString(randomBytes)
}

func isHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
