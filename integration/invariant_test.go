package integration

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// invariantCluster holds all the components of a simulated cluster used by the
// invariant tests. It provides a shared memory store, a set of controllers, and
// node agents backed by the SimulatorRuntime.
type invariantCluster struct {
	// factStore is the shared in-memory fact store backing the cluster.
	factStore *store.MemoryStore
	// clusterContext is the root context for the entire cluster lifecycle.
	clusterContext context.Context
	// clusterCancel stops all cluster components when called.
	clusterCancel context.CancelFunc
	// controllerList holds references to the individual controllers for
	// direct reconciliation testing (e.g. the idempotent fixed-point check).
	controllerList []controllers.Controller
}

// setupInvariantCluster creates a cluster with the specified number of nodes,
// starts all core controllers (instance, scheduler, endpoint, failure, network),
// and launches simulator-backed node agents. The caller must defer clusterCancel
// and factStore.Close().
func setupInvariantCluster(testContext *testing.T, nodeCount int) *invariantCluster {
	testContext.Helper()

	factStore := store.NewMemoryStore()

	clusterContext, clusterCancel := context.WithCancel(context.Background())

	// Register alive nodes with sufficient capacity.
	for nodeIndex := 1; nodeIndex <= nodeCount; nodeIndex++ {
		nodeIdentifier := nodeIDForIndex(nodeIndex)
		types.WriteNode(clusterContext, factStore, types.Node{
			ID: nodeIdentifier, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	// Assemble core controllers.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	networkController := controllers.NewNetworkController()

	allControllers := []controllers.Controller{
		instanceController,
		schedulerController,
		endpointController,
		failureController,
		networkController,
	}

	controllerRunner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		endpointController, failureController, networkController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(clusterContext)

	// Launch simulator-backed node agents with a fast reconciliation interval.
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()
	for nodeIndex := 1; nodeIndex <= nodeCount; nodeIndex++ {
		nodeIdentifier := nodeIDForIndex(nodeIndex)
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeIdentifier, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(clusterContext)
	}

	return &invariantCluster{
		factStore:      factStore,
		clusterContext: clusterContext,
		clusterCancel:  clusterCancel,
		controllerList: allControllers,
	}
}

// nodeIDForIndex returns a deterministic node ID string from a 1-based index.
func nodeIDForIndex(index int) string {
	return "node-" + strconv.Itoa(index)
}

// deployServiceAndWaitForConvergence writes the desired state for a service
// and waits until all instances are running on nodes.
func deployServiceAndWaitForConvergence(
	testContext *testing.T,
	cluster *invariantCluster,
	serviceName string,
	imageName string,
	desiredInstanceCount int,
	exposePort int,
) {
	testContext.Helper()
	backgroundContext := context.Background()

	// Write desired service state.
	cluster.factStore.Put(backgroundContext, types.KeyDesiredServiceImage(serviceName), []byte(imageName))
	if exposePort > 0 {
		cluster.factStore.Put(backgroundContext, types.KeyDesiredServiceExpose(serviceName, exposePort), []byte(""))
	}
	cluster.factStore.Put(backgroundContext, types.KeyEffectiveServiceInstances(serviceName),
		[]byte(strconv.Itoa(desiredInstanceCount)))

	// Wait for all instances to reach running state.
	waitFor(testContext, 10*time.Second, "all instances running for "+serviceName, func() bool {
		return countRunningInstancesForServiceInStore(backgroundContext, cluster.factStore, serviceName) >= desiredInstanceCount
	})
}

// countRunningInstancesForServiceInStore counts instances in the running state
// for a given service name within the provided store.
func countRunningInstancesForServiceInStore(backgroundContext context.Context, factStore store.StateStore, serviceName string) int {
	allInstances, listError := types.ListInstances(backgroundContext, factStore)
	if listError != nil {
		return 0
	}
	runningCount := 0
	for _, instance := range allInstances {
		if instance.Service == serviceName && instance.State == types.InstanceRunning {
			runningCount++
		}
	}
	return runningCount
}

// TestInvariantDesiredObservedSeparation verifies that after deploying a service
// and letting the system converge, the desired/ and observed/ key namespaces are
// strictly separate. No key under "desired/" should share a suffix with a key
// under "observed/" — the two namespaces serve fundamentally different purposes
// (user intent vs. agent-reported reality) and must never be conflated.
func TestInvariantDesiredObservedSeparation(t *testing.T) {
	cluster := setupInvariantCluster(t, 3)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()

	deployServiceAndWaitForConvergence(t, cluster, "web", "nginx:1.28", 3, 0)

	backgroundContext := context.Background()

	// Scan all desired facts.
	desiredFacts, desiredScanError := cluster.factStore.Scan(backgroundContext, types.PrefixDesired+"/")
	if desiredScanError != nil {
		t.Fatalf("failed to scan desired prefix: %v", desiredScanError)
	}

	// Scan all observed facts.
	observedFacts, observedScanError := cluster.factStore.Scan(backgroundContext, types.PrefixObserved+"/")
	if observedScanError != nil {
		t.Fatalf("failed to scan observed prefix: %v", observedScanError)
	}

	// Build a set of key suffixes from the desired namespace (strip the "desired" prefix).
	desiredSuffixes := make(map[string]bool, len(desiredFacts))
	for _, desiredFact := range desiredFacts {
		suffix := strings.TrimPrefix(desiredFact.Key, types.PrefixDesired)
		desiredSuffixes[suffix] = true
	}

	// Build a set of key suffixes from the observed namespace (strip the "observed" prefix).
	observedSuffixes := make(map[string]bool, len(observedFacts))
	for _, observedFact := range observedFacts {
		suffix := strings.TrimPrefix(observedFact.Key, types.PrefixObserved)
		observedSuffixes[suffix] = true
	}

	// Verify no desired suffix appears in the observed set.
	for desiredSuffix := range desiredSuffixes {
		if observedSuffixes[desiredSuffix] {
			t.Errorf("invariant violation: key suffix %q exists in both desired/ and observed/ namespaces", desiredSuffix)
		}
	}

	// Verify the namespaces are non-empty (sanity check that the test actually ran).
	if len(desiredFacts) == 0 {
		t.Error("desired namespace is empty after deploying a service — test setup problem")
	}
	if len(observedFacts) == 0 {
		t.Error("observed namespace is empty after convergence — test setup problem")
	}

	// Additionally verify no desired key starts with the observed prefix and vice versa.
	for _, desiredFact := range desiredFacts {
		if strings.HasPrefix(desiredFact.Key, types.PrefixObserved+"/") {
			t.Errorf("invariant violation: fact with key %q is under desired/ scan but starts with observed/ prefix", desiredFact.Key)
		}
	}
	for _, observedFact := range observedFacts {
		if strings.HasPrefix(observedFact.Key, types.PrefixDesired+"/") {
			t.Errorf("invariant violation: fact with key %q is under observed/ scan but starts with desired/ prefix", observedFact.Key)
		}
	}
}

// TestInvariantPlacementOnlyOnAliveNodes verifies that every scheduler placement
// targets a node whose observed state is "alive". Placements on dead, draining,
// or non-existent nodes would violate the system's correctness guarantee that
// workloads only run on healthy machines.
func TestInvariantPlacementOnlyOnAliveNodes(t *testing.T) {
	cluster := setupInvariantCluster(t, 3)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()

	deployServiceAndWaitForConvergence(t, cluster, "api", "api-server:2.0", 3, 0)

	backgroundContext := context.Background()

	// Scan all placement facts.
	placementFacts, placementScanError := cluster.factStore.Scan(backgroundContext, types.ScanPlacements)
	if placementScanError != nil {
		t.Fatalf("failed to scan placements: %v", placementScanError)
	}
	if len(placementFacts) == 0 {
		t.Fatal("no placements found after convergence — expected at least 3")
	}

	// Build a set of alive node IDs from observed state.
	allNodes, nodeListError := types.ListNodes(backgroundContext, cluster.factStore)
	if nodeListError != nil {
		t.Fatalf("failed to list nodes: %v", nodeListError)
	}
	aliveNodeSet := make(map[string]bool, len(allNodes))
	for _, node := range allNodes {
		if node.State == types.NodeAlive {
			aliveNodeSet[node.ID] = true
		}
	}

	// For each placement, verify the target node is alive.
	for _, placementFact := range placementFacts {
		targetNodeID := string(placementFact.Value)
		if targetNodeID == "" {
			t.Errorf("invariant violation: placement %q has empty node ID", placementFact.Key)
			continue
		}
		if !aliveNodeSet[targetNodeID] {
			t.Errorf("invariant violation: placement %q targets node %q which is not alive (alive nodes: %v)",
				placementFact.Key, targetNodeID, aliveNodeSet)
		}
	}

	t.Logf("verified %d placements all target alive nodes", len(placementFacts))
}

// TestInvariantInstanceCountMatchesDesired verifies that after deploying a
// service with a specific instance count and letting the system converge, the
// number of running instances exactly matches the desired count. This is the
// core promise of the orchestrator: desired state becomes actual state.
func TestInvariantInstanceCountMatchesDesired(t *testing.T) {
	cluster := setupInvariantCluster(t, 3)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()

	desiredCount := 5

	// Use more nodes to accommodate 5 instances.
	backgroundContext := context.Background()
	for extraIndex := 4; extraIndex <= 5; extraIndex++ {
		extraNodeID := "node-" + strconv.Itoa(extraIndex)
		types.WriteNode(backgroundContext, cluster.factStore, types.Node{
			ID: extraNodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
		// Start an agent for the extra node.
		simulatorNetworkProvider := network.NewSimulatorNetworkProvider()
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(extraNodeID, cluster.factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(cluster.clusterContext)
	}

	deployServiceAndWaitForConvergence(t, cluster, "counter", "counter:1.0", desiredCount, 0)

	// Count running instances after convergence.
	allInstances, instanceListError := types.ListInstances(backgroundContext, cluster.factStore)
	if instanceListError != nil {
		t.Fatalf("failed to list instances: %v", instanceListError)
	}

	runningCount := 0
	for _, instance := range allInstances {
		if instance.Service == "counter" && instance.State == types.InstanceRunning {
			runningCount++
		}
	}

	if runningCount != desiredCount {
		t.Errorf("invariant violation: expected %d running instances for service 'counter', got %d",
			desiredCount, runningCount)
	}

	// Also verify the placement count matches.
	placementFacts, placementScanError := cluster.factStore.Scan(backgroundContext, types.ScanPlacements)
	if placementScanError != nil {
		t.Fatalf("failed to scan placements: %v", placementScanError)
	}

	placementCountForService := 0
	for _, placementFact := range placementFacts {
		// Placement keys contain the instance ID; verify by checking the instance belongs
		// to our service.
		for _, instance := range allInstances {
			if instance.Service == "counter" && strings.Contains(placementFact.Key, instance.ID) {
				placementCountForService++
				break
			}
		}
	}

	if placementCountForService != desiredCount {
		t.Errorf("invariant violation: expected %d placements for service 'counter', got %d",
			desiredCount, placementCountForService)
	}

	t.Logf("verified instance count invariant: desired=%d, running=%d, placements=%d",
		desiredCount, runningCount, placementCountForService)
}

// TestInvariantIdempotentReconciliation verifies that the system reaches a
// fixed point after convergence. Once all desired state matches observed state,
// running another reconciliation cycle on each controller must produce zero
// changes. This proves the controllers are truly idempotent — they don't
// oscillate or create spurious work when the system is already converged.
func TestInvariantIdempotentReconciliation(t *testing.T) {
	cluster := setupInvariantCluster(t, 3)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()

	deployServiceAndWaitForConvergence(t, cluster, "stable", "stable:1.0", 3, 8080)

	backgroundContext := context.Background()

	// Wait for the network controller to create VIP facts for the exposed service.
	// VIP creation happens after endpoints are derived from running instances,
	// so it is the last step in the convergence chain.
	waitFor(t, 5*time.Second, "VIP facts created for stable service", func() bool {
		vipFacts, _ := cluster.factStore.Scan(backgroundContext, types.ScanNetworkVIPs)
		for _, vipFact := range vipFacts {
			if strings.Contains(vipFact.Key, "stable") && len(vipFact.Value) > 0 {
				return true
			}
		}
		return false
	})

	// Also wait for DNS mapping to be created for the service.
	waitFor(t, 5*time.Second, "DNS mapping created for stable service", func() bool {
		dnsFacts, _ := cluster.factStore.Scan(backgroundContext, types.ScanNetworkDNS)
		for _, dnsFact := range dnsFacts {
			if strings.Contains(dnsFact.Key, "stable") {
				return true
			}
		}
		return false
	})

	// Allow extra time for any trailing reconciliation cycles to settle
	// after VIP and DNS creation.
	time.Sleep(300 * time.Millisecond)

	// Stop the controller runner and agents to prevent background writes
	// during the idempotency check. Without this, background reconciliation
	// cycles can advance the store revision and cause a false failure.
	cluster.clusterCancel()
	time.Sleep(100 * time.Millisecond)

	// Record the store revision at this point.
	currentRevision, revisionError := cluster.factStore.Revision(backgroundContext)
	if revisionError != nil {
		t.Fatalf("failed to read store revision: %v", revisionError)
	}

	// For each controller, scan its watched prefixes and call Reconcile directly.
	// A converged system must produce zero changes. Facts must be sorted by key
	// before calling Reconcile, matching the runner's behavior — FactsWithPrefix
	// uses binary search and requires sorted input.
	for _, controller := range cluster.controllerList {
		var controllerFacts []store.Fact
		for _, watchPrefix := range controller.Watch() {
			prefixFacts, scanError := cluster.factStore.Scan(backgroundContext, watchPrefix)
			if scanError != nil {
				t.Fatalf("failed to scan prefix %q for controller %s: %v",
					watchPrefix, controller.Name(), scanError)
			}
			controllerFacts = append(controllerFacts, prefixFacts...)
		}
		store.SortFacts(controllerFacts)

		proposedChanges, reconcileError := controller.Reconcile(backgroundContext, controllerFacts)
		if reconcileError != nil {
			t.Fatalf("controller %s returned error during idempotent reconciliation check: %v",
				controller.Name(), reconcileError)
		}

		if len(proposedChanges) > 0 {
			changeDescriptions := make([]string, 0, len(proposedChanges))
			for _, change := range proposedChanges {
				changeDescriptions = append(changeDescriptions,
					change.Key+"="+string(change.Value))
			}
			t.Errorf("invariant violation: controller %s proposed %d changes after convergence (system not at fixed point): %v",
				controller.Name(), len(proposedChanges), changeDescriptions)
		}
	}

	// Also verify the store revision has not advanced (no background writes).
	postCheckRevision, postRevisionError := cluster.factStore.Revision(backgroundContext)
	if postRevisionError != nil {
		t.Fatalf("failed to read post-check store revision: %v", postRevisionError)
	}

	if postCheckRevision != currentRevision {
		t.Errorf("invariant violation: store revision advanced from %d to %d during idempotency check — background activity detected",
			currentRevision, postCheckRevision)
	}

	t.Logf("verified idempotent reconciliation: all %d controllers returned zero changes at revision %d",
		len(cluster.controllerList), currentRevision)
}

// TestInvariantEndpointsMatchRunningInstances verifies that after deploying a
// service with an exposed port and letting the system converge, the number of
// endpoint facts matches the number of running instances. Each running instance
// that exposes a port must have exactly one endpoint, and no phantom endpoints
// should exist for non-running instances.
func TestInvariantEndpointsMatchRunningInstances(t *testing.T) {
	cluster := setupInvariantCluster(t, 3)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()

	serviceName := "frontend"
	servicePort := 8080
	desiredCount := 3

	deployServiceAndWaitForConvergence(t, cluster, serviceName, "frontend:3.0", desiredCount, servicePort)

	backgroundContext := context.Background()

	// Wait for endpoints to be created by the endpoint controller.
	waitFor(t, 5*time.Second, "endpoint facts created for "+serviceName, func() bool {
		endpointFacts, _ := cluster.factStore.Scan(backgroundContext, types.ScanEndpoints)
		serviceEndpointCount := 0
		for _, endpointFact := range endpointFacts {
			if strings.Contains(endpointFact.Key, serviceName) {
				serviceEndpointCount++
			}
		}
		return serviceEndpointCount >= desiredCount
	})

	// Count running instances for the service.
	allInstances, instanceListError := types.ListInstances(backgroundContext, cluster.factStore)
	if instanceListError != nil {
		t.Fatalf("failed to list instances: %v", instanceListError)
	}

	runningInstanceIDs := make(map[string]bool)
	for _, instance := range allInstances {
		if instance.Service == serviceName && instance.State == types.InstanceRunning {
			runningInstanceIDs[instance.ID] = true
		}
	}

	// Scan all endpoint facts for this service.
	allEndpointFacts, endpointScanError := cluster.factStore.Scan(backgroundContext, types.ScanEndpoints)
	if endpointScanError != nil {
		t.Fatalf("failed to scan endpoints: %v", endpointScanError)
	}

	serviceEndpointCount := 0
	endpointInstanceIDs := make(map[string]bool)
	for _, endpointFact := range allEndpointFacts {
		if strings.Contains(endpointFact.Key, serviceName) {
			serviceEndpointCount++
			// Extract the instance ID from the endpoint key.
			// Endpoint keys follow the pattern: endpoint/service/{serviceName}/{instanceID}/{port}
			keyParts := strings.Split(endpointFact.Key, "/")
			// Expected: ["endpoint", "service", serviceName, instanceID, port]
			if len(keyParts) >= 5 {
				endpointInstanceID := keyParts[3]
				endpointInstanceIDs[endpointInstanceID] = true
			}
		}
	}

	// Verify the endpoint count matches the running instance count.
	if serviceEndpointCount != len(runningInstanceIDs) {
		t.Errorf("invariant violation: service %q has %d endpoints but %d running instances",
			serviceName, serviceEndpointCount, len(runningInstanceIDs))
	}

	// Verify every endpoint references a running instance.
	for endpointInstanceID := range endpointInstanceIDs {
		if !runningInstanceIDs[endpointInstanceID] {
			t.Errorf("invariant violation: endpoint references instance %q which is not running",
				endpointInstanceID)
		}
	}

	// Verify every running instance has an endpoint.
	for runningInstanceID := range runningInstanceIDs {
		if !endpointInstanceIDs[runningInstanceID] {
			t.Errorf("invariant violation: running instance %q has no endpoint",
				runningInstanceID)
		}
	}

	t.Logf("verified endpoint invariant: %d endpoints match %d running instances for service %q",
		serviceEndpointCount, len(runningInstanceIDs), serviceName)
}
