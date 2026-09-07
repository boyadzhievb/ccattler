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
