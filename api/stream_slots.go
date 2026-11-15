// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// stream_slots.go counts open streams (/api/watch, /api/events/stream,
// /api/audit?follow=true) per server and per principal, so one principal
// cannot take every slot (M225, design/plans/plan-per-principal-stream-cap.md).
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
)

const (
	// maxStreamsPerPrincipal is how many streams one principal may hold open
	// on one API server. Shared identities (user:local-admin) share it.
	maxStreamsPerPrincipal = 32
	// streamRefusalLogInterval is how often a principal's cap refusals are
	// logged (one line, not one per refused open).
	streamRefusalLogInterval = time.Minute
)

// streamSlotRefusal says why a slot was not reserved; "" means reserved.
type streamSlotRefusal string

const (
	streamSlotServerFull    streamSlotRefusal = "server full"    // the server holds maxConcurrentWatchConnections
	streamSlotPrincipalFull streamSlotRefusal = "principal full" // the principal holds maxStreamsPerPrincipal
)

// streamSlotRegistry counts open streams in total and per principal under
// one mutex, so a check and its increment cannot interleave.
type streamSlotRegistry struct {
	mutex            sync.Mutex           // mutex guards every field below.
	totalOpen        int                  // totalOpen is the number of open streams on this server.
	openByPrincipal  map[string]int       // openByPrincipal counts open streams per kind:name; no zero entries.
	lastRefusalLogAt map[string]time.Time // lastRefusalLogAt rate-limits the cap refusal log per principal.
}

// principalStreamCount is one principal's open streams in a snapshot.
type principalStreamCount struct {
	Principal   string `json:"principal"`    // Principal is the kind:name.
	OpenStreams int    `json:"open_streams"` // OpenStreams is how many streams it holds open.
}

// streamSlotSnapshot is the answer of GET /api/streams.
type streamSlotSnapshot struct {
	Total      int                    `json:"total"`      // Total is every open stream on this server.
	Limit      int                    `json:"limit"`      // Limit is maxStreamsPerPrincipal.
	Principals []principalStreamCount `json:"principals"` // Principals is sorted by open streams, then name.
}

// newStreamSlotRegistry returns an empty registry.
func newStreamSlotRegistry() *streamSlotRegistry {
	return &streamSlotRegistry{openByPrincipal: map[string]int{}, lastRefusalLogAt: map[string]time.Time{}}
}

// reserve takes a slot for principal, or returns why not. The release frees
// the slot once, however often it is called.
func (registry *streamSlotRegistry) reserve(principal string) (func(), streamSlotRefusal) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if registry.totalOpen >= maxConcurrentWatchConnections {
		return nil, streamSlotServerFull
	}
	if registry.openByPrincipal[principal] >= maxStreamsPerPrincipal {
		registry.logCapRefusal(principal)
		return nil, streamSlotPrincipalFull
	}
	registry.totalOpen++
	registry.openByPrincipal[principal]++
	var releaseOnce sync.Once
	return func() { releaseOnce.Do(func() { registry.release(principal) }) }, ""
}

// release frees one of principal's slots and drops its entries at zero.
func (registry *streamSlotRegistry) release(principal string) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	registry.totalOpen--
	registry.openByPrincipal[principal]--
	if registry.openByPrincipal[principal] <= 0 {
		delete(registry.openByPrincipal, principal)
		delete(registry.lastRefusalLogAt, principal)
	}
}

// logCapRefusal logs that principal hit its cap, at most once per
// streamRefusalLogInterval. The caller holds the mutex.
func (registry *streamSlotRegistry) logCapRefusal(principal string) {
	if time.Since(registry.lastRefusalLogAt[principal]) < streamRefusalLogInterval {
		return
	}
	registry.lastRefusalLogAt[principal] = time.Now()
	logging.Default().Warn("stream refused: principal at its open-stream limit",
		"principal", principal, "open_streams", strconv.Itoa(registry.openByPrincipal[principal]), "limit", strconv.Itoa(maxStreamsPerPrincipal))
}

// snapshot returns the open streams per principal, most first.
func (registry *streamSlotRegistry) snapshot() streamSlotSnapshot {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	snapshot := streamSlotSnapshot{Total: registry.totalOpen, Limit: maxStreamsPerPrincipal, Principals: []principalStreamCount{}}
	for principal, openStreams := range registry.openByPrincipal {
		snapshot.Principals = append(snapshot.Principals, principalStreamCount{Principal: principal, OpenStreams: openStreams})
	}
	sort.Slice(snapshot.Principals, func(leftIndex, rightIndex int) bool {
		left, right := snapshot.Principals[leftIndex], snapshot.Principals[rightIndex]
		if left.OpenStreams != right.OpenStreams {
			return left.OpenStreams > right.OpenStreams
		}
		return left.Principal < right.Principal
	})
	return snapshot
}

// reserveStreamSlot takes a stream slot for the request's principal or
// answers the refusal (401 without a principal, 503 server full, 429
// principal at its limit). The caller must call the release when done.
func (apiServer *Server) reserveStreamSlot(responseWriter http.ResponseWriter, request *http.Request) (func(), bool) {
	principal := security.PrincipalStructFromContext(request.Context())
	if principal.Name == "" {
		writeJSONError(responseWriter, "authentication required", http.StatusUnauthorized)
		return nil, false
	}
	releaseStreamSlot, refusal := apiServer.streamSlots.reserve(principal.String())
	switch refusal {
	case streamSlotServerFull:
		writeJSONError(responseWriter, "too many concurrent watch connections", http.StatusServiceUnavailable)
		return nil, false
	case streamSlotPrincipalFull:
		writeJSONError(responseWriter, fmt.Sprintf("too many open streams for %s (limit %d): close some cca watch / events --follow / audit --follow",
			principal.String(), maxStreamsPerPrincipal), http.StatusTooManyRequests)
		return nil, false
	}
	return releaseStreamSlot, true
}

// handleStreams serves GET /api/streams: open streams per principal on this
// API server, for cluster admins (who holds the slots).
func (apiServer *Server) handleStreams(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(responseWriter, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityClusterAdmin, security.ScopeCluster) {
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(apiServer.streamSlots.snapshot())
}
