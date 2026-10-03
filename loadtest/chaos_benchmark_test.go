package loadtest

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/chaos"
	"github.com/boyadzhievb/ccattler/store"
)

const (
	// benchmarkChaosDuration is the total time chaos events are injected.
	benchmarkChaosDuration = 60 * time.Second
	// benchmarkChaosInterval is the minimum time between consecutive injections.
	benchmarkChaosInterval = 3 * time.Second
	// benchmarkChaosConvergenceTimeout is the per-injection convergence timeout.
	benchmarkChaosConvergenceTimeout = 30 * time.Second
	// benchmarkMinimumRecoveryRate is the minimum acceptable recovery success rate.
	benchmarkMinimumRecoveryRate = 80.0
	// benchmarkDeployTimeout is how long initial deployment may take.
	benchmarkDeployTimeout = 120 * time.Second
	// benchmarkMinTestDeadline is the minimum time the test binary must have remaining.
	benchmarkMinTestDeadline = 3 * time.Minute
)

// benchmarkChaosScenarios defines the failure types used in benchmark runs.
var benchmarkChaosScenarios = []chaos.FailureScenario{
	chaos.ScenarioNodeKill,
	chaos.ScenarioNodePartition,
	chaos.ScenarioControllerRestart,
	chaos.ScenarioScaleChange,
	chaos.ScenarioNodeRecovery,
}

// runChaosBenchmark runs a full chaos benchmark at the given scale and returns
// a RecoveryReport with aggregate metrics. The test deploys workloads, waits
// for convergence, runs chaos injections, and computes recovery statistics.
func runChaosBenchmark(testHandle *testing.T, nodeCount int, serviceCount int, instancesPerService int) chaos.RecoveryReport {
	testHandle.Helper()
	totalInstances := serviceCount * instancesPerService

	if deadline, hasDeadline := testHandle.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining < benchmarkMinTestDeadline {
			testHandle.Skipf("skipping chaos benchmark: %v remaining, need at least %v",
				remaining.Round(time.Second), benchmarkMinTestDeadline)
		}
	}

	nodeIDs := buildNodeIDs(nodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	factStore.SetWatchChannelBufferSize(4096)

	cluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	configureClusterForScale(cluster, nodeCount)
	cluster.Start(ctx)

	// Phase 1: Deploy services and measure convergence time.
	testHandle.Logf("deploying %d services × %d instances = %d total on %d nodes",
		serviceCount, instancesPerService, totalInstances, nodeCount)

	for serviceIndex := 0; serviceIndex < serviceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), instancesPerService)
	}

	deployDuration, converged, deployStatus := waitForConvergenceWithProgress(
		testHandle, ctx, cluster, factStore, benchmarkDeployTimeout)
	if !converged {
		testHandle.Fatalf("initial deployment did not converge within %v: %s", benchmarkDeployTimeout, deployStatus)
	}
	testHandle.Logf("deployment converged in %v", deployDuration)

	// Phase 2: Run chaos injections.
	testHandle.Log("starting chaos injections")

	chaosConfig := chaos.ChaosConfig{
		Duration:           benchmarkChaosDuration,
		InjectionInterval:  benchmarkChaosInterval,
		ConvergenceTimeout: benchmarkChaosConvergenceTimeout,
		EnabledScenarios:   benchmarkChaosScenarios,
		RandSource:         rand.New(rand.NewSource(42)), //nolint:gosec // deterministic seed for reproducibility
	}

	chaosRunner := chaos.NewChaosRunner(chaosConfig, cluster)
	chaosRunner.SetEventCallback(func(event chaos.ChaosEvent) {
		convergenceLabel := "TIMEOUT"
		if event.Converged {
			convergenceLabel = fmt.Sprintf("OK in %v", event.ConvergenceTime.Round(time.Millisecond))
		}
		testHandle.Logf("  [%v] %-20s target=%-14s %s",
			event.Elapsed.Round(time.Second), event.Scenario, event.Target, convergenceLabel)
	})

	chaosEvents := chaosRunner.Run(ctx)

	// Phase 3: Compute metrics and build report.
	finalConverged, finalStatus := cluster.CheckConvergence(ctx)
	recoveryMetrics := chaos.ComputeRecoveryMetrics(chaosEvents)

	report := chaos.RecoveryReport{
		NodeCount:             nodeCount,
		ServiceCount:          serviceCount,
		TotalInstances:        totalInstances,
		DeployConvergenceTime: deployDuration,
		ChaosEvents:           chaosEvents,
		Metrics:               recoveryMetrics,
		FinalConverged:        finalConverged,
		FinalStatus:           finalStatus,
	}

	testHandle.Logf("chaos benchmark results: injections=%d recovered=%d/%d (%.1f%%) mean=%v p50=%v p95=%v p99=%v max=%v",
		recoveryMetrics.TotalInjections,
		recoveryMetrics.SuccessfulRecoveries, recoveryMetrics.TotalInjections,
		recoveryMetrics.RecoverySuccessRate,
		recoveryMetrics.MeanConvergenceTime.Round(time.Millisecond),
		recoveryMetrics.P50ConvergenceTime.Round(time.Millisecond),
		recoveryMetrics.P95ConvergenceTime.Round(time.Millisecond),
		recoveryMetrics.P99ConvergenceTime.Round(time.Millisecond),
		recoveryMetrics.MaxConvergenceTime.Round(time.Millisecond))

	if !finalConverged {
		testHandle.Logf("WARNING: cluster did not reconverge after chaos: %s", finalStatus)
	}

	return report
}

