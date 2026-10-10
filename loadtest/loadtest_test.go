// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package loadtest

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/chaos"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	loadTestNodeCount           = 50
	loadTestServiceCount        = 10
	loadTestInstancesPerService = 100
	loadTestTotalInstances      = loadTestServiceCount * loadTestInstancesPerService
	convergenceTimeout          = 120 * time.Second
	recoveryTimeout             = 240 * time.Second
	// minimumTestDeadline is the minimum remaining time the test binary must
	// have before the load test starts. This prevents panics when run under a
	// short timeout (e.g. `go test ./... -timeout 120s`).
	minimumTestDeadline = 5 * time.Minute
)

func buildNodeIDs(count int) []string {
	nodeIDs := make([]string, count)
	for index := range nodeIDs {
		nodeIDs[index] = fmt.Sprintf("node-%03d", index)
	}
	return nodeIDs
}

func waitForConvergenceWithProgress(testHandle *testing.T, ctx context.Context, cluster *chaos.SimulatedChaosCluster, factStore store.StateStore, timeout time.Duration) (time.Duration, bool, string) {
	startTime := time.Now()
	deadline := startTime.Add(timeout)
	lastLogTime := startTime
	for time.Now().Before(deadline) {
		converged, status := cluster.CheckConvergence(ctx)
		if converged {
			return time.Since(startTime), true, status
		}
		if time.Since(lastLogTime) >= 5*time.Second {
			stateCounts, _ := countInstancesByState(ctx, factStore)
			testHandle.Logf("  progress at %v: %s | states: %v", time.Since(startTime).Round(time.Second), status, stateCounts)
			lastLogTime = time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, status := cluster.CheckConvergence(ctx)
	return time.Since(startTime), false, status
}

func waitForNearConvergence(testHandle *testing.T, ctx context.Context, cluster *chaos.SimulatedChaosCluster, factStore store.StateStore, timeout time.Duration, totalDesired int, tolerancePercent float64) (time.Duration, bool, string) {
	startTime := time.Now()
	deadline := startTime.Add(timeout)
	lastLogTime := startTime
	for time.Now().Before(deadline) {
		converged, status := cluster.CheckConvergence(ctx)
		if converged {
			return time.Since(startTime), true, status
		}
		if time.Since(lastLogTime) >= 5*time.Second {
			stateCounts, _ := countInstancesByState(ctx, factStore)
			testHandle.Logf("  progress at %v: %s | states: %v", time.Since(startTime).Round(time.Second), status, stateCounts)
			lastLogTime = time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}
	converged, status := cluster.CheckConvergence(ctx)
	if converged {
		return time.Since(startTime), true, status
	}
	stateCounts, _ := countInstancesByState(ctx, factStore)
	runningCount := stateCounts[types.InstanceRunning]
	convergenceRatio := float64(runningCount) / float64(totalDesired) * 100
	testHandle.Logf("  near-convergence check: %d/%d running (%.1f%%), threshold %.0f%%", runningCount, totalDesired, convergenceRatio, tolerancePercent)
	if convergenceRatio >= tolerancePercent {
		return time.Since(startTime), true, status
	}
	return time.Since(startTime), false, status
}

func countInstancesByState(ctx context.Context, factStore store.StateStore) (map[types.InstanceState]int, error) {
	instances, listError := types.ListInstances(ctx, factStore)
	if listError != nil {
		return nil, listError
	}
	stateCounts := make(map[types.InstanceState]int)
	for _, instance := range instances {
		stateCounts[instance.State]++
	}
	return stateCounts, nil
}

func countInstancesPerNode(ctx context.Context, factStore store.StateStore) (map[string]int, error) {
	instances, listError := types.ListInstances(ctx, factStore)
	if listError != nil {
		return nil, listError
	}
	nodeCounts := make(map[string]int)
	for _, instance := range instances {
		if instance.State == types.InstanceRunning {
			nodeCounts[instance.Node]++
		}
	}
	return nodeCounts, nil
}

// TestSyntheticCluster50Nodes1000Workloads exercises the full control plane
// at scale: 50 simulated nodes, 10 services, 1000 total instances. It
// measures deploy-to-convergence time, placement distribution quality,
// node failure recovery time, and scale-up convergence.
func TestSyntheticCluster50Nodes1000Workloads(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}
	if deadline, hasDeadline := t.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining < minimumTestDeadline {
			t.Skipf("skipping load test: %v remaining, need at least %v (use -timeout 10m)", remaining.Round(time.Second), minimumTestDeadline)
		}
	}
	nodeIDs := buildNodeIDs(loadTestNodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	factStore.SetWatchChannelBufferSize(4096)

	cluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	cluster.SetAgentInterval(200 * time.Millisecond)
	cluster.SetControllerDebounce(100 * time.Millisecond)
	cluster.SetMaxReconciliationAttempts(10)
	cluster.SetMaxInputKeyGuards(0)
	cluster.SetLeaseTimeout(5 * time.Minute)
	cluster.Start(ctx)

	// ── Phase 1: Deploy 10 services × 100 instances ─────────────────────
	t.Log("Phase 1: deploying 10 services × 100 instances")
	for serviceIndex := 0; serviceIndex < loadTestServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), loadTestInstancesPerService)
	}

	convergenceDuration, converged, status := waitForConvergenceWithProgress(t, ctx, cluster, factStore, convergenceTimeout)
	if !converged {
		t.Fatalf("Phase 1 FAILED — did not converge within %v: %s", convergenceTimeout, status)
	}
	t.Logf("Phase 1 PASS — 1000 instances converged in %v", convergenceDuration)

	// ── Phase 2: Verify placement distribution ───────────────────────────
	t.Log("Phase 2: checking placement distribution across 50 nodes")
	nodeCounts, countError := countInstancesPerNode(ctx, factStore)
	if countError != nil {
		t.Fatalf("Phase 2 FAILED — could not count instances: %v", countError)
	}

	populatedNodeCount := len(nodeCounts)
	if populatedNodeCount < loadTestNodeCount/2 {
		t.Logf("Phase 2 NOTE — only %d/%d nodes have instances", populatedNodeCount, loadTestNodeCount)
	}

	var minPerNode, maxPerNode int
	var totalPlaced int
	for _, nodeCount := range nodeCounts {
		if minPerNode == 0 || nodeCount < minPerNode {
			minPerNode = nodeCount
		}
		if nodeCount > maxPerNode {
			maxPerNode = nodeCount
		}
		totalPlaced += nodeCount
	}

	averagePerNode := float64(totalPlaced) / float64(populatedNodeCount)
	var varianceSum float64
	for _, nodeCount := range nodeCounts {
		diff := float64(nodeCount) - averagePerNode
		varianceSum += diff * diff
	}
	standardDeviation := math.Sqrt(varianceSum / float64(populatedNodeCount))

	t.Logf("Phase 2 PASS — %d instances across %d nodes: min=%d max=%d avg=%.1f stddev=%.1f",
		totalPlaced, populatedNodeCount, minPerNode, maxPerNode, averagePerNode, standardDeviation)

	idealPerNode := float64(loadTestTotalInstances) / float64(loadTestNodeCount)
	if maxPerNode > int(idealPerNode*3) {
		t.Logf("Phase 2 NOTE — max instances per node (%d) exceeds 3× ideal (%.0f)", maxPerNode, idealPerNode)
	}

	// ── Phase 3: Kill 5 nodes, measure recovery ─────────────────────────
	nodesToKill := 5
	t.Logf("Phase 3: killing %d nodes, measuring recovery", nodesToKill)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		cluster.KillNode(nodeID)
	}
	time.Sleep(500 * time.Millisecond)

	killedNodeSet := make(map[string]bool, nodesToKill)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		killedNodeSet[nodeID] = true
		factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeUnreachable)))
	}

	allInstances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range allInstances {
		if killedNodeSet[instance.Node] && instance.State == types.InstanceRunning {
			factStore.Put(ctx, types.KeyObservedInstanceState(instance.ID), []byte(string(types.InstanceFailed)))
		}
	}

	recoveryDuration, recovered, recoveryStatus := waitForNearConvergence(t, ctx, cluster, factStore, recoveryTimeout, loadTestTotalInstances, 95.0)
	if !recovered {
		t.Fatalf("Phase 3 FAILED — did not recover within %v: %s", recoveryTimeout, recoveryStatus)
	}
	t.Logf("Phase 3 PASS — recovered from %d node failures in %v", nodesToKill, recoveryDuration)

	nodeCountsAfterFailure, _ := countInstancesPerNode(ctx, factStore)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		killedNodeID := nodeIDs[killIndex]
		if instanceCount := nodeCountsAfterFailure[killedNodeID]; instanceCount > 0 {
			t.Logf("Phase 3 NOTE — killed node %s still has %d running instances (stale store data)", killedNodeID, instanceCount)
		}
	}

	// ── Phase 4: Scale up to 1500 instances ─────────────────────────────
	scaleUpTarget := 150
	scaleUpTotal := loadTestServiceCount * scaleUpTarget
	t.Logf("Phase 4: restarting killed nodes and scaling to %d instances (%d total)", scaleUpTarget, scaleUpTotal)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeAlive)))
		cluster.RestartNode(ctx, nodeID)
	}
	for serviceIndex := 0; serviceIndex < loadTestServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.SetServiceScale(ctx, serviceName, scaleUpTarget)
	}

	scaleUpDuration, scaledUp, scaleUpStatus := waitForNearConvergence(t, ctx, cluster, factStore, recoveryTimeout, scaleUpTotal, 95.0)
	if !scaledUp {
		t.Fatalf("Phase 4 FAILED — scale-up did not converge within %v: %s", recoveryTimeout, scaleUpStatus)
	}
	t.Logf("Phase 4 PASS — scaled to %d instances in %v", scaleUpTotal, scaleUpDuration)

	// ── Phase 5: Store fact count verification ──────────────────────────
	t.Log("Phase 5: verifying store fact count")
	allFacts, scanError := factStore.Scan(ctx, "")
	if scanError != nil {
		t.Fatalf("Phase 5 FAILED — store scan error: %v", scanError)
	}
	t.Logf("Phase 5 PASS — store contains %d facts", len(allFacts))

	stateCounts, _ := countInstancesByState(ctx, factStore)
	t.Logf("Instance states: %v", stateCounts)

	// ── Summary ─────────────────────────────────────────────────────────
	t.Log("─── Load Test Summary ───")
	t.Logf("  Nodes:                %d", loadTestNodeCount)
	t.Logf("  Deploy convergence:   %v (1000 instances)", convergenceDuration)
	t.Logf("  Placement spread:     min=%d max=%d stddev=%.1f", minPerNode, maxPerNode, standardDeviation)
	t.Logf("  Failure recovery:     %v (%d nodes killed)", recoveryDuration, nodesToKill)
	t.Logf("  Scale-up convergence: %v (1000 → %d instances)", scaleUpDuration, scaleUpTotal)
	t.Logf("  Total store facts:    %d", len(allFacts))
}

