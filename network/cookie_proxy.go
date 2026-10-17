// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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
	"time"

	"github.com/boyadzhievb/ccattler/logging"
)

// defaultCookieStickyTTL is how long a sticky cookie remains valid.
const defaultCookieStickyTTL = 24 * time.Hour

// cookieProxyReadHeaderTimeout caps how long the proxy waits for request headers.
const cookieProxyReadHeaderTimeout = 10 * time.Second

// cookieProxyShutdownTimeout caps graceful shutdown of per-route listeners.
const cookieProxyShutdownTimeout = 5 * time.Second

// CookieStickyProxy manages per-route HTTP reverse proxy listeners that
// implement cookie-based sticky sessions. Each cookie-sticky route gets
// its own listener on a dynamically allocated port. The proxy reads the
// sticky cookie to pin requests to a backend, or picks one round-robin
// and sets the cookie on first request.
type CookieStickyProxy struct {
	// mutex guards the activeRoutes map.
	mutex sync.Mutex
	// activeRoutes maps route name to its running proxy instance.
	activeRoutes map[string]*cookieRouteProxy
}

// cookieRouteProxy holds the state for a single route's cookie-sticky proxy.
type cookieRouteProxy struct {
	routeName  string             // route name for logging
	listenPort int                // allocated listen port
	server     *http.Server       // the HTTP server
	cancel     context.CancelFunc // cancels the route context
	backends   []DataPlaneBackend // current backend list
	cookieConf RouteCookieConfig  // cookie settings
	backendMu  sync.RWMutex       // guards backends
	nextIndex  int                // round-robin counter
}

// NewCookieStickyProxy creates a CookieStickyProxy ready to manage
// per-route cookie-sticky proxy listeners.
func NewCookieStickyProxy() *CookieStickyProxy {
	return &CookieStickyProxy{
		activeRoutes: make(map[string]*cookieRouteProxy),
	}
}

// ReconcileRoutes starts, updates, or stops per-route proxy listeners to
// match the desired set of cookie-sticky route configurations. Routes
// without sticky=cookie are ignored.
func (cookieStickyProxy *CookieStickyProxy) ReconcileRoutes(routeConfigs []RouteDataPlaneConfig) {
	cookieStickyProxy.mutex.Lock()
	defer cookieStickyProxy.mutex.Unlock()

	desiredRoutes := make(map[string]RouteDataPlaneConfig)
	for _, routeConfig := range routeConfigs {
		if routeConfig.StickyMethod == "cookie" && routeConfig.CookieConfig != nil {
			desiredRoutes[routeConfig.RouteName] = routeConfig
		}
	}

	for routeName, routeConfig := range desiredRoutes {
		existing, alreadyRunning := cookieStickyProxy.activeRoutes[routeName]
		if alreadyRunning {
			existing.updateBackends(routeConfig.Backends, *routeConfig.CookieConfig)
		} else {
			cookieStickyProxy.startRouteProxy(routeName, routeConfig)
		}
	}

	for routeName, proxy := range cookieStickyProxy.activeRoutes {
		if _, stillDesired := desiredRoutes[routeName]; !stillDesired {
			proxy.stop()
			delete(cookieStickyProxy.activeRoutes, routeName)
			logging.Default().Info("stopped cookie proxy", "route", routeName)
		}
	}
}

// ListenPort returns the allocated listen port for a route's cookie proxy,
// or 0 if the route has no active cookie proxy.
func (cookieStickyProxy *CookieStickyProxy) ListenPort(routeName string) int {
	cookieStickyProxy.mutex.Lock()
	defer cookieStickyProxy.mutex.Unlock()

	if proxy, exists := cookieStickyProxy.activeRoutes[routeName]; exists {
		return proxy.listenPort
	}
	return 0
}

// Cleanup stops all active route proxy listeners.
func (cookieStickyProxy *CookieStickyProxy) Cleanup() {
	cookieStickyProxy.mutex.Lock()
	defer cookieStickyProxy.mutex.Unlock()

	for routeName, proxy := range cookieStickyProxy.activeRoutes {
		proxy.stop()
		delete(cookieStickyProxy.activeRoutes, routeName)
	}
}

