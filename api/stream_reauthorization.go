// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// stream_reauthorization.go keeps the long-lived streams (/api/watch,
// /api/events/stream, /api/audit?follow=true) authorized while open
// (M216, Flow 25, design/plans/plan-stream-reauthorization.md).
package api

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
)

const (
	// defaultStreamReauthorizationInterval is how often an open stream re-runs
	// authentication and its access check.
	defaultStreamReauthorizationInterval = 30 * time.Second
	// streamReauthorizationJitterFraction spreads re-checks (±20 %) so streams
	// opened together do not re-check together.
	streamReauthorizationJitterFraction = 0.2
	// streamMaximumLifetime ends streams whose credential has no real expiry
	// (local token, webhook) as a backstop.
	streamMaximumLifetime = time.Hour
	// streamMaxConsecutiveUnavailable is how many re-checks in a row may fail on
	// ErrAuthenticationUnavailable (store or webhook outage) before the stream ends.
	streamMaxConsecutiveUnavailable = 3
	// streamWriteTimeout bounds one write, so a client that stops reading
	// cannot hold a handler forever.
	streamWriteTimeout = 30 * time.Second
	// streamCloseAuditAction is the audit action of a forced stream end.
	streamCloseAuditAction = "stream.close"
)

// streamCloseReason is the generic reason sent to the client in the close
// frame; the detailed cause is only logged.
type streamCloseReason string

const (
	streamClosedCredentials     streamCloseReason = "credentials no longer valid" // token expired or refused, user deleted
	streamClosedAccess          streamCloseReason = "access changed"              // grant revoked or filter narrowed
	streamClosedUnavailable     streamCloseReason = "credentials could not be re-checked"
	streamClosedLifetimeReached streamCloseReason = "stream lifetime reached"
)

// expiringAuthMethods carry a real credential expiry in ExpiresAt (JWT exp,
// certificate NotAfter); other methods set a cache or synthetic value.
var expiringAuthMethods = map[string]bool{"oidc": true, "mtls": true}

// streamAccessCheck returns a fingerprint of what principal may see on a
// stream route (its filter), or an error when it may see nothing. A changed
// fingerprint ends the stream so the reconnect gets the new filter.
type streamAccessCheck func(apiServer *Server, request *http.Request, principal security.Principal) (string, error)

// withStreamReauthorization wraps a stream handler: every interval it re-runs
// the authenticator chain and accessCheck on the original request and ends
// the stream (close frame, audit) on a refusal or change. Without a chain, or
// when the request is refused at open, the handler runs unchanged.
func (apiServer *Server) withStreamReauthorization(accessCheck streamAccessCheck, streamHandler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if apiServer.authenticatorChain == nil || !isStreamRequest(request) {
			streamHandler(responseWriter, request)
			return
		}
		openResult, authError := apiServer.authenticatorChain.Authenticate(request)
		if authError != nil || openResult == nil {
			streamHandler(responseWriter, request)
			return
		}
		openAccess, accessError := accessCheck(apiServer, request, principalFromAuthenticationResult(openResult))
		if accessError != nil {
			streamHandler(responseWriter, request) // the handler answers with its own status
			return
		}
		streamContext, cancelStream := context.WithCancel(request.Context())
		defer cancelStream()
		reauthorizer := &streamReauthorizer{apiServer: apiServer, request: request, accessCheck: accessCheck, openResult: openResult, openAccess: openAccess}
		go reauthorizer.watch(streamContext, cancelStream)
		trackingWriter := &streamResponseWriter{ResponseWriter: responseWriter}
		streamHandler(trackingWriter, request.WithContext(streamContext))
		if closeReason := reauthorizer.reason(); closeReason != "" && trackingWriter.started {
			trackingWriter.writeCloseFrame(closeReason)
		}
	}
}

// isStreamRequest reports whether request opens a stream: every request on
// the watch and event routes, and /api/audit only with follow=true.
func isStreamRequest(request *http.Request) bool {
	return request.URL.Path != "/api/audit" || request.URL.Query().Get("follow") == "true"
}

