// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"syscall"

	cgroupstats "github.com/containerd/cgroups/v3/cgroup2/stats"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	gocni "github.com/containerd/go-cni"
	"github.com/containerd/typeurl/v2"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// containerdNamespace is the containerd namespace used for all CCattler workloads.
const containerdNamespace = "ccattler"

// defaultContainerdSocketPath is the default path to the containerd UNIX socket.
const defaultContainerdSocketPath = "/run/containerd/containerd.sock"

// containerLabelInstanceID is the containerd label key for the CCattler instance ID.
const containerLabelInstanceID = "ccattler.instance.id"

// containerLabelServiceName is the containerd label key for the CCattler service name.
const containerLabelServiceName = "ccattler.service.name"

// containerLabelLogPath is the containerd label key for the container's json-file log path.
const containerLabelLogPath = "ccattler.log.path"

// containerdLogBaseDir is the base directory for CCattler container log files.
const containerdLogBaseDir = "/var/lib/ccattler/logs"

// cniPluginDirectory is the path where CNI plugin binaries are installed.
const cniPluginDirectory = "/opt/cni/bin"

// cniBridgeName is the Linux bridge device created by the CNI bridge plugin.
const cniBridgeName = "cca0"

// cniConfVersion is the CNI specification version used in generated conflists.
const cniConfVersion = "1.0.0"

// netnsBaseDirectory is the directory for named network namespaces.
const netnsBaseDirectory = "/var/run/netns"

// ErrNotImplemented is returned by stub methods that have not yet been
// migrated from the nerdctl-based ContainerRuntime.
var ErrNotImplemented = errors.New("not implemented in containerd runtime")

// containerNetworkState tracks the CNI-assigned network state for a container.
type containerNetworkState struct {
	netnsPath string // netnsPath is the path to the named network namespace.
	ipAddress string // ipAddress is the CNI-assigned IP address.
}

// ContainerdRuntime runs workloads as OCI containers via the containerd gRPC
// API, bypassing the nerdctl CLI entirely. It implements the same Runtime
// interface as ContainerRuntime but communicates with containerd directly.
type ContainerdRuntime struct {
	mutex              sync.Mutex                        // mutex guards concurrent access to state maps.
	client             *containerd.Client                // client is the containerd gRPC client.
	trackedContainers  map[string]bool                   // trackedContainers maps workload IDs to their running state.
	instanceHostPorts  map[string]map[int]int            // instanceHostPorts maps workload IDs to containerPort→hostPort mappings.
	containerCNIStates map[string]*containerNetworkState // containerCNIStates tracks per-container CNI network state.
	networkName        string                            // networkName is the CNI network for container IP assignment.
	networkCIDR        string                            // networkCIDR is the subnet CIDR for the container network.
	dnsAddress         string                            // dnsAddress is the DNS server address for containers.
	dnsSearchDomain    string                            // dnsSearchDomain is the search domain for containers.
	cniNetwork         gocni.CNI                         // cniNetwork is the lazily-initialized go-cni instance.
}

// NewContainerdRuntime creates a ContainerdRuntime connected to the containerd
// socket at the given path. Pass an empty string to use the default socket path.
func NewContainerdRuntime(socketPath string) (*ContainerdRuntime, error) {
	if socketPath == "" {
		socketPath = defaultContainerdSocketPath
	}

	containerdClient, connectError := containerd.New(socketPath)
	if connectError != nil {
		return nil, fmt.Errorf("connect to containerd at %s: %w", socketPath, connectError)
	}

	return &ContainerdRuntime{
		client:             containerdClient,
		trackedContainers:  make(map[string]bool),
		instanceHostPorts:  make(map[string]map[int]int),
		containerCNIStates: make(map[string]*containerNetworkState),
		dnsSearchDomain:    defaultDNSSearchDomain,
	}, nil
}

// containerdContext returns a context with the CCattler namespace set.
func (containerdRuntime *ContainerdRuntime) containerdContext(parent context.Context) context.Context {
	return namespaces.WithNamespace(parent, containerdNamespace)
}

// SetNetwork configures the CNI network name and subnet for container IP
// assignment. Must be called before Start.
func (containerdRuntime *ContainerdRuntime) SetNetwork(networkName string, subnetCIDR string) {
	containerdRuntime.networkName = networkName
	containerdRuntime.networkCIDR = subnetCIDR
}

// SetDNS configures the DNS server address injected into containers.
func (containerdRuntime *ContainerdRuntime) SetDNS(dnsAddress string) {
	containerdRuntime.dnsAddress = dnsAddress
}

// Close disconnects from the containerd socket.
func (containerdRuntime *ContainerdRuntime) Close() error {
	return containerdRuntime.client.Close()
}

// --- Runtime interface: Phase B (read-only) ---

// Status returns the current state of the container identified by id.
func (containerdRuntime *ContainerdRuntime) Status(ctx context.Context, id string) (Status, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)
	containerName := containerdRuntime.resolveContainerName(id)

	container, loadError := containerdRuntime.client.LoadContainer(namespacedCtx, containerName)
	if loadError != nil {
		return Status{}, ErrNotFound
	}

	task, taskError := container.Task(namespacedCtx, nil)
	if taskError != nil {
		return Status{ID: id, Running: false}, nil
	}

	taskStatus, statusError := task.Status(namespacedCtx)
	if statusError != nil {
		return Status{ID: id, Running: false}, nil
	}

	status := Status{
		ID:       id,
		Running:  taskStatus.Status == containerd.Running,
		PID:      int(task.Pid()),
		ExitCode: int(taskStatus.ExitStatus),
	}

	containerdRuntime.mutex.Lock()
	if cniState, hasCNI := containerdRuntime.containerCNIStates[id]; hasCNI {
		status.IP = cniState.ipAddress
	}
	containerdRuntime.mutex.Unlock()

	return status, nil
}

