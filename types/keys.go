package types

import "fmt"

// Root is the top-level prefix for all CCattler keys in the fact store.
// Every key in the store begins with this prefix.
const Root = "/ccattler"

// Top-level key prefixes. Each prefix partitions the fact store into a
// distinct concern: desired state, effective (derived) state, observed
// (actual) state, scheduler placements, network endpoints, intent layers,
// node leases, and event history.
const (
	// PrefixDesired holds user-declared desired state for services.
	PrefixDesired = Root + "/desired"

	// PrefixEffective holds the derived effective state after resolving
	// all intent layers (user, autoscaler, policy).
	PrefixEffective = Root + "/effective"

	// PrefixObserved holds the actual observed state reported by node agents,
	// including node capacity, instance status, and metrics.
	PrefixObserved = Root + "/observed"

	// PrefixPlacement holds scheduler placement decisions mapping instances
	// to the nodes they should run on.
	PrefixPlacement = Root + "/placement"

	// PrefixEndpoint holds derived network endpoint facts for running
	// service instances, used for traffic routing and service discovery.
	PrefixEndpoint = Root + "/endpoint"

	// PrefixIntent holds per-layer intent facts (user, autoscaler, policy)
	// that are merged to produce effective state.
	PrefixIntent = Root + "/intent"

	// PrefixLease holds node heartbeat leases. A lease expiring signals
	// that the node is unreachable.
	PrefixLease = Root + "/lease"

	// PrefixEvent holds append-only event history for audit and debugging.
	PrefixEvent = Root + "/event"

	// PrefixNetwork holds networking facts: per-node subnets, per-instance
	// IP allocations, service VIPs, and DNS name mappings.
	PrefixNetwork = Root + "/network"
)

// KeyDesiredService returns the store path for a service's root marker key.
// Path: /ccattler/desired/service/{name}
func KeyDesiredService(name string) string {
	return fmt.Sprintf("%s/service/%s", PrefixDesired, name)
}

// KeyDesiredServiceImage returns the store path for a service's container image.
// Path: /ccattler/desired/service/{name}/image
func KeyDesiredServiceImage(name string) string {
	return fmt.Sprintf("%s/service/%s/image", PrefixDesired, name)
}

// KeyDesiredServiceInstances returns the store path for a service's desired instance count.
// Path: /ccattler/desired/service/{name}/instances
func KeyDesiredServiceInstances(name string) string {
	return fmt.Sprintf("%s/service/%s/instances", PrefixDesired, name)
}

// KeyDesiredServiceExpose returns the store path for a service's exposed port entry.
// Path: /ccattler/desired/service/{name}/expose/{port}
func KeyDesiredServiceExpose(name string, port int) string {
	return fmt.Sprintf("%s/service/%s/expose/%d", PrefixDesired, name, port)
}

// KeyDesiredServiceResourcesCPU returns the store path for a service's CPU resource requirement.
// Path: /ccattler/desired/service/{name}/resources/cpu
func KeyDesiredServiceResourcesCPU(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/cpu", PrefixDesired, name)
}

// KeyDesiredServiceResourcesMemory returns the store path for a service's memory resource requirement.
// Path: /ccattler/desired/service/{name}/resources/memory
func KeyDesiredServiceResourcesMemory(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/memory", PrefixDesired, name)
}

// KeyDesiredServiceHealthMethod returns the store path for a service's health check method (http, tcp, exec).
// Path: /ccattler/desired/service/{name}/health/method
func KeyDesiredServiceHealthMethod(name string) string {
	return fmt.Sprintf("%s/service/%s/health/method", PrefixDesired, name)
}

// KeyDesiredServiceHealthPath returns the store path for a service's health check HTTP path.
// Path: /ccattler/desired/service/{name}/health/path
func KeyDesiredServiceHealthPath(name string) string {
	return fmt.Sprintf("%s/service/%s/health/path", PrefixDesired, name)
}

