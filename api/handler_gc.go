// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// gcResponseBody is the JSON response for POST /api/gc.
type gcResponseBody struct {
	Deleted     int      `json:"deleted"`
	InstanceIDs []string `json:"instance_ids"`
}

// handleGC processes POST /api/gc to manually garbage-collect stopped instance
// facts from the store. Returns the count and IDs of deleted instances.
func (apiServer *Server) handleGC(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := request.Context()
	deletedInstanceIDs, deleteError := apiServer.executeInstanceGarbageCollection(ctx)
	if deleteError != nil {
		http.Error(responseWriter, deleteError.Error(), http.StatusInternalServerError)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	responseBody := gcResponseBody{
		Deleted:     len(deletedInstanceIDs),
		InstanceIDs: deletedInstanceIDs,
	}
	_ = json.NewEncoder(responseWriter).Encode(responseBody)
}

// executeInstanceGarbageCollection scans the store for stopped instances whose
// owning service is fully converged, then deletes all their facts. Returns the
// IDs of deleted instances.
func (apiServer *Server) executeInstanceGarbageCollection(ctx context.Context) ([]string, error) {
	observedFacts, observedError := apiServer.factStore.Scan(ctx, types.ScanObservedInstances)
	if observedError != nil {
		return nil, fmt.Errorf("scan observed instances: %w", observedError)
	}
	derivedFacts, derivedError := apiServer.factStore.Scan(ctx, types.ScanDerivedInstances)
	if derivedError != nil {
		return nil, fmt.Errorf("scan derived instances: %w", derivedError)
	}
	effectiveFacts, effectiveError := apiServer.factStore.Scan(ctx, types.ScanEffectiveServices)
	if effectiveError != nil {
		return nil, fmt.Errorf("scan effective services: %w", effectiveError)
	}
	placementFacts, placementError := apiServer.factStore.Scan(ctx, types.ScanPlacements)
	if placementError != nil {
		return nil, fmt.Errorf("scan placements: %w", placementError)
	}

	instanceFields := parseGCInstanceFields(observedFacts, derivedFacts)
	desiredCounts := parseGCEffectiveServiceCounts(effectiveFacts)
	placementMap := parseGCPlacementMap(placementFacts)

	runningCounts := make(map[string]int)
	serviceByInstance := make(map[string]string)
	stateByInstance := make(map[string]string)

	for instanceID, fields := range instanceFields {
		serviceName := fields["service"]
		serviceByInstance[instanceID] = serviceName
		effectiveState := gcEffectiveState(fields)
		stateByInstance[instanceID] = effectiveState
		if effectiveState == "running" {
			runningCounts[serviceName]++
		}
	}

	var deletedInstanceIDs []string
	sortedInstanceIDs := make([]string, 0, len(instanceFields))
	for instanceID := range instanceFields {
		sortedInstanceIDs = append(sortedInstanceIDs, instanceID)
	}
	sort.Strings(sortedInstanceIDs)

	for _, instanceID := range sortedInstanceIDs {
		state := stateByInstance[instanceID]
		if state != "stopped" && state != "failed" {
			continue
		}
		serviceName := serviceByInstance[instanceID]
		desiredCount := desiredCounts[serviceName]
		if runningCounts[serviceName] < desiredCount {
			continue
		}

		keysToDelete := collectInstanceFactKeys(instanceID, observedFacts, derivedFacts, placementMap)
		for _, factKey := range keysToDelete {
			if deleteError := apiServer.factStore.Delete(ctx, factKey); deleteError != nil {
				logging.Default().Warn("gc: failed to delete fact", "key", factKey, "error", deleteError.Error())
			}
		}
		deletedInstanceIDs = append(deletedInstanceIDs, instanceID)
	}

	return deletedInstanceIDs, nil
}

// parseGCInstanceFields builds a map of instanceID → field → value from
// observed and derived instance facts.
func parseGCInstanceFields(observedFacts []store.Fact, derivedFacts []store.Fact) map[string]map[string]string {
	fields := make(map[string]map[string]string)
	for _, fact := range observedFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		instanceID := parts[0]
		if fields[instanceID] == nil {
			fields[instanceID] = make(map[string]string)
		}
		if len(parts) == 2 {
			fields[instanceID][parts[1]] = string(fact.Value)
		}
	}
	for _, fact := range derivedFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		instanceID := parts[0]
		if fields[instanceID] == nil {
			fields[instanceID] = make(map[string]string)
		}
		if len(parts) == 2 {
			fields[instanceID][parts[1]] = string(fact.Value)
		}
	}
	return fields
}

// parseGCEffectiveServiceCounts extracts desired instance counts per service.
func parseGCEffectiveServiceCounts(facts []store.Fact) map[string]int {
	counts := make(map[string]int)
	for _, fact := range facts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEffectiveServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "instances" {
			parsedCount := 0
			_, _ = fmt.Sscanf(string(fact.Value), "%d", &parsedCount)
			counts[parts[0]] = parsedCount
		}
	}
	return counts
}

// parseGCPlacementMap extracts placement/instance/{id} → nodeID.
func parseGCPlacementMap(facts []store.Fact) map[string]string {
	placements := make(map[string]string)
	for _, fact := range facts {
		instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		if instanceID != "" && !strings.Contains(instanceID, "/") {
			placements[instanceID] = string(fact.Value)
		}
	}
	return placements
}

// gcEffectiveState returns the effective state of an instance, considering
// the controller_stopped marker.
func gcEffectiveState(fields map[string]string) string {
	if fields["controller_stopped"] == "true" {
		return "stopped"
	}
	return fields["state"]
}

// collectInstanceFactKeys returns all store keys belonging to the given
// instance across observed, derived, and placement prefixes.
func collectInstanceFactKeys(instanceID string, observedFacts []store.Fact, derivedFacts []store.Fact, placementMap map[string]string) []string {
	observedPrefix := types.ScanObservedInstances + instanceID
	derivedPrefix := types.ScanDerivedInstances + instanceID

	var keysToDelete []string
	for _, fact := range observedFacts {
		if fact.Key == observedPrefix || strings.HasPrefix(fact.Key, observedPrefix+"/") {
			keysToDelete = append(keysToDelete, fact.Key)
		}
	}
	for _, fact := range derivedFacts {
		if fact.Key == derivedPrefix || strings.HasPrefix(fact.Key, derivedPrefix+"/") {
			keysToDelete = append(keysToDelete, fact.Key)
		}
	}

	keysToDelete = append(keysToDelete, types.KeyPlacementInstance(instanceID))
	if nodeID, hasPlacement := placementMap[instanceID]; hasPlacement && nodeID != "" {
		keysToDelete = append(keysToDelete, types.KeyPlacementNodeInstance(nodeID, instanceID))
	}

	return keysToDelete
}
