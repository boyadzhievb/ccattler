// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// state_access.go decides which store keys /api/state and /api/watch may
// return. It is an allowlist: only orchestration state is readable, so new
// store areas (secrets, credentials, auth, audit, enrollment) stay hidden
// unless they are added here on purpose.
package api

import (
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// readableStateRoots are the top-level store areas the raw fact API may expose.
// A prefix or key must equal a root or continue it with "/".
var readableStateRoots = []string{
	types.PrefixDesired,
	types.PrefixEffective,
	types.PrefixObserved,
	types.PrefixPlacement,
	types.PrefixEndpoint,
	types.PrefixIntent,
	types.PrefixNetwork,
	types.PrefixDerived,
	types.PrefixEvent,
}

// unreadableKeySuffixes mark keys inside readable roots that hold private
// material, such as gate TLS private keys under desired/route/{name}/tls/key
// and derived/route/{name}/tls/key. They are dropped from scans and watches.
var unreadableKeySuffixes = []string{
	"/tls/key",
}

// isReadableStatePrefix reports whether a scan or watch prefix lies inside a
// readable root. Prefixes shorter than a full root segment (e.g. "s" or
// "secret") are refused, because a store prefix scan would match more than one area.
func isReadableStatePrefix(prefix string) bool {
	for _, readableRoot := range readableStateRoots {
		if prefix == readableRoot || strings.HasPrefix(prefix, readableRoot+"/") {
			return true
		}
	}
	return false
}

// isReadableFactKey reports whether a single fact may be returned: it must be
// inside a readable root and must not hold private material.
func isReadableFactKey(factKey string) bool {
	if !isReadableStatePrefix(factKey) {
		return false
	}
	for _, unreadableSuffix := range unreadableKeySuffixes {
		if strings.HasSuffix(factKey, unreadableSuffix) {
			return false
		}
	}
	return true
}

// filterReadableFacts returns the facts whose keys pass isReadableFactKey.
func filterReadableFacts(facts []store.Fact) []store.Fact {
	readableFacts := make([]store.Fact, 0, len(facts))
	for _, fact := range facts {
		if isReadableFactKey(fact.Key) {
			readableFacts = append(readableFacts, fact)
		}
	}
	return readableFacts
}