// startRouteProxy allocates a port, starts a listener, and registers the
// proxy in the active routes map.
func (cookieStickyProxy *CookieStickyProxy) startRouteProxy(routeName string, routeConfig RouteDataPlaneConfig) {
	listener, listenError := net.Listen("tcp", "127.0.0.1:0")
	if listenError != nil {
		logging.Default().Error("cookie proxy: failed to allocate port", "route", routeName, "error", listenError.Error())
		return
	}

	tcpAddress, addressOK := listener.Addr().(*net.TCPAddr)
	if !addressOK {
		_ = listener.Close()
		logging.Default().Error("cookie proxy: unexpected listener address type", "route", routeName)
		return
	}
	allocatedPort := tcpAddress.Port

	routeContext, routeCancel := context.WithCancel(context.Background())

	routeProxy := &cookieRouteProxy{
		routeName:  routeName,
		listenPort: allocatedPort,
		cancel:     routeCancel,
		backends:   routeConfig.Backends,
		cookieConf: *routeConfig.CookieConfig,
	}

	routeProxy.server = &http.Server{
		Handler:           routeProxy,
		ReadHeaderTimeout: cookieProxyReadHeaderTimeout,
	}

	cookieStickyProxy.activeRoutes[routeName] = routeProxy

	go func() {
		logging.Default().Info("starting cookie proxy", "route", routeName, "port", fmt.Sprintf("%d", allocatedPort))
		if serveError := routeProxy.server.Serve(listener); serveError != nil && serveError != http.ErrServerClosed {
			logging.Default().Error("cookie proxy serve error", "route", routeName, "error", serveError.Error())
		}
	}()

	go func() {
		<-routeContext.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cookieProxyShutdownTimeout)
		defer shutdownCancel()
		_ = routeProxy.server.Shutdown(shutdownCtx)
	}()
}

// updateBackends replaces the backend list and cookie config for a running proxy.
func (routeProxy *cookieRouteProxy) updateBackends(backends []DataPlaneBackend, cookieConf RouteCookieConfig) {
	routeProxy.backendMu.Lock()
	defer routeProxy.backendMu.Unlock()
	routeProxy.backends = backends
	routeProxy.cookieConf = cookieConf
}

// stop gracefully shuts down the route proxy.
func (routeProxy *cookieRouteProxy) stop() {
	routeProxy.cancel()
}

// ServeHTTP handles an incoming request by reading the sticky cookie,
// selecting a backend, and proxying the request with a Set-Cookie header.
func (routeProxy *cookieRouteProxy) ServeHTTP(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
	routeProxy.backendMu.RLock()
	backends := routeProxy.backends
	cookieConf := routeProxy.cookieConf
	routeProxy.backendMu.RUnlock()

	if len(backends) == 0 {
		http.Error(responseWriter, "no backends available", http.StatusBadGateway)
		return
	}

	selectedBackend := routeProxy.selectBackendFromCookie(incomingRequest, backends, cookieConf.Name)

	backendURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", selectedBackend.Address, selectedBackend.Port),
	}

	cookieValue := fmt.Sprintf("%s:%d", selectedBackend.Address, selectedBackend.Port)

	reverseProxy := &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			proxyRequest.SetURL(backendURL)
			proxyRequest.Out.Host = incomingRequest.Host
		},
		ModifyResponse: func(response *http.Response) error {
			stickySetCookie := buildStickyCookie(cookieConf, cookieValue)
			response.Header.Add("Set-Cookie", stickySetCookie)
			return nil
		},
	}

	reverseProxy.ServeHTTP(responseWriter, incomingRequest)
}

// selectBackendFromCookie reads the sticky cookie and returns the matching
// backend. Falls back to round-robin if the cookie is missing or the
// backend is no longer available.
func (routeProxy *cookieRouteProxy) selectBackendFromCookie(
	incomingRequest *http.Request,
	backends []DataPlaneBackend,
	cookieName string,
) DataPlaneBackend {
	if cookieName == "" {
		cookieName = "__CCA_STICKY_" + routeProxy.routeName
	}

	existingCookie, cookieError := incomingRequest.Cookie(cookieName)
	if cookieError == nil && existingCookie.Value != "" {
		for _, backend := range backends {
			backendKey := fmt.Sprintf("%s:%d", backend.Address, backend.Port)
			if existingCookie.Value == backendKey {
				return backend
			}
		}
	}

	routeProxy.backendMu.Lock()
	selectedIndex := routeProxy.nextIndex % len(backends)
	routeProxy.nextIndex++
	routeProxy.backendMu.Unlock()

	return backends[selectedIndex]
}

// buildStickyCookie constructs the Set-Cookie header value from the cookie
// config and backend address value.
func buildStickyCookie(cookieConf RouteCookieConfig, cookieValue string) string {
	cookieName := cookieConf.Name
	if cookieName == "" {
		cookieName = "__CCA_STICKY"
	}

	var builder strings.Builder
	builder.WriteString(cookieName)
	builder.WriteString("=")
	builder.WriteString(cookieValue)
	builder.WriteString("; Path=/")
	fmt.Fprintf(&builder, "; Max-Age=%d", int(defaultCookieStickyTTL.Seconds()))

	if cookieConf.Secure {
		builder.WriteString("; Secure")
	}
	if cookieConf.HTTPOnly {
		builder.WriteString("; HttpOnly")
	}
	if cookieConf.SameSite != "" {
		builder.WriteString("; SameSite=")
		builder.WriteString(cookieConf.SameSite)
	}

	return builder.String()
}
