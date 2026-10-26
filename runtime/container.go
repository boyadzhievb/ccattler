// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
)

var validImageReferencePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]*$`)

// defaultDNSSearchDomain is the search domain added to container resolv.conf
// so bare service names (e.g. "metadata-db") resolve via CCattler DNS.
// Must match network.DefaultDNSDomain.
const defaultDNSSearchDomain = "cca.local"

// nerdctlCommandTimeout is the maximum time nerdctl inspect/stats commands are
// allowed to run before being killed. Prevents the agent from blocking when
// nerdctl hangs on exited containers.
const nerdctlCommandTimeout = 10 * time.Second

// ContainerRuntime runs workloads as OCI containers via the nerdctl CLI.
// Spec.Image is the OCI image reference (e.g., "nginx:1.28"). Resource limits
// (CPU millicores and memory bytes) are translated to --cpus and --memory
// flags when provided. When a network name and subnet are configured via
// SetNetwork, containers with an allocated IP are started on that network.
// Config files specified in Spec.ConfigFiles are materialized to a temporary
// directory on the host and bind-mounted read-only into the container.
type ContainerRuntime struct {
	mutex                     sync.Mutex             // mutex guards concurrent access to the trackedContainers and configFileTempDirectories maps.
	trackedContainers         map[string]bool        // trackedContainers maps workload IDs to their running state (true = started, false = stopped).
	containerNames            map[string]string      // containerNames maps workload IDs to their container names.
	allocatedHostPorts        map[int]int            // allocatedHostPorts tracks the next host port offset per container port.
	instanceHostPorts         map[string]map[int]int // instanceHostPorts maps workload IDs to containerPort→hostPort mappings.
	configFileTempDirectories map[string]string      // configFileTempDirectories maps workload IDs to the temp directory holding their materialized config files.
	networkName               string                 // networkName is the container network to connect containers to for IP assignment.
	networkCIDR               string                 // networkCIDR is the subnet CIDR for the container network (e.g. "10.100.0.0/16").
	networkReady              bool                   // networkReady is true once the container network has been verified or created.
	dnsAddress                string                 // dnsAddress is the DNS server address passed to containers via --dns (empty = use runtime default).
	dnsSearchDomain           string                 // dnsSearchDomain is the search domain passed to containers via --dns-search (e.g. "cca.local").
	containerCommand          string                 // containerCommand is the CLI binary to use: "nerdctl" or "lima".
}

// detectContainerCommand returns the first available container CLI. Checks
// nerdctl (Linux) then lima (macOS with Lima+nerdctl). Returns "nerdctl" as
// default if none found (will fail at runtime with a clear error).
func detectContainerCommand() string {
	if _, lookupError := exec.LookPath("nerdctl"); lookupError == nil {
		return "nerdctl"
	}
	if _, lookupError := exec.LookPath("lima"); lookupError == nil {
		return "lima"
	}
	return "nerdctl"
}

// buildExecCommand creates an exec.Cmd for the detected container CLI. When
// the container command is "lima", it wraps the call as "lima nerdctl <args>".
func (containerRuntime *ContainerRuntime) buildExecCommand(ctx context.Context, args ...string) *exec.Cmd {
	var command *exec.Cmd
	if containerRuntime.containerCommand == "lima" {
		limaArgs := append([]string{"nerdctl"}, args...)
		command = exec.CommandContext(ctx, "lima", limaArgs...) //nolint:gosec // container runtime executes nerdctl commands
	} else {
		command = exec.CommandContext(ctx, containerRuntime.containerCommand, args...) //nolint:gosec // container runtime executes nerdctl commands
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 2 * time.Second
	return command
}

// NewContainerRuntime creates a ContainerRuntime with an empty container
// registry. Automatically detects whether to use nerdctl or lima.
func NewContainerRuntime() *ContainerRuntime {
	return &ContainerRuntime{
		trackedContainers:         make(map[string]bool),
		containerNames:            make(map[string]string),
		allocatedHostPorts:        make(map[int]int),
		instanceHostPorts:         make(map[string]map[int]int),
		configFileTempDirectories: make(map[string]string),
		containerCommand:          detectContainerCommand(),
	}
}

// SetNetwork configures the runtime to create and use a container network
// with the given name and subnet CIDR. All containers are connected to this
// network with a DNS alias matching their service name. When Spec.IP is also
// set, the container receives that specific IP address.
func (containerRuntime *ContainerRuntime) SetNetwork(networkName string, subnetCIDR string) {
	containerRuntime.networkName = networkName
	containerRuntime.networkCIDR = subnetCIDR
}

// SetDNS configures the DNS server address and search domain that containers
// will use for name resolution. The address overrides the runtime default so
// that containers resolve service names via the CCattler DNS server. The search
// domain allows bare service names like "metadata-db" to resolve as
// "metadata-db.cca.local".
func (containerRuntime *ContainerRuntime) SetDNS(dnsAddress string) {
	containerRuntime.dnsAddress = dnsAddress
	containerRuntime.dnsSearchDomain = defaultDNSSearchDomain
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
		if err := os.MkdirAll(parentDirectory, 0750); err != nil {
			_ = os.RemoveAll(configTempDirectory)
			return nil, "", fmt.Errorf("creating parent directory for config file %s: %w", containerFilePath, err)
		}
		if err := os.WriteFile(hostFilePath, []byte(fileContent), 0755); err != nil { //nolint:gosec // config files are bind-mounted :ro; 0755 allows scripts like cca-secret to be executable
			_ = os.RemoveAll(configTempDirectory)
			return nil, "", fmt.Errorf("writing config file %s: %w", containerFilePath, err)
		}
		volumeMountArgs = append(volumeMountArgs, "-v", fmt.Sprintf("%s:%s:ro", hostFilePath, containerFilePath))
	}

	return volumeMountArgs, configTempDirectory, nil
}

// Start launches a nerdctl container for the workload described by spec. If the
// workload is already tracked as running, this is a no-op (idempotent). The
// container is started in detached mode with a deterministic name derived from
// the workload ID. When a nerdctl network is configured, the container is
// connected to it with a DNS alias matching the service name; if spec.IP is
// also set, the container receives that specific IP. Config files from
// spec.ConfigFiles are materialized to a temp directory and bind-mounted
// read-only into the container.
func (containerRuntime *ContainerRuntime) Start(ctx context.Context, spec Spec) error {
	containerRuntime.mutex.Lock()
	if containerRuntime.trackedContainers[spec.ID] {
		containerName := containerRuntime.resolveContainerName(spec.ID)
		if containerRuntime.isContainerActuallyRunning(ctx, containerName) {
			containerRuntime.mutex.Unlock()
			return nil
		}
		containerRuntime.removeExitedContainerLocked(ctx, spec.ID, containerName)
	}
	containerRuntime.mutex.Unlock()

	if containerRuntime.networkName != "" {
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
			_ = os.RemoveAll(configTempDirectoryPath)
		}
		return nil
	}

	containerName := buildContainerName(spec.ServiceName, spec.ID)

	if containerRuntime.adoptOrRemoveExistingContainer(ctx, spec.ID, containerName) {
		if configTempDirectoryPath != "" {
			_ = os.RemoveAll(configTempDirectoryPath)
		}
		return nil
	}

	args, portMappings, validationError := containerRuntime.buildRunArguments(spec, containerName, volumeMountArgs)
	if validationError != nil {
		return validationError
	}

	runCommand := containerRuntime.buildExecCommand(ctx, args...)
	var stderr bytes.Buffer
	runCommand.Stderr = &stderr
	if err := runCommand.Run(); err != nil {
		if configTempDirectoryPath != "" {
			_ = os.RemoveAll(configTempDirectoryPath)
		}
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("%v: %s", err, stderr.String())}
	}

	containerRuntime.trackedContainers[spec.ID] = true
	containerRuntime.containerNames[spec.ID] = containerName
	if len(portMappings) > 0 {
		containerRuntime.instanceHostPorts[spec.ID] = portMappings
	}
	if configTempDirectoryPath != "" {
		containerRuntime.configFileTempDirectories[spec.ID] = configTempDirectoryPath
	}
	return nil
}

// buildRunArguments constructs the nerdctl run argument list from a
// workload spec. Returns the args slice, a map of containerPort→hostPort
// for all exposed ports, and a validation error if the image reference is
// invalid. This function is separated from Start for testability.
func (containerRuntime *ContainerRuntime) buildRunArguments(spec Spec, containerName string, volumeMountArgs []string) ([]string, map[int]int, error) {
	args := []string{"run", "-d", "--pull", "never", "--name", containerName}

	if containerRuntime.networkName != "" {
		args = append(args, "--network", containerRuntime.networkName)
		if spec.IP != "" {
			args = append(args, "--ip", spec.IP)
		}
		if spec.ServiceName != "" {
			args = append(args, "--hostname", spec.ServiceName)
		}
	}

	if containerRuntime.dnsAddress != "" {
		args = append(args, "--dns", containerRuntime.dnsAddress)
	}
	if containerRuntime.dnsSearchDomain != "" {
		args = append(args, "--dns-search", containerRuntime.dnsSearchDomain)
	}

	if spec.CPUm > 0 {
		args = append(args, fmt.Sprintf("--cpus=%d.%03d", spec.CPUm/1000, spec.CPUm%1000))
	}
	if spec.MemoryB > 0 {
		args = append(args, fmt.Sprintf("--memory=%d", spec.MemoryB))
	}

	if spec.LogMaxSize != "" || spec.LogMaxFiles > 0 {
		args = append(args, "--log-driver", "json-file")
		if spec.LogMaxSize != "" {
			args = append(args, "--log-opt", fmt.Sprintf("max-size=%s", spec.LogMaxSize))
		}
		if spec.LogMaxFiles > 0 {
			args = append(args, "--log-opt", fmt.Sprintf("max-file=%d", spec.LogMaxFiles))
		}
	}

	portMappings := make(map[int]int, len(spec.Ports))
	for _, containerPort := range spec.Ports {
		hostPort := containerPort + containerRuntime.allocatedHostPorts[containerPort]
		containerRuntime.allocatedHostPorts[containerPort]++
		args = append(args, "-p", fmt.Sprintf("%d:%d", hostPort, containerPort))
		portMappings[containerPort] = hostPort
	}

	args = append(args, volumeMountArgs...)

	for envKey, envValue := range spec.Env {
		if !isValidEnvVarName(envKey) {
			continue
		}
		args = append(args, "-e", fmt.Sprintf("%s=%s", envKey, envValue))
	}

	if !validImageReferencePattern.MatchString(spec.Image) {
		return nil, nil, &StartError{ID: spec.ID, Reason: fmt.Sprintf("invalid image reference: %q", spec.Image)}
	}

	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args, portMappings, nil
}

// HostPortsForInstance returns all containerPort→hostPort mappings allocated
// for the given workload. Returns nil if the workload has no port mappings
// or was not started by this runtime.
func (containerRuntime *ContainerRuntime) HostPortsForInstance(instanceID string) map[int]int {
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
	_ = stopCommand.Run()

	removeCommand := containerRuntime.buildExecCommand(ctx, "rm", "-f", containerName)
	_ = removeCommand.Run()

	if configTempDir, hasConfigFiles := containerRuntime.configFileTempDirectories[id]; hasConfigFiles {
		_ = os.RemoveAll(configTempDir)
		delete(containerRuntime.configFileTempDirectories, id)
	}

	containerRuntime.trackedContainers[id] = false
	return nil
}

// StopWithGracePeriod stops a container using a custom nerdctl stop -t timeout
// instead of the default 10 seconds.
func (containerRuntime *ContainerRuntime) StopWithGracePeriod(ctx context.Context, instanceID string, gracePeriod time.Duration) error {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	containerName := containerRuntime.resolveContainerName(instanceID)
	timeoutSeconds := strconv.Itoa(int(gracePeriod.Seconds()))
	stopCommand := containerRuntime.buildExecCommand(ctx, "stop", "-t", timeoutSeconds, containerName)
	_ = stopCommand.Run()

	removeCommand := containerRuntime.buildExecCommand(ctx, "rm", "-f", containerName)
	_ = removeCommand.Run()

	if configTempDir, hasConfigFiles := containerRuntime.configFileTempDirectories[instanceID]; hasConfigFiles {
		_ = os.RemoveAll(configTempDir)
		delete(containerRuntime.configFileTempDirectories, instanceID)
	}

	containerRuntime.trackedContainers[instanceID] = false
	return nil
}

// isContainerActuallyRunning checks if a container is truly running by calling
// nerdctl inspect. Must be called with the mutex held.
func (containerRuntime *ContainerRuntime) isContainerActuallyRunning(ctx context.Context, containerName string) bool {
	inspectContext, cancelInspect := context.WithTimeout(ctx, nerdctlCommandTimeout)
	defer cancelInspect()

	inspectCommand := containerRuntime.buildExecCommand(inspectContext, "inspect", "--format", "{{.State.Running}}", containerName)
	var inspectOutput bytes.Buffer
	inspectCommand.Stdout = &inspectOutput
	if inspectError := inspectCommand.Run(); inspectError != nil {
		return false
	}
	return strings.TrimSpace(inspectOutput.String()) == "true"
}

// removeExitedContainerLocked removes a stopped container and clears all
// tracking state so Start can create a fresh container. Must be called with
// the mutex held.
func (containerRuntime *ContainerRuntime) removeExitedContainerLocked(ctx context.Context, instanceID string, containerName string) {
	removeCommand := containerRuntime.buildExecCommand(ctx, "rm", "-f", containerName)
	_ = removeCommand.Run()

	if configTempDir, hasConfigFiles := containerRuntime.configFileTempDirectories[instanceID]; hasConfigFiles {
		_ = os.RemoveAll(configTempDir)
		delete(containerRuntime.configFileTempDirectories, instanceID)
	}
	delete(containerRuntime.trackedContainers, instanceID)
	delete(containerRuntime.containerNames, instanceID)
	delete(containerRuntime.instanceHostPorts, instanceID)
}

// adoptOrRemoveExistingContainer handles containers left over from a previous
// agent run. If a container with containerName is running, it is adopted into
// tracking and returns true. If it exists but is stopped, it is removed and
// returns false so the caller can create a fresh one. Must be called with mutex held.
func (containerRuntime *ContainerRuntime) adoptOrRemoveExistingContainer(ctx context.Context, instanceID string, containerName string) bool {
	if containerRuntime.isContainerActuallyRunning(ctx, containerName) {
		logging.Default().Info("adopting running container from previous agent run", "instance", instanceID, "container", containerName)
		containerRuntime.trackedContainers[instanceID] = true
		containerRuntime.containerNames[instanceID] = containerName
		return true
	}
	removeCommand := containerRuntime.buildExecCommand(ctx, "rm", "-f", containerName)
	_ = removeCommand.Run()
	return false
}

// Status returns the current state of the nerdctl container for the workload
// identified by id. It queries the container runtime via `nerdctl inspect` to
// determine whether the container is running. Returns ErrNotFound if the
// workload has never been started.
func (containerRuntime *ContainerRuntime) Status(ctx context.Context, id string) (Status, error) {
	containerRuntime.mutex.Lock()
	defer containerRuntime.mutex.Unlock()

	containerName := containerRuntime.resolveContainerName(id)

	inspectContext, cancelInspect := context.WithTimeout(ctx, nerdctlCommandTimeout)
	defer cancelInspect()

	inspectCommand := containerRuntime.buildExecCommand(inspectContext, "inspect",
		"--format", "{{.State.Running}}|{{.NetworkSettings.IPAddress}}|{{.State.ExitCode}}", containerName)
	var inspectOutput bytes.Buffer
	inspectCommand.Stdout = &inspectOutput
	if err := inspectCommand.Run(); err != nil {
		if !containerRuntime.trackedContainers[id] {
			return Status{}, ErrNotFound
		}
		return Status{ID: id, Running: false}, nil
	}

	rawOutput := strings.TrimSpace(inspectOutput.String())
	outputParts := strings.SplitN(rawOutput, "|", 3)
	if len(outputParts) < 2 {
		logging.Default().Warn("unexpected nerdctl inspect format", "id", id, "output", rawOutput)
		return Status{ID: id, Running: false}, nil
	}
	running := outputParts[0] == "true"
	containerIP := outputParts[1]

	exitCode := 0
	if len(outputParts) > 2 {
		parsedCode, parseErr := strconv.Atoi(outputParts[2])
		if parseErr == nil {
			exitCode = parsedCode
		}
	}

	exitReason := ""
	if !running && exitCode != 0 {
		oomContext, cancelOOM := context.WithTimeout(ctx, nerdctlCommandTimeout)
		oomCommand := containerRuntime.buildExecCommand(oomContext, "inspect", "--format", "{{.State.OOMKilled}}", containerName)
		var oomOutput bytes.Buffer
		oomCommand.Stdout = &oomOutput
		if oomCommand.Run() == nil && strings.TrimSpace(oomOutput.String()) == "true" {
			exitReason = "OOMKilled"
		}
		cancelOOM()
	}

	return Status{ID: id, Running: running, IP: containerIP, ExitCode: exitCode, Error: exitReason}, nil
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

// ExecCapture runs a command inside a running container and returns the
// combined stdout and stderr output. Unlike Exec, this captures the output
// for relay back to callers (e.g. the debug API exec endpoint).
func (containerRuntime *ContainerRuntime) ExecCapture(ctx context.Context, instanceID string, execSpec ExecSpec) ([]byte, error) {
	containerRuntime.mutex.Lock()
	tracked := containerRuntime.trackedContainers[instanceID]
	containerName := containerRuntime.resolveContainerName(instanceID)
	containerRuntime.mutex.Unlock()

	if !tracked {
		return nil, ErrNotFound
	}

	args := []string{"exec", containerName, "sh", "-c", execSpec.Command}
	execCommand := containerRuntime.buildExecCommand(ctx, args...)
	var combinedOutput bytes.Buffer
	execCommand.Stdout = &combinedOutput
	execCommand.Stderr = &combinedOutput
	execError := execCommand.Run()
	return combinedOutput.Bytes(), execError
}

// ExecInit runs an initialization command by creating a temporary container
// from the given image, executing the command, and removing the container.
// Uses bash instead of sh because the cca-secret shell function uses a hyphen
// in its name, which POSIX sh (dash) rejects as a syntax error. bash supports
// hyphens in function names. Common image entrypoints (Airflow, Postgres) also
// recognise "bash" and pass through correctly.
func (containerRuntime *ContainerRuntime) ExecInit(ctx context.Context, image string, execSpec ExecSpec) error {
	if !validImageReferencePattern.MatchString(image) {
		return fmt.Errorf("invalid image reference: %q", image)
	}
	args := []string{"run", "--rm"}
	if containerRuntime.networkName != "" {
		if initNetworkError := containerRuntime.ensureNetworkExists(ctx); initNetworkError != nil {
			return fmt.Errorf("init network setup: %w", initNetworkError)
		}
		args = append(args, "--network", containerRuntime.networkName)
	}
	if containerRuntime.dnsAddress != "" {
		args = append(args, "--dns", containerRuntime.dnsAddress)
	}
	if containerRuntime.dnsSearchDomain != "" {
		args = append(args, "--dns-search", containerRuntime.dnsSearchDomain)
	}
	for envKey, envValue := range execSpec.Env {
		if !isValidEnvVarName(envKey) {
			continue
		}
		args = append(args, "-e", fmt.Sprintf("%s=%s", envKey, envValue))
	}
	initContainerName := fmt.Sprintf("cca-init-%d", time.Now().UnixNano())
	initHostDir := filepath.Join(containerdLogBaseDir, initContainerName)
	for containerPath, fileContent := range execSpec.ConfigFiles {
		hostPath := filepath.Join(initHostDir, "config", filepath.Base(containerPath))
		_ = os.MkdirAll(filepath.Dir(hostPath), 0750)
		_ = os.WriteFile(hostPath, []byte(fileContent), 0600) // #nosec G306
		args = append(args, "-v", hostPath+":"+containerPath+":ro")
	}
	defer func() { _ = os.RemoveAll(initHostDir) }()
	args = append(args, "--entrypoint", "bash", image, "-c", execSpec.Command)
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

	statsContext, cancelStats := context.WithTimeout(ctx, nerdctlCommandTimeout)
	defer cancelStats()

	statsCommand := containerRuntime.buildExecCommand(statsContext, "stats", "--no-stream",
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

// parseMemoryValue parses a nerdctl memory string like "45.6MiB" or
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
// Resize is not supported by the nerdctl container runtime. Live cgroup
// updates require direct cgroup v2 manipulation, which is not available
// through the nerdctl CLI. The caller should stop and restart the container.
func (containerRuntime *ContainerRuntime) Resize(_ context.Context, _ string, _ int64, _ int64) error {
	return ErrResizeUnsupported
}

// Logs returns a reader that streams the stdout/stderr output of the
// container identified by id. If follow is true, the reader blocks for
// new output. The caller must close the returned ReadCloser when done.
func (containerRuntime *ContainerRuntime) Logs(ctx context.Context, id string, options LogsOptions) (io.ReadCloser, error) {
	containerRuntime.mutex.Lock()
	tracked := containerRuntime.trackedContainers[id]
	containerName := containerRuntime.resolveContainerName(id)
	containerRuntime.mutex.Unlock()

	if !tracked {
		return nil, ErrNotFound
	}

	logsArgs := []string{"logs"}
	if options.Follow {
		logsArgs = append(logsArgs, "--follow")
	}
	if options.TailLines != "" {
		logsArgs = append(logsArgs, "--tail", options.TailLines)
	}
	if options.Since != "" {
		logsArgs = append(logsArgs, "--since", options.Since)
	}
	if options.Timestamps {
		logsArgs = append(logsArgs, "--timestamps")
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
	_ = commandReader.reader.Close()
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
		if stopError := containerRuntime.Stop(ctx, id); stopError != nil {
			logging.Default().Error("failed to stop container", "container", id, "error", stopError.Error())
		}
	}

	containerRuntime.mutex.Lock()
	for workloadID, configTempDir := range containerRuntime.configFileTempDirectories {
		_ = os.RemoveAll(configTempDir)
		delete(containerRuntime.configFileTempDirectories, workloadID)
	}
	containerRuntime.mutex.Unlock()

	if containerRuntime.networkReady && containerRuntime.networkName != "" {
		rmNetwork := containerRuntime.buildExecCommand(ctx, "network", "rm", containerRuntime.networkName)
		_ = rmNetwork.Run()
	}
}

// ListImages returns all locally cached container images by running
// `nerdctl images`. Implements the ImageLister interface.
func (containerRuntime *ContainerRuntime) ListImages(ctx context.Context) ([]ImageInfo, error) {
	listCommand := containerRuntime.buildExecCommand(ctx, "images",
		"--format", "{{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.Size}}")
	var commandOutput bytes.Buffer
	listCommand.Stdout = &commandOutput
	if runError := listCommand.Run(); runError != nil {
		return nil, fmt.Errorf("listing images: %w", runError)
	}

	var images []ImageInfo
	for _, outputLine := range strings.Split(commandOutput.String(), "\n") {
		outputLine = strings.TrimSpace(outputLine)
		if outputLine == "" {
			continue
		}
		fields := strings.SplitN(outputLine, "\t", 4)
		if len(fields) < 4 {
			continue
		}
		sizeBytes := parseMemoryValue(fields[3])
		images = append(images, ImageInfo{
			Repository: fields[0],
			Tag:        fields[1],
			ImageID:    fields[2],
			SizeBytes:  sizeBytes,
		})
	}
	return images, nil
}

// DiscoveredContainer describes a container found by scanning the local
// container runtime directly via nerdctl ps, without requiring
// an agent or tracked state.
type DiscoveredContainer struct {
	Name    string // Name is the container name assigned at creation.
	ID      string // ID is the container's short identifier.
	Image   string // Image is the OCI image reference the container was started from.
	Status  string // Status is the human-readable status string (e.g. "Up 2 hours").
	Running bool   // Running is true when the status indicates the container is up.
}

// ContainerResourceUsage describes resource consumption for a single container
// as reported by nerdctl/docker stats.
type ContainerResourceUsage struct {
	Name          string // Name is the container name.
	CPUMillicores int64  // CPUMillicores is the current CPU usage (percent * 10).
	MemoryBytes   int64  // MemoryBytes is the current resident memory in bytes.
}

// DetectedCommand returns the container CLI binary that this runtime uses
// (e.g. "nerdctl", "lima").
func (containerRuntime *ContainerRuntime) DetectedCommand() string {
	return containerRuntime.containerCommand
}

// DiscoverAllContainers scans the local container runtime for all containers
// (running and stopped) by shelling out to nerdctl/docker ps -a. This
// bypasses the in-memory tracking and shows ground-truth container state.
func (containerRuntime *ContainerRuntime) DiscoverAllContainers(ctx context.Context) ([]DiscoveredContainer, error) {
	listCommand := containerRuntime.buildExecCommand(ctx, "ps", "-a",
		"--format", "{{.Names}}\t{{.ID}}\t{{.Image}}\t{{.Status}}")
	var commandOutput bytes.Buffer
	listCommand.Stdout = &commandOutput
	var stderrOutput bytes.Buffer
	listCommand.Stderr = &stderrOutput
	if runError := listCommand.Run(); runError != nil {
		return nil, fmt.Errorf("listing containers: %v: %s", runError, stderrOutput.String())
	}
	return parseContainerListOutput(commandOutput.String()), nil
}

// parseContainerListOutput parses tab-separated nerdctl/docker ps output into
// DiscoveredContainer entries. Each line has the format:
// Name\tID\tImage\tStatus
func parseContainerListOutput(output string) []DiscoveredContainer {
	var containers []DiscoveredContainer
	for _, outputLine := range strings.Split(output, "\n") {
		outputLine = strings.TrimSpace(outputLine)
		if outputLine == "" {
			continue
		}
		fields := strings.SplitN(outputLine, "\t", 4)
		if len(fields) < 4 {
			continue
		}
		running := strings.HasPrefix(strings.ToLower(strings.TrimSpace(fields[3])), "up")
		containers = append(containers, DiscoveredContainer{
			Name:    strings.TrimSpace(fields[0]),
			ID:      strings.TrimSpace(fields[1]),
			Image:   strings.TrimSpace(fields[2]),
			Running: running,
			Status:  strings.TrimSpace(fields[3]),
		})
	}
	return containers
}

// DiscoverAllContainerStats queries nerdctl/docker stats for all running
// containers and returns their resource usage. This reads from the real
// cgroup metrics, bypassing agent tracking.
func (containerRuntime *ContainerRuntime) DiscoverAllContainerStats(ctx context.Context) ([]ContainerResourceUsage, error) {
	statsContext, cancelStats := context.WithTimeout(ctx, nerdctlCommandTimeout)
	defer cancelStats()

	statsCommand := containerRuntime.buildExecCommand(statsContext, "stats", "--no-stream",
		"--format", "{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}")
	var commandOutput bytes.Buffer
	statsCommand.Stdout = &commandOutput
	var stderrOutput bytes.Buffer
	statsCommand.Stderr = &stderrOutput
	if runError := statsCommand.Run(); runError != nil {
		return nil, fmt.Errorf("querying container stats: %v: %s", runError, stderrOutput.String())
	}
	return parseContainerStatsOutput(commandOutput.String()), nil
}

// parseContainerStatsOutput parses tab-separated nerdctl/docker stats output
// into ContainerResourceUsage entries. Each line has the format:
// Name\tCPUPerc\tMemUsage (e.g. "web-abc\t1.23%\t45.6MiB / 7.8GiB")
func parseContainerStatsOutput(output string) []ContainerResourceUsage {
	var usages []ContainerResourceUsage
	for _, outputLine := range strings.Split(output, "\n") {
		outputLine = strings.TrimSpace(outputLine)
		if outputLine == "" {
			continue
		}
		fields := strings.SplitN(outputLine, "\t", 3)
		if len(fields) < 3 {
			continue
		}

		var cpuMillicores int64
		cpuPercent := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(fields[1]), "%"))
		if parsedCPU, parseError := parseFloat64Safe(cpuPercent); parseError == nil {
			cpuMillicores = int64(parsedCPU * 10)
		}

		memoryParts := strings.SplitN(strings.TrimSpace(fields[2]), " / ", 2)
		memoryBytes := parseMemoryValue(strings.TrimSpace(memoryParts[0]))

		usages = append(usages, ContainerResourceUsage{
			Name:          strings.TrimSpace(fields[0]),
			CPUMillicores: cpuMillicores,
			MemoryBytes:   memoryBytes,
		})
	}
	return usages
}

// PullImage pulls a container image from a registry by shelling out to
// nerdctl/docker pull. Blocks until the pull completes or the context
// is cancelled. The agent should set a timeout on the context (default 5m).
// When credentials is non-empty, it is passed as --creds user:pass to nerdctl.
func (containerRuntime *ContainerRuntime) PullImage(ctx context.Context, imageReference string, credentials string) error {
	if !validImageReferencePattern.MatchString(imageReference) {
		return fmt.Errorf("invalid image reference: %q", imageReference)
	}
	pullArgs := []string{"pull"}
	if credentials != "" {
		pullArgs = append(pullArgs, "--creds", credentials)
	}
	pullArgs = append(pullArgs, imageReference)
	pullCommand := containerRuntime.buildExecCommand(ctx, pullArgs...)
	var stderrOutput bytes.Buffer
	pullCommand.Stderr = &stderrOutput
	if pullError := pullCommand.Run(); pullError != nil {
		return fmt.Errorf("pulling image %s: %v: %s", imageReference, pullError, stderrOutput.String())
	}
	return nil
}

// IsImageCached checks whether a container image exists in the local store.
func (containerRuntime *ContainerRuntime) IsImageCached(ctx context.Context, imageReference string) bool {
	inspectCommand := containerRuntime.buildExecCommand(ctx, "image", "inspect", imageReference)
	inspectCommand.Stdout = nil
	inspectCommand.Stderr = nil
	return inspectCommand.Run() == nil
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
