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

// ServiceVIP represents a stable virtual IP address assigned to a service
// for DNS resolution and load balancing. Traffic sent to this VIP is
// distributed across the service's healthy endpoints by the per-node proxy.
type ServiceVIP struct {
	Service string // Service is the name of the service this VIP belongs to.
	VIP     string // VIP is the virtual IP address (e.g. "10.200.0.1").
	Port    int    // Port is the port on which the VIP accepts traffic.
}

// ScalePolicy represents a horizontal autoscaling policy for a service.
// It defines the instance count bounds and the target metric thresholds
// that the autoscaler uses to compute scaling recommendations.
type ScalePolicy struct {
	Service string        // Service is the name of the service this policy applies to.
	Min     int           // Min is the minimum number of instances (floor for scale-down).
	Max     int           // Max is the maximum number of instances (ceiling for scale-up).
	Targets []ScaleTarget // Targets is the set of metric thresholds to evaluate.
}

// ScaleTarget represents a single metric threshold within a scaling policy.
// The autoscaler compares the observed metric value against the target to
// compute a per-metric instance recommendation.
type ScaleTarget struct {
	Metric string // Metric is the metric name (e.g. "cpu", "memory", "requests_per_second").
	Value  int    // Value is the target threshold (e.g. 60 for 60% CPU utilization).
}

// VerticalScalePolicy represents a vertical autoscaling policy for a service,
// defining the resource bounds within which the autoscaler can adjust CPU and memory.
type VerticalScalePolicy struct {
	Service   string // Service is the name of the service this policy applies to.
	CPUMin    string // CPUMin is the minimum CPU allocation (e.g. "250m").
	CPUMax    string // CPUMax is the maximum CPU allocation (e.g. "4000m").
	MemoryMin string // MemoryMin is the minimum memory allocation (e.g. "512Mi").
	MemoryMax string // MemoryMax is the maximum memory allocation (e.g. "8Gi").
}

// UpdatePolicy represents a rolling update strategy for a service.
type UpdatePolicy struct {
	Service        string // Service is the name of the service.
	MaxUnavailable int    // MaxUnavailable is the maximum number of instances that can be down during update.
	MaxExtra       int    // MaxExtra is the maximum number of extra instances allowed during surge.
}

// PlacementPolicy represents placement constraints for a service.
type PlacementPolicy struct {
	Service      string // Service is the name of the service.
	Architecture string // Architecture is the required CPU architecture (e.g. "amd64", "arm64").
	ZonePolicy   string // ZonePolicy is "spread" for zone-aware spreading, or a specific zone name.
}

// ScheduleRule represents a time-based scaling rule that sets a minimum
// instance count during specific time windows.
type ScheduleRule struct {
	Days    string // Days is when the rule applies (e.g. "weekdays", "everyday").
	Start   string // Start is the start time in HH:MM format (e.g. "08:00").
	End     string // End is the end time in HH:MM format (e.g. "18:00").
	Minimum int    // Minimum is the minimum instance count during the active window.
}

// Volume represents a persistent storage volume that can be attached to a
// node and mounted into a service instance. Volumes survive instance
// restarts and node moves — the data follows the workload.
type Volume struct {
	Name       string      // Name is the unique identifier for this volume (e.g. "pgdata").
	Size       string      // Size is the raw DSL size string (e.g. "100Gi"), not parsed to bytes.
	Persistent bool        // Persistent is true if the volume's data survives instance deletion.
	State      VolumeState // State is the current lifecycle state (available, attached).
	Node       string      // Node is the ID of the node this volume is attached to, empty if available.
	Instance   string      // Instance is the ID of the instance this volume is mounted into, empty if unmounted.
	MountPath  string      // MountPath is the filesystem path where the volume is mounted.
}