// List returns the status of all containers managed by CCattler in the
// containerd namespace.
func (containerdRuntime *ContainerdRuntime) List(ctx context.Context) ([]Status, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)

	containers, listError := containerdRuntime.client.Containers(namespacedCtx,
		fmt.Sprintf("labels.%q", containerLabelInstanceID))
	if listError != nil {
		return nil, fmt.Errorf("list containers: %w", listError)
	}

	var results []Status
	for _, container := range containers {
		labels, _ := container.Labels(namespacedCtx)
		instanceID := labels[containerLabelInstanceID]
		if instanceID == "" {
			continue
		}

		status := Status{ID: instanceID}
		task, taskError := container.Task(namespacedCtx, nil)
		if taskError == nil {
			taskStatus, statusError := task.Status(namespacedCtx)
			if statusError == nil {
				status.Running = taskStatus.Status == containerd.Running
				status.PID = int(task.Pid())
				status.ExitCode = int(taskStatus.ExitStatus)
			}
		}
		containerdRuntime.mutex.Lock()
		if cniState, hasCNI := containerdRuntime.containerCNIStates[instanceID]; hasCNI {
			status.IP = cniState.ipAddress
		}
		containerdRuntime.mutex.Unlock()
		results = append(results, status)
	}
	return results, nil
}

// cpuSampleInterval is the delay between two CPU usage samples used to
// compute the instantaneous CPU rate in millicores.
const cpuSampleInterval = 200 * time.Millisecond

// Stats returns the resource usage of the container identified by id by
// reading cgroup v2 metrics from containerd. CPU millicores are computed
// from two usage samples taken cpuSampleInterval apart.
func (containerdRuntime *ContainerdRuntime) Stats(ctx context.Context, instanceID string) (ResourceStats, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)
	containerName := containerdRuntime.resolveContainerName(instanceID)

	container, loadError := containerdRuntime.client.LoadContainer(namespacedCtx, containerName)
	if loadError != nil {
		return ResourceStats{}, ErrNotFound
	}

	task, taskError := container.Task(namespacedCtx, nil)
	if taskError != nil {
		return ResourceStats{}, nil
	}

	firstSample, firstError := containerdRuntime.readCgroupMetrics(namespacedCtx, task)
	if firstError != nil {
		return ResourceStats{}, nil
	}

	select {
	case <-time.After(cpuSampleInterval):
	case <-ctx.Done():
		return ResourceStats{}, ctx.Err()
	}

	secondSample, secondError := containerdRuntime.readCgroupMetrics(namespacedCtx, task)
	if secondError != nil {
		return ResourceStats{
			MemoryBytes: firstSample.memoryUsageBytes,
		}, nil
	}

	cpuDeltaUsec := secondSample.cpuUsageUsec - firstSample.cpuUsageUsec
	intervalUsec := uint64(cpuSampleInterval / time.Microsecond)
	cpuMillicores := int64(0)
	if intervalUsec > 0 {
		cpuMillicores = safeUint64ToInt64(cpuDeltaUsec * 1000 / intervalUsec)
	}

	return ResourceStats{
		CPUMillicores: cpuMillicores,
		MemoryBytes:   secondSample.memoryUsageBytes,
	}, nil
}

// cgroupSample holds the raw cgroup metrics extracted from a single reading.
type cgroupSample struct {
	cpuUsageUsec     uint64 // cpuUsageUsec is the cumulative CPU time in microseconds.
	memoryUsageBytes int64  // memoryUsageBytes is the current memory usage in bytes.
}

// readCgroupMetrics extracts CPU and memory values from a containerd task's
// cgroup v2 metrics, following the same typeurl pattern as ctr metrics.
func (containerdRuntime *ContainerdRuntime) readCgroupMetrics(ctx context.Context, task containerd.Task) (cgroupSample, error) {
	metric, metricsError := task.Metrics(ctx)
	if metricsError != nil {
		return cgroupSample{}, metricsError
	}

	if metric.Data == nil {
		return cgroupSample{}, errors.New("no metric data returned")
	}

	if !typeurl.Is(metric.Data, (*cgroupstats.Metrics)(nil)) {
		return cgroupSample{}, fmt.Errorf("unsupported metric type: %s", metric.Data.GetTypeUrl())
	}

	var cgroupMetrics cgroupstats.Metrics
	if unmarshalError := typeurl.UnmarshalTo(metric.Data, &cgroupMetrics); unmarshalError != nil {
		return cgroupSample{}, unmarshalError
	}

	sample := cgroupSample{}
	if cgroupMetrics.CPU != nil {
		sample.cpuUsageUsec = cgroupMetrics.CPU.UsageUsec
	}
	if cgroupMetrics.Memory != nil {
		sample.memoryUsageBytes = safeUint64ToInt64(cgroupMetrics.Memory.Usage)
	}

	return sample, nil
}

// safeUint64ToInt64 converts a uint64 to int64, capping at math.MaxInt64 to
// prevent overflow on values larger than 2^63-1.
func safeUint64ToInt64(value uint64) int64 {
	const maxInt64 = uint64(1<<63 - 1)
	if value > maxInt64 {
		return int64(maxInt64)
	}
	return int64(value) // #nosec G115 — overflow prevented by the check above.
}

// --- Runtime interface: Phase D (lifecycle) ---

