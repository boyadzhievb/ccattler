package agent

import (
	"context"
	"fmt"
	"strconv"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/types"
)

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
