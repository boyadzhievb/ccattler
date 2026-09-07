package agent

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestHTTPHealthy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, mux)
	defer ln.Close()

	ok := CheckHealth(context.Background(), HealthProbe{
		Type: ProbeHTTP, Port: port, Path: "/health", Timeout: time.Second,
	}, "127.0.0.1")
	if !ok {
		t.Error("expected healthy")
	}
}

func TestHTTPUnhealthy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, mux)
	defer ln.Close()

	ok := CheckHealth(context.Background(), HealthProbe{
		Type: ProbeHTTP, Port: port, Path: "/health", Timeout: time.Second,
	}, "127.0.0.1")
	if ok {
		t.Error("expected unhealthy for 500")
	}
}

func TestHTTPConnectionRefused(t *testing.T) {
	ok := CheckHealth(context.Background(), HealthProbe{
		Type: ProbeHTTP, Port: 19999, Path: "/health", Timeout: 100 * time.Millisecond,
	}, "127.0.0.1")
	if ok {
		t.Error("expected unhealthy for connection refused")
	}
}

func TestTCPHealthy(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	defer ln.Close()

	ok := CheckHealth(context.Background(), HealthProbe{
		Type: ProbeTCP, Port: port, Timeout: time.Second,
	}, "127.0.0.1")
	if !ok {
		t.Error("expected healthy")
	}
}

func TestTCPConnectionRefused(t *testing.T) {
	ok := CheckHealth(context.Background(), HealthProbe{
		Type: ProbeTCP, Port: 19999, Timeout: 100 * time.Millisecond,
	}, "127.0.0.1")
	if ok {
		t.Error("expected unhealthy for connection refused")
	}
}

func TestHTTPPortFromString(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, mux)
	defer ln.Close()

	portStr := strconv.Itoa(port)
	portInt, _ := strconv.Atoi(portStr)

	ok := CheckHealth(context.Background(), HealthProbe{
		Type: ProbeHTTP, Port: portInt, Path: "/", Timeout: time.Second,
	}, "127.0.0.1")
	if !ok {
		t.Error("expected healthy")
	}
}
