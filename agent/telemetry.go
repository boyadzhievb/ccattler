package agent

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// NodeReporter encapsulates all telemetry and reporting logic for a node agent.
// It collects resource utilization data for the node and its workloads, publishes
// heartbeats and alive state, and writes observed facts back to the store.
type NodeReporter struct {
	nodeID           string             // nodeID is the unique identifier for the node this reporter manages.
	factStore        store.StateStore   // factStore is the fact store used to write observed telemetry and state facts.
	runtimeAdapter   runtime.Runtime    // runtimeAdapter is the pluggable container/process runtime adapter for querying workload stats.
	advertiseAddress string             // advertiseAddress is this node's LAN-routable IP published for cross-host communication.
}

// NewNodeReporter creates a new NodeReporter wired to the provided state store
// and runtime adapter for the given node.
func NewNodeReporter(nodeID string, factStore store.StateStore, runtimeAdapter runtime.Runtime, advertiseAddress string) *NodeReporter {
	return &NodeReporter{
		nodeID:           nodeID,
		factStore:        factStore,
		runtimeAdapter:   runtimeAdapter,
		advertiseAddress: advertiseAddress,
	}
}

// WriteHeartbeat writes the current Unix-millisecond timestamp to the node's
// lease key in the store. This acts as the node's heartbeat signal — if the
// agent disappears, the lease eventually expires and the control plane derives
// the node as unreachable.
func (nodeReporter *NodeReporter) WriteHeartbeat(ctx context.Context) {
	timestampMillis := fmt.Sprintf("%d", time.Now().UnixMilli())
	nodeReporter.factStore.Put(ctx, types.KeyLeaseNode(nodeReporter.nodeID), []byte(timestampMillis))
}

// PublishAliveState writes the NodeAlive state to the store, indicating that
// this node is healthy and ready to accept workloads.
func (nodeReporter *NodeReporter) PublishAliveState(ctx context.Context) {
	nodeReporter.factStore.Put(ctx, types.KeyObservedNodeState(nodeReporter.nodeID), []byte(string(types.NodeAlive)))
}

// PublishAdvertiseAddress writes this node's advertise address to the store if
// one is configured. Other nodes use this address for cross-host data plane
// communication.
func (nodeReporter *NodeReporter) PublishAdvertiseAddress(ctx context.Context) {
	if nodeReporter.advertiseAddress != "" {
		nodeReporter.factStore.Put(ctx, types.KeyObservedNodeAddress(nodeReporter.nodeID), []byte(nodeReporter.advertiseAddress))
	}
}

// RegisterNode writes the initial node registration fact to the store, making
// the node visible to the control plane's scheduling and controller logic.
func (nodeReporter *NodeReporter) RegisterNode(ctx context.Context) {
	nodeReporter.factStore.Put(ctx, types.KeyObservedNode(nodeReporter.nodeID), []byte(""))
}

// CollectAndReportTelemetry gathers resource utilization data for the node
// and its workloads, then writes the results as observed facts in the store.
// This powers `cca top nodes` and `cca top workloads` without requiring an
// external metrics server.
func (nodeReporter *NodeReporter) CollectAndReportTelemetry(ctx context.Context) {
	workloads, listError := nodeReporter.runtimeAdapter.List(ctx)
	if listError != nil {
		logging.Default().Error("telemetry: failed to list workloads", "agent", nodeReporter.nodeID, "error", listError.Error())
		return
	}

	runningCount := 0
	for _, workloadStatus := range workloads {
		if workloadStatus.Running {
			runningCount++
		}
	}

	nodeReporter.factStore.Put(ctx, types.KeyObservedNodeWorkloadCount(nodeReporter.nodeID), []byte(strconv.Itoa(runningCount)))

	nodeReporter.reportWorkloadTelemetry(ctx, workloads)
}

// reportWorkloadTelemetry collects and reports per-workload CPU and memory usage
// from the runtime's Stats() API. This queries actual resource consumption from
// the underlying runtime (cgroup stats for containers, /proc for processes,
// synthetic values for the simulator).
func (nodeReporter *NodeReporter) reportWorkloadTelemetry(ctx context.Context, workloads []runtime.Status) {
	for _, workloadStatus := range workloads {
		if !workloadStatus.Running {
			continue
		}

		resourceStats, statsError := nodeReporter.runtimeAdapter.Stats(ctx, workloadStatus.ID)
		if statsError != nil {
			continue
		}

		nodeReporter.factStore.Put(ctx, types.KeyObservedInstanceCPU(workloadStatus.ID),
			[]byte(fmt.Sprintf("%d", resourceStats.CPUMillicores)))
		nodeReporter.factStore.Put(ctx, types.KeyObservedInstanceMemory(workloadStatus.ID),
			[]byte(fmt.Sprintf("%d", resourceStats.MemoryBytes)))
	}
}

// lookupServiceResourcesFromStore reads the desired CPU and memory resource
// requests for a service from the store and returns them as parsed values
// (millicores and bytes). Returns zero for either value if not configured.
func (nodeReporter *NodeReporter) lookupServiceResourcesFromStore(ctx context.Context, serviceName string) (int64, int64) {
	var cpuMillicores int64
	var memoryBytes int64

	cpuFact, cpuError := nodeReporter.factStore.Get(ctx, types.KeyDesiredServiceResourcesCPU(serviceName))
	if cpuError == nil {
		cpuMillicores = parseMillicores(string(cpuFact.Value))
	}

	memFact, memError := nodeReporter.factStore.Get(ctx, types.KeyDesiredServiceResourcesMemory(serviceName))
	if memError == nil {
		memoryBytes = parseMemoryBytes(string(memFact.Value))
	}

	return cpuMillicores, memoryBytes
}

// parseMillicores parses a Kubernetes-style CPU value (e.g. "500m") to millicores.
func parseMillicores(cpuString string) int64 {
	if len(cpuString) == 0 {
		return 0
	}
	if cpuString[len(cpuString)-1] == 'm' {
		milliValue, parseError := strconv.ParseInt(cpuString[:len(cpuString)-1], 10, 64)
		if parseError == nil {
			return milliValue
		}
	}
	coreValue, parseError := strconv.ParseFloat(cpuString, 64)
	if parseError == nil {
		return int64(coreValue * 1000)
	}
	return 0
}

// parseMemoryBytes parses a Kubernetes-style memory value (e.g. "512Mi") to bytes.
func parseMemoryBytes(memString string) int64 {
	if len(memString) < 2 {
		parsedValue, parseError := strconv.ParseInt(memString, 10, 64)
		if parseError == nil {
			return parsedValue
		}
		return 0
	}

	suffix := memString[len(memString)-2:]
	numPart := memString[:len(memString)-2]

	var multiplier int64
	switch suffix {
	case "Ki":
		multiplier = 1024
	case "Mi":
		multiplier = 1024 * 1024
	case "Gi":
		multiplier = 1024 * 1024 * 1024
	case "Ti":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		parsedValue, parseError := strconv.ParseInt(memString, 10, 64)
		if parseError == nil {
			return parsedValue
		}
		return 0
	}

	parsedValue, parseError := strconv.ParseInt(numPart, 10, 64)
	if parseError != nil {
		return 0
	}
	return parsedValue * multiplier
}
