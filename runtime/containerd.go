// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	cgroupstats "github.com/containerd/cgroups/v3/cgroup2/stats"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/typeurl/v2"
)

// containerdNamespace is the containerd namespace used for all CCattler workloads.
const containerdNamespace = "ccattler"

// defaultContainerdSocketPath is the default path to the containerd UNIX socket.
const defaultContainerdSocketPath = "/run/containerd/containerd.sock"

// containerLabelInstanceID is the containerd label key for the CCattler instance ID.
const containerLabelInstanceID = "ccattler.instance.id"

// containerLabelServiceName is the containerd label key for the CCattler service name.
const containerLabelServiceName = "ccattler.service.name"

// ErrNotImplemented is returned by stub methods that have not yet been
// migrated from the nerdctl-based ContainerRuntime.
var ErrNotImplemented = errors.New("not implemented in containerd runtime")

// ContainerdRuntime runs workloads as OCI containers via the containerd gRPC
// API, bypassing the nerdctl CLI entirely. It implements the same Runtime
// interface as ContainerRuntime but communicates with containerd directly.
type ContainerdRuntime struct {
	mutex             sync.Mutex             // mutex guards concurrent access to state maps.
	client            *containerd.Client     // client is the containerd gRPC client.
	trackedContainers map[string]bool        // trackedContainers maps workload IDs to their running state.
	instanceHostPorts map[string]map[int]int // instanceHostPorts maps workload IDs to containerPort→hostPort mappings.
	networkName       string                 // networkName is the CNI network for container IP assignment.
	networkCIDR       string                 // networkCIDR is the subnet CIDR for the container network.
	dnsAddress        string                 // dnsAddress is the DNS server address for containers.
	dnsSearchDomain   string                 // dnsSearchDomain is the search domain for containers.
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
		client:            containerdClient,
		trackedContainers: make(map[string]bool),
		instanceHostPorts: make(map[string]map[int]int),
		dnsSearchDomain:   defaultDNSSearchDomain,
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

	return Status{
		ID:       id,
		Running:  taskStatus.Status == containerd.Running,
		PID:      int(task.Pid()),
		ExitCode: int(taskStatus.ExitStatus),
	}, nil
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

// Start creates and starts a container from the given spec.
func (containerdRuntime *ContainerdRuntime) Start(_ context.Context, _ Spec) error {
	return ErrNotImplemented
}

// Stop terminates the container identified by id.
func (containerdRuntime *ContainerdRuntime) Stop(_ context.Context, _ string) error {
	return ErrNotImplemented
}

// --- Runtime interface: Phase E (exec & logs) ---

// Exec runs a command inside a running container.
func (containerdRuntime *ContainerdRuntime) Exec(_ context.Context, _ string, _ ExecSpec) error {
	return ErrNotImplemented
}

// ExecCapture runs a command inside a running container and captures output.
func (containerdRuntime *ContainerdRuntime) ExecCapture(_ context.Context, _ string, _ ExecSpec) ([]byte, error) {
	return nil, ErrNotImplemented
}

// ExecInit runs an initialization command in a temporary container.
func (containerdRuntime *ContainerdRuntime) ExecInit(_ context.Context, _ string, _ ExecSpec) error {
	return ErrNotImplemented
}

// Logs returns a reader that streams the stdout/stderr output of a container.
func (containerdRuntime *ContainerdRuntime) Logs(_ context.Context, _ string, _ LogsOptions) (io.ReadCloser, error) {
	return nil, ErrNotImplemented
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

// PullImage pulls a container image into the containerd store.
func (containerdRuntime *ContainerdRuntime) PullImage(_ context.Context, _ string, _ string) error {
	return ErrNotImplemented
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
