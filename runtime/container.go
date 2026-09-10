package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ContainerRuntime runs workloads as OCI containers via the docker CLI.
// Spec.Image is the OCI image reference (e.g., "nginx:1.28"). Resource limits
// (CPU millicores and memory bytes) are translated to docker --cpus and --memory
// flags when provided. When a docker network name and subnet are configured via
// SetDockerNetwork, containers with an allocated IP are started on that network.
// Config files specified in Spec.ConfigFiles are materialized to a temporary
// directory on the host and bind-mounted read-only into the container.
type ContainerRuntime struct {
	mutex                    sync.Mutex      // mutex guards concurrent access to the trackedContainers and configFileTempDirectories maps.
	trackedContainers        map[string]bool // trackedContainers maps workload IDs to their running state (true = started, false = stopped).
	allocatedHostPorts       map[int]int     // allocatedHostPorts tracks the next host port offset per container port.
	configFileTempDirectories map[string]string // configFileTempDirectories maps workload IDs to the temp directory holding their materialized config files.
	dockerNetworkName        string          // dockerNetworkName is the docker network to connect containers to for IP assignment.
	dockerNetworkCIDR        string          // dockerNetworkCIDR is the subnet CIDR for the docker network (e.g. "10.100.0.0/16").
	networkReady             bool            // networkReady is true once the docker network has been verified or created.
}

// NewContainerRuntime creates a ContainerRuntime with an empty container
// registry, ready to manage docker containers.
func NewContainerRuntime() *ContainerRuntime {
	return &ContainerRuntime{
		trackedContainers:         make(map[string]bool),
		allocatedHostPorts:        make(map[int]int),
		configFileTempDirectories: make(map[string]string),
	}
}

// SetDockerNetwork configures the runtime to create and use a docker network
// with the given name and subnet CIDR. Containers started with a Spec.IP will
// be connected to this network with the specified IP address.
func (containerRuntime *ContainerRuntime) SetDockerNetwork(networkName string, subnetCIDR string) {
	containerRuntime.dockerNetworkName = networkName
	containerRuntime.dockerNetworkCIDR = subnetCIDR
}

// ensureDockerNetworkExists creates the docker network if it does not already
// exist. Subsequent calls are no-ops once the network has been verified. Must
// be called with the mutex NOT held (it shells out to docker).
func (containerRuntime *ContainerRuntime) ensureDockerNetworkExists(ctx context.Context) error {
	if containerRuntime.networkReady || containerRuntime.dockerNetworkName == "" {
		return nil
	}

	inspectCommand := exec.CommandContext(ctx, "docker", "network", "inspect", containerRuntime.dockerNetworkName)
	if err := inspectCommand.Run(); err == nil {
		containerRuntime.networkReady = true
		return nil
	}

	var stderr bytes.Buffer
	createCommand := exec.CommandContext(ctx, "docker", "network", "create",
		"--driver", "bridge",
		"--subnet", containerRuntime.dockerNetworkCIDR,
		containerRuntime.dockerNetworkName)
	createCommand.Stderr = &stderr
	if err := createCommand.Run(); err != nil {
		return fmt.Errorf("creating docker network %s (subnet %s): %v: %s",
			containerRuntime.dockerNetworkName, containerRuntime.dockerNetworkCIDR, err, stderr.String())
	}

	containerRuntime.networkReady = true
	return nil
}

// materializeConfigFilesToTempDirectory writes the desired config files for a
// workload to a temporary directory on the host filesystem and returns the
// docker volume mount arguments needed to bind-mount each file into the
// container at its target path. Returns nil args and empty path when the config
// files map is empty. Cleans up the temp directory on any write failure.
func (containerRuntime *ContainerRuntime) materializeConfigFilesToTempDirectory(workloadID string, configFiles map[string]string) ([]string, string, error) {
	if len(configFiles) == 0 {
		return nil, "", nil
	}

	configTempDirectory, err := os.MkdirTemp("", "cca-config-"+workloadID+"-")
	if err != nil {
		return nil, "", fmt.Errorf("creating config temp directory for %s: %w", workloadID, err)
	}

	var volumeMountArgs []string
	for containerFilePath, fileContent := range configFiles {
		hostFilePath := filepath.Join(configTempDirectory, containerFilePath)
		parentDirectory := filepath.Dir(hostFilePath)
		if err := os.MkdirAll(parentDirectory, 0755); err != nil {
			os.RemoveAll(configTempDirectory)
			return nil, "", fmt.Errorf("creating parent directory for config file %s: %w", containerFilePath, err)
		}
		if err := os.WriteFile(hostFilePath, []byte(fileContent), 0644); err != nil {
			os.RemoveAll(configTempDirectory)
			return nil, "", fmt.Errorf("writing config file %s: %w", containerFilePath, err)
		}
		volumeMountArgs = append(volumeMountArgs, "-v", fmt.Sprintf("%s:%s:ro", hostFilePath, containerFilePath))
	}

	return volumeMountArgs, configTempDirectory, nil
}