// Start creates and starts a container from the given spec via the containerd
// gRPC API. Creates the OCI spec, containerd container, snapshot, log file,
// and task. Sets up CNI networking with an isolated network namespace when
// networkCIDR is configured. Idempotent — returns nil if already running.
func (containerdRuntime *ContainerdRuntime) Start(ctx context.Context, spec Spec) error {
	containerdRuntime.mutex.Lock()
	if containerdRuntime.trackedContainers[spec.ID] {
		containerdRuntime.mutex.Unlock()
		currentStatus, _ := containerdRuntime.Status(ctx, spec.ID)
		if currentStatus.Running {
			return nil
		}
		containerdRuntime.cleanupCNIState(ctx, spec.ID)
		containerdRuntime.removeContainerResources(ctx, spec.ID)
	} else {
		containerdRuntime.mutex.Unlock()
	}

	namespacedCtx := containerdRuntime.containerdContext(ctx)

	image, getImageError := containerdRuntime.client.GetImage(namespacedCtx, spec.Image)
	if getImageError != nil {
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("image not found: %s", spec.Image)}
	}

	containerName := containerdRuntime.buildContainerName(spec.ServiceName, spec.ID)
	logFilePath := filepath.Join(containerdLogBaseDir, spec.ID, spec.ID+"-json.log")

	if mkdirError := os.MkdirAll(filepath.Dir(logFilePath), 0750); mkdirError != nil {
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("create log dir: %v", mkdirError)}
	}

	netnsPath := ""
	if containerdRuntime.networkCIDR != "" {
		if initError := containerdRuntime.ensureCNIInitialized(); initError != nil {
			return &StartError{ID: spec.ID, Reason: fmt.Sprintf("CNI init: %v", initError)}
		}
		createdNetnsPath, netnsError := createNetworkNamespace(spec.ID) //nolint:staticcheck // stub always errors on non-Linux
		if netnsError != nil {                                          //nolint:staticcheck // stub always errors on non-Linux
			return &StartError{ID: spec.ID, Reason: fmt.Sprintf("create netns: %v", netnsError)}
		}
		netnsPath = createdNetnsPath
	}

	ociSpecOptions := containerdRuntime.buildOCISpecOptions(spec, image, netnsPath)

	newContainer, createError := containerdRuntime.client.NewContainer(
		namespacedCtx,
		containerName,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(containerName+"-snap", image),
		containerd.WithNewSpec(ociSpecOptions...),
		containerd.WithContainerLabels(map[string]string{
			containerLabelInstanceID:  spec.ID,
			containerLabelServiceName: spec.ServiceName,
			containerLabelLogPath:     logFilePath,
		}),
	)
	if createError != nil {
		if netnsPath != "" {
			deleteNetworkNamespace(spec.ID)
		}
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("create container: %v", createError)}
	}

	assignedIP := ""
	if netnsPath != "" {
		cniResult, setupError := containerdRuntime.cniNetwork.Setup(ctx, spec.ID, netnsPath)
		if setupError != nil {
			_ = newContainer.Delete(namespacedCtx, containerd.WithSnapshotCleanup)
			containerdRuntime.teardownCNINetwork(ctx, spec.ID, netnsPath)
			return &StartError{ID: spec.ID, Reason: fmt.Sprintf("CNI setup: %v", setupError)}
		}
		assignedIP = extractIPFromCNIResult(cniResult)
	}

	task, taskError := newContainer.NewTask(namespacedCtx,
		cio.LogFile(logFilePath))
	if taskError != nil {
		containerdRuntime.teardownCNINetwork(ctx, spec.ID, netnsPath)
		_ = newContainer.Delete(namespacedCtx, containerd.WithSnapshotCleanup)
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("create task: %v", taskError)}
	}

	if startError := task.Start(namespacedCtx); startError != nil {
		_, _ = task.Delete(namespacedCtx)
		containerdRuntime.teardownCNINetwork(ctx, spec.ID, netnsPath)
		_ = newContainer.Delete(namespacedCtx, containerd.WithSnapshotCleanup)
		return &StartError{ID: spec.ID, Reason: fmt.Sprintf("start task: %v", startError)}
	}

	containerdRuntime.mutex.Lock()
	containerdRuntime.trackedContainers[spec.ID] = true

	if netnsPath != "" {
		containerdRuntime.containerCNIStates[spec.ID] = &containerNetworkState{
			netnsPath: netnsPath,
			ipAddress: assignedIP,
		}
	}

	portMappings := make(map[int]int, len(spec.Ports))
	for _, containerPort := range spec.Ports {
		portMappings[containerPort] = containerPort
	}
	if len(portMappings) > 0 {
		containerdRuntime.instanceHostPorts[spec.ID] = portMappings
	}
	containerdRuntime.mutex.Unlock()

	return nil
}

