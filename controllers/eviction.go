// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// hardEvictionMemoryThresholdPercent is the memory utilization percentage above
// which the eviction controller begins stopping instances on a node.
const hardEvictionMemoryThresholdPercent = 90

// softEvictionMemoryThresholdPercent is the target utilization after eviction.
// Eviction stops once projected utilization drops below this threshold, and
// the node pressure marker is cleared when actual utilization is below it.
const softEvictionMemoryThresholdPercent = 80

// maxEvictionsPerCycle limits how many instances the eviction controller stops
// in a single reconciliation cycle. Each eviction emits 1 store operation
// (controller_stopped). Remaining evictions converge in subsequent cycles.
const maxEvictionsPerCycle = 10

// qosPriority maps QoS class names to eviction priority. Lower values are
// evicted first.
var qosPriority = map[string]int{
	"BestEffort": 0,
	"Burstable":  1,
	"Guaranteed": 2,
}

// evictionCandidate holds the information needed to decide which instance to
// evict: its ID, parent service, QoS class priority, and memory footprint.
type evictionCandidate struct {
	instanceID         string // instanceID is the unique instance identifier.
	serviceName        string // serviceName is the parent service.
	qosPriorityValue   int    // qosPriorityValue is the eviction order (lower = evicted first).
	memoryRequestBytes int    // memoryRequestBytes is the service's memory request in bytes.
}

// EvictionController watches node resource utilization and evicts instances
// when memory pressure exceeds the hard threshold. Instances are evicted in
// QoS priority order: BestEffort first, then Burstable, then Guaranteed.
// The failure controller handles replacement on healthy nodes.
type EvictionController struct{}

// NewEvictionController returns an EvictionController ready for use.
func NewEvictionController() *EvictionController {
	return &EvictionController{}
}

// Name returns "eviction", identifying this controller in logs and runner
// bookkeeping.
func (evictionController *EvictionController) Name() string { return "eviction" }

// Watch returns the fact prefixes that the eviction controller needs: observed
// nodes (capacity/available), observed instances (node mapping), desired
// services (memory requests), and derived services (QoS class).
func (evictionController *EvictionController) Watch() []string {
	return []string{
		types.ScanObservedNodes,
		types.ScanObservedInstances,
		types.ScanDesiredServices,
		types.ScanDerivedServices,
	}
}

// Reconcile examines node memory utilization and evicts lowest-QoS instances
// on nodes that exceed the hard eviction threshold.
func (evictionController *EvictionController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	nodeCapacity := extractNodeMemoryCapacity(facts)
	nodeAvailable := extractNodeMemoryAvailable(facts)
	instanceNodes := extractInstanceNodeMapping(facts)
	instanceServices := extractInstanceServiceMapping(facts)
	instanceStates := extractInstanceStates(facts)
	serviceQoSClasses := extractServiceQoSClasses(facts)
	serviceMemoryRequests := extractServiceMemoryRequests(facts)
	existingPressure := extractExistingNodePressure(facts)

	var allChanges []Change

	nodeIDs := sortedNodeIDs(nodeCapacity, nodeAvailable)
	for _, nodeID := range nodeIDs {
		capacityBytes, hasCapacity := nodeCapacity[nodeID]
		availableBytes, hasAvailable := nodeAvailable[nodeID]
		if !hasCapacity || !hasAvailable || capacityBytes == 0 {
			continue
		}

		utilizationPercent := calculateMemoryUtilizationPercent(capacityBytes, availableBytes)
		nodeChanges := reconcileNodePressure(
			nodeID, utilizationPercent, capacityBytes, availableBytes,
			instanceNodes, instanceServices, instanceStates,
			serviceQoSClasses, serviceMemoryRequests, existingPressure,
		)
		allChanges = append(allChanges, nodeChanges...)
	}

	return allChanges, nil
}

