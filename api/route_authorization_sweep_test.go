// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bytes"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// fixedPrincipalAuthenticator authenticates every request as one principal.
type fixedPrincipalAuthenticator struct {
	principal string // principal is the identity returned for every request.
}

// Authenticate returns the fixed principal.
func (authenticator *fixedPrincipalAuthenticator) Authenticate(_ *http.Request) (*security.AuthenticationResult, error) {
	return &security.AuthenticationResult{Principal: authenticator.principal, PrincipalKind: security.PrincipalKindUser}, nil
}

// routesPublicByDesign are routes a read-only principal may call with a
// writing method, each with the reason. Anything not listed must refuse.
var routesPublicByDesign = map[string]string{
	"/healthz":                          "liveness probe: answers any method, reads nothing, writes nothing",
	"/metrics":                          "Prometheus scrape: read-only for any method (unauthenticated scrape is an open M182 item, not a write)",
	"/.well-known/openid-configuration": "public OIDC discovery document: read-only, 404 until an issuer is configured",
	"/oidc/jwks":                        "public token-verification keys: read-only",
	"/api/enroll":                       "node enrollment: authenticated by a one-time enrollment token, not by a principal",
	"/api/diff":                         "dry run: compiles and diffs a configuration, writes nothing; needs workload.read only",
}

// sweepProbeSuffix completes routes registered as path prefixes (ending in
// "/") with a real endpoint, so the probe reaches the capability check: the
// node debug proxy picks the capability by endpoint and answers 404 for an
// unknown one before any check (by design; the endpoint list is static).
const sweepProbeSuffix = "probe-node/exec"

// registeredRoutePattern matches route registrations in api/server.go, by
// string literal (group 1) or by constant name (group 2).
var registeredRoutePattern = regexp.MustCompile(`mux\.HandleFunc\((?:"([^"]+)"|([A-Za-z]\w*))`)

// routeConstants resolves routes registered by constant name; a new
// constant-registered route fails the sweep until it is added here.
var routeConstants = map[string]string{
	"NodeSecretDeliveryPath":  NodeSecretDeliveryPath,
	"NodeGateKeyDeliveryPath": NodeGateKeyDeliveryPath,
	"GateKeyPath":             GateKeyPath,
	"nodeDebugPathPrefix":     nodeDebugPathPrefix,
}

// refusalStatusCodes are the only answers a read-only principal may get from
// a writing method: a 400 or 404 could come from validation that runs before
// a missing capability check and would hide it.
var refusalStatusCodes = map[int]bool{http.StatusUnauthorized: true, http.StatusForbidden: true, http.StatusMethodNotAllowed: true}

// collectRegisteredRoutes returns every route registered in server.go and
// fails when a registration is not understood or a constant is unknown.
func collectRegisteredRoutes(t *testing.T) []string {
	t.Helper()
	serverSource, readError := os.ReadFile("server.go")
	if readError != nil {
		t.Fatal(readError)
	}
	var routes []string
	for _, match := range registeredRoutePattern.FindAllStringSubmatch(string(serverSource), -1) {
		if match[1] != "" {
			routes = append(routes, match[1])
			continue
		}
		resolvedRoute, isKnown := routeConstants[match[2]]
		if !isKnown {
			t.Fatalf("route registered by unknown constant %s: add it to routeConstants", match[2])
		}
		routes = append(routes, resolvedRoute)
	}
	if registrationCount := strings.Count(string(serverSource), "mux.HandleFunc("); len(routes) != registrationCount {
		t.Fatalf("matched %d of %d route registrations in server.go; the registration pattern changed", len(routes), registrationCount)
	}
	sort.Strings(routes)
	return routes
}

// TestReadOnlyPrincipalCannotWriteThroughAnyRoute sends POST, PUT and DELETE
// to every route registered in server.go as a principal holding only the
// built-in api-reader role and accepts only 401, 403 or 405. It found /api/gc
// (M216, M214 run 1) and keeps the next unchecked writing route from shipping.
func TestReadOnlyPrincipalCannotWriteThroughAnyRoute(t *testing.T) {
	routes := collectRegisteredRoutes(t)

	factStore := store.NewMemoryStore()
	defer factStore.Close()
	apiServer := NewServer(factStore)
	// The sweep sends ~100 requests from one address; the default per-client
	// limit would answer 429 and hide the handlers' own answers.
	apiServer.rateLimiter = &RateLimiter{config: rateLimiterConfig{requestsPerSecond: 10000, burstSize: 10000, cleanupInterval: time.Minute}, buckets: map[string]*tokenBucket{}, timeNow: time.Now}
	readerAuthorizer := security.NewAPIAuthorizer()
	readerAuthorizer.GrantRole("user:reader", "api-reader")
	apiServer.SetAPIAuthorizer(readerAuthorizer)
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(&fixedPrincipalAuthenticator{principal: "user:reader"}))
	apiServer.SetRequirePrincipal(true)
	address, startError := apiServer.Start("127.0.0.1:0")
	if startError != nil {
		t.Fatal(startError)
	}
	defer apiServer.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, route := range routes {
		if _, isPublic := routesPublicByDesign[route]; isPublic {
			continue
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			probePath := route
			if strings.HasSuffix(route, "/") {
				probePath += sweepProbeSuffix
			}
			request, _ := http.NewRequest(method, "http://"+address+probePath+"?name=probe&service=probe", bytes.NewBufferString(`{"service":"probe","instances":1,"name":"probe"}`))
			request.Header.Set("Content-Type", "application/json")
			response, requestError := client.Do(request)
			if requestError != nil {
				t.Fatalf("%s %s: %v", method, route, requestError)
			}
			_ = response.Body.Close()
			if !refusalStatusCodes[response.StatusCode] {
				t.Errorf("%s %s answered %d to a read-only principal (want 401, 403 or 405)", method, route, response.StatusCode)
			}
		}
	}
}