// KeyDesiredServiceHealthInterval returns the store path for a service's health check interval.
// Path: /ccattler/desired/service/{name}/health/interval
func KeyDesiredServiceHealthInterval(name string) string {
	return fmt.Sprintf("%s/service/%s/health/interval", PrefixDesired, name)
}

// KeyEffectiveServiceInstances returns the store path for a service's effective (resolved)
// instance count, derived from all intent layers.
// Path: /ccattler/effective/service/{name}/instances
func KeyEffectiveServiceInstances(name string) string {
	return fmt.Sprintf("%s/service/%s/instances", PrefixEffective, name)
}

// KeyObservedNode returns the store path for a node's root marker key.
// Path: /ccattler/observed/node/{nodeID}
func KeyObservedNode(nodeID string) string {
	return fmt.Sprintf("%s/node/%s", PrefixObserved, nodeID)
}

// KeyObservedNodeState returns the store path for a node's observed lifecycle state.
// Path: /ccattler/observed/node/{nodeID}/state
func KeyObservedNodeState(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/state", PrefixObserved, nodeID)
}

// KeyObservedNodeCapacityCPU returns the store path for a node's total CPU capacity.
// Path: /ccattler/observed/node/{nodeID}/capacity/cpu
func KeyObservedNodeCapacityCPU(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/capacity/cpu", PrefixObserved, nodeID)
}

// KeyObservedNodeCapacityMemory returns the store path for a node's total memory capacity.
// Path: /ccattler/observed/node/{nodeID}/capacity/memory
func KeyObservedNodeCapacityMemory(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/capacity/memory", PrefixObserved, nodeID)
}

// KeyObservedNodeAvailableCPU returns the store path for a node's currently available CPU.
// Path: /ccattler/observed/node/{nodeID}/available/cpu
func KeyObservedNodeAvailableCPU(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/available/cpu", PrefixObserved, nodeID)
}

// KeyObservedNodeAvailableMemory returns the store path for a node's currently available memory.
// Path: /ccattler/observed/node/{nodeID}/available/memory
func KeyObservedNodeAvailableMemory(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/available/memory", PrefixObserved, nodeID)
}

// KeyObservedNodeArchitecture returns the store path for a node's CPU architecture.
// Path: /ccattler/observed/node/{nodeID}/architecture
func KeyObservedNodeArchitecture(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/architecture", PrefixObserved, nodeID)
}

// KeyObservedNodeZone returns the store path for a node's availability zone.
// Path: /ccattler/observed/node/{nodeID}/zone
func KeyObservedNodeZone(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/zone", PrefixObserved, nodeID)
}

// KeyObservedInstance returns the store path for an instance's root marker key.
// Path: /ccattler/observed/instance/{instanceID}
func KeyObservedInstance(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s", PrefixObserved, instanceID)
}

// KeyObservedInstanceService returns the store path for an instance's parent service name.
// Path: /ccattler/observed/instance/{instanceID}/service
func KeyObservedInstanceService(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/service", PrefixObserved, instanceID)
}

// KeyObservedInstanceNode returns the store path for the node an instance is running on.
// Path: /ccattler/observed/instance/{instanceID}/node
func KeyObservedInstanceNode(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/node", PrefixObserved, instanceID)
}

// KeyObservedInstanceState returns the store path for an instance's lifecycle state.
// Path: /ccattler/observed/instance/{instanceID}/state
func KeyObservedInstanceState(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/state", PrefixObserved, instanceID)
}

// KeyObservedInstanceImage returns the store path for the container image an instance is running.
// Path: /ccattler/observed/instance/{instanceID}/image
func KeyObservedInstanceImage(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/image", PrefixObserved, instanceID)
}

// KeyObservedInstanceIP returns the store path for an instance's assigned IP address.
// Path: /ccattler/observed/instance/{instanceID}/ip
func KeyObservedInstanceIP(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/ip", PrefixObserved, instanceID)
}

