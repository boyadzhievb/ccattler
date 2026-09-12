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

// ContainerRuntime runs workloads as OCI containers via the nerdctl CLI.
// Spec.Image is the OCI image reference (e.g., "nginx:1.28"). Resource limits
// (CPU millicores and memory bytes) are translated to nerdctl --cpus and --memory
// flags when provided. When a network name and subnet are configured via
// SetNetwork, containers with an allocated IP are started on that network.
// Config files specified in Spec.ConfigFiles are materialized to a temporary
// directory on the host and bind-mounted read-only into the container.
type ContainerRuntime struct {
	mutex                    sync.Mutex        // mutex guards concurrent access to the trackedContainers and configFileTempDirectories maps.
	trackedContainers        map[string]bool   // trackedContainers maps workload IDs to their running state (true = started, false = stopped).
	containerNames           map[string]string // containerNames maps workload IDs to their nerdctl container names.
	allocatedHostPorts       map[int]int       // allocatedHostPorts tracks the next host port offset per container port.
	instanceHostPorts        map[string]int    // instanceHostPorts maps workload IDs to their allocated host port (first exposed port).
	configFileTempDirectories map[string]string // configFileTempDirectories maps workload IDs to the temp directory holding their materialized config files.
	networkName              string            // networkName is the nerdctl network to connect containers to for IP assignment.
	networkCIDR              string            // networkCIDR is the subnet CIDR for the nerdctl network (e.g. "10.100.0.0/16").
	networkReady             bool              // networkReady is true once the nerdctl network has been verified or created.
}

// NewContainerRuntime creates a ContainerRuntime with an empty container
// registry, ready to manage nerdctl containers.
func NewContainerRuntime() *ContainerRuntime {
	return &ContainerRuntime{
		trackedContainers:         make(map[string]bool),
		containerNames:            make(map[string]string),
		allocatedHostPorts:        make(map[int]int),
		instanceHostPorts:         make(map[string]int),
		configFileTempDirectories: make(map[string]string),
	}
}

// SetNetwork configures the runtime to create and use a nerdctl network
// with the given name and subnet CIDR. Containers started with a Spec.IP will
// be connected to this network with the specified IP address.
func (containerRuntime *ContainerRuntime) SetNetwork(networkName string, subnetCIDR string) {
	containerRuntime.networkName = networkName
	containerRuntime.networkCIDR = subnetCIDR
}

// ensureNetworkExists creates the nerdctl network if it does not already
// exist. Subsequent calls are no-ops once the network has been verified. Must
// be called with the mutex NOT held (it shells out to nerdctl).
func (containerRuntime *ContainerRuntime) ensureNetworkExists(ctx context.Context) error {
	if containerRuntime.networkReady || containerRuntime.networkName == "" {
		return nil
	}

	inspectCommand := exec.CommandContext(ctx, "nerdctl", "network", "inspect", containerRuntime.networkName)
	if err := inspectCommand.Run(); err == nil {
		containerRuntime.networkReady = true
		return nil
	}

	var stderr bytes.Buffer
	createCommand := exec.CommandContext(ctx, "nerdctl", "network", "create",
		"--driver", "bridge",
		"--subnet", containerRuntime.networkCIDR,
		containerRuntime.networkName)
	createCommand.Stderr = &stderr
	if err := createCommand.Run(); err != nil {
		return fmt.Errorf("creating nerdctl network %s (subnet %s): %v: %s",
			containerRuntime.networkName, containerRuntime.networkCIDR, err, stderr.String())
	}

	containerRuntime.networkReady = true
	return nil
}

// materializeConfigFilesToTempDirectory writes the desired config files for a
// workload to a temporary directory on the host filesystem and returns the
// nerdctl volume mount arguments needed to bind-mount each file into the
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

// Start launches a nerdctl container for the workload described by spec. If the
// workload is already tracked as running, this is a no-op (idempotent). The
// container is started in detached mode with a deterministic name derived from
// the workload ID. When a nerdctl network is configured and spec.IP is set, the
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

	if spec.IP != "" && containerRuntime.networkName != "" {
		if err := containerRuntime.ensureNetworkExists(ctx); err != nil {
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

	containerName := buildContainerName(spec.ServiceName, spec.ID)
	args := []string{"run", "-d", "--name", containerName}

	if spec.IP != "" && containerRuntime.networkName != "" {
		args = append(args, "--network", containerRuntime.networkName, "--ip", spec.IP)
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

	firstHostPort := 0
	for portIndex, containerPort := range spec.Ports {
		hostPort := containerPort + containerRuntime.allocatedHostPorts[containerPort]
		containerRuntime.allocatedHostPorts[containerPort]++
		args = append(args, "-p", fmt.Sprintf("%d:%d", hostPort, containerPort))
		if portIndex == 0 {
			firstHostPort = hostPort
		}
	}

	args = append(args, volumeMountArgs...)

	for envKey, envValue := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", envKey, envValue))
	}

	args = append(args, spec.Image)

	runCommand := exec.CommandContext(ctx, "nerdctl", args...)
	var stderr bytes.Buffer
	runCommand.Stderr = &stderr
	if err := runCommand.Run(); err != nil {
		if configTempDirectoryPath != "" {
			os.RemoveAll(configTempDirectoryPath)
		}
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("%v: %s", err, stderr.String())}
	}

	containerRuntime.trackedContainers[spec.ID] = true
	containerRuntime.containerNames[spec.ID] = containerName
	if firstHostPort > 0 {
		containerRuntime.instanceHostPorts[spec.ID] = firstHostPort
	}
	if configTempDirectoryPath != "" {
		containerRuntime.configFileTempDirectories[spec.ID] = configTempDirectoryPath
	}
	return nil
}

