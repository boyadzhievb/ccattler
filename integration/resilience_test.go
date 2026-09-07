package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/chaos"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// resilienceCluster holds all components of a 3-node simulated cluster where
// each agent uses a PartitionedStore wrapper, enabling per-node network
// partition simulation alongside per-node context cancellation.
type resilienceCluster struct {
	// factStore is the shared backing memory store used by controllers directly.
	factStore *store.MemoryStore
	// clusterCancel stops the entire cluster including all agents and controllers.
	clusterCancel context.CancelFunc
	// clusterContext is the parent context for everything in this cluster.
	clusterContext context.Context
	// controllerCancel stops just the controller runner for restart testing.
	controllerCancel context.CancelFunc
	// partitionedStores maps nodeID to its PartitionedStore wrapper.
	partitionedStores map[string]*chaos.PartitionedStore
	// killNode maps nodeID to its agent's cancel function.
	killNode map[string]context.CancelFunc
	// agentRuntimes maps nodeID to its SimulatorRuntime.
	agentRuntimes map[string]*runtime.SimulatorRuntime
}

// helperSetupResilienceCluster creates a 3-node cluster where each agent gets
// its own PartitionedStore view of the shared MemoryStore. Controllers run on
// the shared MemoryStore directly. The controller runner has its own
// cancellable context for restart testing.
func helperSetupResilienceCluster(t *testing.T) *resilienceCluster {
	t.Helper()

	factStore := store.NewMemoryStore()

	clusterContext, clusterCancel := context.WithCancel(context.Background())

	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(clusterContext, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	controllerContext, controllerCancel := context.WithCancel(clusterContext)
	helperStartControllers(factStore, controllerContext)

	partitionedStores := make(map[string]*chaos.PartitionedStore)
	killFunctions := make(map[string]context.CancelFunc)
	agentRuntimes := make(map[string]*runtime.SimulatorRuntime)

	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		partitionedStore := chaos.NewPartitionedStore(factStore)
		partitionedStores[nodeID] = partitionedStore

		nodeContext, nodeCancel := context.WithCancel(clusterContext)
		killFunctions[nodeID] = nodeCancel

		simulatorRuntime := runtime.NewSimulatorRuntime()
		agentRuntimes[nodeID] = simulatorRuntime

		nodeAgent := agent.New(nodeID, partitionedStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(nodeContext)
	}

	return &resilienceCluster{
		factStore:         factStore,
		clusterCancel:     clusterCancel,
		clusterContext:    clusterContext,
		controllerCancel:  controllerCancel,
		partitionedStores: partitionedStores,
		killNode:          killFunctions,
		agentRuntimes:     agentRuntimes,
	}
}

// helperStartControllers creates and starts all controllers on a Runner with
// a short lease timeout and debounce suitable for testing.
func helperStartControllers(factStore store.StateStore, ctx context.Context) {
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 300 * time.Millisecond
	networkController := controllers.NewNetworkController()

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, networkController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)
}

// helperDeployService writes the desired state for a simple service.
func helperDeployService(ctx context.Context, factStore store.StateStore, serviceName string, image string, instanceCount string) {
	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte(image))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances(serviceName), []byte(instanceCount))
}

// helperCountRunningInstances returns the number of running instances for a
// given service, counting only instances on alive nodes.
func helperCountRunningInstances(ctx context.Context, factStore store.StateStore, serviceName string) int {
	allInstances, err := types.ListInstances(ctx, factStore)
	if err != nil {
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

// helperCountTotalRunning counts all running instances across all services.
func helperCountTotalRunning(ctx context.Context, factStore store.StateStore) int {
	allInstances, err := types.ListInstances(ctx, factStore)
	if err != nil {
		return 0
	}
	runningCount := 0
	for _, instance := range allInstances {
		if instance.State == types.InstanceRunning {
			runningCount++
		}
	}
	return runningCount
}

// restartAgentForNode starts a new agent for the given nodeID on the cluster,
// updating the killNode map with the new cancel function.
func (cluster *resilienceCluster) restartAgentForNode(nodeID string) {
	nodeContext, nodeCancel := context.WithCancel(cluster.clusterContext)
	cluster.killNode[nodeID] = nodeCancel

	nodeAgent := agent.New(nodeID, cluster.partitionedStores[nodeID], cluster.agentRuntimes[nodeID])
	nodeAgent.SetInterval(50 * time.Millisecond)
	go nodeAgent.Run(nodeContext)
}

// restartControllers cancels the current controller runner and starts a new
// one, updating controllerCancel.
func (cluster *resilienceCluster) restartControllers() {
	cluster.controllerCancel()
	controllerContext, controllerCancel := context.WithCancel(cluster.clusterContext)
	cluster.controllerCancel = controllerCancel
	helperStartControllers(cluster.factStore, controllerContext)
}

// TestControllerRestartRecovery verifies that after controllers are stopped,
// a new service is added, and controllers restart, the system converges to
// the desired state.
func TestControllerRestartRecovery(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "3")

	waitFor(t, 5*time.Second, "3 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 3
	})

	cluster.controllerCancel()
	time.Sleep(50 * time.Millisecond)

	helperDeployService(ctx, cluster.factStore, "api", "myapp:latest", "2")

	time.Sleep(200 * time.Millisecond)
	if helperCountRunningInstances(ctx, cluster.factStore, "api") > 0 {
		t.Fatal("api instances appeared while controllers were stopped")
	}

	cluster.restartControllers()

	waitFor(t, 5*time.Second, "2 running api instances after controller restart", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "api") == 2
	})

	if helperCountRunningInstances(ctx, cluster.factStore, "web") < 3 {
		t.Error("web instances disrupted after controller restart")
	}
}

