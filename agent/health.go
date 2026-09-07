package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ProbeType represents the protocol used by a health probe.
type ProbeType string

const (
	// ProbeHTTP indicates an HTTP GET health check.
	ProbeHTTP ProbeType = "http"
	// ProbeTCP indicates a raw TCP connection health check.
	ProbeTCP ProbeType = "tcp"
)

// HealthProbe describes how to check the health of a running instance.
type HealthProbe struct {
	Type    ProbeType     // Type is the probe protocol (HTTP or TCP).
	Path    string        // Path is the URL path for HTTP probes (ignored for TCP).
	Port    int           // Port is the TCP port to connect to on the target host.
	Timeout time.Duration // Timeout is the maximum duration to wait for a probe response.
}

// CheckHealth executes the given health probe against the specified host and
// returns true if the instance is considered healthy. For HTTP probes a 2xx or
// 3xx status code is required; for TCP probes a successful dial is sufficient.
// If the probe timeout is zero, a default of 2 seconds is used.
func CheckHealth(ctx context.Context, probe HealthProbe, host string) bool {
	if probe.Timeout == 0 {
		probe.Timeout = 2 * time.Second
	}

	switch probe.Type {
	case ProbeHTTP:
		return performHTTPHealthCheck(ctx, host, probe.Port, probe.Path, probe.Timeout)
	case ProbeTCP:
		return performTCPHealthCheck(host, probe.Port, probe.Timeout)
	default:
		return false
	}
}

// performHTTPHealthCheck sends an HTTP GET request to the given host, port, and
// path and returns true if the response status code is in the 2xx-3xx range.
// The request is bound to the provided context and the given timeout.
func performHTTPHealthCheck(ctx context.Context, host string, port int, path string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	url := fmt.Sprintf("http://%s:%d%s", host, port, path)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// performTCPHealthCheck attempts to open a TCP connection to the given host and
// port within the specified timeout. It returns true if the connection succeeds,
// indicating that the target is accepting connections.
func performTCPHealthCheck(host string, port int, timeout time.Duration) bool {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