// HostPortForInstance returns the host port allocated to the given workload's
// first exposed port. Returns 0 if the workload has no port mapping or was
// not started by this runtime.
func (containerRuntime *ContainerRuntime) HostPortForInstance(instanceID string) int {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()
	return containerRuntime.instanceHostPorts[instanceID]
}

// Stop terminates and removes the nerdctl container for the workload identified
// by id. It sends a stop command with a 10-second timeout, then force-removes
// the container. Cleans up any materialized config file temp directory. Always
// returns nil — errors from nerdctl are silently ignored to maintain idempotency.
func (containerRuntime *ContainerRuntime) Stop(ctx context.Context, id string) error {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	containerName := containerRuntime.resolveContainerName(id)
	stopCommand := exec.CommandContext(ctx, "nerdctl", "stop", "-t", "10", containerName)
	stopCommand.Run()

	removeCommand := exec.CommandContext(ctx, "nerdctl", "rm", "-f", containerName)
	removeCommand.Run()

	if configTempDir, hasConfigFiles := containerRuntime.configFileTempDirectories[id]; hasConfigFiles {
		os.RemoveAll(configTempDir)
		delete(containerRuntime.configFileTempDirectories, id)
	}

	containerRuntime.trackedContainers[id] = false
	return nil
}

// Status returns the current state of the nerdctl container for the workload
// identified by id. It queries the container runtime via `nerdctl inspect` to
// determine whether the container is running. Returns ErrNotFound if the
// workload has never been started.
func (containerRuntime *ContainerRuntime) Status(ctx context.Context, id string) (Status, error) {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	containerName := containerRuntime.resolveContainerName(id)
	inspectCommand := exec.CommandContext(ctx, "nerdctl", "inspect", "--format", "{{.State.Running}}", containerName)
	var inspectOutput bytes.Buffer
	inspectCommand.Stdout = &inspectOutput
	if err := inspectCommand.Run(); err != nil {
		if !containerRuntime.trackedContainers[id] {
			return Status{}, ErrNotFound
		}
		return Status{ID: id, Running: false}, nil
	}

	running := strings.TrimSpace(inspectOutput.String()) == "true"
	return Status{ID: id, Running: running}, nil
}

// List returns the status of every container the runtime has tracked, querying
// nerdctl for each one's current running state. Containers that fail inspection
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

// Exec runs a command inside a running nerdctl container via `nerdctl exec`.
func (containerRuntime *ContainerRuntime) Exec(ctx context.Context, id string, execSpec ExecSpec) error {
	containerRuntime.mutex.Lock()
	tracked := containerRuntime.trackedContainers[id]
	containerName := containerRuntime.resolveContainerName(id)
	containerRuntime.mutex.Unlock()

	if !tracked {
		return ErrNotFound
	}

	args := []string{"exec", containerName, "sh", "-c", execSpec.Command}
	execCommand := exec.CommandContext(ctx, "nerdctl", args...)
	var stderr bytes.Buffer
	execCommand.Stderr = &stderr
	if err := execCommand.Run(); err != nil {
		return fmt.Errorf("nerdctl exec in %s: %v: %s", id, err, stderr.String())
	}
	return nil
}

// StopAll terminates and removes every tracked container that is currently
// marked as running, cleans up all config file temp directories, then removes
// the nerdctl network if one was created. Typically called during shutdown.
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

	if containerRuntime.networkReady && containerRuntime.networkName != "" {
		rmNetwork := exec.CommandContext(ctx, "nerdctl", "network", "rm", containerRuntime.networkName)
		rmNetwork.Run()
	}
}

// buildContainerName generates a deterministic nerdctl container name
// from a service name and instance ID, following the Kubernetes pattern of
// {resource}-{hash}. For example, "web-a8f31bc2" instead of "cca-a8f31bc2".
func buildContainerName(serviceName string, instanceID string) string {
	if serviceName != "" {
		return serviceName + "-" + instanceID
	}
	return "cca-" + instanceID
}

// resolveContainerName returns the tracked nerdctl container name for an
// instance. Falls back to "cca-{id}" for containers started before service
// names were tracked.
func (containerRuntime *ContainerRuntime) resolveContainerName(instanceID string) string {
	if trackedName, exists := containerRuntime.containerNames[instanceID]; exists {
		return trackedName
	}
	return "cca-" + instanceID
}
