// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// statusScanPrefix is one prefix the status snapshot loads with a single Scan.
type statusScanPrefix struct {
	prefix   string // prefix is the store key prefix scanned
	required bool   // required makes a failed scan fail the status build instead of leaving the section empty
}

// statusScanPrefixes lists every prefix a status build reads (M175). The
// count is fixed, so store round-trips do not grow with cluster size. The
// prefixes are disjoint. secrets/ is optional: the api-server store role has
// no rule for it, so in production the secret list stays empty (M174).
var statusScanPrefixes = []statusScanPrefix{
	{prefix: types.ScanObservedInstances, required: true},
	{prefix: types.ScanDerivedInstances, required: true},
	{prefix: types.ScanObservedTelemetryInstances, required: true},
	{prefix: types.ScanObservedNodes, required: true},
	{prefix: types.ScanObservedVolumes, required: true},
	{prefix: types.ScanDesiredServices, required: true},
	{prefix: types.ScanDesiredCloudIdentities, required: true},
	{prefix: types.ScanDesiredRoutes, required: true},
	{prefix: types.ScanDerivedRoutes, required: true},
	{prefix: types.ScanDerivedCrashLoops, required: true},
	{prefix: types.ScanPlacements, required: true},
	{prefix: types.ScanNetworkVIPs, required: true},
	{prefix: types.ScanNetworkDNS, required: true},
	{prefix: types.ScanEndpoints, required: true},
	{prefix: security.SecretStorePrefix, required: false},
}

// statusFactSnapshot holds every fact a status build reads, loaded with one
// Scan per statusScanPrefixes entry. It answers exact-key and prefix lookups
// from memory, replacing the per-instance, per-node and per-service store
// reads. It is eventually consistent: scans are not pinned to one revision.
type statusFactSnapshot struct {
	sortedFacts    []store.Fact      // sortedFacts holds all loaded facts sorted by key, for prefix ranges
	factValueByKey map[string]string // factValueByKey maps each loaded key to its value, for exact-key lookups
}

// loadStatusFactSnapshot runs all status scans concurrently and indexes the
// results. A failed required scan returns an error, so a store outage or
// denial never looks like an empty cluster; a failed optional scan leaves
// its prefix unloaded and its section empty.
func loadStatusFactSnapshot(ctx context.Context, factStore store.StateStore) (*statusFactSnapshot, error) {
	scanResults := make([][]store.Fact, len(statusScanPrefixes))
	scanErrors := make([]error, len(statusScanPrefixes))
	var scanWaitGroup sync.WaitGroup
	for scanIndex, scanPrefix := range statusScanPrefixes {
		scanWaitGroup.Add(1)
		go func(scanIndex int, prefix string) {
			defer scanWaitGroup.Done()
			scanResults[scanIndex], scanErrors[scanIndex] = factStore.Scan(ctx, prefix)
		}(scanIndex, scanPrefix.prefix)
	}
	scanWaitGroup.Wait()

	snapshot := &statusFactSnapshot{factValueByKey: make(map[string]string)}
	for scanIndex, scanPrefix := range statusScanPrefixes {
		if scanErrors[scanIndex] != nil {
			if scanPrefix.required {
				return nil, fmt.Errorf("scan %s: %w", scanPrefix.prefix, scanErrors[scanIndex])
			}
			continue
		}
		for _, fact := range scanResults[scanIndex] {
			snapshot.sortedFacts = append(snapshot.sortedFacts, fact)
			snapshot.factValueByKey[fact.Key] = string(fact.Value)
		}
	}
	sort.Slice(snapshot.sortedFacts, func(i, j int) bool { return snapshot.sortedFacts[i].Key < snapshot.sortedFacts[j].Key })
	return snapshot, nil
}

// requireCoveredKey panics when key lies outside every status scan prefix: a
// lookup there would silently read nothing, so a builder reading a fact the
// loader never scanned is a programming error caught by the status tests.
func requireCoveredKey(key string) {
	for _, scanPrefix := range statusScanPrefixes {
		if strings.HasPrefix(key, scanPrefix.prefix) {
			return
		}
	}
	panic(fmt.Sprintf("status snapshot: %q is outside every loaded prefix; add it to statusScanPrefixes", key))
}

// value returns the loaded fact value at key and whether it exists, the
// in-memory equivalent of a store Get.
func (snapshot *statusFactSnapshot) value(key string) (string, bool) {
	requireCoveredKey(key)
	factValue, exists := snapshot.factValueByKey[key]
	return factValue, exists
}

// factsUnder returns the loaded facts whose keys start with prefix, sorted by
// key, the in-memory equivalent of a store Scan. The result shares memory
// with the snapshot and must not be modified.
func (snapshot *statusFactSnapshot) factsUnder(prefix string) []store.Fact {
	requireCoveredKey(prefix)
	rangeStart := sort.Search(len(snapshot.sortedFacts), func(factIndex int) bool {
		return snapshot.sortedFacts[factIndex].Key >= prefix
	})
	rangeEnd := rangeStart
	for rangeEnd < len(snapshot.sortedFacts) && strings.HasPrefix(snapshot.sortedFacts[rangeEnd].Key, prefix) {
		rangeEnd++
	}
	return snapshot.sortedFacts[rangeStart:rangeEnd]
}

// valueOrEmpty returns the loaded fact value at key, or "" when the fact does
// not exist (an optional status field stays blank).
func (snapshot *statusFactSnapshot) valueOrEmpty(key string) string {
	factValue, _ := snapshot.value(key)
	return factValue
}