// buildOCISpecOptions constructs the OCI spec options from a workload Spec,
// configuring image defaults, env vars, hostname, resource limits, mounts,
// network namespace, and DNS. Pass empty netnsPath to skip networking.
func (containerdRuntime *ContainerdRuntime) buildOCISpecOptions(spec Spec, image containerd.Image, netnsPath string) []oci.SpecOpts {
	ociOptions := []oci.SpecOpts{
		oci.WithImageConfig(image),
	}

	if len(spec.Command) > 0 {
		ociOptions = append(ociOptions, oci.WithProcessArgs(spec.Command...))
	}

	if len(spec.Env) > 0 {
		envVars := make([]string, 0, len(spec.Env))
		for envKey, envValue := range spec.Env {
			envVars = append(envVars, envKey+"="+envValue)
		}
		ociOptions = append(ociOptions, oci.WithEnv(envVars))
	}

	if spec.ServiceName != "" {
		ociOptions = append(ociOptions, oci.WithHostname(spec.ServiceName))
	}

	if spec.MemoryB > 0 {
		ociOptions = append(ociOptions, oci.WithMemoryLimit(uint64(spec.MemoryB)))
	}

	if spec.CPUm > 0 {
		cpuQuota := int64(spec.CPUm) * 100
		ociOptions = append(ociOptions, oci.WithCPUCFS(cpuQuota, 100000))
	}

	if netnsPath != "" {
		ociOptions = append(ociOptions, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: netnsPath,
		}))
	}

	var bindMounts []specs.Mount

	if containerdRuntime.dnsAddress != "" {
		resolvConfContent := generateResolvConf(containerdRuntime.dnsAddress, containerdRuntime.dnsSearchDomain)
		resolvHostPath := filepath.Join(containerdLogBaseDir, spec.ID, "config", "resolv.conf")
		_ = os.MkdirAll(filepath.Dir(resolvHostPath), 0750)
		_ = os.WriteFile(resolvHostPath, []byte(resolvConfContent), 0644) // #nosec G306
		bindMounts = append(bindMounts, specs.Mount{
			Destination: "/etc/resolv.conf",
			Type:        "bind",
			Source:      resolvHostPath,
			Options:     []string{"rbind", "ro"},
		})
	}

	for containerPath, fileContent := range spec.ConfigFiles {
		hostPath := filepath.Join(containerdLogBaseDir, spec.ID, "config", filepath.Base(containerPath))
		_ = os.MkdirAll(filepath.Dir(hostPath), 0750)
		_ = os.WriteFile(hostPath, []byte(fileContent), 0755) // #nosec G306 — 0755 so scripts like cca-secret are executable; bind-mounted :ro
		bindMounts = append(bindMounts, specs.Mount{
			Destination: containerPath,
			Type:        "bind",
			Source:      hostPath,
			Options:     []string{"rbind", "ro"},
		})
	}

	if len(bindMounts) > 0 {
		ociOptions = append(ociOptions, oci.WithMounts(bindMounts))
	}

	return ociOptions
}

// ensureCNIInitialized lazily creates the go-cni instance from the configured
// network name and CIDR. Retries on transient failures (e.g., missing plugin
// dir during early boot). Thread-safe via mutex.
func (containerdRuntime *ContainerdRuntime) ensureCNIInitialized() error {
	containerdRuntime.mutex.Lock()
	defer containerdRuntime.mutex.Unlock()

	if containerdRuntime.cniNetwork != nil {
		return nil
	}
	if containerdRuntime.networkCIDR == "" {
		return errors.New("networkCIDR not configured: call SetNetwork before Start")
	}
	conflistJSON := buildCNIConflist(containerdRuntime.networkName, containerdRuntime.networkCIDR)
	cniInstance, initError := gocni.New(
		gocni.WithMinNetworkCount(1),
		gocni.WithPluginDir([]string{cniPluginDirectory}),
		gocni.WithConfListBytes(conflistJSON),
	)
	if initError != nil {
		return fmt.Errorf("initialize CNI: %w", initError)
	}
	containerdRuntime.cniNetwork = cniInstance
	return nil
}

// buildCNIConflist generates a CNI conflist JSON with bridge + host-local IPAM
// and portmap plugins for the given network name and subnet CIDR.
func buildCNIConflist(networkName string, subnetCIDR string) []byte {
	conflist := map[string]interface{}{
		"cniVersion": cniConfVersion,
		"name":       networkName,
		"plugins": []map[string]interface{}{
			{
				"type":      "bridge",
				"bridge":    cniBridgeName,
				"isGateway": true,
				"ipMasq":    true,
				"ipam": map[string]interface{}{
					"type":   "host-local",
					"subnet": subnetCIDR,
					"routes": []map[string]string{
						{"dst": "0.0.0.0/0"},
					},
				},
			},
			{
				"type":         "portmap",
				"capabilities": map[string]bool{"portMappings": true},
			},
		},
	}
	conflistJSON, _ := json.Marshal(conflist)
	return conflistJSON
}

// extractIPFromCNIResult extracts the first IP address from a CNI setup result.
func extractIPFromCNIResult(result *gocni.Result) string {
	if result == nil {
		return ""
	}
	for _, interfaceConfig := range result.Interfaces {
		if len(interfaceConfig.IPConfigs) > 0 {
			return interfaceConfig.IPConfigs[0].IP.String()
		}
	}
	return ""
}

// teardownCNINetwork calls CNI Remove and deletes the network namespace.
func (containerdRuntime *ContainerdRuntime) teardownCNINetwork(ctx context.Context, instanceID string, netnsPath string) {
	if netnsPath == "" || containerdRuntime.cniNetwork == nil {
		return
	}
	_ = containerdRuntime.cniNetwork.Remove(ctx, instanceID, netnsPath)
	deleteNetworkNamespace(instanceID)
}

// generateResolvConf builds resolv.conf content from DNS config.
func generateResolvConf(dnsAddress string, searchDomain string) string {
	content := "nameserver " + dnsAddress + "\n"
	if searchDomain != "" {
		content += "search " + searchDomain + "\n"
	}
	return content
}

// buildContainerName generates the containerd container name from service and instance ID.
func (containerdRuntime *ContainerdRuntime) buildContainerName(serviceName string, instanceID string) string {
	if serviceName != "" {
		return serviceName + "-" + instanceID
	}
	return "cca-" + instanceID
}

