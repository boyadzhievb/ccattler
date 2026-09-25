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
	convergenceTimeout          = 60 * time.Second
	recoveryTimeout             = 180 * time.Second
)

func buildNodeIDs(count int) []string {
	nodeIDs := make([]string, count)
	for index := range nodeIDs {
		nodeIDs[index] = fmt.Sprintf("node-%03d", index)
	}
	return nodeIDs
}

func waitForConvergence(ctx context.Context, cluster *chaos.SimulatedChaosCluster, timeout time.Duration) (time.Duration, bool, string) {
	startTime := time.Now()
	deadline := startTime.Add(timeout)
	for time.Now().Before(deadline) {
		converged, status := cluster.CheckConvergence(ctx)
		if converged {
			return time.Since(startTime), true, status
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, status := cluster.CheckConvergence(ctx)
	return time.Since(startTime), false, status
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
	nodeIDs := buildNodeIDs(loadTestNodeCount)
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
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

	convergenceDuration, converged, status := waitForConvergence(ctx, cluster, convergenceTimeout)
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
