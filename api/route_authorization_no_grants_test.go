// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// routesAnsweringWithoutCapability is the reviewed public allowlist (spec
// api-route-authorization): these routes answer a principal without grants.
var routesAnsweringWithoutCapability = map[string]string{
	"/healthz":                          "liveness probe",
	"/api/auth/whoami":                  "tells the caller who it is authenticated as",
	"/api/auth/oidc-config":             "public login configuration for cca login",
	"/api/enroll":                       "authenticated by a one-time join token, not by a principal",
	"/.well-known/openid-configuration": "public OIDC discovery document",
	"/oidc/jwks":                        "public token-verification keys",
	"/metrics":                          "unauthenticated scrape: open M182 item, out of scope here",
}

// routeRegistrationCallPattern matches a route registration on a mux, in any
// form the sweep's server.go parser would not see.
var routeRegistrationCallPattern = regexp.MustCompile(`\.(HandleFunc|Handle)\(\s*[A-Za-z"/]`)

// TestRoutesAreRegisteredOnlyInServerGo fails when a non-test file in api/
// other than server.go registers a route: the sweep and the AUTHZ route table
// read server.go only, so a route registered elsewhere would escape both.
func TestRoutesAreRegisteredOnlyInServerGo(t *testing.T) {
	sourceFiles, globError := filepath.Glob("*.go")
	if globError != nil {
		t.Fatal(globError)
	}
	for _, sourceFile := range sourceFiles {
		if strings.HasSuffix(sourceFile, "_test.go") || sourceFile == "server.go" {
			continue
		}
		sourceBytes, readError := os.ReadFile(sourceFile) //nolint:gosec // reads this package's own .go files, listed by Glob
		if readError != nil {
			t.Fatal(readError)
		}
		for lineNumber, line := range strings.Split(string(sourceBytes), "\n") {
			trimmedLine := strings.TrimSpace(line)
			if strings.HasPrefix(trimmedLine, "//") {
				continue
			}
			if routeRegistrationCallPattern.MatchString(line) && !strings.Contains(line, "http.HandlerFunc(") {
				t.Errorf("%s:%d registers a route outside server.go: %s", sourceFile, lineNumber+1, trimmedLine)
			}
		}
	}
}

// noGrantsSweepTimeout bounds each request, so a stream that opens for a
// principal without grants fails the sweep instead of hanging it.
const noGrantsSweepTimeout = 5 * time.Second

// TestEveryRouteRefusesPrincipalWithoutGrants sends GET, POST, PUT and DELETE
// to every route registered in server.go as an authenticated user with no
// grants; every non-allowlisted route must answer 401, 403 or 405 and the
// store must be unchanged (spec api-route-authorization).
func TestEveryRouteRefusesPrincipalWithoutGrants(t *testing.T) {
	routes := collectRegisteredRoutes(t)

	factStore := store.NewMemoryStore()
	defer factStore.Close()
	apiServer := NewServer(factStore)
	apiServer.rateLimiter = &RateLimiter{config: rateLimiterConfig{requestsPerSecond: 10000, burstSize: 10000, cleanupInterval: time.Minute}, buckets: map[string]*tokenBucket{}, timeNow: time.Now}
	apiServer.SetAPIAuthorizer(security.NewAPIAuthorizer())
	apiServer.SetAuthenticatorChain(security.NewAuthenticatorChain(&fixedPrincipalAuthenticator{principal: "user:nogrants"}))
	apiServer.SetRequirePrincipal(true)
	address, startError := apiServer.Start("127.0.0.1:0")
	if startError != nil {
		t.Fatal(startError)
	}
	defer apiServer.Close()

	revisionBefore := currentStoreRevision(t, factStore)
	client := &http.Client{
		Timeout:       noGrantsSweepTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, route := range routes {
		if _, isPublic := routesAnsweringWithoutCapability[route]; isPublic {
			continue
		}
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			statusCode := sendNoGrantsProbe(t, client, method, "http://"+address+noGrantsProbePath(route))
			if !refusalStatusCodes[statusCode] {
				t.Errorf("%s %s answered %d to a principal without grants (want 401, 403 or 405)", method, route, statusCode)
			}
		}
	}
	if revisionAfter := currentStoreRevision(t, factStore); revisionAfter != revisionBefore {
		t.Errorf("store revision moved from %d to %d: a refused request changed facts", revisionBefore, revisionAfter)
	}
}

// noGrantsProbePath completes prefix routes with a real endpoint and adds the
// query parameters handlers read, so the probe reaches the capability check.
func noGrantsProbePath(route string) string {
	probePath := route
	if strings.HasSuffix(route, "/") {
		probePath += sweepProbeSuffix
	}
	return probePath + "?name=probe&service=probe&type=service&prefix=desired/&key=desired/service/probe"
}

// sendNoGrantsProbe sends one request and returns its status code; a timeout
// is reported as 0, which no refusal set contains.
func sendNoGrantsProbe(t *testing.T, client *http.Client, method string, probeURL string) int {
	t.Helper()
	request, _ := http.NewRequest(method, probeURL, bytes.NewBufferString(`{"service":"probe","instances":1,"name":"probe"}`))
	request.Header.Set("Content-Type", "application/json")
	response, requestError := client.Do(request)
	if requestError != nil {
		t.Errorf("%s %s: %v", method, probeURL, requestError)
		return 0
	}
	_ = response.Body.Close()
	return response.StatusCode
}

// currentStoreRevision returns the store-global revision.
func currentStoreRevision(t *testing.T, factStore store.StateStore) int64 {
	t.Helper()
	scanResult, scanError := factStore.ScanWithRevision(context.Background(), "")
	if scanError != nil {
		t.Fatal(scanError)
	}
	return scanResult.Revision
}