// reconcileNodePressure handles eviction decisions for a single node based on
// its current memory utilization.
func reconcileNodePressure(
	nodeID string,
	utilizationPercent int,
	capacityBytes int,
	availableBytes int,
	instanceNodes map[string]string,
	instanceServices map[string]string,
	instanceStates map[string]string,
	serviceQoSClasses map[string]string,
	serviceMemoryRequests map[string]int,
	existingPressure map[string]bool,
) []Change {
	_, alreadyUnderPressure := existingPressure[nodeID]

	if utilizationPercent < softEvictionMemoryThresholdPercent {
		if alreadyUnderPressure {
			return []Change{DeleteChange(types.KeyDerivedNodePressure(nodeID))}
		}
		return nil
	}

	if utilizationPercent < hardEvictionMemoryThresholdPercent {
		return nil
	}

	candidates := buildEvictionCandidates(
		nodeID, instanceNodes, instanceServices, instanceStates,
		serviceQoSClasses, serviceMemoryRequests,
	)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].qosPriorityValue != candidates[j].qosPriorityValue {
			return candidates[i].qosPriorityValue < candidates[j].qosPriorityValue
		}
		return candidates[i].instanceID < candidates[j].instanceID
	})

	changes := buildEvictionChanges(candidates, capacityBytes, availableBytes)

	if !alreadyUnderPressure {
		changes = append(changes, Change{
			Key:   types.KeyDerivedNodePressure(nodeID),
			Value: []byte("memory"),
		})
	}

	return changes
}

// buildEvictionCandidates collects all running instances on a node that are
// eligible for eviction, annotated with their QoS priority and memory request.
func buildEvictionCandidates(
	nodeID string,
	instanceNodes map[string]string,
	instanceServices map[string]string,
	instanceStates map[string]string,
	serviceQoSClasses map[string]string,
	serviceMemoryRequests map[string]int,
) []evictionCandidate {
	var candidates []evictionCandidate
	for instanceID, assignedNode := range instanceNodes {
		if assignedNode != nodeID {
			continue
		}
		if instanceStates[instanceID] != "running" {
			continue
		}
		serviceName := instanceServices[instanceID]
		if serviceName == "" {
			continue
		}
		qosClass := serviceQoSClasses[serviceName]
		priority, knownClass := qosPriority[qosClass]
		if !knownClass {
			priority = qosPriority["BestEffort"]
		}
		candidates = append(candidates, evictionCandidate{
			instanceID:         instanceID,
			serviceName:        serviceName,
			qosPriorityValue:   priority,
			memoryRequestBytes: serviceMemoryRequests[serviceName],
		})
	}
	return candidates
}

// buildEvictionChanges selects candidates to evict until projected memory
// drops below the soft threshold or the per-cycle cap is reached.
func buildEvictionChanges(
	candidates []evictionCandidate,
	capacityBytes int,
	availableBytes int,
) []Change {
	var changes []Change
	projectedAvailable := availableBytes
	evictionCount := 0

	for _, candidate := range candidates {
		if evictionCount >= maxEvictionsPerCycle {
			break
		}
		projectedUtilization := calculateMemoryUtilizationPercent(capacityBytes, projectedAvailable)
		if projectedUtilization < softEvictionMemoryThresholdPercent {
			break
		}
		changes = append(changes, Change{
			Key:   types.KeyDerivedInstanceControllerStopped(candidate.instanceID),
			Value: []byte("true"),
		})
		projectedAvailable += candidate.memoryRequestBytes
		evictionCount++
	}

	return changes
}

// calculateMemoryUtilizationPercent computes the integer percentage of memory
// used on a node: (capacity - available) / capacity * 100.
func calculateMemoryUtilizationPercent(capacityBytes int, availableBytes int) int {
	usedBytes := capacityBytes - availableBytes
	if usedBytes < 0 {
		return 0
	}
	return (usedBytes * 100) / capacityBytes
}

// extractNodeMemoryCapacity reads observed/node/{id}/capacity/memory facts and
// returns a map of nodeID → capacity in bytes.
func extractNodeMemoryCapacity(facts []store.Fact) map[string]int {
	capacities := make(map[string]int)
	prefix := "observed/node/"
	suffix := "/capacity/memory"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		nodeID := extractMiddleSegment(fact.Key, prefix, suffix)
		if nodeID == "" {
			continue
		}
		capacities[nodeID] = parseResourceQuantity(string(fact.Value))
	}
	return capacities
}

// extractNodeMemoryAvailable reads observed/node/{id}/available/memory facts
// and returns a map of nodeID → available bytes.
func extractNodeMemoryAvailable(facts []store.Fact) map[string]int {
	available := make(map[string]int)
	prefix := "observed/node/"
	suffix := "/available/memory"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		nodeID := extractMiddleSegment(fact.Key, prefix, suffix)
		if nodeID == "" {
			continue
		}
		available[nodeID] = parseResourceQuantity(string(fact.Value))
	}
	return available
}

