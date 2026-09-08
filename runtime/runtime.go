package runtime

import "context"

// Spec describes a workload to run. It carries all the information a runtime
// backend needs to start a process or container: the command/image, environment
// variables, and resource hints.
type Spec struct {
	ID          string            // ID is the unique identifier for this workload.
	ServiceName string            // ServiceName is the name of the service this workload belongs to. Used by ContainerRuntime as a Docker network alias for DNS-based service discovery.
	Image       string            // Image is the OCI image reference for container runtimes, or the shell command for ProcessRuntime (e.g. "python3 -m http.server 8080").
	Env         map[string]string // Env holds environment variables to inject into the workload.
	ConfigFiles map[string]string // ConfigFiles maps container-absolute paths to file content. The runtime materializes and bind-mounts them.
	Ports       []int             // Ports to map from container to host (each port is mapped to a random host port).
	CPUm        int64             // CPUm is the CPU allocation in millicores (informational, not enforced by process runtime).
	MemoryB     int64             // MemoryB is the memory allocation in bytes (informational, not enforced by process runtime).
	IP          string            // IP is the cluster-network IP address allocated to this workload. Used by ContainerRuntime to assign a specific IP via --network/--ip.
}

// Status describes the current observed state of a running or stopped workload.
type Status struct {
	ID       string // ID is the unique identifier of the workload this status describes.
	Running  bool   // Running is true if the workload is currently alive and executing.
	PID      int    // PID is the OS process ID (only meaningful for ProcessRuntime).
	ExitCode int    // ExitCode is the exit code of the workload after it has stopped.
	Error    string // Error holds a human-readable description of any failure that occurred.
}

// Runtime is the pluggable interface for running workloads.
// Controllers never know which backend they are using — the same reconciliation
// logic works against a simulator, OS processes, or real containers.
type Runtime interface {
	// Start ensures a workload described by spec is running. Idempotent — if the
	// workload is already running, this is a no-op.
	Start(ctx context.Context, spec Spec) error

	// Stop ensures a workload identified by id is not running. Idempotent — if
	// the workload is already stopped or unknown, this returns nil.
	Stop(ctx context.Context, id string) error

	// Status returns the current observed state of the workload identified by id.
	// Returns ErrNotFound if the workload has never been started.
	Status(ctx context.Context, id string) (Status, error)

	// List returns the current state of all known workloads managed by this runtime.
	List(ctx context.Context) ([]Status, error)
}