// configureClusterForScale adjusts agent and controller tuning parameters
// based on cluster size. Larger clusters need longer intervals to avoid
// excessive store contention.
func configureClusterForScale(cluster *chaos.SimulatedChaosCluster, nodeCount int) {
	switch {
	case nodeCount <= 20:
		cluster.SetAgentInterval(100 * time.Millisecond)
		cluster.SetControllerDebounce(50 * time.Millisecond)
		cluster.SetMaxReconciliationAttempts(5)
		cluster.SetLeaseTimeout(5 * time.Second)
	case nodeCount <= 100:
		cluster.SetAgentInterval(200 * time.Millisecond)
		cluster.SetControllerDebounce(100 * time.Millisecond)
		cluster.SetMaxReconciliationAttempts(10)
		cluster.SetLeaseTimeout(30 * time.Second)
	default:
		cluster.SetAgentInterval(500 * time.Millisecond)
		cluster.SetControllerDebounce(200 * time.Millisecond)
		cluster.SetMaxReconciliationAttempts(15)
		cluster.SetLeaseTimeout(60 * time.Second)
	}
	cluster.SetMaxInputKeyGuards(0)
}

// TestChaosBenchmark_100Workloads runs a chaos benchmark at small scale:
// 10 nodes, 10 services, 10 instances each (100 total). This validates the
// chaos benchmark infrastructure and provides a quick smoke test of recovery.
func TestChaosBenchmark_100Workloads(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping chaos benchmark in short mode")
	}
	report := runChaosBenchmark(testHandle, 10, 10, 10)

	if report.Metrics.RecoverySuccessRate < benchmarkMinimumRecoveryRate {
		testHandle.Fatalf("recovery rate %.1f%% below minimum %.1f%%",
			report.Metrics.RecoverySuccessRate, benchmarkMinimumRecoveryRate)
	}
}

// TestChaosBenchmark_1000Workloads runs a chaos benchmark at medium scale:
// 50 nodes, 10 services, 100 instances each (1000 total). This matches the
// existing load test scale but adds structured chaos injection and recovery
// metrics reporting.
func TestChaosBenchmark_1000Workloads(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping chaos benchmark in short mode")
	}
	report := runChaosBenchmark(testHandle, 50, 10, 100)

	if report.Metrics.RecoverySuccessRate < benchmarkMinimumRecoveryRate {
		testHandle.Fatalf("recovery rate %.1f%% below minimum %.1f%%",
			report.Metrics.RecoverySuccessRate, benchmarkMinimumRecoveryRate)
	}
}

// TestChaosBenchmark_5000Workloads runs a chaos benchmark at large scale:
// 200 nodes, 10 services, 500 instances each (5000 total). This tests
// scheduler and controller performance at a scale where contention and
// convergence latency become visible.
func TestChaosBenchmark_5000Workloads(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping chaos benchmark in short mode")
	}
	report := runChaosBenchmark(testHandle, 200, 10, 500)

	if report.Metrics.RecoverySuccessRate < benchmarkMinimumRecoveryRate {
		testHandle.Fatalf("recovery rate %.1f%% below minimum %.1f%%",
			report.Metrics.RecoverySuccessRate, benchmarkMinimumRecoveryRate)
	}
}