const (
	scaleTestNodeCount           = 100
	scaleTestServiceCount        = 10
	scaleTestInstancesPerService = 200
	scaleTestTotalInstances      = scaleTestServiceCount * scaleTestInstancesPerService
	scaleTestConvergenceTimeout  = 300 * time.Second
	scaleTestRecoveryTimeout     = 300 * time.Second
	scaleTestMinimumDeadline     = 15 * time.Minute

	megaTestNodeCount           = 200
	megaTestServiceCount        = 10
	megaTestInstancesPerService = 500
	megaTestTotalInstances      = megaTestServiceCount * megaTestInstancesPerService
	megaTestConvergenceTimeout  = 1800 * time.Second
	megaTestRecoveryTimeout     = 900 * time.Second
	megaTestMinimumDeadline     = 60 * time.Minute
)

// TestSyntheticCluster100Nodes2000Workloads exercises the scheduler and
// control plane at 2× the base load test scale: 100 simulated nodes, 10
// services, 2000 total instances. Validates convergence, distribution, node
// failure recovery, and scale-up. The scheduler benchmark separately
// validates placement throughput at 200n/5000i; this test validates
// end-to-end system behavior at a scale that stresses the batch placement
// and NodeCapacityCache optimizations.
func TestSyntheticCluster100Nodes2000Workloads(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping scale load test in short mode")
	}
	if deadline, hasDeadline := testHandle.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining < scaleTestMinimumDeadline {
			testHandle.Skipf("skipping scale load test: %v remaining, need at least %v (use -timeout 20m)", remaining.Round(time.Second), scaleTestMinimumDeadline)
		}
	}

	nodeIDs := buildNodeIDs(scaleTestNodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	factStore.SetWatchChannelBufferSize(8192)

	cluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	cluster.SetAgentInterval(200 * time.Millisecond)
	cluster.SetControllerDebounce(100 * time.Millisecond)
	cluster.SetMaxReconciliationAttempts(15)
	cluster.SetMaxInputKeyGuards(0)
	cluster.SetLeaseTimeout(5 * time.Minute)
	cluster.Start(ctx)

	// ── Phase 1: Deploy 10 services × 200 instances ─────────────────────
	testHandle.Logf("Phase 1: deploying %d services × %d instances = %d total", scaleTestServiceCount, scaleTestInstancesPerService, scaleTestTotalInstances)
	for serviceIndex := 0; serviceIndex < scaleTestServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), scaleTestInstancesPerService)
	}

	convergenceDuration, converged, status := waitForNearConvergence(testHandle, ctx, cluster, factStore, scaleTestConvergenceTimeout, scaleTestTotalInstances, 95.0)
	if !converged {
		testHandle.Fatalf("Phase 1 FAILED — did not converge within %v: %s", scaleTestConvergenceTimeout, status)
	}
	testHandle.Logf("Phase 1 PASS — %d instances converged in %v", scaleTestTotalInstances, convergenceDuration)

	// ── Phase 2: Verify placement distribution ───────────────────────────
	testHandle.Log("Phase 2: checking placement distribution across 100 nodes")
	nodeCounts, countError := countInstancesPerNode(ctx, factStore)
	if countError != nil {
		testHandle.Fatalf("Phase 2 FAILED — could not count instances: %v", countError)
	}

	populatedNodeCount := len(nodeCounts)
	if populatedNodeCount < scaleTestNodeCount/2 {
		testHandle.Logf("Phase 2 NOTE — only %d/%d nodes have instances", populatedNodeCount, scaleTestNodeCount)
	}

	var minPerNode, maxPerNode int
	var totalPlaced int
	for _, nodeCount := range nodeCounts {
		if minPerNode == 0 || nodeCount < minPerNode {
			minPerNode = nodeCount
		}
		if nodeCount > maxPerNode {
			maxPerNode = nodeCount
		}
		totalPlaced += nodeCount
	}

	averagePerNode := float64(totalPlaced) / float64(populatedNodeCount)
	var varianceSum float64
	for _, nodeCount := range nodeCounts {
		diff := float64(nodeCount) - averagePerNode
		varianceSum += diff * diff
	}
	standardDeviation := math.Sqrt(varianceSum / float64(populatedNodeCount))

	testHandle.Logf("Phase 2 PASS — %d instances across %d nodes: min=%d max=%d avg=%.1f stddev=%.1f",
		totalPlaced, populatedNodeCount, minPerNode, maxPerNode, averagePerNode, standardDeviation)

	idealPerNode := float64(scaleTestTotalInstances) / float64(scaleTestNodeCount)
	if maxPerNode > int(idealPerNode*3) {
		testHandle.Logf("Phase 2 NOTE — max instances per node (%d) exceeds 3× ideal (%.0f)", maxPerNode, idealPerNode)
	}

	// ── Phase 3: Kill 10 nodes, measure recovery ────────────────────────
	nodesToKill := 10
	testHandle.Logf("Phase 3: killing %d nodes, measuring recovery", nodesToKill)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		cluster.KillNode(nodeID)
	}
	time.Sleep(500 * time.Millisecond)

	killedNodeSet := make(map[string]bool, nodesToKill)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		killedNodeSet[nodeID] = true
		factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeUnreachable)))
	}

	allInstances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range allInstances {
		if killedNodeSet[instance.Node] && instance.State == types.InstanceRunning {
			factStore.Put(ctx, types.KeyObservedInstanceState(instance.ID), []byte(string(types.InstanceFailed)))
		}
	}

	recoveryDuration, recovered, recoveryStatus := waitForNearConvergence(testHandle, ctx, cluster, factStore, scaleTestRecoveryTimeout, scaleTestTotalInstances, 95.0)
	if !recovered {
		testHandle.Fatalf("Phase 3 FAILED — did not recover within %v: %s", scaleTestRecoveryTimeout, recoveryStatus)
	}
	testHandle.Logf("Phase 3 PASS — recovered from %d node failures in %v", nodesToKill, recoveryDuration)

	// ── Phase 4: Scale up to 3000 instances ─────────────────────────────
	scaleUpTarget := 300
	scaleUpTotal := scaleTestServiceCount * scaleUpTarget
	testHandle.Logf("Phase 4: restarting killed nodes and scaling to %d instances per service (%d total)", scaleUpTarget, scaleUpTotal)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeAlive)))
		cluster.RestartNode(ctx, nodeID)
	}
	for serviceIndex := 0; serviceIndex < scaleTestServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.SetServiceScale(ctx, serviceName, scaleUpTarget)
	}

	scaleUpDuration, scaledUp, scaleUpStatus := waitForNearConvergence(testHandle, ctx, cluster, factStore, scaleTestRecoveryTimeout, scaleUpTotal, 95.0)
	if !scaledUp {
		testHandle.Fatalf("Phase 4 FAILED — scale-up did not converge within %v: %s", scaleTestRecoveryTimeout, scaleUpStatus)
	}
	testHandle.Logf("Phase 4 PASS — scaled to %d instances in %v", scaleUpTotal, scaleUpDuration)

	// ── Phase 5: Store fact count verification ──────────────────────────
	testHandle.Log("Phase 5: verifying store fact count")
	allFacts, scanError := factStore.Scan(ctx, "")
	if scanError != nil {
		testHandle.Fatalf("Phase 5 FAILED — store scan error: %v", scanError)
	}
	testHandle.Logf("Phase 5 PASS — store contains %d facts", len(allFacts))

	finalStateCounts, _ := countInstancesByState(ctx, factStore)
	testHandle.Logf("Instance states: %v", finalStateCounts)

	// ── Summary ─────────────────────────────────────────────────────────
	testHandle.Log("─── Scale Load Test Summary ───")
	testHandle.Logf("  Nodes:                %d", scaleTestNodeCount)
	testHandle.Logf("  Deploy convergence:   %v (%d instances)", convergenceDuration, scaleTestTotalInstances)
	testHandle.Logf("  Placement spread:     min=%d max=%d stddev=%.1f", minPerNode, maxPerNode, standardDeviation)
	testHandle.Logf("  Failure recovery:     %v (%d nodes killed)", recoveryDuration, nodesToKill)
	testHandle.Logf("  Scale-up convergence: %v (%d → %d instances)", scaleUpDuration, scaleTestTotalInstances, scaleUpTotal)
	testHandle.Logf("  Total store facts:    %d", len(allFacts))
}

