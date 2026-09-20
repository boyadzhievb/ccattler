package agent

import (
	"context"
	"fmt"
	"strconv"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/types"
)

// lookupServiceResourcesFromStore reads the desired CPU and memory resource
// requests for a service from the store and returns them as parsed values
// (millicores and bytes). Returns zero for either value if not configured.
func (nodeAgent *Agent) lookupServiceResourcesFromStore(ctx context.Context, serviceName string) (int64, int64) {
	var cpuMillicores int64
	var memoryBytes int64

	cpuFact, cpuError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceResourcesCPU(serviceName))
	if cpuError == nil {
		cpuMillicores = parseMillicores(string(cpuFact.Value))
	}

	memFact, memError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceResourcesMemory(serviceName))
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

// collectAndReportNodeTelemetry gathers resource utilization data for the node
// and its workloads, then writes the results as observed facts in the store.
// This powers `cca top nodes` and `cca top workloads` without requiring an
// external metrics server.
func (nodeAgent *Agent) collectAndReportNodeTelemetry(ctx context.Context) {
	workloads, listError := nodeAgent.runtime.List(ctx)
	if listError != nil {
		logging.Default().Error("telemetry: failed to list workloads", "agent", nodeAgent.nodeID, "error", listError.Error())
		return
	}

	runningCount := 0
	for _, workloadStatus := range workloads {
		if workloadStatus.Running {
			runningCount++
		}
	}

	nodeAgent.store.Put(ctx, types.KeyObservedNodeWorkloadCount(nodeAgent.nodeID), []byte(strconv.Itoa(runningCount)))

	nodeAgent.reportWorkloadTelemetry(ctx, workloads)
}

// reportWorkloadTelemetry collects and reports per-workload CPU and memory usage
// from the runtime's Stats() API. This queries actual resource consumption from
// the underlying runtime (cgroup stats for containers, /proc for processes,
// synthetic values for the simulator).
func (nodeAgent *Agent) reportWorkloadTelemetry(ctx context.Context, workloads []runtime.Status) {
	for _, workloadStatus := range workloads {
		if !workloadStatus.Running {
			continue
		}

		resourceStats, statsError := nodeAgent.runtime.Stats(ctx, workloadStatus.ID)
		if statsError != nil {
			continue
		}

		nodeAgent.store.Put(ctx, types.KeyObservedInstanceCPU(workloadStatus.ID),
			[]byte(fmt.Sprintf("%d", resourceStats.CPUMillicores)))
		nodeAgent.store.Put(ctx, types.KeyObservedInstanceMemory(workloadStatus.ID),
			[]byte(fmt.Sprintf("%d", resourceStats.MemoryBytes)))
	}
}
