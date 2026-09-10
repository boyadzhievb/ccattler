package types

// InstanceState represents the lifecycle state of a container instance.
// Instances transition through these states as they are created, scheduled,
// started, and eventually stopped or replaced on failure.
type InstanceState string

// Instance lifecycle states.
const (
	// InstancePending means the instance has been created but is not yet running.
	// It is waiting to be scheduled onto a node and started by the node agent.
	InstancePending InstanceState = "pending"

	// InstanceRunning means the instance is actively running on a node.
	// The node agent has confirmed the container or process is alive.
	InstanceRunning InstanceState = "running"

	// InstanceFailed means the instance encountered an error and is no longer
	// running. The failure controller will create a replacement instance.
	InstanceFailed InstanceState = "failed"

	// InstanceStopped means the instance was intentionally stopped, typically
	// during a scale-down or service removal. Stopped instances are not replaced.
	InstanceStopped InstanceState = "stopped"
)

// NodeState represents the availability state of a cluster node.
// The node agent maintains a lease; if the lease expires, the control plane
// marks the node as unreachable.
type NodeState string

// Node availability states.
const (
	// NodeAlive means the node is healthy, maintaining its lease, and ready
	// to accept new instance placements.
	NodeAlive NodeState = "alive"

	// NodeUnreachable means the node's lease has expired or communication
	// has been lost. Instances on unreachable nodes are candidates for
	// rescheduling to healthy nodes.
	NodeUnreachable NodeState = "unreachable"

	// NodeDraining means the node is being taken offline gracefully.
	// Existing instances are being migrated away and no new placements
	// will be scheduled here.
	NodeDraining NodeState = "draining"
)

// HealthStatus represents the result of a health check probe against
// an instance. Health checks can be HTTP, TCP, or exec-based.
type HealthStatus string

// VolumeState represents the lifecycle state of a persistent volume.
// Volumes transition between available (unattached) and attached (bound
// to a node and mounted into an instance).
type VolumeState string

// Volume lifecycle states.
const (
	// VolumeAvailable means the volume exists and is not attached to any node.
	// It is ready to be attached by an agent when an instance needs it.
	VolumeAvailable VolumeState = "available"

	// VolumeAttached means the volume is currently bound to a node and
	// mounted into an instance. Only one node can hold a ReadWriteOnce
	// volume at a time.
	VolumeAttached VolumeState = "attached"
)

// InitStepState represents the execution state of a single initialization step.
type InitStepState string

// Init step execution states.
const (
	// InitStepPending means the step has not started yet.
	InitStepPending InitStepState = "pending"

	// InitStepRunning means the step is currently executing.
	InitStepRunning InitStepState = "running"

	// InitStepSucceeded means the step completed successfully.
	InitStepSucceeded InitStepState = "succeeded"

	// InitStepFailed means the step failed to complete.
	InitStepFailed InitStepState = "failed"
)

// InitPhase represents the overall initialization phase of an instance.
type InitPhase string

// Instance initialization phases.
const (
	// InitPhasePending means initialization has not started yet.
	InitPhasePending InitPhase = "pending"

	// InitPhaseRunning means at least one init step is in progress.
	InitPhaseRunning InitPhase = "running"

	// InitPhaseComplete means all init steps completed successfully.
	InitPhaseComplete InitPhase = "complete"

	// InitPhaseFailed means an init step failed and initialization cannot proceed.
	InitPhaseFailed InitPhase = "failed"
)

// Health check result values.
const (
	// HealthHealthy means the instance passed its most recent health check.
	HealthHealthy HealthStatus = "healthy"

	// HealthUnhealthy means the instance failed its most recent health check.
	// Repeated failures may trigger replacement by the failure controller.
	HealthUnhealthy HealthStatus = "unhealthy"

	// HealthUnknown means no health check result is available yet, either
	// because the instance just started or no health probe is configured.
	HealthUnknown HealthStatus = "unknown"
)
