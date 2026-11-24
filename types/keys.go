// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

// Fact-store keys are split by subject. This file holds what every subject
// shares: Root, the layer prefixes and the shared scan prefixes. Each subject's
// constructors, scan prefixes and key constants are in its own file:
//
//	keys_auth.go          roles, grants, groups, policies, users
//	keys_cloud.go         cloud provider, load balancers, cloud routes, cloud identity, credentials
//	keys_instance.go      instance facts
//	keys_job.go           jobs and cron jobs
//	keys_network.go       endpoints, subnets, allocations, VIPs, DNS, network policy
//	keys_node.go          nodes, leases, utilization, drain, pressure
//	keys_placement.go     placement, groups, priority, unplaced demand, capacity requests, cluster autoscale bounds
//	keys_probe.go         probe config and results
//	keys_rollout.go       update pacing, disruption budget, rollout state
//	keys_route.go         routes and route TLS key secret names
//	keys_secret.go        secret generations, delivery, gate keys, keyring
//	keys_service.go       the rest of a service: image, command, logging, lifecycle, expose,
//	                      resources, health, tags, stateful, config, secrets, connections, init steps
//	keys_service_scale.go instance counts, scaling policy, autoscaler intents, metrics, scale-to-zero
//	keys_tenant.go        tenants, quotas, usage, export/import
//	keys_volume.go        volumes

// Root is the top-level prefix for all CCattler keys in the fact store.
// Every key in the store begins with this prefix.
const Root = ""

// Top-level key prefixes. Each prefix partitions the fact store into a
// distinct concern: desired state, effective (derived) state, observed
// (actual) state, scheduler placements, network endpoints, intent layers,
// node leases, and event history. Keys are relative paths without a leading
// slash; the store prefix handles namespacing (e.g. "/ccattler/" + "desired/...").
const (
	// PrefixDesired holds user-declared desired state for services.
	PrefixDesired = "desired"

	// PrefixEffective holds the derived effective state after resolving
	// all intent layers (user, autoscaler, policy).
	PrefixEffective = "effective"

	// PrefixObserved holds the actual observed state reported by node agents,
	// including node capacity, instance status, and metrics.
	PrefixObserved = "observed"

	// PrefixPlacement holds scheduler placement decisions mapping instances
	// to the nodes they should run on.
	PrefixPlacement = "placement"

	// PrefixEndpoint holds derived network endpoint facts for running
	// service instances, used for traffic routing and service discovery.
	PrefixEndpoint = "endpoint"

	// PrefixIntent holds per-layer intent facts (user, autoscaler, policy)
	// that are merged to produce effective state.
	PrefixIntent = "intent"

	// PrefixLease holds node heartbeat leases. A lease expiring signals
	// that the node is unreachable.
	PrefixLease = "lease"

	// PrefixEvent holds append-only event history for audit and debugging.
	PrefixEvent = "event"

	// PrefixNetwork holds networking facts: per-node subnets, per-instance
	// IP allocations, service VIPs, and DNS name mappings.
	PrefixNetwork = "network"

	// PrefixDerived holds controller-derived state that is computed from
	// observed and desired facts but not directly reported by node agents.
	// Examples: rollout tracking, credential lifecycle, init phase derivation,
	// drain coordination.
	PrefixDerived = "derived"

	// PrefixAuth holds authorization declarations compiled from the DSL:
	// roles, grants, and groups.
	PrefixAuth = "auth"

	// PrefixAudit holds persisted authorization audit log entries.
	PrefixAudit = "audit"

	// PrefixObservedTelemetry holds high-frequency resource telemetry reported
	// by node agents. Separated from PrefixObserved so that controllers watching
	// observed/instance/ are not triggered by per-instance CPU/memory updates.
	PrefixObservedTelemetry = PrefixObserved + "/telemetry"
)