// extractInstanceNodeMapping reads observed/instance/{id}/node facts and
// returns a map of instanceID → nodeID.
func extractInstanceNodeMapping(facts []store.Fact) map[string]string {
	mapping := make(map[string]string)
	prefix := "observed/instance/"
	suffix := "/node"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		instanceID := extractMiddleSegment(fact.Key, prefix, suffix)
		if instanceID == "" {
			continue
		}
		mapping[instanceID] = string(fact.Value)
	}
	return mapping
}

// extractInstanceServiceMapping reads observed/instance/{id}/service facts and
// returns a map of instanceID → serviceName.
func extractInstanceServiceMapping(facts []store.Fact) map[string]string {
	mapping := make(map[string]string)
	prefix := "observed/instance/"
	suffix := "/service"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		instanceID := extractMiddleSegment(fact.Key, prefix, suffix)
		if instanceID == "" {
			continue
		}
		mapping[instanceID] = string(fact.Value)
	}
	return mapping
}

// extractInstanceStates reads observed/instance/{id}/state facts and returns
// a map of instanceID → state string.
func extractInstanceStates(facts []store.Fact) map[string]string {
	states := make(map[string]string)
	prefix := "observed/instance/"
	suffix := "/state"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		instanceID := extractMiddleSegment(fact.Key, prefix, suffix)
		if instanceID == "" {
			continue
		}
		// Disambiguate from /node (which also appears under instance prefix)
		// by verifying the key ends exactly with "/state"
		if strings.HasSuffix(fact.Key, "/node") {
			continue
		}
		states[instanceID] = string(fact.Value)
	}
	return states
}

// extractServiceQoSClasses reads derived/service/{name}/qos_class facts and
// returns a map of serviceName → QoS class string.
func extractServiceQoSClasses(facts []store.Fact) map[string]string {
	classes := make(map[string]string)
	prefix := "derived/service/"
	suffix := "/qos_class"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		serviceName := extractMiddleSegment(fact.Key, prefix, suffix)
		if serviceName == "" {
			continue
		}
		classes[serviceName] = string(fact.Value)
	}
	return classes
}

// extractServiceMemoryRequests reads desired/service/{name}/resources/memory
// facts and returns a map of serviceName → memory request in bytes.
func extractServiceMemoryRequests(facts []store.Fact) map[string]int {
	requests := make(map[string]int)
	prefix := "desired/service/"
	suffix := "/resources/memory"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		serviceName := extractMiddleSegment(fact.Key, prefix, suffix)
		if serviceName == "" {
			continue
		}
		requests[serviceName] = parseResourceQuantity(string(fact.Value))
	}
	return requests
}

// extractExistingNodePressure reads derived/node/{id}/pressure facts and
// returns a set of nodeIDs that currently have a pressure marker.
func extractExistingNodePressure(facts []store.Fact) map[string]bool {
	pressure := make(map[string]bool)
	prefix := "derived/node/"
	suffix := "/pressure"
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) || !strings.HasSuffix(fact.Key, suffix) {
			continue
		}
		nodeID := extractMiddleSegment(fact.Key, prefix, suffix)
		if nodeID == "" {
			continue
		}
		pressure[nodeID] = true
	}
	return pressure
}

// extractMiddleSegment extracts the part of a key between a known prefix and
// suffix. Returns empty string if the key doesn't match.
func extractMiddleSegment(key string, prefix string, suffix string) string {
	if len(key) <= len(prefix)+len(suffix) {
		return ""
	}
	middle := key[len(prefix) : len(key)-len(suffix)]
	if strings.Contains(middle, "/") {
		return ""
	}
	return middle
}

// sortedNodeIDs returns a deterministic list of node IDs from the capacity and
// available maps combined.
func sortedNodeIDs(capacityMap map[string]int, availableMap map[string]int) []string {
	seen := make(map[string]bool)
	for nodeID := range capacityMap {
		seen[nodeID] = true
	}
	for nodeID := range availableMap {
		seen[nodeID] = true
	}
	nodeIDs := make([]string, 0, len(seen))
	for nodeID := range seen {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	return nodeIDs
}