// KeyObservedInstanceHealth returns the store path for an instance's latest health check result.
// Path: /ccattler/observed/instance/{instanceID}/health
func KeyObservedInstanceHealth(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/health", PrefixObserved, instanceID)
}

// KeyObservedMetric returns the store path for a simulated metric value used by
// the autoscaler. Metrics are keyed by service name and metric name.
// Path: /ccattler/observed/metric/service/{service}/{metric}
func KeyObservedMetric(service, metric string) string {
	return fmt.Sprintf("%s/metric/service/%s/%s", PrefixObserved, service, metric)
}

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
)

// KeyPlacementInstance returns the store path for the scheduler's placement decision
// for a given instance. The value stored at this key is the target node ID.
// Path: /ccattler/placement/instance/{instanceID}
func KeyPlacementInstance(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s", PrefixPlacement, instanceID)
}

// KeyEndpoint returns the store path for a service instance's network endpoint.
// The value stored at this key is the IP:port pair.
// Path: /ccattler/endpoint/service/{serviceName}/{instanceID}
func KeyEndpoint(serviceName, instanceID string) string {
	return fmt.Sprintf("%s/service/%s/%s", PrefixEndpoint, serviceName, instanceID)
}

// KeyIntentUserServiceInstances returns the store path for the user intent layer's
// desired instance count for a service.
// Path: /ccattler/intent/user/service/{name}/instances
func KeyIntentUserServiceInstances(name string) string {
	return fmt.Sprintf("%s/user/service/%s/instances", PrefixIntent, name)
}

// KeyIntentAutoscalerServiceInstances returns the store path for the autoscaler
// intent layer's recommended instance count for a service.
// Path: /ccattler/intent/autoscaler/service/{name}/instances
func KeyIntentAutoscalerServiceInstances(name string) string {
	return fmt.Sprintf("%s/autoscaler/service/%s/instances", PrefixIntent, name)
}

// KeyLeaseNode returns the store path for a node's heartbeat lease key.
// The node agent periodically refreshes this lease; expiry signals the node
// is unreachable.
// Path: /ccattler/lease/node/{nodeID}
func KeyLeaseNode(nodeID string) string {
	return fmt.Sprintf("%s/node/%s", PrefixLease, nodeID)
}

// KeyNetworkNodeSubnet returns the store path for a node's assigned subnet CIDR.
// Path: /ccattler/network/node/{nodeID}/subnet
func KeyNetworkNodeSubnet(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/subnet", PrefixNetwork, nodeID)
}

// KeyNetworkAllocation returns the store path for an instance's allocated IP address.
// Path: /ccattler/network/allocation/{instanceID}
func KeyNetworkAllocation(instanceID string) string {
	return fmt.Sprintf("%s/allocation/%s", PrefixNetwork, instanceID)
}

// KeyNetworkVIPService returns the store path for a service's virtual IP address.
// Path: /ccattler/network/vip/service/{serviceName}
func KeyNetworkVIPService(serviceName string) string {
	return fmt.Sprintf("%s/vip/service/%s", PrefixNetwork, serviceName)
}

// KeyNetworkVIPServicePort returns the store path for a service VIP's port number.
// Path: /ccattler/network/vip/service/{serviceName}/port
func KeyNetworkVIPServicePort(serviceName string) string {
	return fmt.Sprintf("%s/vip/service/%s/port", PrefixNetwork, serviceName)
}

// KeyNetworkDNS returns the store path for a service's DNS name-to-VIP mapping.
// Path: /ccattler/network/dns/{serviceName}
func KeyNetworkDNS(serviceName string) string {
	return fmt.Sprintf("%s/dns/%s", PrefixNetwork, serviceName)
}

// KeyDesiredServiceScaleHorizontalMin returns the store path for a service's
// horizontal autoscaling minimum instance count.
// Path: /ccattler/desired/service/{name}/scale/horizontal/min
func KeyDesiredServiceScaleHorizontalMin(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/min", PrefixDesired, name)
}