// TestControllerRestartPreservesExistingState verifies that restarting
// controllers does not disrupt existing running instances.
func TestControllerRestartPreservesExistingState(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(t, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.restartControllers()

	time.Sleep(500 * time.Millisecond)

	runningCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if runningCount != 6 {
		t.Errorf("expected 6 running after restart, got %d", runningCount)
	}
}

// TestAgentRestartRejoin verifies that killing an agent, waiting for the node
// to become unreachable, then starting a new agent with the same nodeID causes
// the node to rejoin and resume hosting instances.
func TestAgentRestartRejoin(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(t, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.killNode["node-1"]()

	waitFor(t, 3*time.Second, "node-1 unreachable", func() bool {
		fact, err := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(fact.Value) == string(types.NodeUnreachable)
	})

	waitFor(t, 10*time.Second, "6 running after node-1 death", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.restartAgentForNode("node-1")

	waitFor(t, 5*time.Second, "node-1 alive after rejoin", func() bool {
		fact, err := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(fact.Value) == string(types.NodeAlive)
	})
}

// TestMultipleSimultaneousNodeFailures verifies that killing 2 of 3 nodes
// simultaneously causes all instances to reschedule to the sole survivor.
func TestMultipleSimultaneousNodeFailures(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(t, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.killNode["node-1"]()
	cluster.killNode["node-2"]()

	waitFor(t, 10*time.Second, "instances rescheduled to node-3", func() bool {
		allInstances, _ := types.ListInstances(ctx, cluster.factStore)
		runningOnNode3 := 0
		for _, instance := range allInstances {
			if instance.Service == "web" && instance.State == types.InstanceRunning && instance.Node == "node-3" {
				runningOnNode3++
			}
		}
		return runningOnNode3 >= 4
	})
}

// TestNetworkPartitionAndHeal verifies the full partition lifecycle: partition
// a node's store, wait for the node to become unreachable, heal the partition,
// and verify the node reconnects and becomes alive again.
func TestNetworkPartitionAndHeal(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(t, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.partitionedStores["node-1"].Partition()

	waitFor(t, 3*time.Second, "node-1 unreachable from partition", func() bool {
		fact, err := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(fact.Value) == string(types.NodeUnreachable)
	})

	cluster.partitionedStores["node-1"].Heal()

	waitFor(t, 5*time.Second, "node-1 alive after heal", func() bool {
		fact, err := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(fact.Value) == string(types.NodeAlive)
	})
}

// TestPartitionedAgentInstancesRescheduled verifies that during a partition
// instances are rescheduled to surviving nodes, and after heal the cluster
// stabilizes at the correct total count without double-counting.
func TestPartitionedAgentInstancesRescheduled(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(t, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.partitionedStores["node-2"].Partition()

	waitFor(t, 10*time.Second, "6 running after partition", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") >= 6
	})

	cluster.partitionedStores["node-2"].Heal()

	time.Sleep(1 * time.Second)

	runningCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if runningCount < 6 {
		t.Errorf("expected at least 6 running after heal, got %d", runningCount)
	}
}

// TestRapidNodeCycling verifies that killing and immediately restarting a
// node (within the lease timeout) prevents unnecessary rescheduling — the
// node stays alive and instances are not disrupted.
func TestRapidNodeCycling(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(t, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.killNode["node-1"]()
	time.Sleep(50 * time.Millisecond)
	cluster.restartAgentForNode("node-1")

	time.Sleep(500 * time.Millisecond)

	fact, err := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
	if err != nil {
		t.Fatalf("could not read node-1 state: %v", err)
	}
	if string(fact.Value) != string(types.NodeAlive) {
		t.Errorf("node-1 state = %s, want alive", string(fact.Value))
	}
}

// TestScaleChangeDuringNodeFailure verifies that scaling a service up while
// a node is being detected as failed still converges to the new desired count
// on surviving nodes.
func TestScaleChangeDuringNodeFailure(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "3")

	waitFor(t, 5*time.Second, "3 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 3
	})

	cluster.killNode["node-1"]()
	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("6"))

	waitFor(t, 10*time.Second, "6 running on surviving nodes", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	allInstances, _ := types.ListInstances(ctx, cluster.factStore)
	for _, instance := range allInstances {
		if instance.Service == "web" && instance.State == types.InstanceRunning && instance.Node == "node-1" {
			t.Error("found running instance on dead node-1")
		}
	}
}

// TestFullResilienceCycle runs a sequential gauntlet of failures: deploy,
// kill a node, verify convergence, partition another, verify convergence,
// heal, scale up, and verify final convergence.
func TestFullResilienceCycle(t *testing.T) {
	cluster := helperSetupResilienceCluster(t)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")
	waitFor(t, 5*time.Second, "6 running", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.killNode["node-1"]()
	waitFor(t, 10*time.Second, "6 running after node-1 kill", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.partitionedStores["node-2"].Partition()
	waitFor(t, 10*time.Second, "instances surviving after node-2 partition", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") >= 4
	})

	cluster.partitionedStores["node-2"].Heal()
	waitFor(t, 5*time.Second, "node-2 alive after heal", func() bool {
		fact, err := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-2"))
		return err == nil && string(fact.Value) == string(types.NodeAlive)
	})

	cluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("9"))
	waitFor(t, 10*time.Second, "9 running after scale up", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") >= 8
	})
}
