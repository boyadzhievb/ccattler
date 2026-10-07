// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package loadtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/chaos"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// convergenceTestNodeCount is the number of simulated nodes in the
	// transaction budgeting regression test.
	convergenceTestNodeCount = 50

	// convergenceTestServiceCount is the number of services deployed.
	convergenceTestServiceCount = 10

	// convergenceTestInstancesPerService is the number of instances per service.
	convergenceTestInstancesPerService = 100

	// convergenceTestTotalInstances is the total number of instances.
	convergenceTestTotalInstances = convergenceTestServiceCount * convergenceTestInstancesPerService

	// convergenceTestNodesToKill is the number of nodes killed to trigger mass
	// failure — roughly 100 affected instances.
	convergenceTestNodesToKill = 5

	// convergenceTestDeployTimeout is how long to wait for the initial deploy.
	convergenceTestDeployTimeout = 120 * time.Second

	// convergenceTestRecoveryTimeout is how long to wait for full recovery after
	// node failures. Must be long enough for multiple batched reconciliation
	// cycles to converge all replacements.
	convergenceTestRecoveryTimeout = 300 * time.Second

	// convergenceTestMinimumDeadline is the minimum test binary timeout needed.
	convergenceTestMinimumDeadline = 7 * time.Minute
)

// TestTransactionBudgetingConvergence is the regression test for M70. It
// verifies that 50 nodes / 1000 instances / 5 failed nodes recovers fully
// with the 128-op transaction limit enforced by MemoryStore. KillNode
// injects failure state directly (node unreachable + instances failed)
// to avoid reliance on lease expiry, which is unreliable in single-process
// MemoryStore testing due to mutex contention starving agent heartbeats.
func TestTransactionBudgetingConvergence(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping convergence regression test in short mode")
	}
	if deadline, hasDeadline := testHandle.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining < convergenceTestMinimumDeadline {
			testHandle.Skipf("skipping convergence test: %v remaining, need at least %v",
				remaining.Round(time.Second), convergenceTestMinimumDeadline)
		}
	}

	nodeIDs := buildNodeIDs(convergenceTestNodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	factStore.SetWatchChannelBufferSize(4096)

	cluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	cluster.SetAgentInterval(200 * time.Millisecond)
	cluster.SetControllerDebounce(100 * time.Millisecond)
	cluster.SetMaxReconciliationAttempts(10)
	cluster.SetMaxInputKeyGuards(0)
	cluster.SetLeaseTimeout(5 * time.Minute)
	cluster.Start(ctx)

	// ── Phase 1: Deploy services and wait for convergence ───────────────
	testHandle.Logf("Phase 1: deploying %d services × %d instances = %d total",
		convergenceTestServiceCount, convergenceTestInstancesPerService, convergenceTestTotalInstances)

	for serviceIndex := 0; serviceIndex < convergenceTestServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), convergenceTestInstancesPerService)
	}

	deployDuration, deployConverged, deployStatus := waitForConvergenceWithProgress(
		testHandle, ctx, cluster, factStore, convergenceTestDeployTimeout)
	if !deployConverged {
		testHandle.Fatalf("Phase 1 FAILED — initial deploy did not converge within %v: %s",
			convergenceTestDeployTimeout, deployStatus)
	}
	testHandle.Logf("Phase 1 PASS — %d instances converged in %v", convergenceTestTotalInstances, deployDuration)

	// ── Phase 2: Kill nodes (KillNode injects failure state directly) ───
	testHandle.Logf("Phase 2: killing %d nodes (failure state injected by KillNode)",
		convergenceTestNodesToKill)

	for killIndex := 0; killIndex < convergenceTestNodesToKill; killIndex++ {
		cluster.KillNode(nodeIDs[killIndex])
	}

	testHandle.Log("Phase 2: waiting for batched recovery...")

	// Build killed-node set for convergence checking.
	killedNodeIDs := make(map[string]bool, convergenceTestNodesToKill)
	for killIndex := 0; killIndex < convergenceTestNodesToKill; killIndex++ {
		killedNodeIDs[nodeIDs[killIndex]] = true
	}

	// Wait for BOTH conditions: 1000 running instances AND 0 on killed nodes.
	// The dying agent goroutines may write stale "running" states after KillNode
	// cancels their context, so we must wait for the NodeFailureController to
	// re-mark those instances and the system to fully re-converge.
	recoveryStart := time.Now()
	recoveryDeadline := recoveryStart.Add(convergenceTestRecoveryTimeout)
	recovered := false
	var lastStatus string
	for time.Now().Before(recoveryDeadline) {
		converged, status := cluster.CheckConvergence(ctx)
		lastStatus = status

		if converged {
			nodeCounts, nodeCountError := countInstancesPerNode(ctx, factStore)
			if nodeCountError == nil {
				staleCount := 0
				for killedNodeID := range killedNodeIDs {
					staleCount += nodeCounts[killedNodeID]
				}
				if staleCount == 0 {
					recovered = true
					break
				}
			}
		}

		stateCounts, _ := countInstancesByState(ctx, factStore)
		elapsed := time.Since(recoveryStart).Truncate(time.Second)
		testHandle.Logf("  recovery at %v: %s | states: %v", elapsed, status, stateCounts)
		time.Sleep(5 * time.Second)
	}
	recoveryDuration := time.Since(recoveryStart)

	if !recovered {
		stateCounts, _ := countInstancesByState(ctx, factStore)
		nodeCounts, _ := countInstancesPerNode(ctx, factStore)
		staleDetail := ""
		for killedNodeID := range killedNodeIDs {
			if count := nodeCounts[killedNodeID]; count > 0 {
				staleDetail += fmt.Sprintf(" %s=%d", killedNodeID, count)
			}
		}
		testHandle.Fatalf("Phase 2 FAILED — did not recover within %v: %s | states: %v | stale on killed:%s",
			convergenceTestRecoveryTimeout, lastStatus, stateCounts, staleDetail)
	}
	testHandle.Logf("Phase 2 PASS — recovered from %d node failures in %v", convergenceTestNodesToKill, recoveryDuration)

	// ── Phase 3: Verify exact convergence (not near-convergence) ────────
	testHandle.Log("Phase 3: verifying exact convergence (100% running)")
	stateCounts, countError := countInstancesByState(ctx, factStore)
	if countError != nil {
		testHandle.Fatalf("Phase 3 FAILED — could not count instances: %v", countError)
	}

	runningCount := stateCounts[types.InstanceRunning]
	if runningCount != convergenceTestTotalInstances {
		testHandle.Fatalf("Phase 3 FAILED — expected %d running instances, got %d (states: %v)",
			convergenceTestTotalInstances, runningCount, stateCounts)
	}
	testHandle.Logf("Phase 3 PASS — all %d instances running", runningCount)

	// ── Phase 4: Verify no instances remain on killed nodes ─────────────
	testHandle.Log("Phase 4: verifying killed nodes have no running instances")
	nodeCounts, nodeCountError := countInstancesPerNode(ctx, factStore)
	if nodeCountError != nil {
		testHandle.Fatalf("Phase 4 FAILED — could not count per-node instances: %v", nodeCountError)
	}
	for killedNodeID := range killedNodeIDs {
		if runningOnKilled := nodeCounts[killedNodeID]; runningOnKilled > 0 {
			testHandle.Errorf("Phase 4 FAILED — killed node %s still has %d running instances",
				killedNodeID, runningOnKilled)
		}
	}

	// ── Summary ─────────────────────────────────────────────────────────
	testHandle.Log("─── Transaction Budgeting Convergence Test Summary ───")
	testHandle.Logf("  Nodes:              %d", convergenceTestNodeCount)
	testHandle.Logf("  Total instances:    %d", convergenceTestTotalInstances)
	testHandle.Logf("  Nodes killed:       %d", convergenceTestNodesToKill)
	testHandle.Logf("  Deploy time:        %v", deployDuration)
	testHandle.Logf("  Recovery time:      %v", recoveryDuration)
	testHandle.Logf("  Transaction limit:  128 ops (enforced by MemoryStore)")
	testHandle.Logf("  Final state:        %v", stateCounts)
}
