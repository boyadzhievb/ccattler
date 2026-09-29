package security

// Capability represents a named permission in the API-layer authorization
// model. Unlike the store-layer Permission type (read/write/delete/watch on
// key prefixes), capabilities describe domain-level operations like
// "workload.create" or "secret.use".
type Capability string

const (
	// CapabilityWorkloadRead allows reading workload state (services, instances, placements).
	CapabilityWorkloadRead Capability = "workload.read"

	// CapabilityWorkloadCreate allows creating new services via apply.
	CapabilityWorkloadCreate Capability = "workload.create"

	// CapabilityWorkloadUpdate allows modifying existing services (image, config).
	CapabilityWorkloadUpdate Capability = "workload.update"

	// CapabilityWorkloadDelete allows removing services.
	CapabilityWorkloadDelete Capability = "workload.delete"

	// CapabilitySecretMetadataRead allows listing secret names and grants without values.
	CapabilitySecretMetadataRead Capability = "secret.metadata.read"

	// CapabilitySecretUse allows mounting secrets into workloads.
	CapabilitySecretUse Capability = "secret.use"

	// CapabilityNodeRead allows reading node state (health, capacity, labels).
	CapabilityNodeRead Capability = "node.read"

	// CapabilityNodeManage allows draining, cordoning, and labelling nodes.
	CapabilityNodeManage Capability = "node.manage"

	// CapabilityPlacementRead allows reading placement decisions.
	CapabilityPlacementRead Capability = "placement.read"

	// CapabilityPlacementWrite allows overriding placement (manual scheduling).
	CapabilityPlacementWrite Capability = "placement.write"

	// CapabilityScalingRead allows reading autoscaler state and scaling history.
	CapabilityScalingRead Capability = "scaling.read"

	// CapabilityScalingWrite allows changing instance counts and scaling policies.
	CapabilityScalingWrite Capability = "scaling.write"

	// CapabilityClusterAdmin grants all capabilities at all scopes.
	CapabilityClusterAdmin Capability = "cluster.admin"
)

// AllCapabilities lists every defined capability for enumeration and validation.
var AllCapabilities = []Capability{
	CapabilityWorkloadRead,
	CapabilityWorkloadCreate,
	CapabilityWorkloadUpdate,
	CapabilityWorkloadDelete,
	CapabilitySecretMetadataRead,
	CapabilitySecretUse,
	CapabilityNodeRead,
	CapabilityNodeManage,
	CapabilityPlacementRead,
	CapabilityPlacementWrite,
	CapabilityScalingRead,
	CapabilityScalingWrite,
	CapabilityClusterAdmin,
}