// KeyDesiredServiceScaleHorizontalMax returns the store path for a service's
// horizontal autoscaling maximum instance count.
// Path: /ccattler/desired/service/{name}/scale/horizontal/max
func KeyDesiredServiceScaleHorizontalMax(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/max", PrefixDesired, name)
}

// KeyDesiredServiceScaleHorizontalTarget returns the store path for a single
// autoscaling target metric and its threshold value.
// Path: /ccattler/desired/service/{name}/scale/horizontal/target/{metric}
func KeyDesiredServiceScaleHorizontalTarget(name string, metric string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/target/%s", PrefixDesired, name, metric)
}

// ScanDesiredServiceScaleTargets returns the scan prefix for all horizontal
// autoscaling target metrics of a specific service.
func ScanDesiredServiceScaleTargets(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/target/", PrefixDesired, name)
}

// KeyDesiredServiceScaleStabilizationUp returns the store path for the scale-up
// stabilization window duration in seconds.
func KeyDesiredServiceScaleStabilizationUp(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/stabilization/up", PrefixDesired, name)
}

// KeyDesiredServiceScaleStabilizationDown returns the store path for the scale-down
// stabilization window duration in seconds.
func KeyDesiredServiceScaleStabilizationDown(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/stabilization/down", PrefixDesired, name)
}

// KeyDesiredServiceScaleVerticalCPUMin returns the store path for the minimum CPU
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalCPUMin(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/cpu/min", PrefixDesired, name)
}

// KeyDesiredServiceScaleVerticalCPUMax returns the store path for the maximum CPU
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalCPUMax(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/cpu/max", PrefixDesired, name)
}

// KeyDesiredServiceScaleVerticalMemoryMin returns the store path for the minimum memory
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalMemoryMin(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/memory/min", PrefixDesired, name)
}

// KeyDesiredServiceScaleVerticalMemoryMax returns the store path for the maximum memory
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalMemoryMax(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/memory/max", PrefixDesired, name)
}

// KeyIntentAutoscalerServiceResourcesCPU returns the store path for the autoscaler's
// recommended CPU resource for a service.
func KeyIntentAutoscalerServiceResourcesCPU(name string) string {
	return fmt.Sprintf("%s/autoscaler/service/%s/resources/cpu", PrefixIntent, name)
}

// KeyIntentAutoscalerServiceResourcesMemory returns the store path for the autoscaler's
// recommended memory resource for a service.
func KeyIntentAutoscalerServiceResourcesMemory(name string) string {
	return fmt.Sprintf("%s/autoscaler/service/%s/resources/memory", PrefixIntent, name)
}

// KeyEffectiveServiceResourcesCPU returns the store path for a service's effective CPU.
func KeyEffectiveServiceResourcesCPU(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/cpu", PrefixEffective, name)
}

// KeyEffectiveServiceResourcesMemory returns the store path for a service's effective memory.
func KeyEffectiveServiceResourcesMemory(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/memory", PrefixEffective, name)
}

// KeyDesiredServiceScaleHorizontalEvent returns the store path for an event-driven
// scaling source and its target messages-per-instance value.
func KeyDesiredServiceScaleHorizontalEvent(name, source string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/event/%s", PrefixDesired, name, source)
}

// KeyDesiredServiceScaleScheduleDays returns the store path for schedule scaling days.
func KeyDesiredServiceScaleScheduleDays(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/days", PrefixDesired, name)
}

// KeyDesiredServiceScaleScheduleStart returns the store path for schedule scaling start time.
func KeyDesiredServiceScaleScheduleStart(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/start", PrefixDesired, name)
}

// KeyDesiredServiceScaleScheduleEnd returns the store path for schedule scaling end time.
func KeyDesiredServiceScaleScheduleEnd(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/end", PrefixDesired, name)
}