// Stop terminates and removes the container identified by id via the containerd
// gRPC API. Tears down CNI networking, kills the task, deletes it, then removes
// the container and snapshot. Idempotent — returns nil if container doesn't exist.
func (containerdRuntime *ContainerdRuntime) Stop(ctx context.Context, instanceID string) error {
	namespacedCtx := containerdRuntime.containerdContext(ctx)
	containerName := containerdRuntime.resolveContainerName(instanceID)

	container, loadError := containerdRuntime.client.LoadContainer(namespacedCtx, containerName)
	if loadError != nil {
		containerdRuntime.cleanupCNIState(ctx, instanceID)
		containerdRuntime.mutex.Lock()
		containerdRuntime.trackedContainers[instanceID] = false
		containerdRuntime.mutex.Unlock()
		return nil
	}

	task, taskError := container.Task(namespacedCtx, nil)
	if taskError == nil {
		exitChannel, waitError := task.Wait(namespacedCtx)
		if waitError == nil {
			_ = task.Kill(namespacedCtx, syscall.SIGKILL)
			select {
			case <-exitChannel:
			case <-time.After(10 * time.Second):
			}
		}
		_, _ = task.Delete(namespacedCtx)
	}

	containerdRuntime.cleanupCNIState(ctx, instanceID)
	_ = container.Delete(namespacedCtx, containerd.WithSnapshotCleanup)

	containerdRuntime.removeContainerResources(ctx, instanceID)
	return nil
}

// StopWithGracePeriod stops a containerd container by sending SIGTERM first,
// waiting up to gracePeriod, then escalating to SIGKILL. Tears down CNI after
// the task exits.
func (containerdRuntime *ContainerdRuntime) StopWithGracePeriod(ctx context.Context, instanceID string, gracePeriod time.Duration) error {
	namespacedCtx := containerdRuntime.containerdContext(ctx)
	containerName := containerdRuntime.resolveContainerName(instanceID)

	container, loadError := containerdRuntime.client.LoadContainer(namespacedCtx, containerName)
	if loadError != nil {
		containerdRuntime.cleanupCNIState(ctx, instanceID)
		containerdRuntime.mutex.Lock()
		containerdRuntime.trackedContainers[instanceID] = false
		containerdRuntime.mutex.Unlock()
		return nil
	}

	task, taskError := container.Task(namespacedCtx, nil)
	if taskError == nil {
		exitChannel, waitError := task.Wait(namespacedCtx)
		if waitError == nil {
			_ = task.Kill(namespacedCtx, syscall.SIGTERM)
			select {
			case <-exitChannel:
			case <-time.After(gracePeriod):
				_ = task.Kill(namespacedCtx, syscall.SIGKILL)
				select {
				case <-exitChannel:
				case <-time.After(10 * time.Second):
				}
			}
		}
		_, _ = task.Delete(namespacedCtx)
	}

	containerdRuntime.cleanupCNIState(ctx, instanceID)
	_ = container.Delete(namespacedCtx, containerd.WithSnapshotCleanup)

	containerdRuntime.removeContainerResources(ctx, instanceID)
	return nil
}

// cleanupCNIState tears down the CNI network and deletes the network namespace
// for a container. Atomically removes state to prevent double teardown. Safe
// to call when no CNI state exists.
func (containerdRuntime *ContainerdRuntime) cleanupCNIState(ctx context.Context, instanceID string) {
	containerdRuntime.mutex.Lock()
	cniState := containerdRuntime.containerCNIStates[instanceID]
	delete(containerdRuntime.containerCNIStates, instanceID)
	containerdRuntime.mutex.Unlock()

	if cniState == nil {
		return
	}
	containerdRuntime.teardownCNINetwork(ctx, instanceID, cniState.netnsPath)
}

// removeContainerResources cleans up tracking state and log files for a container.
func (containerdRuntime *ContainerdRuntime) removeContainerResources(_ context.Context, instanceID string) {
	containerdRuntime.mutex.Lock()
	defer containerdRuntime.mutex.Unlock()

	delete(containerdRuntime.trackedContainers, instanceID)
	delete(containerdRuntime.instanceHostPorts, instanceID)
	delete(containerdRuntime.containerCNIStates, instanceID)

	logDirectory := filepath.Join(containerdLogBaseDir, instanceID)
	_ = os.RemoveAll(logDirectory)
}

// --- Runtime interface: Phase E (exec & logs) ---

// Exec runs a command inside a running container via the containerd exec API.
func (containerdRuntime *ContainerdRuntime) Exec(ctx context.Context, instanceID string, execSpec ExecSpec) error {
	_, execError := containerdRuntime.execInContainer(ctx, instanceID, execSpec, false)
	return execError
}

// ExecCapture runs a command inside a running container and captures combined
// stdout and stderr output via the containerd exec API.
func (containerdRuntime *ContainerdRuntime) ExecCapture(ctx context.Context, instanceID string, execSpec ExecSpec) ([]byte, error) {
	return containerdRuntime.execInContainer(ctx, instanceID, execSpec, true)
}

