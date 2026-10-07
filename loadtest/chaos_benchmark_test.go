// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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
	// benchmarkChaosInterval is the minimum time between consecutive injections.
	benchmarkChaosInterval = 3 * time.Second
	// benchmarkMinimumRecoveryRate is the minimum acceptable recovery success rate.
	benchmarkMinimumRecoveryRate = 80.0
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

// deployTimeoutForScale returns a deploy convergence timeout proportional to
// cluster size. Small clusters (<=20 nodes) converge in 120s; medium (<=100)
// get 300s; large clusters get 1800s to match the regular load test timeouts.
func deployTimeoutForScale(nodeCount int) time.Duration {
	switch {
	case nodeCount <= 20:
		return 120 * time.Second
	case nodeCount <= 100:
		return 300 * time.Second
	default:
		return 1800 * time.Second
	}
}

// runChaosBenchmark runs a full chaos benchmark at the given scale and returns
// a RecoveryReport with aggregate metrics. The test deploys workloads, waits
// for convergence, runs chaos injections, and computes recovery statistics.
func runChaosBenchmark(testHandle *testing.T, nodeCount int, serviceCount int, instancesPerService int) chaos.RecoveryReport {
	testHandle.Helper()
	totalInstances := serviceCount * instancesPerService

	deployTimeout := deployTimeoutForScale(nodeCount)
	clusterTuning := scaleConfigForNodeCount(nodeCount)
	minimumDeadline := deployTimeout + clusterTuning.chaosDuration + benchmarkMinTestDeadline

	if deadline, hasDeadline := testHandle.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining < minimumDeadline {
			testHandle.Skipf("skipping chaos benchmark: %v remaining, need at least %v",
				remaining.Round(time.Second), minimumDeadline)
		}
	}

	nodeIDs := buildNodeIDs(nodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	contextTimeout := deployTimeout + clusterTuning.chaosDuration + 5*time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()

	factStore.SetWatchChannelBufferSize(4096)

	cluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	clusterConfig := configureClusterForScale(cluster, nodeCount)
	cluster.Start(ctx)

	// Phase 1: Deploy services and measure convergence time.
	testHandle.Logf("deploying %d services × %d instances = %d total on %d nodes (timeout %v)",
		serviceCount, instancesPerService, totalInstances, nodeCount, deployTimeout)

	for serviceIndex := 0; serviceIndex < serviceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), instancesPerService)
	}

	convergenceThreshold := 95.0
	if nodeCount > 100 {
		convergenceThreshold = 85.0
	}
	deployDuration, converged, deployStatus := waitForNearConvergence(
		testHandle, ctx, cluster, factStore, deployTimeout, totalInstances, convergenceThreshold)
	if !converged {
		testHandle.Fatalf("initial deployment did not converge within %v: %s", deployTimeout, deployStatus)
	}
	testHandle.Logf("deployment converged in %v", deployDuration)

	// Phase 2: Run chaos injections.
	perInjectionTimeout := clusterConfig.leaseTimeout + clusterConfig.convergenceHeadroom
	testHandle.Logf("starting chaos injections (per-injection timeout %v = %v lease + %v headroom)",
		perInjectionTimeout, clusterConfig.leaseTimeout, clusterConfig.convergenceHeadroom)

	chaosConfig := chaos.ChaosConfig{
		Duration:           clusterConfig.chaosDuration,
		InjectionInterval:  benchmarkChaosInterval,
		ConvergenceTimeout: perInjectionTimeout,
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

// scaleConfig holds per-tier tuning parameters for the simulated cluster.
type scaleConfig struct {
	agentInterval        time.Duration
	controllerDebounce   time.Duration
	maxReconcileAttempts int
	leaseTimeout         time.Duration
	convergenceHeadroom  time.Duration
	chaosDuration        time.Duration
}

// scaleConfigForNodeCount returns tuning parameters appropriate for the given
// cluster size. The convergenceHeadroom is the time needed beyond the lease
// timeout for rescheduling to complete after a failure injection.
func scaleConfigForNodeCount(nodeCount int) scaleConfig {
	switch {
	case nodeCount <= 20:
		return scaleConfig{
			agentInterval:        100 * time.Millisecond,
			controllerDebounce:   50 * time.Millisecond,
			maxReconcileAttempts: 5,
			leaseTimeout:         5 * time.Second,
			convergenceHeadroom:  10 * time.Second,
			chaosDuration:        120 * time.Second,
		}
	case nodeCount <= 100:
		return scaleConfig{
			agentInterval:        200 * time.Millisecond,
			controllerDebounce:   100 * time.Millisecond,
			maxReconcileAttempts: 15,
			leaseTimeout:         5 * time.Minute,
			convergenceHeadroom:  15 * time.Second,
			chaosDuration:        270 * time.Second,
		}
	default:
		return scaleConfig{
			agentInterval:        500 * time.Millisecond,
			controllerDebounce:   200 * time.Millisecond,
			maxReconcileAttempts: 20,
			leaseTimeout:         60 * time.Second,
			convergenceHeadroom:  30 * time.Second,
			chaosDuration:        480 * time.Second,
		}
	}
}

// configureClusterForScale adjusts agent and controller tuning parameters
// based on cluster size. Larger clusters need longer intervals to avoid
// excessive store contention.
func configureClusterForScale(cluster *chaos.SimulatedChaosCluster, nodeCount int) scaleConfig {
	config := scaleConfigForNodeCount(nodeCount)
	cluster.SetAgentInterval(config.agentInterval)
	cluster.SetControllerDebounce(config.controllerDebounce)
	cluster.SetMaxReconciliationAttempts(config.maxReconcileAttempts)
	cluster.SetLeaseTimeout(config.leaseTimeout)
	cluster.SetMaxInputKeyGuards(0)
	return config
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
