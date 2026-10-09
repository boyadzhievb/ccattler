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
	// heartbeatStressNodeCount is the number of simulated nodes. Matches
	// the convergence test to exercise the same scale.
	heartbeatStressNodeCount = 50

	// heartbeatStressServiceCount is the number of services deployed.
	heartbeatStressServiceCount = 5

	// heartbeatStressInstancesPerService keeps the workload moderate so
	// reconciliation contention does not mask the heartbeat signal.
	heartbeatStressInstancesPerService = 20

	// heartbeatStressTotalInstances is the expected running count.
	heartbeatStressTotalInstances = heartbeatStressServiceCount * heartbeatStressInstancesPerService

	// heartbeatStressReconcileDelay is injected into each agent's runtime
	// to simulate slow container operations (200ms per Start/List call).
	heartbeatStressReconcileDelay = 200 * time.Millisecond

	// heartbeatStressHeartbeatInterval is the production default.
	heartbeatStressHeartbeatInterval = 5 * time.Second

	// heartbeatStressLeaseTimeout is the production default for the
	// NodeFailureController.
	heartbeatStressLeaseTimeout = 30 * time.Second

	// heartbeatStressDeployTimeout is how long to wait for initial convergence.
	heartbeatStressDeployTimeout = 120 * time.Second

	// heartbeatStressSteadyStateDuration is how long to observe the cluster
	// under load after initial convergence, checking for false failures.
	heartbeatStressSteadyStateDuration = 60 * time.Second

	// heartbeatStressMinimumDeadline is the minimum remaining time the test
	// binary must have before starting.
	heartbeatStressMinimumDeadline = 5 * time.Minute
)