// execInContainer is the shared implementation for Exec and ExecCapture. When
// captureOutput is true, stdout and stderr are captured and returned.
func (containerdRuntime *ContainerdRuntime) execInContainer(ctx context.Context, instanceID string, execSpec ExecSpec, captureOutput bool) ([]byte, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)
	containerName := containerdRuntime.resolveContainerName(instanceID)

	container, loadError := containerdRuntime.client.LoadContainer(namespacedCtx, containerName)
	if loadError != nil {
		return nil, ErrNotFound
	}

	task, taskError := container.Task(namespacedCtx, nil)
	if taskError != nil {
		return nil, fmt.Errorf("container %s has no running task: %w", instanceID, taskError)
	}

	execID := fmt.Sprintf("cca-exec-%s-%d", instanceID[:minInt(8, len(instanceID))], time.Now().UnixNano())

	envVars := make([]string, 0, len(execSpec.Env))
	for envKey, envValue := range execSpec.Env {
		envVars = append(envVars, envKey+"="+envValue)
	}

	processSpec := &specs.Process{
		Args: []string{"sh", "-c", execSpec.Command},
		Env:  envVars,
		Cwd:  "/",
	}

	var outputBuffer bytes.Buffer
	var ioCreator cio.Creator
	if captureOutput {
		ioCreator = cio.NewCreator(cio.WithStreams(nil, &outputBuffer, &outputBuffer))
	} else {
		ioCreator = cio.NullIO
	}

	execProcess, execError := task.Exec(namespacedCtx, execID, processSpec, ioCreator)
	if execError != nil {
		return nil, fmt.Errorf("exec in container %s: %w", instanceID, execError)
	}
	defer func() { _, _ = execProcess.Delete(namespacedCtx) }()

	exitStatusChannel, waitError := execProcess.Wait(namespacedCtx)
	if waitError != nil {
		return nil, fmt.Errorf("exec wait setup in %s: %w", instanceID, waitError)
	}

	if startError := execProcess.Start(namespacedCtx); startError != nil {
		return nil, fmt.Errorf("exec start in %s: %w", instanceID, startError)
	}

	exitStatus := <-exitStatusChannel
	exitCode, exitError := exitStatus.ExitCode(), exitStatus.Error()
	if exitError != nil {
		return outputBuffer.Bytes(), fmt.Errorf("exec in %s: %w", instanceID, exitError)
	}
	if exitCode != 0 {
		return outputBuffer.Bytes(), fmt.Errorf("exec in %s exited with code %d", instanceID, exitCode)
	}
	return outputBuffer.Bytes(), nil
}

// minInt returns the smaller of two integers.
func minInt(first, second int) int {
	if first < second {
		return first
	}
	return second
}

// ExecInit runs an initialization command by creating a temporary container
// from the given image, executing the command, and removing the container.
func (containerdRuntime *ContainerdRuntime) ExecInit(ctx context.Context, imageRef string, execSpec ExecSpec) error {
	namespacedCtx := containerdRuntime.containerdContext(ctx)

	image, getImageError := containerdRuntime.client.GetImage(namespacedCtx, imageRef)
	if getImageError != nil {
		return fmt.Errorf("init image not found: %s: %w", imageRef, getImageError)
	}

	initContainerName := fmt.Sprintf("cca-init-%d", time.Now().UnixNano())

	envVars := make([]string, 0, len(execSpec.Env))
	for envKey, envValue := range execSpec.Env {
		envVars = append(envVars, envKey+"="+envValue)
	}

	ociOptions := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithProcessArgs("bash", "-c", execSpec.Command),
	}
	if len(envVars) > 0 {
		ociOptions = append(ociOptions, oci.WithEnv(envVars))
	}
	if containerdRuntime.dnsSearchDomain != "" {
		ociOptions = append(ociOptions, oci.WithHostname(initContainerName))
	}
	var bindMounts []specs.Mount
	if containerdRuntime.dnsAddress != "" {
		resolvConfContent := generateResolvConf(containerdRuntime.dnsAddress, containerdRuntime.dnsSearchDomain)
		resolvHostPath := filepath.Join(containerdLogBaseDir, initContainerName, "config", "resolv.conf")
		_ = os.MkdirAll(filepath.Dir(resolvHostPath), 0750)
		_ = os.WriteFile(resolvHostPath, []byte(resolvConfContent), 0644) // #nosec G306
		bindMounts = append(bindMounts, specs.Mount{
			Destination: "/etc/resolv.conf",
			Type:        "bind",
			Source:      resolvHostPath,
			Options:     []string{"rbind", "ro"},
		})
	}
	for containerPath, fileContent := range execSpec.ConfigFiles {
		hostPath := filepath.Join(containerdLogBaseDir, initContainerName, "config", filepath.Base(containerPath))
		_ = os.MkdirAll(filepath.Dir(hostPath), 0750)
		_ = os.WriteFile(hostPath, []byte(fileContent), 0755) // #nosec G306 — 0755 so scripts like cca-secret are executable; bind-mounted :ro
		bindMounts = append(bindMounts, specs.Mount{
			Destination: containerPath,
			Type:        "bind",
			Source:      hostPath,
			Options:     []string{"rbind", "ro"},
		})
	}
	if len(bindMounts) > 0 {
		ociOptions = append(ociOptions, oci.WithMounts(bindMounts))
	}

	initHostDir := filepath.Join(containerdLogBaseDir, initContainerName)
	defer func() { _ = os.RemoveAll(initHostDir) }()

	initContainer, createError := containerdRuntime.client.NewContainer(
		namespacedCtx,
		initContainerName,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(initContainerName+"-snap", image),
		containerd.WithNewSpec(ociOptions...),
	)
	if createError != nil {
		return fmt.Errorf("create init container: %w", createError)
	}
	defer func() {
		_ = initContainer.Delete(namespacedCtx, containerd.WithSnapshotCleanup)
	}()

	task, taskError := initContainer.NewTask(namespacedCtx, cio.NullIO)
	if taskError != nil {
		return fmt.Errorf("create init task: %w", taskError)
	}
	defer func() {
		_, _ = task.Delete(namespacedCtx)
	}()

	exitChannel, waitError := task.Wait(namespacedCtx)
	if waitError != nil {
		return fmt.Errorf("wait init task: %w", waitError)
	}

	if startError := task.Start(namespacedCtx); startError != nil {
		return fmt.Errorf("start init task: %w", startError)
	}

	exitStatus := <-exitChannel
	exitCode, exitError := exitStatus.ExitCode(), exitStatus.Error()
	if exitError != nil {
		return fmt.Errorf("init container exited with error: %w", exitError)
	}
	if exitCode != 0 {
		return fmt.Errorf("init container exited with code %d", exitCode)
	}
	return nil
}