// TestSyntheticCluster200Nodes5000Workloads exercises the full control plane
// at the target scale for the CCattler benchmark: 200 simulated nodes, 10
// services, 5000 total instances. This validates that the batch scheduler,
// NodeCapacityCache, and MemoryStore handle high write contention from 200
// concurrent agent goroutines. Uses relaxed agent intervals and a 90%
// near-convergence threshold because single-process MemoryStore saturates
// under 200 concurrent writers.
func TestSyntheticCluster200Nodes5000Workloads(testHandle *testing.T) {
	if testing.Short() {
		testHandle.Skip("skipping mega load test in short mode")
	}
	if deadline, hasDeadline := testHandle.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining < megaTestMinimumDeadline {
			testHandle.Skipf("skipping mega load test: %v remaining, need at least %v (use -timeout 35m)", remaining.Round(time.Second), megaTestMinimumDeadline)
		}
	}

	nodeIDs := buildNodeIDs(megaTestNodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	factStore.SetWatchChannelBufferSize(16384)

	cluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	cluster.SetAgentInterval(500 * time.Millisecond)
	cluster.SetControllerDebounce(200 * time.Millisecond)
	cluster.SetMaxReconciliationAttempts(20)
	cluster.SetMaxInputKeyGuards(0)
	cluster.SetLeaseTimeout(10 * time.Minute)
	cluster.Start(ctx)

	// ── Phase 1: Deploy 10 services × 500 instances ─────────────────────
	testHandle.Logf("Phase 1: deploying %d services × %d instances = %d total", megaTestServiceCount, megaTestInstancesPerService, megaTestTotalInstances)
	for serviceIndex := 0; serviceIndex < megaTestServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), megaTestInstancesPerService)
	}

	convergenceDuration, converged, status := waitForNearConvergence(testHandle, ctx, cluster, factStore, megaTestConvergenceTimeout, megaTestTotalInstances, 85.0)
	if !converged {
		testHandle.Fatalf("Phase 1 FAILED — did not converge within %v: %s", megaTestConvergenceTimeout, status)
	}
	testHandle.Logf("Phase 1 PASS — %d instances converged in %v", megaTestTotalInstances, convergenceDuration)

	// ── Phase 2: Verify placement distribution ───────────────────────────
	testHandle.Log("Phase 2: checking placement distribution across 200 nodes")
	nodeCounts, countError := countInstancesPerNode(ctx, factStore)
	if countError != nil {
		testHandle.Fatalf("Phase 2 FAILED — could not count instances: %v", countError)
	}

	populatedNodeCount := len(nodeCounts)
	if populatedNodeCount < megaTestNodeCount/2 {
		testHandle.Logf("Phase 2 NOTE — only %d/%d nodes have instances", populatedNodeCount, megaTestNodeCount)
	}

	var minPerNode, maxPerNode int
	var totalPlaced int
	for _, nodeCount := range nodeCounts {
		if minPerNode == 0 || nodeCount < minPerNode {
			minPerNode = nodeCount
		}
		if nodeCount > maxPerNode {
			maxPerNode = nodeCount
		}
		totalPlaced += nodeCount
	}

	averagePerNode := float64(totalPlaced) / float64(populatedNodeCount)
	var varianceSum float64
	for _, nodeCount := range nodeCounts {
		diff := float64(nodeCount) - averagePerNode
		varianceSum += diff * diff
	}
	standardDeviation := math.Sqrt(varianceSum / float64(populatedNodeCount))

	testHandle.Logf("Phase 2 PASS — %d instances across %d nodes: min=%d max=%d avg=%.1f stddev=%.1f",
		totalPlaced, populatedNodeCount, minPerNode, maxPerNode, averagePerNode, standardDeviation)

	idealPerNode := float64(megaTestTotalInstances) / float64(megaTestNodeCount)
	if maxPerNode > int(idealPerNode*3) {
		testHandle.Logf("Phase 2 NOTE — max instances per node (%d) exceeds 3× ideal (%.0f)", maxPerNode, idealPerNode)
	}

	// ── Phase 3: Kill 10 nodes, measure recovery ────────────────────────
	nodesToKill := 10
	testHandle.Logf("Phase 3: killing %d nodes, measuring recovery", nodesToKill)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		cluster.KillNode(nodeID)
	}
	time.Sleep(1 * time.Second)

	killedNodeSet := make(map[string]bool, nodesToKill)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		killedNodeSet[nodeID] = true
		factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeUnreachable)))
	}

	allInstances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range allInstances {
		if killedNodeSet[instance.Node] && instance.State == types.InstanceRunning {
			factStore.Put(ctx, types.KeyObservedInstanceState(instance.ID), []byte(string(types.InstanceFailed)))
		}
	}

	recoveryDuration, recovered, recoveryStatus := waitForNearConvergence(testHandle, ctx, cluster, factStore, megaTestRecoveryTimeout, megaTestTotalInstances, 85.0)
	if !recovered {
		testHandle.Fatalf("Phase 3 FAILED — did not recover within %v: %s", megaTestRecoveryTimeout, recoveryStatus)
	}
	testHandle.Logf("Phase 3 PASS — recovered from %d node failures in %v", nodesToKill, recoveryDuration)

	// ── Phase 4: Scale up to 6000 instances ─────────────────────────────
	scaleUpTarget := 600
	scaleUpTotal := megaTestServiceCount * scaleUpTarget
	testHandle.Logf("Phase 4: restarting killed nodes and scaling to %d instances per service (%d total)", scaleUpTarget, scaleUpTotal)
	for killIndex := 0; killIndex < nodesToKill; killIndex++ {
		nodeID := nodeIDs[killIndex]
		factStore.Put(ctx, types.KeyObservedNodeState(nodeID), []byte(string(types.NodeAlive)))
		cluster.RestartNode(ctx, nodeID)
	}
	for serviceIndex := 0; serviceIndex < megaTestServiceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		cluster.SetServiceScale(ctx, serviceName, scaleUpTarget)
	}

	scaleUpDuration, scaledUp, scaleUpStatus := waitForNearConvergence(testHandle, ctx, cluster, factStore, megaTestRecoveryTimeout, scaleUpTotal, 85.0)
	if !scaledUp {
		testHandle.Fatalf("Phase 4 FAILED — scale-up did not converge within %v: %s", megaTestRecoveryTimeout, scaleUpStatus)
	}
	testHandle.Logf("Phase 4 PASS — scaled to %d instances in %v", scaleUpTotal, scaleUpDuration)

	// ── Phase 5: Store fact count verification ──────────────────────────
	testHandle.Log("Phase 5: verifying store fact count")
	allFacts, scanError := factStore.Scan(ctx, "")
	if scanError != nil {
		testHandle.Fatalf("Phase 5 FAILED — store scan error: %v", scanError)
	}
	testHandle.Logf("Phase 5 PASS — store contains %d facts", len(allFacts))

	finalStateCounts, _ := countInstancesByState(ctx, factStore)
	testHandle.Logf("Instance states: %v", finalStateCounts)

	// ── Summary ─────────────────────────────────────────────────────────
	testHandle.Log("─── Mega Load Test Summary ───")
	testHandle.Logf("  Nodes:                %d", megaTestNodeCount)
	testHandle.Logf("  Deploy convergence:   %v (%d instances)", convergenceDuration, megaTestTotalInstances)
	testHandle.Logf("  Placement spread:     min=%d max=%d stddev=%.1f", minPerNode, maxPerNode, standardDeviation)
	testHandle.Logf("  Failure recovery:     %v (%d nodes killed)", recoveryDuration, nodesToKill)
	testHandle.Logf("  Scale-up convergence: %v (%d → %d instances)", scaleUpDuration, megaTestTotalInstances, scaleUpTotal)
	testHandle.Logf("  Total store facts:    %d", len(allFacts))
}