// Start launches a docker container for the workload described by spec. If the
// workload is already tracked as running, this is a no-op (idempotent). The
// container is started in detached mode with a deterministic name derived from
// the workload ID. When a docker network is configured and spec.IP is set, the
// container is connected to that network with the specified IP address. Config
// files from spec.ConfigFiles are materialized to a temp directory and
// bind-mounted read-only into the container.
func (containerRuntime *ContainerRuntime) Start(ctx context.Context, spec Spec) error {
	containerRuntime.mutex.Lock()
	if containerRuntime.trackedContainers[spec.ID] {
		containerRuntime.mutex.Unlock()
		return nil
	}
	containerRuntime.mutex.Unlock()

	if spec.IP != "" && containerRuntime.dockerNetworkName != "" {
		if err := containerRuntime.ensureDockerNetworkExists(ctx); err != nil {
			return &StartError{ID: spec.ID, Reason: fmt.Sprintf("network setup: %v", err)}
		}
	}

	volumeMountArgs, configTempDirectoryPath, err := containerRuntime.materializeConfigFilesToTempDirectory(spec.ID, spec.ConfigFiles)
	if err != nil {
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("config files: %v", err)}
	}

	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	if containerRuntime.trackedContainers[spec.ID] {
		if configTempDirectoryPath != "" {
			os.RemoveAll(configTempDirectoryPath)
		}
		return nil
	}

	args := []string{"run", "-d", "--name", buildDockerContainerName(spec.ID)}

	if spec.IP != "" && containerRuntime.dockerNetworkName != "" {
		args = append(args, "--network", containerRuntime.dockerNetworkName, "--ip", spec.IP)
		if spec.ServiceName != "" {
			args = append(args, "--network-alias", spec.ServiceName)
		}
	}

	if spec.CPUm > 0 {
		args = append(args, fmt.Sprintf("--cpus=%d.%03d", spec.CPUm/1000, spec.CPUm%1000))
	}
	if spec.MemoryB > 0 {
		args = append(args, fmt.Sprintf("--memory=%d", spec.MemoryB*1024*1024))
	}

	for _, containerPort := range spec.Ports {
		hostPort := containerPort + containerRuntime.allocatedHostPorts[containerPort]
		containerRuntime.allocatedHostPorts[containerPort]++
		args = append(args, "-p", fmt.Sprintf("%d:%d", hostPort, containerPort))
	}

	args = append(args, volumeMountArgs...)

	for envKey, envValue := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", envKey, envValue))
	}

	args = append(args, spec.Image)

	dockerRunCommand := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	dockerRunCommand.Stderr = &stderr
	if err := dockerRunCommand.Run(); err != nil {
		if configTempDirectoryPath != "" {
			os.RemoveAll(configTempDirectoryPath)
		}
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("%v: %s", err, stderr.String())}
	}

	containerRuntime.trackedContainers[spec.ID] = true
	if configTempDirectoryPath != "" {
		containerRuntime.configFileTempDirectories[spec.ID] = configTempDirectoryPath
	}
	return nil
}

// Stop terminates and removes the docker container for the workload identified
// by id. It sends a stop command with a 10-second timeout, then force-removes
// the container. Cleans up any materialized config file temp directory. Always
// returns nil — errors from docker are silently ignored to maintain idempotency.
func (containerRuntime *ContainerRuntime) Stop(ctx context.Context, id string) error {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	containerName := buildDockerContainerName(id)
	dockerStopCommand := exec.CommandContext(ctx, "docker", "stop", "-t", "10", containerName)
	dockerStopCommand.Run()

	dockerRemoveCommand := exec.CommandContext(ctx, "docker", "rm", "-f", containerName)
	dockerRemoveCommand.Run()

	if configTempDir, hasConfigFiles := containerRuntime.configFileTempDirectories[id]; hasConfigFiles {
		os.RemoveAll(configTempDir)
		delete(containerRuntime.configFileTempDirectories, id)
	}

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

	containerName := buildDockerContainerName(id)
	dockerInspectCommand := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", containerName)
	var inspectOutput bytes.Buffer
	dockerInspectCommand.Stdout = &inspectOutput
	if err := dockerInspectCommand.Run(); err != nil {
		if !containerRuntime.trackedContainers[id] {
			return Status{}, ErrNotFound
		}
		return Status{ID: id, Running: false}, nil
	}

	running := strings.TrimSpace(inspectOutput.String()) == "true"
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
// marked as running, cleans up all config file temp directories, then removes
// the docker network if one was created. Typically called during shutdown.
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

	containerRuntime.mutex.Lock()
	for workloadID, configTempDir := range containerRuntime.configFileTempDirectories {
		os.RemoveAll(configTempDir)
		delete(containerRuntime.configFileTempDirectories, workloadID)
	}
	containerRuntime.mutex.Unlock()

	if containerRuntime.networkReady && containerRuntime.dockerNetworkName != "" {
		rmNetwork := exec.CommandContext(ctx, "docker", "network", "rm", containerRuntime.dockerNetworkName)
		rmNetwork.Run()
	}
}

// buildDockerContainerName generates a deterministic docker container name from
// a workload ID by prefixing it with "cca-". This ensures container names
// are predictable and scoped to this orchestrator.
func buildDockerContainerName(id string) string {
	return "cca-" + id
}
