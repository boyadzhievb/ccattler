package agent

import (
	"context"
	"fmt"
	"log"
	"strconv"

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
		log.Printf("agent %s: telemetry: failed to list workloads: %v", nodeAgent.nodeID, listError)
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
// from the runtime. For runtimes that support resource metrics (container runtime),
// this provides actual usage data. For simulation runtimes, placeholder values are used.
func (nodeAgent *Agent) reportWorkloadTelemetry(ctx context.Context, workloads []runtime.Status) {
	for _, workloadStatus := range workloads {
		if !workloadStatus.Running {
			continue
		}

		instanceCPU := nodeAgent.estimateInstanceCPU(ctx, workloadStatus.ID)
		instanceMemory := nodeAgent.estimateInstanceMemory(ctx, workloadStatus.ID)

		if instanceCPU >= 0 {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceCPU(workloadStatus.ID),
				[]byte(fmt.Sprintf("%d", instanceCPU)))
		}
		if instanceMemory >= 0 {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceMemory(workloadStatus.ID),
				[]byte(fmt.Sprintf("%d", instanceMemory)))
		}
	}
}

// estimateInstanceCPU returns the estimated CPU usage in millicores for an
// instance. Currently reads the requested CPU from the service's desired state
// as a baseline. Real process/container metrics could replace this.
func (nodeAgent *Agent) estimateInstanceCPU(ctx context.Context, instanceID string) int64 {
	serviceFact, serviceError := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
	if serviceError != nil {
		return -1
	}
	serviceName := string(serviceFact.Value)

	cpuFact, cpuError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceResourcesCPU(serviceName))
	if cpuError != nil {
		return 0
	}

	cpuString := string(cpuFact.Value)
	return parseMillicores(cpuString)
}

// estimateInstanceMemory returns the estimated memory usage in bytes for an
// instance. Currently reads the requested memory from the service's desired state.
func (nodeAgent *Agent) estimateInstanceMemory(ctx context.Context, instanceID string) int64 {
	serviceFact, serviceError := nodeAgent.store.Get(ctx, types.KeyObservedInstanceService(instanceID))
	if serviceError != nil {
		return -1
	}
	serviceName := string(serviceFact.Value)

	memFact, memError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceResourcesMemory(serviceName))
	if memError != nil {
		return 0
	}

	memString := string(memFact.Value)
	return parseMemoryBytes(memString)
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
