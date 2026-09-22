package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var validImageReferencePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]*$`)

// ContainerRuntime runs workloads as OCI containers via the nerdctl or docker CLI.
// Spec.Image is the OCI image reference (e.g., "nginx:1.28"). Resource limits
// (CPU millicores and memory bytes) are translated to --cpus and --memory
// flags when provided. When a network name and subnet are configured via
// SetNetwork, containers with an allocated IP are started on that network.
// Config files specified in Spec.ConfigFiles are materialized to a temporary
// directory on the host and bind-mounted read-only into the container.
type ContainerRuntime struct {
	mutex                    sync.Mutex        // mutex guards concurrent access to the trackedContainers and configFileTempDirectories maps.
	trackedContainers        map[string]bool   // trackedContainers maps workload IDs to their running state (true = started, false = stopped).
	containerNames           map[string]string // containerNames maps workload IDs to their container names.
	allocatedHostPorts       map[int]int       // allocatedHostPorts tracks the next host port offset per container port.
	instanceHostPorts        map[string]int    // instanceHostPorts maps workload IDs to their allocated host port (first exposed port).
	configFileTempDirectories map[string]string // configFileTempDirectories maps workload IDs to the temp directory holding their materialized config files.
	networkName              string            // networkName is the container network to connect containers to for IP assignment.
	networkCIDR              string            // networkCIDR is the subnet CIDR for the container network (e.g. "10.100.0.0/16").
	networkReady             bool              // networkReady is true once the container network has been verified or created.
	containerCommand         string            // containerCommand is the CLI binary to use: "nerdctl" or "docker".
}

// detectContainerCommand returns the first available container CLI. Checks in
// order: nerdctl (Linux), docker (Linux/macOS via Docker Desktop), lima (macOS
// with Lima+nerdctl). Returns "docker" as default if none found (will fail at
// runtime with a clear error).
func detectContainerCommand() string {
	if _, lookupError := exec.LookPath("nerdctl"); lookupError == nil {
		return "nerdctl"
	}
	if _, lookupError := exec.LookPath("docker"); lookupError == nil {
		return "docker"
	}
	if _, lookupError := exec.LookPath("lima"); lookupError == nil {
		return "lima"
	}
	return "docker"
}

// buildExecCommand creates an exec.Cmd for the detected container CLI. When
// the container command is "lima", it wraps the call as "lima nerdctl <args>".
func (containerRuntime *ContainerRuntime) buildExecCommand(ctx context.Context, args ...string) *exec.Cmd {
	if containerRuntime.containerCommand == "lima" {
		limaArgs := append([]string{"nerdctl"}, args...)
		return exec.CommandContext(ctx, "lima", limaArgs...)
	}
	return exec.CommandContext(ctx, containerRuntime.containerCommand, args...)
}

// NewContainerRuntime creates a ContainerRuntime with an empty container
// registry. Automatically detects whether to use nerdctl or docker.
func NewContainerRuntime() *ContainerRuntime {
	return &ContainerRuntime{
		trackedContainers:         make(map[string]bool),
		containerNames:            make(map[string]string),
		allocatedHostPorts:        make(map[int]int),
		instanceHostPorts:         make(map[string]int),
		configFileTempDirectories: make(map[string]string),
		containerCommand:          detectContainerCommand(),
	}
}

// SetNetwork configures the runtime to create and use a container network
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

	inspectCommand := containerRuntime.buildExecCommand(ctx, "network", "inspect", containerRuntime.networkName)
	if err := inspectCommand.Run(); err == nil {
		containerRuntime.networkReady = true
		return nil
	}

	var stderr bytes.Buffer
	createCommand := containerRuntime.buildExecCommand(ctx, "network", "create",
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
		hostFilePath = filepath.Clean(hostFilePath)
		if !strings.HasPrefix(hostFilePath, configTempDirectory) {
			return nil, "", fmt.Errorf("config file path traversal detected: %s", containerFilePath)
		}
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
		if !isValidEnvVarName(envKey) {
			continue
		}
		args = append(args, "-e", fmt.Sprintf("%s=%s", envKey, envValue))
	}

	if !validImageReferencePattern.MatchString(spec.Image) {
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("invalid image reference: %q", spec.Image)}
	}

	args = append(args, spec.Image)

	runCommand := containerRuntime.buildExecCommand(ctx, args...)
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
	stopCommand := containerRuntime.buildExecCommand(ctx, "stop", "-t", "10", containerName)
	stopCommand.Run()

	removeCommand := containerRuntime.buildExecCommand(ctx, "rm", "-f", containerName)
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
	inspectCommand := containerRuntime.buildExecCommand(ctx, "inspect", "--format", "{{.State.Running}}", containerName)
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
	execCommand := containerRuntime.buildExecCommand(ctx, args...)
	var stderr bytes.Buffer
	execCommand.Stderr = &stderr
	if err := execCommand.Run(); err != nil {
		return fmt.Errorf("container exec in %s: %v: %s", id, err, stderr.String())
	}
	return nil
}

// ExecInit runs an initialization command by creating a temporary container
// from the given image, executing the command, and removing the container.
func (containerRuntime *ContainerRuntime) ExecInit(ctx context.Context, image string, execSpec ExecSpec) error {
	args := []string{"run", "--rm", image, "sh", "-c", execSpec.Command}
	execCommand := containerRuntime.buildExecCommand(ctx, args...)
	var stderr bytes.Buffer
	execCommand.Stderr = &stderr
	if err := execCommand.Run(); err != nil {
		return fmt.Errorf("container run --rm init: %v: %s", err, stderr.String())
	}
	return nil
}

// Stats returns the current resource usage of the container identified by id
// by querying `nerdctl stats --no-stream`. Returns zero values if the container
// is not running or the stats cannot be parsed.
func (containerRuntime *ContainerRuntime) Stats(ctx context.Context, id string) (ResourceStats, error) {
	containerRuntime.mutex.Lock()
	tracked := containerRuntime.trackedContainers[id]
	containerName := containerRuntime.resolveContainerName(id)
	containerRuntime.mutex.Unlock()

	if !tracked {
		return ResourceStats{}, ErrNotFound
	}

	statsCommand := containerRuntime.buildExecCommand(ctx, "stats", "--no-stream",
		"--format", "{{.CPUPerc}} {{.MemUsage}}", containerName)
	var statsOutput bytes.Buffer
	statsCommand.Stdout = &statsOutput
	if statsError := statsCommand.Run(); statsError != nil {
		return ResourceStats{}, nil
	}

	return parseNerdctlStats(statsOutput.String()), nil
}

// parseNerdctlStats parses the output of nerdctl stats --format
// '{{.CPUPerc}} {{.MemUsage}}' into a ResourceStats. The format is:
// "1.23% 45.6MiB / 7.8GiB" — we extract CPU percentage and current memory.
func parseNerdctlStats(statsLine string) ResourceStats {
	statsLine = strings.TrimSpace(statsLine)
	if statsLine == "" {
		return ResourceStats{}
	}

	fields := strings.Fields(statsLine)
	if len(fields) < 1 {
		return ResourceStats{}
	}

	var resourceStats ResourceStats

	cpuPercent := strings.TrimSuffix(fields[0], "%")
	if parsedCPU, parseError := parseFloat64Safe(cpuPercent); parseError == nil {
		resourceStats.CPUMillicores = int64(parsedCPU * 10)
	}

	if len(fields) >= 2 {
		resourceStats.MemoryBytes = parseMemoryValue(fields[1])
	}

	return resourceStats
}

// parseFloat64Safe parses a string as float64, returning an error on failure.
func parseFloat64Safe(value string) (float64, error) {
	var result float64
	_, parseError := fmt.Sscanf(value, "%f", &result)
	return result, parseError
}

// parseMemoryValue parses a Docker/nerdctl memory string like "45.6MiB" or
// "1.2GiB" into bytes.
func parseMemoryValue(memoryString string) int64 {
	memoryString = strings.TrimSpace(memoryString)
	if len(memoryString) == 0 {
		return 0
	}

	var numericValue float64
	var suffix string

	if _, scanError := fmt.Sscanf(memoryString, "%f%s", &numericValue, &suffix); scanError != nil {
		return 0
	}

	switch strings.ToLower(suffix) {
	case "b":
		return int64(numericValue)
	case "kib":
		return int64(numericValue * 1024)
	case "mib":
		return int64(numericValue * 1024 * 1024)
	case "gib":
		return int64(numericValue * 1024 * 1024 * 1024)
	case "tib":
		return int64(numericValue * 1024 * 1024 * 1024 * 1024)
	case "kb":
		return int64(numericValue * 1000)
	case "mb":
		return int64(numericValue * 1000 * 1000)
	case "gb":
		return int64(numericValue * 1000 * 1000 * 1000)
	default:
		return 0
	}
}

// Logs returns the stdout/stderr output of a container via `nerdctl logs`.
// When follow is true, the returned reader streams new output as it arrives.
func (containerRuntime *ContainerRuntime) Logs(ctx context.Context, id string, follow bool) (io.ReadCloser, error) {
	containerRuntime.mutex.Lock()
	tracked := containerRuntime.trackedContainers[id]
	containerName := containerRuntime.resolveContainerName(id)
	containerRuntime.mutex.Unlock()

	if !tracked {
		return nil, ErrNotFound
	}

	logsArgs := []string{"logs"}
	if follow {
		logsArgs = append(logsArgs, "--follow")
	}
	logsArgs = append(logsArgs, containerName)

	logsCommand := containerRuntime.buildExecCommand(ctx, logsArgs...)
	stdoutPipe, pipeError := logsCommand.StdoutPipe()
	if pipeError != nil {
		return nil, fmt.Errorf("container logs pipe: %w", pipeError)
	}
	logsCommand.Stderr = logsCommand.Stdout

	if startError := logsCommand.Start(); startError != nil {
		return nil, fmt.Errorf("container logs start: %w", startError)
	}

	return &commandReadCloser{reader: stdoutPipe, command: logsCommand}, nil
}

// commandReadCloser wraps an io.Reader and waits for the command to finish on Close.
type commandReadCloser struct {
	reader  io.ReadCloser
	command *exec.Cmd
}

func (commandReader *commandReadCloser) Read(buffer []byte) (int, error) {
	return commandReader.reader.Read(buffer)
}

func (commandReader *commandReadCloser) Close() error {
	commandReader.reader.Close()
	return commandReader.command.Wait()
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
		rmNetwork := containerRuntime.buildExecCommand(ctx, "network", "rm", containerRuntime.networkName)
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