// Scan prefixes for controllers. Each prefix is used with store.Scan() to
// enumerate all facts of a particular kind.
const (
	// ScanDesiredServices scans all desired service definitions.
	ScanDesiredServices = PrefixDesired + "/service/"

	// ScanEffectiveServices scans all effective (resolved) service states.
	ScanEffectiveServices = PrefixEffective + "/service/"

	// ScanObservedNodes scans all observed node facts.
	ScanObservedNodes = PrefixObserved + "/node/"

	// ScanObservedInstances scans all observed instance facts.
	ScanObservedInstances = PrefixObserved + "/instance/"

	// ScanObservedServices scans all observed service-level facts (rollout state, etc).
	ScanObservedServices = PrefixObserved + "/service/"

	// ScanPlacements scans all scheduler placement decisions.
	ScanPlacements = PrefixPlacement + "/instance/"

	// ScanEndpoints scans all derived network endpoints.
	ScanEndpoints = PrefixEndpoint + "/service/"

	// ScanLeaseNodes scans all node heartbeat lease timestamps.
	ScanLeaseNodes = PrefixLease + "/node/"

	// ScanNetworkNodeSubnets scans all per-node subnet assignments.
	ScanNetworkNodeSubnets = PrefixNetwork + "/node/"

	// ScanNetworkAllocations scans all per-instance IP allocations.
	ScanNetworkAllocations = PrefixNetwork + "/allocation/"

	// ScanNetworkVIPs scans all service virtual IP assignments.
	ScanNetworkVIPs = PrefixNetwork + "/vip/service/"

	// ScanNetworkDNS scans all service-name-to-VIP DNS mappings.
	ScanNetworkDNS = PrefixNetwork + "/dns/"

	// ScanDesiredVolumes scans all desired volume declarations.
	ScanDesiredVolumes = PrefixDesired + "/volume/"

	// ScanObservedVolumes scans all observed volume state facts.
	ScanObservedVolumes = PrefixObserved + "/volume/"

	// ScanObservedMetrics scans all observed metric values for autoscaling.
	ScanObservedMetrics = PrefixObserved + "/metric/"

	// ScanIntentUserServices scans all user intent layer facts for services.
	ScanIntentUserServices = PrefixIntent + "/user/service/"

	// ScanIntentAutoscalerServices scans all autoscaler intent layer facts.
	ScanIntentAutoscalerServices = PrefixIntent + "/autoscaler/service/"

	// ScanDerivedServices scans all controller-derived service-level facts
	// (rollout state, rollout previous image).
	ScanDerivedServices = PrefixDerived + "/service/"

	// ScanDerivedInstances scans all controller-derived instance-level facts
	// (init phase, drain coordination).
	ScanDerivedInstances = PrefixDerived + "/instance/"

	// ScanObservedTelemetryInstances scans all per-instance telemetry (CPU,
	// memory, OOM kills) reported by node agents.
	ScanObservedTelemetryInstances = PrefixObservedTelemetry + "/instance/"

	// ScanDerivedCredentials scans all controller-derived credential state
	// (broker lifecycle, expiry, errors).
	ScanDerivedCredentials = PrefixDerived + "/credential/"

	// ScanDerivedNodes scans all controller-derived node-level facts
	// (drain coordination: started timestamp, initiator, complete marker).
	ScanDerivedNodes = PrefixDerived + "/node/"

	// ScanDerivedVolumes scans all controller-derived volume-level facts
	// (pending operations, last completed operation).
	ScanDerivedVolumes = PrefixDerived + "/volume/"

	// ScanDerivedCloudLoadBalancers scans all controller-derived cloud
	// load balancer facts (pending ensure/delete operations).
	ScanDerivedCloudLoadBalancers = PrefixDerived + "/cloud/loadbalancer/"

	// ScanDerivedCloudRoutes scans all controller-derived cloud route
	// facts (pending ensure/delete operations).
	ScanDerivedCloudRoutes = PrefixDerived + "/cloud/route/"

	// ScanDerivedCapacityRequests scans all durable capacity request facts
	// written by the ClusterAutoscaleController.
	ScanDerivedCapacityRequests = PrefixDerived + "/capacity/request/"

	// ScanDerivedSchedulerUnplaced scans all scheduler unplaced-demand facts
	// explaining why instances could not be placed.
	ScanDerivedSchedulerUnplaced = PrefixDerived + "/scheduler/unplaced/"

	// ScanDerivedSchedulerInvalidResourceRequests scans the services whose
	// stored resource request the scheduler cannot parse (M243 D5).
	ScanDerivedSchedulerInvalidResourceRequests = PrefixDerived + "/scheduler/invalid_resource_request/"

	// ScanDerivedSchedulerRebalanced scans the scheduler's last rebalance move
	// time per instance, the anti-flap cooldown (M267 D4).
	ScanDerivedSchedulerRebalanced = PrefixDerived + "/scheduler/rebalanced/"
)