// TestHeartbeatIndependenceUnderLoad verifies that the independent heartbeat
// goroutine keeps nodes alive even when reconciliation cycles are slow. With
// 200ms+ artificial latency per agent operation and production-default
// heartbeat/lease settings (5s / 30s), zero nodes should transition to
// unreachable during normal operation.
//
// This is the production-config heartbeat stress test from M76 Fix 5.
func TestHeartbeatIndependenceUnderLoad(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping heartbeat stress test in short mode")
	}
	if deadline, hasDeadline := testHandle.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining < heartbeatStressMinimumDeadline {
			testHandle.Skipf("skipping heartbeat stress test: %v remaining, need at least %v",
				remaining.Round(time.Second), heartbeatStressMinimumDeadline)
		}
	}

	nodeIDs := buildNodeIDs(heartbeatStressNodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	factStore.SetWatchChannelBufferSize(4096)

	cluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	cluster.SetAgentInterval(1 * time.Second)
	cluster.SetControllerDebounce(500 * time.Millisecond)
	cluster.SetLeaseTimeout(heartbeatStressLeaseTimeout)
	cluster.SetAgentReconcileDelay(heartbeatStressReconcileDelay)
	cluster.Start(ctx)

	// ── Phase 1: Deploy services and wait for convergence ───────────────
	testHandle.Logf("Phase 1: deploying %d services × %d instances = %d total (with %v reconcile delay per agent)",
		heartbeatStressServiceCount, heartbeatStressInstancesPerService,
		heartbeatStressTotalInstances, heartbeatStressReconcileDelay)

	for serviceIndex := 0; serviceIndex < heartbeatStressServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("hb-svc-%02d", serviceIndex)
		cluster.DeployService(ctx, serviceName, fmt.Sprintf("hb-app:v%d", serviceIndex), heartbeatStressInstancesPerService)
	}

	deployDuration, deployConverged, deployStatus := waitForNearConvergence(
		testHandle, ctx, cluster, factStore,
		heartbeatStressDeployTimeout, heartbeatStressTotalInstances, 5.0)
	if !deployConverged {
		testHandle.Fatalf("Phase 1 FAILED — initial deploy did not converge within %v: %s",
			heartbeatStressDeployTimeout, deployStatus)
	}
	testHandle.Logf("Phase 1 PASS — converged in %v", deployDuration)

	// ── Phase 2: Steady-state observation ───────────────────────────────
	testHandle.Logf("Phase 2: observing cluster for %v under slow reconciliation", heartbeatStressSteadyStateDuration)

	falseFailureCount := 0
	observationEnd := time.Now().Add(heartbeatStressSteadyStateDuration)
	checkInterval := 5 * time.Second

	for time.Now().Before(observationEnd) {
		nodeStates, scanError := factStore.Scan(ctx, types.ScanObservedNodes)
		if scanError != nil {
			testHandle.Fatalf("failed to scan node states: %v", scanError)
		}

		for _, nodeFact := range nodeStates {
			if !isNodeStateFact(nodeFact.Key) {
				continue
			}
			nodeState := types.NodeState(nodeFact.Value)
			if nodeState == types.NodeUnreachable {
				nodeID := extractNodeIDFromStateFact(nodeFact.Key)
				if cluster.IsNodeAlive(nodeID) {
					falseFailureCount++
					testHandle.Logf("  FALSE FAILURE: node %s marked unreachable while agent is alive", nodeID)
				}
			}
		}

		time.Sleep(checkInterval)
	}

	if falseFailureCount > 0 {
		testHandle.Fatalf("Phase 2 FAILED — %d false node failures detected during steady state", falseFailureCount)
	}
	testHandle.Log("Phase 2 PASS — zero false node failures during observation period")

	// ── Phase 3: Kill nodes via context cancel (full heartbeat pipeline) ─
	testHandle.Log("Phase 3: killing 3 nodes via context cancel to exercise heartbeat→lease-expiry→failure pipeline")

	killedNodeIDs := make(map[string]bool)
	for killIndex := 0; killIndex < 3; killIndex++ {
		nodeID := nodeIDs[killIndex]
		cluster.KillNode(nodeID)
		killedNodeIDs[nodeID] = true
	}

	// Wait for the NodeFailureController to detect the missing heartbeats.
	failureDetected := false
	failureDeadline := time.Now().Add(heartbeatStressLeaseTimeout + 30*time.Second)
	for time.Now().Before(failureDeadline) {
		allDetected := true
		for killedNodeID := range killedNodeIDs {
			stateFact, getError := factStore.Get(ctx, types.KeyObservedNodeState(killedNodeID))
			if getError != nil || types.NodeState(stateFact.Value) != types.NodeUnreachable {
				allDetected = false
				break
			}
		}
		if allDetected {
			failureDetected = true
			break
		}
		time.Sleep(2 * time.Second)
	}

	if !failureDetected {
		testHandle.Fatal("Phase 3 FAILED — killed nodes were not detected as unreachable within lease timeout")
	}
	testHandle.Log("Phase 3 PASS — killed nodes correctly detected as unreachable")

	// ── Summary ─────────────────────────────────────────────────────────
	testHandle.Log("─── Heartbeat Independence Stress Test Summary ───")
	testHandle.Logf("  Nodes:              %d", heartbeatStressNodeCount)
	testHandle.Logf("  Total instances:    %d", heartbeatStressTotalInstances)
	testHandle.Logf("  Reconcile delay:    %v per agent operation", heartbeatStressReconcileDelay)
	testHandle.Logf("  Heartbeat interval: %v (production default)", heartbeatStressHeartbeatInterval)
	testHandle.Logf("  Lease timeout:      %v (production default)", heartbeatStressLeaseTimeout)
	testHandle.Logf("  False failures:     %d", falseFailureCount)
}

// isNodeStateFact returns true if the key is an observed node state fact.
func isNodeStateFact(key string) bool {
	prefix := types.PrefixObserved + "/node/"
	if len(key) <= len(prefix) {
		return false
	}
	rest := key[len(prefix):]
	return len(rest) > 6 && rest[len(rest)-6:] == "/state"
}

// extractNodeIDFromStateFact extracts the node ID from an observed/node/{id}/state key.
func extractNodeIDFromStateFact(key string) string {
	prefix := types.PrefixObserved + "/node/"
	rest := key[len(prefix):]
	return rest[:len(rest)-6]
}