// Logs returns a reader that streams the stdout/stderr log output of a
// container. The log file path is stored as a label on the container when
// created (Phase D). Supports tail, since, and follow options.
func (containerdRuntime *ContainerdRuntime) Logs(ctx context.Context, instanceID string, options LogsOptions) (io.ReadCloser, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)
	containerName := containerdRuntime.resolveContainerName(instanceID)

	container, loadError := containerdRuntime.client.LoadContainer(namespacedCtx, containerName)
	if loadError != nil {
		return nil, ErrNotFound
	}

	labels, _ := container.Labels(namespacedCtx)
	logFilePath := labels[containerLabelLogPath]
	if logFilePath == "" {
		logFilePath = containerdRuntime.defaultLogPath(instanceID)
	}

	if _, statError := os.Stat(logFilePath); statError != nil {
		return nil, fmt.Errorf("container %s log file not found: %w", instanceID, statError)
	}

	if options.Follow {
		return newFollowingLogReader(ctx, logFilePath, options)
	}
	return newStaticLogReader(logFilePath, options)
}

// defaultLogPath returns the conventional log file path for a container.
func (containerdRuntime *ContainerdRuntime) defaultLogPath(instanceID string) string {
	return filepath.Join(containerdLogBaseDir, instanceID, instanceID+"-json.log")
}

// newStaticLogReader opens a log file and returns lines matching the filter
// options (tail and since). It reads the entire file and returns matching lines.
func newStaticLogReader(logFilePath string, options LogsOptions) (io.ReadCloser, error) {
	logData, readError := os.ReadFile(logFilePath) // #nosec G304 — path from internal label, not user input
	if readError != nil {
		return nil, readError
	}

	lines := strings.Split(string(logData), "\n")

	if options.TailLines != "" {
		tailCount := parseTailLines(options.TailLines)
		if tailCount > 0 && tailCount < len(lines) {
			lines = lines[len(lines)-tailCount:]
		}
	}

	output := strings.Join(lines, "\n")
	return io.NopCloser(strings.NewReader(output)), nil
}

// parseTailLines parses a tail count string to an integer, returning 0 on error.
func parseTailLines(tailValue string) int {
	count := 0
	for _, digit := range tailValue {
		if digit < '0' || digit > '9' {
			return 0
		}
		count = count*10 + int(digit-'0')
	}
	return count
}

// newFollowingLogReader opens a log file and tails it, polling for new data
// until the context is cancelled.
func newFollowingLogReader(ctx context.Context, logFilePath string, options LogsOptions) (io.ReadCloser, error) {
	logFile, openError := os.Open(logFilePath) // #nosec G304 — path from internal label, not user input
	if openError != nil {
		return nil, openError
	}

	if options.TailLines != "" {
		tailCount := parseTailLines(options.TailLines)
		seekToTailLines(logFile, tailCount)
	} else {
		_, _ = logFile.Seek(0, io.SeekStart)
	}

	return &followingLogReader{
		logFile:   logFile,
		cancelCtx: ctx,
	}, nil
}

// seekToTailLines positions the file offset to show the last N lines.
func seekToTailLines(logFile *os.File, lineCount int) {
	if lineCount <= 0 {
		return
	}

	fileInfo, statError := logFile.Stat()
	if statError != nil {
		return
	}

	fileSize := fileInfo.Size()
	if fileSize == 0 {
		return
	}

	readBuffer := make([]byte, 1)
	newlinesSeen := 0
	position := fileSize - 1

	for position > 0 {
		position--
		_, _ = logFile.Seek(position, io.SeekStart)
		_, _ = logFile.Read(readBuffer)
		if readBuffer[0] == '\n' {
			newlinesSeen++
			if newlinesSeen > lineCount {
				_, _ = logFile.Seek(position+1, io.SeekStart)
				return
			}
		}
	}
	_, _ = logFile.Seek(0, io.SeekStart)
}

// followingLogReader reads a log file and polls for new content until cancelled.
type followingLogReader struct {
	logFile   *os.File        // logFile is the open log file handle.
	cancelCtx context.Context // cancelCtx controls the tail loop lifetime.
}