// streamReauthorizer holds one open stream's credential and access at open.
type streamReauthorizer struct {
	apiServer         *Server                        // apiServer holds the chain and authorizer.
	request           *http.Request                  // request is the original request (headers, TLS state).
	accessCheck       streamAccessCheck              // accessCheck is the route's filter fingerprint.
	openResult        *security.AuthenticationResult // openResult is the authentication at open.
	openAccess        string                         // openAccess is the fingerprint at open.
	unavailableInARow int                            // unavailableInARow counts consecutive unavailable re-checks.
	mutex             sync.Mutex                     // mutex guards closeReason.
	closeReason       streamCloseReason              // closeReason is set when the stream was ended.
}

// watch re-checks at a jittered interval and at the credential's deadline,
// ending the stream on the first refusal. Returns when the stream ends.
func (reauthorizer *streamReauthorizer) watch(streamContext context.Context, cancelStream context.CancelFunc) {
	deadlineTimer := time.NewTimer(time.Until(reauthorizer.deadline()))
	defer deadlineTimer.Stop()
	recheckTimer := time.NewTimer(reauthorizer.jitteredInterval())
	defer recheckTimer.Stop()
	for {
		select {
		case <-streamContext.Done():
			return
		case <-deadlineTimer.C:
			reauthorizer.end(cancelStream, reauthorizer.deadlineReason(), "credential deadline reached")
			return
		case <-recheckTimer.C:
			if closeReason, detail := reauthorizer.recheck(); closeReason != "" {
				reauthorizer.end(cancelStream, closeReason, detail)
				return
			}
			recheckTimer.Reset(reauthorizer.jitteredInterval())
		}
	}
}

// recheck re-authenticates the original request and re-runs the access
// check. Returns the close reason and a detail for the log, or "" to go on.
func (reauthorizer *streamReauthorizer) recheck() (streamCloseReason, string) {
	result, authError := reauthorizer.apiServer.authenticatorChain.Authenticate(reauthorizer.request)
	if errors.Is(authError, security.ErrAuthenticationUnavailable) {
		reauthorizer.unavailableInARow++
		if reauthorizer.unavailableInARow >= streamMaxConsecutiveUnavailable {
			return streamClosedUnavailable, authError.Error()
		}
		return "", ""
	}
	reauthorizer.unavailableInARow = 0
	if authError != nil || result == nil {
		return streamClosedCredentials, fmt.Sprintf("re-authentication refused: %v", authError)
	}
	if result.Principal != reauthorizer.openResult.Principal {
		return streamClosedCredentials, "principal changed from " + reauthorizer.openResult.Principal + " to " + result.Principal
	}
	access, accessError := reauthorizer.accessCheck(reauthorizer.apiServer, reauthorizer.request, principalFromAuthenticationResult(result))
	if accessError != nil {
		return streamClosedAccess, accessError.Error()
	}
	if access != reauthorizer.openAccess {
		return streamClosedAccess, "filter changed from " + reauthorizer.openAccess + " to " + access
	}
	return "", ""
}

// deadline is the credential's real expiry for JWT and mTLS, otherwise the
// maximum stream lifetime; a zero or later expiry is capped by neither.
func (reauthorizer *streamReauthorizer) deadline() time.Time {
	expiresAt := reauthorizer.openResult.ExpiresAt
	if expiringAuthMethods[reauthorizer.openResult.AuthMethod] && !expiresAt.IsZero() {
		return expiresAt
	}
	return time.Now().Add(streamMaximumLifetime)
}

// deadlineReason names why the deadline ended the stream.
func (reauthorizer *streamReauthorizer) deadlineReason() streamCloseReason {
	if expiringAuthMethods[reauthorizer.openResult.AuthMethod] && !reauthorizer.openResult.ExpiresAt.IsZero() {
		return streamClosedCredentials
	}
	return streamClosedLifetimeReached
}

// jitteredInterval is the server's interval ±streamReauthorizationJitterFraction.
func (reauthorizer *streamReauthorizer) jitteredInterval() time.Duration {
	baseInterval := reauthorizer.apiServer.streamReauthorizationInterval
	jitterFactor := 1 + streamReauthorizationJitterFraction*(2*rand.Float64()-1) //nolint:gosec // timing jitter, not security
	return time.Duration(float64(baseInterval) * jitterFactor)
}