// KeyDesiredServiceScaleScheduleMinimum returns the store path for schedule scaling minimum instances.
func KeyDesiredServiceScaleScheduleMinimum(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/minimum", PrefixDesired, name)
}

// KeyDesiredServiceQuotaInstances returns the store path for a service's instance quota ceiling.
func KeyDesiredServiceQuotaInstances(name string) string {
	return fmt.Sprintf("%s/service/%s/quota/instances", PrefixDesired, name)
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

// KeyDesiredServiceUpdateMaxUnavailable returns the store path for the maximum number
// of instances that can be unavailable during a rolling update.
func KeyDesiredServiceUpdateMaxUnavailable(name string) string {
	return fmt.Sprintf("%s/service/%s/update/max_unavailable", PrefixDesired, name)
}

// KeyDesiredServiceUpdateMaxExtra returns the store path for the maximum number
// of extra instances allowed during a rolling update surge.
func KeyDesiredServiceUpdateMaxExtra(name string) string {
	return fmt.Sprintf("%s/service/%s/update/max_extra", PrefixDesired, name)
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

// KeyObservedServiceRolloutImage returns the store path tracking the previous image
// during a rolling update for rollback purposes.
func KeyObservedServiceRolloutImage(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/previous_image", PrefixObserved, name)
}

// KeyObservedServiceRolloutState returns the store path for a service's rollout state.
func KeyObservedServiceRolloutState(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/state", PrefixObserved, name)
}

// KeyObservedServiceRolloutFailures returns the count of failed new-image instances
// during a rollout, used for rollback decisions.
func KeyObservedServiceRolloutFailures(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/failures", PrefixObserved, name)
}

// KeyDesiredServiceConfigEnv returns the store path for a service's environment variable.
// Path: /ccattler/desired/service/{name}/config/env/{varName}
func KeyDesiredServiceConfigEnv(serviceName, varName string) string {
	return fmt.Sprintf("%s/service/%s/config/env/%s", PrefixDesired, serviceName, varName)
}

// KeyDesiredServiceConfigFile returns the store path for a service's config file.
// Path: /ccattler/desired/service/{name}/config/file/{path}
func KeyDesiredServiceConfigFile(serviceName, filePath string) string {
	return fmt.Sprintf("%s/service/%s/config/file/%s", PrefixDesired, serviceName, filePath)
}

// KeyDesiredServiceSecret returns the store path for a service's secret grant.
// The value is the mount path.
// Path: /ccattler/desired/service/{name}/secret/{secretName}
func KeyDesiredServiceSecret(serviceName, secretName string) string {
	return fmt.Sprintf("%s/service/%s/secret/%s", PrefixDesired, serviceName, secretName)
}

// ScanDesiredServiceConfig returns the scan prefix for all config facts of a service.
func ScanDesiredServiceConfig(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/config/", PrefixDesired, serviceName)
}

// ScanDesiredServiceSecrets returns the scan prefix for all secret grants of a service.
func ScanDesiredServiceSecrets(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/secret/", PrefixDesired, serviceName)
}

// Node utilization keys. The agent reports current resource utilization as
// observed facts, separate from capacity/allocated facts used by the scheduler.

// KeyObservedNodeUtilizationCPU returns the store path for a node's current
// CPU utilization as a percentage (0-100).
// Path: /ccattler/observed/node/{nodeID}/utilization/cpu
func KeyObservedNodeUtilizationCPU(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/utilization/cpu", PrefixObserved, nodeID)
}

// KeyObservedNodeUtilizationMemory returns the store path for a node's current
// memory utilization as a percentage (0-100).
// Path: /ccattler/observed/node/{nodeID}/utilization/memory
func KeyObservedNodeUtilizationMemory(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/utilization/memory", PrefixObserved, nodeID)
}

// KeyObservedNodeDiskUsed returns the store path for a node's disk usage as bytes.
// Path: /ccattler/observed/node/{nodeID}/disk/used
func KeyObservedNodeDiskUsed(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/disk/used", PrefixObserved, nodeID)
}

// KeyObservedNodeDiskCapacity returns the store path for a node's total disk capacity.
// Path: /ccattler/observed/node/{nodeID}/disk/capacity
func KeyObservedNodeDiskCapacity(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/disk/capacity", PrefixObserved, nodeID)
}

// KeyObservedNodeWorkloadCount returns the store path for the number of workloads
// running on a node.
// Path: /ccattler/observed/node/{nodeID}/workloads
func KeyObservedNodeWorkloadCount(nodeID string) string {
	return fmt.Sprintf("%s/node/%s/workloads", PrefixObserved, nodeID)
}

// Workload utilization keys. The agent reports per-instance resource usage.

// KeyObservedInstanceCPU returns the store path for an instance's current CPU usage
// in millicores.
// Path: /ccattler/observed/instance/{instanceID}/cpu
func KeyObservedInstanceCPU(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/cpu", PrefixObserved, instanceID)
}

// KeyObservedInstanceMemory returns the store path for an instance's current memory
// usage in bytes.
// Path: /ccattler/observed/instance/{instanceID}/memory
func KeyObservedInstanceMemory(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/memory", PrefixObserved, instanceID)
}

// KeyObservedInstanceRestarts returns the store path for an instance's restart count.
// Path: /ccattler/observed/instance/{instanceID}/restarts
func KeyObservedInstanceRestarts(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/restarts", PrefixObserved, instanceID)
}

// Init step key functions. Init steps are ordered by index (0, 1, 2, ...) and
// stored as desired facts per service. Observed init results are per-instance.

// KeyDesiredServiceInitStep returns the store path for an init step's root marker.
// Path: /ccattler/desired/service/{name}/init/{index}
func KeyDesiredServiceInitStep(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d", PrefixDesired, serviceName, stepIndex)
}

// KeyDesiredServiceInitStepExec returns the store path for an init step's exec command.
// Path: /ccattler/desired/service/{name}/init/{index}/exec
func KeyDesiredServiceInitStepExec(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d/exec", PrefixDesired, serviceName, stepIndex)
}

// KeyDesiredServiceInitStepTimeout returns the store path for an init step's timeout.
// Path: /ccattler/desired/service/{name}/init/{index}/timeout
func KeyDesiredServiceInitStepTimeout(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d/timeout", PrefixDesired, serviceName, stepIndex)
}

// KeyDesiredServiceInitStepRetry returns the store path for an init step's retry count.
// Path: /ccattler/desired/service/{name}/init/{index}/retry
func KeyDesiredServiceInitStepRetry(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d/retry", PrefixDesired, serviceName, stepIndex)
}

// ScanDesiredServiceInitSteps returns the scan prefix for all init steps of a service.
func ScanDesiredServiceInitSteps(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/init/", PrefixDesired, serviceName)
}

// KeyObservedInstanceInitPhase returns the store path for an instance's overall init phase.
// Path: /ccattler/observed/instance/{instanceID}/init/phase
func KeyObservedInstanceInitPhase(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/init/phase", PrefixObserved, instanceID)
}

// KeyObservedInstanceInitStepState returns the store path for an instance's init step result.
// Path: /ccattler/observed/instance/{instanceID}/init/step/{index}/state
func KeyObservedInstanceInitStepState(instanceID string, stepIndex int) string {
	return fmt.Sprintf("%s/instance/%s/init/step/%d/state", PrefixObserved, instanceID, stepIndex)
}

// KeyObservedInstanceInitStepReason returns the store path for an init step's failure reason.
// Path: /ccattler/observed/instance/{instanceID}/init/step/{index}/reason
func KeyObservedInstanceInitStepReason(instanceID string, stepIndex int) string {
	return fmt.Sprintf("%s/instance/%s/init/step/%d/reason", PrefixObserved, instanceID, stepIndex)
}

// ScanObservedInstanceInitSteps returns the scan prefix for all init step observations of an instance.
func ScanObservedInstanceInitSteps(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/init/step/", PrefixObserved, instanceID)
}

// Tenant key prefixes.
const (
	// PrefixDesiredTenant holds desired tenant definitions and quotas.
	PrefixDesiredTenant = PrefixDesired + "/tenant/"

	// PrefixObservedTenant holds observed tenant usage metrics.
	PrefixObservedTenant = PrefixObserved + "/tenant/"

	// ScanDesiredTenants scans all desired tenant definitions.
	ScanDesiredTenants = PrefixDesiredTenant
)

// KeyDesiredTenant returns the store path for a tenant's root marker key.
// Path: /ccattler/desired/tenant/{name}
func KeyDesiredTenant(tenantName string) string {
	return fmt.Sprintf("%s%s", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaCPU returns the store path for a tenant's CPU quota in millicores.
// Path: /ccattler/desired/tenant/{name}/quota/cpu
func KeyDesiredTenantQuotaCPU(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/cpu", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaMemory returns the store path for a tenant's memory quota in bytes.
// Path: /ccattler/desired/tenant/{name}/quota/memory
func KeyDesiredTenantQuotaMemory(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/memory", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaInstances returns the store path for a tenant's maximum instance count.
// Path: /ccattler/desired/tenant/{name}/quota/instances
func KeyDesiredTenantQuotaInstances(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/instances", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaVolumes returns the store path for a tenant's maximum volume count.
// Path: /ccattler/desired/tenant/{name}/quota/volumes
func KeyDesiredTenantQuotaVolumes(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/volumes", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantQuotaStorage returns the store path for a tenant's total storage quota.
// Path: /ccattler/desired/tenant/{name}/quota/storage
func KeyDesiredTenantQuotaStorage(tenantName string) string {
	return fmt.Sprintf("%s%s/quota/storage", PrefixDesiredTenant, tenantName)
}

// KeyDesiredTenantWeight returns the store path for a tenant's scheduling weight.
// Path: /ccattler/desired/tenant/{name}/weight
func KeyDesiredTenantWeight(tenantName string) string {
	return fmt.Sprintf("%s%s/weight", PrefixDesiredTenant, tenantName)
}

// KeyObservedTenantUsageCPU returns the store path for a tenant's current CPU usage.
// Path: /ccattler/observed/tenant/{name}/usage/cpu
func KeyObservedTenantUsageCPU(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/cpu", PrefixObservedTenant, tenantName)
}

// KeyObservedTenantUsageMemory returns the store path for a tenant's current memory usage.
// Path: /ccattler/observed/tenant/{name}/usage/memory
func KeyObservedTenantUsageMemory(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/memory", PrefixObservedTenant, tenantName)
}

// KeyObservedTenantUsageInstances returns the store path for a tenant's current instance count.
// Path: /ccattler/observed/tenant/{name}/usage/instances
func KeyObservedTenantUsageInstances(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/instances", PrefixObservedTenant, tenantName)
}

// KeyObservedTenantUsageVolumes returns the store path for a tenant's current volume count.
// Path: /ccattler/observed/tenant/{name}/usage/volumes
func KeyObservedTenantUsageVolumes(tenantName string) string {
	return fmt.Sprintf("%s%s/usage/volumes", PrefixObservedTenant, tenantName)
}

// KeyDesiredServiceOwner returns the store path for a service's owning tenant.
// Path: /ccattler/desired/service/{name}/owner
func KeyDesiredServiceOwner(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/owner", PrefixDesired, serviceName)
}

// Shared service export/import keys.
const (
	// PrefixExport holds shared service export declarations.
	PrefixExport = Root + "/export/"

	// PrefixImport holds service import ("uses") declarations.
	PrefixImport = Root + "/import/"
)

// KeyExportService returns the store path for a shared service export marker.
// Path: /ccattler/export/{serviceName}
func KeyExportService(serviceName string) string {
	return fmt.Sprintf("%s%s", PrefixExport, serviceName)
}

// KeyExportServiceAllowTenant returns the store path for a tenant allowed to
// consume an exported service.
// Path: /ccattler/export/{serviceName}/allow/{tenantName}
func KeyExportServiceAllowTenant(serviceName, tenantName string) string {
	return fmt.Sprintf("%s%s/allow/%s", PrefixExport, serviceName, tenantName)
}

// ScanExportServiceAllowTenants returns the scan prefix for all tenants
// allowed to consume an exported service.
func ScanExportServiceAllowTenants(serviceName string) string {
	return fmt.Sprintf("%s%s/allow/", PrefixExport, serviceName)
}

// KeyImportService returns the store path for a service's import declaration.
// Path: /ccattler/import/{consumerService}/uses/{exportedService}
func KeyImportService(consumerService, exportedService string) string {
	return fmt.Sprintf("%s%s/uses/%s", PrefixImport, consumerService, exportedService)
}

// ScanImportsForService returns the scan prefix for all imports declared by a service.
func ScanImportsForService(consumerService string) string {
	return fmt.Sprintf("%s%s/uses/", PrefixImport, consumerService)
}

// Tenant lifecycle keys.

// KeyDesiredTenantState returns the store path for a tenant's lifecycle state
// (active, deleting).
// Path: /ccattler/desired/tenant/{name}/state
func KeyDesiredTenantState(tenantName string) string {
	return fmt.Sprintf("%s%s/state", PrefixDesiredTenant, tenantName)
}

// KeyDesiredVolume returns the store path for a volume's root marker key.
// Path: /ccattler/desired/volume/{name}
func KeyDesiredVolume(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s", PrefixDesired, volumeName)
}

// KeyDesiredVolumeSize returns the store path for a volume's declared size.
// Path: /ccattler/desired/volume/{name}/size
func KeyDesiredVolumeSize(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/size", PrefixDesired, volumeName)
}

// KeyDesiredVolumePersistent returns the store path for a volume's persistence flag.
// Path: /ccattler/desired/volume/{name}/persistent
func KeyDesiredVolumePersistent(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/persistent", PrefixDesired, volumeName)
}

// KeyDesiredServiceVolume returns the store path binding a service to a named
// volume. The value stored at this key is the mount path inside the instance.
// Path: /ccattler/desired/service/{serviceName}/volume/{volumeName}
func KeyDesiredServiceVolume(serviceName string, volumeName string) string {
	return fmt.Sprintf("%s/service/%s/volume/%s", PrefixDesired, serviceName, volumeName)
}

// KeyObservedVolume returns the store path for an observed volume's root marker.
// Path: /ccattler/observed/volume/{name}
func KeyObservedVolume(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s", PrefixObserved, volumeName)
}

// KeyObservedVolumeState returns the store path for an observed volume's lifecycle state.
// Path: /ccattler/observed/volume/{name}/state
func KeyObservedVolumeState(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/state", PrefixObserved, volumeName)
}

// KeyObservedVolumeNode returns the store path for the node a volume is attached to.
// Path: /ccattler/observed/volume/{name}/node
func KeyObservedVolumeNode(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/node", PrefixObserved, volumeName)
}

// KeyObservedVolumeInstance returns the store path for the instance a volume is mounted into.
// Path: /ccattler/observed/volume/{name}/instance
func KeyObservedVolumeInstance(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/instance", PrefixObserved, volumeName)
}

// KeyObservedVolumeSize returns the store path for an observed volume's size.
// Path: /ccattler/observed/volume/{name}/size
func KeyObservedVolumeSize(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/size", PrefixObserved, volumeName)
}

// KeyObservedVolumeMountPath returns the store path for an observed volume's mount path.
// Path: /ccattler/observed/volume/{name}/mount_path
func KeyObservedVolumeMountPath(volumeName string) string {
	return fmt.Sprintf("%s/volume/%s/mount_path", PrefixObserved, volumeName)
}
