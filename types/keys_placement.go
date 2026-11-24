// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyPlacementInstance returns the store path for the scheduler's placement decision
// for a given instance. The value stored at this key is the target node ID.
// Path: placement/instance/{instanceID}
func KeyPlacementInstance(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s", PrefixPlacement, instanceID)
}

// KeyPlacementNodeInstance returns the per-node reverse index for a placement.
// Agents scan this prefix to find their assigned instances without reading all
// placements. The value is empty — the key existence is the signal.
// Path: placement/node/{nodeID}/{instanceID}
func KeyPlacementNodeInstance(nodeID string, instanceID string) string {
	return fmt.Sprintf("%s/node/%s/%s", PrefixPlacement, nodeID, instanceID)
}

// ScanPlacementsForNode returns the prefix to scan for all instances placed
// on a specific node.
func ScanPlacementsForNode(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/", PrefixPlacement, nodeID)
}

// KeyDesiredServicePlacementArchitecture returns the store path for a service's
// required CPU architecture constraint.
func KeyDesiredServicePlacementArchitecture(name string) string {
	return fmt.Sprintf("%s/service/%s/placement/architecture", PrefixDesired, name)
}

// KeyDesiredServicePlacementZonePolicy returns the store path for a service's
// zone placement policy ("spread" or a specific zone name).
func KeyDesiredServicePlacementZonePolicy(name string) string {
	return fmt.Sprintf("%s/service/%s/placement/zone", PrefixDesired, name)
}

// KeyDesiredServicePlacementZoneMaxSkew returns the store path for a service's
// maximum allowed skew between zone instance counts (only meaningful with "spread").
func KeyDesiredServicePlacementZoneMaxSkew(name string) string {
	return fmt.Sprintf("%s/service/%s/placement/zone/maxSkew", PrefixDesired, name)
}

// KeyDesiredServicePlacementRequire returns the store path for a hard node label
// requirement. The scheduler will only place instances on nodes where this label matches.
// Path: desired/service/{name}/placement/require/{label}
func KeyDesiredServicePlacementRequire(name, label string) string {
	return fmt.Sprintf("%s/service/%s/placement/require/%s", PrefixDesired, name, label)
}

// KeyDesiredServicePlacementPrefer returns the store path for a soft node label
// preference. The scheduler gives a scoring bonus to nodes matching this label.
// Path: desired/service/{name}/placement/prefer/{label}
func KeyDesiredServicePlacementPrefer(name, label string) string {
	return fmt.Sprintf("%s/service/%s/placement/prefer/%s", PrefixDesired, name, label)
}

// KeyDesiredServicePlacementAccept returns the store path for a node restriction
// acceptance. Services with this key can be placed on restricted nodes.
// Path: desired/service/{name}/placement/accept/{label}
func KeyDesiredServicePlacementAccept(name, label string) string {
	return fmt.Sprintf("%s/service/%s/placement/accept/%s", PrefixDesired, name, label)
}

// KeyDesiredServicePlacementColocate returns the store path for a soft
// co-location hint. The scheduler prefers nodes running the target service.
// Path: desired/service/{name}/placement/colocate/{targetService}
func KeyDesiredServicePlacementColocate(name, targetService string) string {
	return fmt.Sprintf("%s/service/%s/placement/colocate/%s", PrefixDesired, name, targetService)
}

// KeyDesiredServicePlacementSeparate returns the store path for a soft
// separation hint. The scheduler avoids nodes running the target service.
// Path: desired/service/{name}/placement/separate/{targetService}
func KeyDesiredServicePlacementSeparate(name, targetService string) string {
	return fmt.Sprintf("%s/service/%s/placement/separate/%s", PrefixDesired, name, targetService)
}

// KeyDesiredServicePlacementRebalance returns the store path for a service's
// rebalance opt-in. "true" lets the scheduler move running instances.
// Path: desired/service/{name}/placement/rebalance
func KeyDesiredServicePlacementRebalance(name string) string {
	return fmt.Sprintf("%s/service/%s/placement/rebalance", PrefixDesired, name)
}

// KeyDesiredServicePlacementMode returns the store path for a service's
// placement mode (e.g. "per-node" for one instance per alive node).
// Path: desired/service/{name}/placement/mode
func KeyDesiredServicePlacementMode(name string) string {
	return fmt.Sprintf("%s/service/%s/placement/mode", PrefixDesired, name)
}

// KeyEffectiveServicePlacementMode returns the store path for the resolved
// placement mode. Written by the IntentResolverController so downstream
// controllers (InstanceController, Scheduler) know the service mode.
// Path: effective/service/{name}/placement_mode
func KeyEffectiveServicePlacementMode(name string) string {
	return fmt.Sprintf("%s/service/%s/placement_mode", PrefixEffective, name)
}

// KeyDesiredServicePriority returns the store path for a service's scheduling
// and eviction priority (0–1000). Higher values are harder to evict/preempt.
// Path: desired/service/{name}/priority
func KeyDesiredServicePriority(name string) string {
	return fmt.Sprintf("%s/service/%s/priority", PrefixDesired, name)
}

