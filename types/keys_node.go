// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyObservedNode returns the store path for a node's root marker key.
// Path: observed/node/{nodeID}
func KeyObservedNode(nodeID string) string {
	return fmt.Sprintf("%s/node/%s", PrefixObserved, nodeID)
}

// KeyObservedNodeState returns the store path for a node's observed lifecycle state.
// Path: observed/node/{nodeID}/state
func KeyObservedNodeState(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/state", PrefixObserved, nodeID)
}

// KeyObservedNodeCapacityCPU returns the store path for a node's total CPU capacity.
// Path: observed/node/{nodeID}/capacity/cpu
func KeyObservedNodeCapacityCPU(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/capacity/cpu", PrefixObserved, nodeID)
}

// KeyObservedNodeCapacityMemory returns the store path for a node's total memory capacity.
// Path: observed/node/{nodeID}/capacity/memory
func KeyObservedNodeCapacityMemory(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/capacity/memory", PrefixObserved, nodeID)
}

// KeyObservedNodeAvailableCPU returns the store path for a node's currently available CPU.
// Path: observed/node/{nodeID}/available/cpu
func KeyObservedNodeAvailableCPU(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/available/cpu", PrefixObserved, nodeID)
}

// KeyObservedNodeAvailableMemory returns the store path for a node's currently available memory.
// Path: observed/node/{nodeID}/available/memory
func KeyObservedNodeAvailableMemory(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/available/memory", PrefixObserved, nodeID)
}

// KeyObservedNodeArchitecture returns the store path for a node's CPU architecture.
// Path: observed/node/{nodeID}/architecture
func KeyObservedNodeArchitecture(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/architecture", PrefixObserved, nodeID)
}

// KeyObservedNodeZone returns the store path for a node's availability zone.
// Path: observed/node/{nodeID}/zone
func KeyObservedNodeZone(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/zone", PrefixObserved, nodeID)
}

// KeyObservedNodeAddress returns the store path for a node's advertised
// routable IP address. This is the LAN-reachable address other nodes use
// for cross-host data plane DNAT targets.
// Path: observed/node/{nodeID}/address
func KeyObservedNodeAddress(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/address", PrefixObserved, nodeID)
}

// KeyObservedNodeDebugAddress returns the store path for a node's debug API
// listen address. The agent publishes this when --debug-listen is set so that
// `cca node-inspect` can discover and query the agent's debug endpoints.
// Path: observed/node/{nodeID}/debug_address
func KeyObservedNodeDebugAddress(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/debug_address", PrefixObserved, nodeID)
}

// KeyLeaseNode returns the store path for a node's heartbeat lease key.
// The node agent periodically refreshes this lease; expiry signals the node
// is unreachable.
// Path: lease/node/{nodeID}
func KeyLeaseNode(nodeID string) string {
	return fmt.Sprintf("%s/node/%s", PrefixLease, nodeID)
}

// Node utilization keys. The agent reports current resource utilization as
// observed facts, separate from capacity/allocated facts used by the scheduler.

// KeyObservedNodeUtilizationCPU returns the store path for a node's current
// CPU utilization as a percentage (0-100).
// Path: observed/node/{nodeID}/utilization/cpu
func KeyObservedNodeUtilizationCPU(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/utilization/cpu", PrefixObserved, nodeID)
}

// KeyObservedNodeUtilizationMemory returns the store path for a node's current
// memory utilization as a percentage (0-100).
// Path: observed/node/{nodeID}/utilization/memory
func KeyObservedNodeUtilizationMemory(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/utilization/memory", PrefixObserved, nodeID)
}

// KeyObservedNodeWorkloadCount returns the store path for the number of workloads
// running on a node. Stored under the telemetry prefix to avoid triggering
// controller watches on observed/node/.
// Path: observed/telemetry/node/{nodeID}/workloads
func KeyObservedNodeWorkloadCount(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/workloads", PrefixObservedTelemetry, nodeID)
}

// KeyDerivedNodeDrainStarted returns the path for a node's drain start timestamp.
// The value is a Unix-millisecond timestamp recorded when the drain was initiated.
// Path: derived/node/{nodeID}/drain/started
func KeyDerivedNodeDrainStarted(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/drain/started", PrefixDerived, nodeID)
}

// KeyDerivedNodeDrainInitiator returns the path for who initiated the drain.
// The value identifies the principal that requested the drain (e.g. "user:cli").
// Path: derived/node/{nodeID}/drain/initiator
func KeyDerivedNodeDrainInitiator(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/drain/initiator", PrefixDerived, nodeID)
}

// KeyDerivedNodeDrainComplete returns the path for the drain-complete marker.
// Written when zero active instances remain on a draining node.
// Path: derived/node/{nodeID}/drain/complete
func KeyDerivedNodeDrainComplete(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/drain/complete", PrefixDerived, nodeID)
}

// KeyDerivedNodeDrainEvict returns the path for a per-instance eviction marker.
// The drain controller writes this to signal the agent that it should stop the
// instance. Agents read these markers to avoid overwriting them with "running".
// Path: derived/node/{nodeID}/drain/evict/{instanceID}
func KeyDerivedNodeDrainEvict(nodeID string, instanceID string) string {
	return fmt.Sprintf("%s/node/%s/drain/evict/%s", PrefixDerived, nodeID, instanceID)
}

// KeyDerivedNodePressure returns the store path for a node's resource pressure marker.
// Written by the eviction controller when memory utilization exceeds the hard threshold.
// Path: derived/node/{nodeID}/pressure
func KeyDerivedNodePressure(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/pressure", PrefixDerived, nodeID)
}
