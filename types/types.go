package types

// Service represents a user's desired service definition. It captures the
// container image to run, the number of instances to maintain, which ports
// to expose, and the resource limits each instance should receive.
type Service struct {
	Name      string // Name is the unique identifier for the service (e.g. "web", "api").
	Image     string // Image is the container image reference (e.g. "nginx:1.28").
	Instances int    // Instances is the desired number of running replicas.
	Ports     []int  // Ports lists the network ports the service exposes to the outside.
	CPU       string // CPU is the CPU resource request/limit (e.g. "500m" for half a core).
	Memory    string // Memory is the memory resource request/limit (e.g. "512Mi").
}

// Instance represents a single running (or desired-to-run) replica of a service.
// Instances are identified by a random hex ID and tracked through their lifecycle
// from pending through running or failed to stopped.
type Instance struct {
	ID      string        // ID is the unique random hex identifier for this instance.
	Service string        // Service is the name of the service this instance belongs to.
	Node    string        // Node is the ID of the node this instance is placed on (empty if unplaced).
	State   InstanceState // State is the current lifecycle state (pending, running, failed, stopped).
	Image   string        // Image is the container image this instance is running.
	IP      string        // IP is the network address assigned to this instance.
	Health  HealthStatus  // Health is the last known health check result (healthy, unhealthy, unknown).
}

// Node represents a machine in the cluster that can run container instances.
// Nodes report their total capacity and currently available resources so the
// scheduler can make placement decisions.
type Node struct {
	ID              string    // ID is the unique identifier for this node (e.g. "node-1").
	State           NodeState // State is the current node lifecycle state (alive, unreachable, draining).
	CapacityCPU     int64     // CapacityCPU is the total CPU capacity in millicores.
	CapacityMemory  int64     // CapacityMemory is the total memory capacity in MiB.
	AvailableCPU    int64     // AvailableCPU is the currently unallocated CPU in millicores.
	AvailableMemory int64     // AvailableMemory is the currently unallocated memory in MiB.
	Architecture    string    // Architecture is the CPU architecture (e.g. "amd64", "arm64").
	Zone            string    // Zone is the availability zone or region the node resides in.
}

// Placement represents a scheduler decision binding an instance to a node.
// The scheduler writes placement facts; other controllers read them to know
// where instances should run.
type Placement struct {
	InstanceID string // InstanceID is the ID of the instance being placed.
	NodeID     string // NodeID is the ID of the node the instance is assigned to.
}

// Endpoint represents a network endpoint for a running service instance.
// The endpoint controller derives these from observed running instances
// so that traffic can be routed to them.
type Endpoint struct {
	Service    string // Service is the name of the service this endpoint belongs to.
	InstanceID string // InstanceID is the ID of the instance this endpoint points to.
	IP         string // IP is the network address of the instance.
	Port       int    // Port is the exposed port number for this endpoint.
}