// KeyEffectiveServicePriority returns the store path for the resolved service
// priority. Written directly by the compiler (priority is static, not scaled).
// Path: effective/service/{name}/priority
func KeyEffectiveServicePriority(name string) string {
	return fmt.Sprintf("%s/service/%s/priority", PrefixEffective, name)
}

// KeyDesiredClusterAutoscaleMinNodes returns the store path for the minimum number
// of nodes the cluster autoscaler should maintain.
func KeyDesiredClusterAutoscaleMinNodes() string {
	return fmt.Sprintf("%s/cluster/autoscale/min_nodes", PrefixDesired)
}

// KeyDesiredClusterAutoscaleMaxNodes returns the store path for the maximum number
// of nodes the cluster autoscaler can provision.
func KeyDesiredClusterAutoscaleMaxNodes() string {
	return fmt.Sprintf("%s/cluster/autoscale/max_nodes", PrefixDesired)
}

// Service group key prefixes and helpers.

// ScanDesiredGroups scans all desired service group definitions.
const ScanDesiredGroups = PrefixDesired + "/group/"

// KeyDesiredGroup returns the store path for a service group's root marker key.
// Path: desired/group/{name}
func KeyDesiredGroup(groupName string) string {
	return fmt.Sprintf("%s/group/%s", PrefixDesired, groupName)
}

// KeyDesiredGroupProcess returns the store path for a service membership
// in a group. The value is "true".
// Path: desired/group/{name}/process/{serviceName}
func KeyDesiredGroupProcess(groupName string, serviceName string) string {
	return fmt.Sprintf("%s/group/%s/process/%s", PrefixDesired, groupName, serviceName)
}

// KeyDesiredGroupShareNetwork returns the store path for a group's shared
// network namespace flag. The value is "true" when the group shares networking.
// Path: desired/group/{name}/share/network
func KeyDesiredGroupShareNetwork(groupName string) string {
	return fmt.Sprintf("%s/group/%s/share/network", PrefixDesired, groupName)
}

// KeyDesiredGroupShareVolume returns the store path for a shared volume
// binding within a group.
// Path: desired/group/{name}/share/volume/{volumeName}
func KeyDesiredGroupShareVolume(groupName string, volumeName string) string {
	return fmt.Sprintf("%s/group/%s/share/volume/%s", PrefixDesired, groupName, volumeName)
}

// --- Capacity request keys ---

// KeyDerivedCapacityRequestState returns the store path for a capacity
// request's lifecycle state.
// Path: derived/capacity/request/{requestID}/state
func KeyDerivedCapacityRequestState(requestID string) string {
	return fmt.Sprintf("%s/capacity/request/%s/state", PrefixDerived, requestID)
}

// KeyDerivedCapacityRequestRequirements returns the store path for a capacity
// request's resource requirements (JSON-encoded).
// Path: derived/capacity/request/{requestID}/requirements
func KeyDerivedCapacityRequestRequirements(requestID string) string {
	return fmt.Sprintf("%s/capacity/request/%s/requirements", PrefixDerived, requestID)
}

// KeyDerivedCapacityRequestReason returns the store path for a capacity
// request's human-readable reason.
// Path: derived/capacity/request/{requestID}/reason
func KeyDerivedCapacityRequestReason(requestID string) string {
	return fmt.Sprintf("%s/capacity/request/%s/reason", PrefixDerived, requestID)
}

// KeyDerivedCapacityRequestNodeID returns the store path for the node ID
// assigned by the infrastructure provider after a successful launch.
// Path: derived/capacity/request/{requestID}/node_id
func KeyDerivedCapacityRequestNodeID(requestID string) string {
	return fmt.Sprintf("%s/capacity/request/%s/node_id", PrefixDerived, requestID)
}

// --- Scheduler unplaced-demand keys ---

// KeyDerivedSchedulerUnplacedReason returns the store path for the reason
// the scheduler could not place an instance.
// Path: derived/scheduler/unplaced/{instanceID}/reason
func KeyDerivedSchedulerUnplacedReason(instanceID string) string {
	return fmt.Sprintf("%s/scheduler/unplaced/%s/reason", PrefixDerived, instanceID)
}

// KeyDerivedSchedulerUnplacedRequirements returns the store path for the
// resource requirements of an unplaced instance (JSON-encoded).
// Path: derived/scheduler/unplaced/{instanceID}/requirements
func KeyDerivedSchedulerUnplacedRequirements(instanceID string) string {
	return fmt.Sprintf("%s/scheduler/unplaced/%s/requirements", PrefixDerived, instanceID)
}

// KeyDerivedSchedulerRebalanced returns the store path of the RFC3339 time
// the scheduler last moved an instance during rebalancing (M267 D4).
// Path: derived/scheduler/rebalanced/{instanceID}
func KeyDerivedSchedulerRebalanced(instanceID string) string {
	return ScanDerivedSchedulerRebalanced + instanceID
}

// KeyDerivedSchedulerInvalidResourceRequest returns the store path the
// scheduler writes for a service whose stored request it cannot parse; the
// value is "{key}={value}" of the unreadable fact (M243 D5).
// Path: derived/scheduler/invalid_resource_request/{serviceName}
func KeyDerivedSchedulerInvalidResourceRequest(serviceName string) string {
	return ScanDerivedSchedulerInvalidResourceRequests + serviceName
}
