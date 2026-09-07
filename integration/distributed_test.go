package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestDistributedSpreadAcrossNodes verifies that instances are spread evenly
// across 3 nodes with agents running. Each node gets its own SimulatorRuntime.
func TestDistributedSpreadAcrossNodes(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register 3 nodes with equal capacity.
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	// Start all reconciliation controllers including the node failure detector.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 500 * time.Millisecond

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController, endpointController, failureController, nodeFailureController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Start 3 agents, each with its own simulator runtime.
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(ctx)
	}

	// Deploy service with 6 instances — should spread 2 per node.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("6"))

	waitFor(t, 5*time.Second, "6 instances running", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if instance.Service == "web" && instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount >= 6
	})

	// Verify placements are spread evenly: 2 per node.
	placementFacts, _ := factStore.Scan(ctx, types.ScanPlacements)
	instancesPerNode := make(map[string]int)
	for _, placementFact := range placementFacts {
		instancesPerNode[string(placementFact.Value)]++
	}
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		if instancesPerNode[nodeID] != 2 {
			t.Errorf("node %s got %d placements, want 2", nodeID, instancesPerNode[nodeID])
		}
	}
}

// TestDistributedNodeFailureReschedules verifies the full failure recovery
// pipeline: kill a node's agent, detect lease expiry, mark instances failed,
// create replacements, place on surviving nodes, and start them.
func TestDistributedNodeFailureReschedules(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register 3 nodes with equal capacity.
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	// Start controllers with a short lease timeout for fast failure detection.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 300 * time.Millisecond

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController, endpointController, failureController, nodeFailureController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Start 3 agents — node-1 gets its own cancellable context so we can simulate a crash.
	node1Context, cancelNode1Agent := context.WithCancel(ctx)
	agentRuntimes := make(map[string]*runtime.SimulatorRuntime)
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		agentRuntimes[nodeID] = simulatorRuntime
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		if nodeID == "node-1" {
			go nodeAgent.Run(node1Context)
		} else {
			go nodeAgent.Run(ctx)
		}
	}

	// Deploy 6 instances spread across 3 nodes.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("6"))

	waitFor(t, 10*time.Second, "6 running instances", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if instance.Service == "web" && instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount >= 6
	})

	// Kill node-1's agent — it stops writing heartbeats, triggering lease expiry.
	cancelNode1Agent()

	// Wait for the node failure detector to mark node-1 as unreachable.
	waitFor(t, 3*time.Second, "node-1 unreachable", func() bool {
		nodeFact, err := factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(nodeFact.Value) == string(types.NodeUnreachable)
	})

	// Wait for the system to converge: node failure controller marks instances
	// as failed → failure controller creates replacements → scheduler places
	// them on node-2/node-3 → those agents start them.
	waitFor(t, 10*time.Second, "6 running instances after node failure", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if instance.Service == "web" && instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount >= 6
	})

	// Verify no running instances remain placed on the dead node.
	allInstances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range allInstances {
		if instance.Service == "web" && instance.State == types.InstanceRunning {
			placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID))
			if err == nil && string(placementFact.Value) == "node-1" {
				t.Errorf("instance %s still placed on dead node-1", instance.ID)
			}
		}
	}
}

// TestDistributedHeartbeatsVisibleInStore verifies that the agent writes
// heartbeat lease timestamps to the store that can be scanned.
func TestDistributedHeartbeatsVisibleInStore(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	go nodeAgent.Run(ctx)

	// Verify heartbeat lease appears in the store.
	waitFor(t, 2*time.Second, "heartbeat lease written", func() bool {
		leaseFact, err := factStore.Get(ctx, types.KeyLeaseNode("node-1"))
		return err == nil && len(leaseFact.Value) > 0
	})

	// Verify the lease scan prefix returns exactly 1 result.
	allLeases, _ := factStore.Scan(ctx, types.ScanLeaseNodes)
	if len(allLeases) != 1 {
		t.Errorf("expected 1 lease, got %d", len(allLeases))
	}
}

// TestDistributedMultiServiceSpread verifies that multiple services are
// deployed and spread across nodes simultaneously with independent instance counts.
func TestDistributedMultiServiceSpread(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController, failureController, nodeFailureController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	for _, nodeID := range []string{"node-1", "node-2"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(ctx)
	}

	// Deploy two services: web with 4 instances and api with 2 instances.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("4"))
	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("myapp:latest"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("2"))

	waitFor(t, 5*time.Second, "6 total running instances", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if (instance.Service == "web" || instance.Service == "api") && instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount >= 6
	})

	// Verify both services have their expected running instance counts.
	allInstances, _ := types.ListInstances(ctx, factStore)
	runningByService := make(map[string]int)
	for _, instance := range allInstances {
		if instance.State == types.InstanceRunning {
			runningByService[instance.Service]++
		}
	}
	if runningByService["web"] < 4 {
		t.Errorf("web: expected 4 running, got %d", runningByService["web"])
	}
	if runningByService["api"] < 2 {
		t.Errorf("api: expected 2 running, got %d", runningByService["api"])
	}
}
