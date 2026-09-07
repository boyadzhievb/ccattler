package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// ContainerRuntime runs workloads as OCI containers via the docker CLI.
// Spec.Image is the OCI image reference (e.g., "nginx:1.28"). Resource limits
// (CPU millicores and memory bytes) are translated to docker --cpus and --memory
// flags when provided.
type ContainerRuntime struct {
	mutex             sync.Mutex      // mutex guards concurrent access to the trackedContainers map.
	trackedContainers map[string]bool // trackedContainers maps workload IDs to their running state (true = started, false = stopped).
}

// NewContainerRuntime creates a ContainerRuntime with an empty container
// registry, ready to manage docker containers.
func NewContainerRuntime() *ContainerRuntime {
	return &ContainerRuntime{
		trackedContainers: make(map[string]bool),
	}
}

// Start launches a docker container for the workload described by spec. If the
// workload is already tracked as running, this is a no-op (idempotent). The
// container is started in detached mode with a deterministic name derived from
// the workload ID.
func (containerRuntime *ContainerRuntime) Start(ctx context.Context, spec Spec) error {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	if containerRuntime.trackedContainers[spec.ID] {
		return nil
	}

	args := []string{"run", "-d", "--name", buildDockerContainerName(spec.ID)}

	if spec.CPUm > 0 {
		args = append(args, fmt.Sprintf("--cpus=%d.%03d", spec.CPUm/1000, spec.CPUm%1000))
	}
	if spec.MemoryB > 0 {
		args = append(args, fmt.Sprintf("--memory=%d", spec.MemoryB*1024*1024))
	}

	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, spec.Image)

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("%v: %s", err, stderr.String())}
	}

	containerRuntime.trackedContainers[spec.ID] = true
	return nil
}

// Stop terminates and removes the docker container for the workload identified
// by id. It sends a stop command with a 10-second timeout, then force-removes
// the container. Always returns nil — errors from docker are silently ignored
// to maintain idempotency.
func (containerRuntime *ContainerRuntime) Stop(ctx context.Context, id string) error {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	name := buildDockerContainerName(id)
	cmd := exec.CommandContext(ctx, "docker", "stop", "-t", "10", name)
	cmd.Run()

	rm := exec.CommandContext(ctx, "docker", "rm", "-f", name)
	rm.Run()

	containerRuntime.trackedContainers[id] = false
	return nil
}

// Status returns the current state of the docker container for the workload
// identified by id. It queries the docker daemon via `docker inspect` to
// determine whether the container is running. Returns ErrNotFound if the
// workload has never been started.
func (containerRuntime *ContainerRuntime) Status(ctx context.Context, id string) (Status, error) {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	name := buildDockerContainerName(id)
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", name)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if !containerRuntime.trackedContainers[id] {
			return Status{}, ErrNotFound
		}
		return Status{ID: id, Running: false}, nil
	}

	running := strings.TrimSpace(out.String()) == "true"
	return Status{ID: id, Running: running}, nil
}

// List returns the status of every container the runtime has tracked, querying
// docker for each one's current running state. Containers that fail inspection
// are silently skipped.
func (containerRuntime *ContainerRuntime) List(ctx context.Context) ([]Status, error) {
	containerRuntime.mutex.Lock()
	ids := make([]string, 0, len(containerRuntime.trackedContainers))
	for id := range containerRuntime.trackedContainers {
		ids = append(ids, id)
	}
	containerRuntime.mutex.Unlock()

	var result []Status
	for _, id := range ids {
		st, err := containerRuntime.Status(ctx, id)
		if err != nil {
			continue
		}
		result = append(result, st)
	}
	return result, nil
}

// StopAll terminates and removes every tracked container that is currently
// marked as running. It is typically called during application shutdown to
// clean up docker containers.
func (containerRuntime *ContainerRuntime) StopAll(ctx context.Context) {
	containerRuntime.mutex.Lock()
	ids := make([]string, 0, len(containerRuntime.trackedContainers))
	for id, running := range containerRuntime.trackedContainers {
		if running {
			ids = append(ids, id)
		}
	}
	containerRuntime.mutex.Unlock()

	for _, id := range ids {
		containerRuntime.Stop(ctx, id)
	}
}

// buildDockerContainerName generates a deterministic docker container name from
// a workload ID by prefixing it with "ccattler-". This ensures container names
// are predictable and scoped to this orchestrator.
func buildDockerContainerName(id string) string {
	return "ccattler-" + id
}