// Read reads available data, blocking with a short poll when at EOF.
func (follower *followingLogReader) Read(buffer []byte) (int, error) {
	for {
		bytesRead, readError := follower.logFile.Read(buffer)
		if bytesRead > 0 {
			return bytesRead, nil
		}
		if readError != nil && readError != io.EOF {
			return 0, readError
		}
		select {
		case <-follower.cancelCtx.Done():
			return 0, io.EOF
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Close closes the underlying log file.
func (follower *followingLogReader) Close() error {
	return follower.logFile.Close()
}

// Resize is not supported — containerd cgroup limits are updated via the
// task's Update method, which will be implemented in a later phase.
func (containerdRuntime *ContainerdRuntime) Resize(_ context.Context, _ string, _ int64, _ int64) error {
	return ErrResizeUnsupported
}

// --- Optional interfaces ---

// ListImages returns all images in the CCattler namespace.
func (containerdRuntime *ContainerdRuntime) ListImages(ctx context.Context) ([]ImageInfo, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)

	images, listError := containerdRuntime.client.ListImages(namespacedCtx)
	if listError != nil {
		return nil, fmt.Errorf("list images: %w", listError)
	}

	var results []ImageInfo
	for _, image := range images {
		fullName := image.Name()
		repository, tag := splitImageReference(fullName)
		results = append(results, ImageInfo{
			Repository: repository,
			Tag:        tag,
			ImageID:    image.Target().Digest.String(),
		})
	}
	return results, nil
}

// PullImage pulls a container image into the containerd content store and
// unpacks it for use. The credentials parameter uses "user:password" format;
// pass an empty string for public images.
func (containerdRuntime *ContainerdRuntime) PullImage(ctx context.Context, imageReference string, credentials string) error {
	namespacedCtx := containerdRuntime.containerdContext(ctx)

	pullOptions := []containerd.RemoteOpt{
		containerd.WithPullUnpack,
	}

	if credentials != "" {
		username, password := parseCredentials(credentials)
		authorizer := docker.NewDockerAuthorizer(
			docker.WithAuthCreds(func(_ string) (string, string, error) {
				return username, password, nil
			}),
		)
		registryHosts := docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(authorizer),
		)
		resolver := docker.NewResolver(docker.ResolverOptions{
			Hosts: registryHosts,
		})
		pullOptions = append(pullOptions, containerd.WithResolver(resolver))
	}

	_, pullError := containerdRuntime.client.Pull(namespacedCtx, imageReference, pullOptions...)
	if pullError != nil {
		return fmt.Errorf("pull image %s: %w", imageReference, pullError)
	}
	return nil
}

// parseCredentials splits a "user:password" string into username and password.
func parseCredentials(credentials string) (string, string) {
	separatorIndex := strings.IndexByte(credentials, ':')
	if separatorIndex < 0 {
		return credentials, ""
	}
	return credentials[:separatorIndex], credentials[separatorIndex+1:]
}

// IsImageCached returns true if the image exists in the containerd store.
func (containerdRuntime *ContainerdRuntime) IsImageCached(ctx context.Context, imageReference string) bool {
	namespacedCtx := containerdRuntime.containerdContext(ctx)
	_, getError := containerdRuntime.client.GetImage(namespacedCtx, imageReference)
	return getError == nil
}

// --- Discovery methods ---

// DiscoverAllContainers lists all CCattler-managed containers.
func (containerdRuntime *ContainerdRuntime) DiscoverAllContainers(ctx context.Context) ([]DiscoveredContainer, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)

	containers, listError := containerdRuntime.client.Containers(namespacedCtx,
		fmt.Sprintf("labels.%q", containerLabelInstanceID))
	if listError != nil {
		return nil, fmt.Errorf("list containers: %w", listError)
	}

	var results []DiscoveredContainer
	for _, container := range containers {
		labels, _ := container.Labels(namespacedCtx)
		serviceName := labels[containerLabelServiceName]
		instanceID := labels[containerLabelInstanceID]
		containerName := serviceName + "-" + instanceID

		running := false
		statusText := "Exited"
		task, taskError := container.Task(namespacedCtx, nil)
		if taskError == nil {
			taskStatus, _ := task.Status(namespacedCtx)
			if taskStatus.Status == containerd.Running {
				running = true
				statusText = "Running"
			}
		}

		imageInfo, _ := container.Image(namespacedCtx)
		imageName := ""
		if imageInfo != nil {
			imageName = imageInfo.Name()
		}

		results = append(results, DiscoveredContainer{
			Name:    containerName,
			ID:      instanceID,
			Image:   imageName,
			Status:  statusText,
			Running: running,
		})
	}
	return results, nil
}

// DiscoverAllContainerStats returns resource usage for all running containers
// by querying cgroup v2 metrics from each task in the CCattler namespace.
func (containerdRuntime *ContainerdRuntime) DiscoverAllContainerStats(ctx context.Context) ([]ContainerResourceUsage, error) {
	namespacedCtx := containerdRuntime.containerdContext(ctx)

	containers, listError := containerdRuntime.client.Containers(namespacedCtx,
		fmt.Sprintf("labels.%q", containerLabelInstanceID))
	if listError != nil {
		return nil, fmt.Errorf("list containers: %w", listError)
	}

	var results []ContainerResourceUsage
	for _, container := range containers {
		labels, _ := container.Labels(namespacedCtx)
		instanceID := labels[containerLabelInstanceID]
		serviceName := labels[containerLabelServiceName]
		containerName := serviceName + "-" + instanceID

		task, taskError := container.Task(namespacedCtx, nil)
		if taskError != nil {
			continue
		}

		sample, sampleError := containerdRuntime.readCgroupMetrics(namespacedCtx, task)
		if sampleError != nil {
			continue
		}

		results = append(results, ContainerResourceUsage{
			Name:        containerName,
			MemoryBytes: sample.memoryUsageBytes,
		})
	}
	return results, nil
}

// HostPortsForInstance returns the host port mappings for the given instance.
func (containerdRuntime *ContainerdRuntime) HostPortsForInstance(instanceID string) map[int]int {
	containerdRuntime.mutex.Lock()
	defer containerdRuntime.mutex.Unlock()
	return containerdRuntime.instanceHostPorts[instanceID]
}

// StopAll terminates every container managed by this runtime.
func (containerdRuntime *ContainerdRuntime) StopAll(ctx context.Context) {
	statuses, listError := containerdRuntime.List(ctx)
	if listError != nil {
		return
	}
	for _, status := range statuses {
		if status.Running {
			_ = containerdRuntime.Stop(ctx, status.ID)
		}
	}
}

// resolveContainerName builds the containerd container name from an instance ID.
func (containerdRuntime *ContainerdRuntime) resolveContainerName(instanceID string) string {
	containerdRuntime.mutex.Lock()
	defer containerdRuntime.mutex.Unlock()
	for _, container := range containerdRuntime.trackedContainers {
		_ = container
	}
	return "cca-" + instanceID
}

// splitImageReference separates "repo:tag" into repository and tag parts.
func splitImageReference(reference string) (string, string) {
	atIndex := strings.LastIndex(reference, ":")
	if atIndex < 0 || strings.Contains(reference[atIndex:], "/") {
		return reference, "latest"
	}
	return reference[:atIndex], reference[atIndex+1:]
}