// end records the reason, logs and audits the forced end, and cancels the
// stream context, which every stream handler stops on.
func (reauthorizer *streamReauthorizer) end(cancelStream context.CancelFunc, closeReason streamCloseReason, detail string) {
	reauthorizer.mutex.Lock()
	reauthorizer.closeReason = closeReason
	reauthorizer.mutex.Unlock()
	logging.Default().Info("stream closed", "path", reauthorizer.request.URL.Path,
		"principal", reauthorizer.openResult.Principal, "reason", string(closeReason), "detail", detail)
	if reauthorizer.apiServer.apiAuthorizer != nil {
		reauthorizer.apiServer.apiAuthorizer.RecordAuditEntry(security.AuditEntry{
			Timestamp: time.Now(), Principal: reauthorizer.openResult.Principal, Action: streamCloseAuditAction,
			Target: reauthorizer.request.URL.Path, Decision: security.AuditDecisionDeny,
			AuthMethod: reauthorizer.openResult.AuthMethod, Policy: string(closeReason),
		})
	}
	cancelStream()
}

// reason returns the close reason, or "" when the stream was not ended.
func (reauthorizer *streamReauthorizer) reason() streamCloseReason {
	reauthorizer.mutex.Lock()
	defer reauthorizer.mutex.Unlock()
	return reauthorizer.closeReason
}

// streamResponseWriter records whether the stream started (200 sent) and
// bounds every write with streamWriteTimeout.
type streamResponseWriter struct {
	http.ResponseWriter      // ResponseWriter is the server's writer.
	started             bool // started is true once a 200 response began.
}

// WriteHeader records whether the stream started.
func (trackingWriter *streamResponseWriter) WriteHeader(statusCode int) {
	trackingWriter.started = statusCode == http.StatusOK
	trackingWriter.ResponseWriter.WriteHeader(statusCode)
}

// Write sets the write deadline and records an implicit 200.
func (trackingWriter *streamResponseWriter) Write(data []byte) (int, error) {
	_ = http.NewResponseController(trackingWriter.ResponseWriter).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if !trackingWriter.started {
		trackingWriter.started = true
	}
	return trackingWriter.ResponseWriter.Write(data)
}

// Flush passes flushes through (the SSE handlers require http.Flusher).
func (trackingWriter *streamResponseWriter) Flush() {
	if !trackingWriter.started {
		trackingWriter.started = true
	}
	if flusher, isFlusher := trackingWriter.ResponseWriter.(http.Flusher); isFlusher {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the server's writer.
func (trackingWriter *streamResponseWriter) Unwrap() http.ResponseWriter {
	return trackingWriter.ResponseWriter
}

// writeCloseFrame sends the final SSE event that tells the client why the
// stream ended; the CLI prints it and exits non-zero.
func (trackingWriter *streamResponseWriter) writeCloseFrame(closeReason streamCloseReason) {
	_, _ = fmt.Fprintf(trackingWriter, "event: closed\ndata: {\"reason\":%q}\n\n", strings.ReplaceAll(string(closeReason), `"`, ""))
	trackingWriter.Flush()
}

// watchStreamAccess is the access check of /api/watch: the workload
// visibility (cluster-wide or the sorted team list).
func watchStreamAccess(apiServer *Server, _ *http.Request, principal security.Principal) (string, error) {
	visibility := resolveWorkloadVisibility(apiServer.apiAuthorizer, principal)
	if visibility.isEmpty() {
		return "", errors.New("no workload.read grant at any scope")
	}
	if visibility.allTeams {
		return "teams:*", nil
	}
	return "teams:" + strings.Join(visibility.teams, ","), nil
}

// eventStreamAccess is the access check of /api/events/stream: workload.read
// at cluster scope.
func eventStreamAccess(apiServer *Server, _ *http.Request, principal security.Principal) (string, error) {
	if authError := apiServer.apiAuthorizer.AuthorizeAPI(principal, security.CapabilityWorkloadRead, security.ScopeCluster, nil); authError != nil {
		return "", authError
	}
	return "cluster", nil
}

// auditFollowAccess is the access check of /api/audit?follow=true:
// workload.read at cluster scope and the tenant filter.
func auditFollowAccess(apiServer *Server, request *http.Request, principal security.Principal) (string, error) {
	if _, capabilityError := eventStreamAccess(apiServer, request, principal); capabilityError != nil {
		return "", capabilityError
	}
	principalRequest := request.WithContext(security.WithPrincipalStruct(request.Context(), principal))
	tenantFilter, tenantAllowed := apiServer.resolveAuditTenantFilter(principalRequest)
	if !tenantAllowed {
		return "", errors.New("no team or platform membership")
	}
	return "tenant:" + tenantFilter, nil
}
