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

// isolationCluster extends the resilience cluster concept with per-controller
// lifecycle management. Each controller group runs in its own Runner with its
// own cancellable context, enabling independent kill/restart of individual
// controller types while the rest of the system continues operating.
type isolationCluster struct {
	// factStore is the shared backing memory store.
	factStore *store.MemoryStore
	// clusterContext is the parent context for everything in the cluster.
	clusterContext context.Context
	// clusterCancel stops the entire cluster.
	clusterCancel context.CancelFunc
	// controllerCancels maps controller name to its cancel function.
	controllerCancels map[string]context.CancelFunc
	// partitionedStores maps nodeID to its PartitionedStore wrapper.
	partitionedStores map[string]*chaos.PartitionedStore
	// nodeAgentCancels maps nodeID to its agent's cancel function.
	nodeAgentCancels map[string]context.CancelFunc
	// agentRuntimes maps nodeID to its SimulatorRuntime.
	agentRuntimes map[string]*runtime.SimulatorRuntime
}

// controllerGroup holds a named set of controllers for isolated lifecycle.
type controllerGroup struct {
	// name identifies this controller group (e.g. "scheduler", "network").
	name string
	// controllers are the controllers in this group.
	controllers []controllers.Controller
}

// helperSetupIsolationCluster creates a 3-node cluster where controllers run
// in separate Runner instances grouped by name, each with its own cancel
// function. This allows killing or restarting individual controller groups
// without affecting others.
func helperSetupIsolationCluster(testHandle *testing.T, groups []controllerGroup) *isolationCluster {
	testHandle.Helper()

	factStore := store.NewMemoryStore()
	clusterContext, clusterCancel := context.WithCancel(context.Background())

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	for _, nodeID := range nodeIDs {
		types.WriteNode(clusterContext, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	controllerCancels := make(map[string]context.CancelFunc, len(groups))
	for _, group := range groups {
		groupContext, groupCancel := context.WithCancel(clusterContext)
		controllerCancels[group.name] = groupCancel
		controllerRunner := controllers.NewRunner(factStore, group.controllers...)
		controllerRunner.SetDebounce(10 * time.Millisecond)
		go controllerRunner.Run(groupContext)
	}

	partitionedStores := make(map[string]*chaos.PartitionedStore, len(nodeIDs))
	nodeAgentCancels := make(map[string]context.CancelFunc, len(nodeIDs))
	agentRuntimes := make(map[string]*runtime.SimulatorRuntime, len(nodeIDs))

	for _, nodeID := range nodeIDs {
		partitionedStore := chaos.NewPartitionedStore(factStore)
		partitionedStores[nodeID] = partitionedStore

		nodeContext, nodeCancel := context.WithCancel(clusterContext)
		nodeAgentCancels[nodeID] = nodeCancel

		simulatorRuntime := runtime.NewSimulatorRuntime()
		agentRuntimes[nodeID] = simulatorRuntime

		nodeAgent := agent.New(nodeID, partitionedStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(nodeContext)
	}

	return &isolationCluster{
		factStore:         factStore,
		clusterContext:    clusterContext,
		clusterCancel:     clusterCancel,
		controllerCancels: controllerCancels,
		partitionedStores: partitionedStores,
		nodeAgentCancels:  nodeAgentCancels,
		agentRuntimes:     agentRuntimes,
	}
}

// killControllerGroup cancels the context for a named controller group.
func (isolationCluster *isolationCluster) killControllerGroup(groupName string) {
	if cancelFunc, exists := isolationCluster.controllerCancels[groupName]; exists {
		cancelFunc()
	}
}

// restartControllerGroup cancels the old group and starts a fresh Runner with
// the given controllers under a new context.
func (isolationCluster *isolationCluster) restartControllerGroup(groupName string, controllerList []controllers.Controller) {
	if cancelFunc, exists := isolationCluster.controllerCancels[groupName]; exists {
		cancelFunc()
	}
	time.Sleep(20 * time.Millisecond)

	groupContext, groupCancel := context.WithCancel(isolationCluster.clusterContext)
	isolationCluster.controllerCancels[groupName] = groupCancel
	controllerRunner := controllers.NewRunner(isolationCluster.factStore, controllerList...)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(groupContext)
}

// defaultIsolationGroups returns the standard controller groups split by
// functional area, each independently cancellable.
func defaultIsolationGroups() []controllerGroup {
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 300 * time.Millisecond

	return []controllerGroup{
		{name: "instance", controllers: []controllers.Controller{controllers.NewInstanceController()}},
		{name: "scheduler", controllers: []controllers.Controller{scheduler.NewScheduler()}},
		{name: "endpoint", controllers: []controllers.Controller{controllers.NewEndpointController()}},
		{name: "failure", controllers: []controllers.Controller{controllers.NewFailureController()}},
		{name: "node-failure", controllers: []controllers.Controller{nodeFailureController}},
		{name: "network", controllers: []controllers.Controller{controllers.NewNetworkController()}},
	}
}

// TestControllerIsolation_SchedulerCrash verifies that killing the scheduler
// does not affect already-placed workloads. Instance, endpoint, failure, and
// network controllers continue operating. New instances cannot be placed until
// the scheduler is restarted, at which point they converge.
func TestControllerIsolation_SchedulerCrash(testHandle *testing.T) {
	cluster := helperSetupIsolationCluster(testHandle, defaultIsolationGroups())
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(testHandle, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.killControllerGroup("scheduler")
	time.Sleep(50 * time.Millisecond)

	existingRunningCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if existingRunningCount != 6 {
		testHandle.Fatalf("existing instances disrupted after scheduler crash: got %d, want 6", existingRunningCount)
	}

	helperDeployService(ctx, cluster.factStore, "api", "myapp:v1", "3")
	time.Sleep(500 * time.Millisecond)

	apiPlacedCount := helperCountPlacedInstances(ctx, cluster.factStore, "api")
	if apiPlacedCount > 0 {
		testHandle.Fatalf("api instances got placed without scheduler: %d", apiPlacedCount)
	}

	webStillRunning := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if webStillRunning != 6 {
		testHandle.Errorf("web instances changed while scheduler was down: got %d, want 6", webStillRunning)
	}

	cluster.restartControllerGroup("scheduler", []controllers.Controller{scheduler.NewScheduler()})

	waitFor(testHandle, 5*time.Second, "3 running api instances after scheduler restart", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "api") == 3
	})

	webFinalCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if webFinalCount != 6 {
		testHandle.Errorf("web instance count changed: got %d, want 6", webFinalCount)
	}
}

// TestControllerIsolation_NetworkControllerCrash verifies that killing the
// network controller does not prevent the scheduler from placing instances or
// agents from starting them. Only endpoint/DNS/VIP reconciliation stops.
func TestControllerIsolation_NetworkControllerCrash(testHandle *testing.T) {
	cluster := helperSetupIsolationCluster(testHandle, defaultIsolationGroups())
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "3")

	waitFor(testHandle, 5*time.Second, "3 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 3
	})

	cluster.killControllerGroup("network")
	time.Sleep(50 * time.Millisecond)

	helperDeployService(ctx, cluster.factStore, "api", "myapp:v1", "3")

	waitFor(testHandle, 5*time.Second, "3 running api instances without network controller", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "api") == 3
	})

	webStillRunning := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if webStillRunning != 3 {
		testHandle.Errorf("web instances disrupted: got %d, want 3", webStillRunning)
	}
}

// TestControllerIsolation_InstanceControllerRestart verifies that restarting
// the instance controller mid-reconciliation produces no duplicate instances
// and no orphaned instances. All services converge to their desired counts.
func TestControllerIsolation_InstanceControllerRestart(testHandle *testing.T) {
	cluster := helperSetupIsolationCluster(testHandle, defaultIsolationGroups())
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(testHandle, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.restartControllerGroup("instance", []controllers.Controller{controllers.NewInstanceController()})

	helperDeployService(ctx, cluster.factStore, "api", "myapp:v1", "4")

	waitFor(testHandle, 5*time.Second, "4 running api instances after instance controller restart", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "api") == 4
	})

	webCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if webCount != 6 {
		testHandle.Errorf("web duplicated or lost after instance controller restart: got %d, want 6", webCount)
	}

	apiCount := helperCountRunningInstances(ctx, cluster.factStore, "api")
	if apiCount != 4 {
		testHandle.Errorf("api count wrong: got %d, want 4", apiCount)
	}
}

// TestControllerIsolation_AllControllersRestart verifies that stopping all
// controllers at once does not disrupt running workloads (agents keep them
// alive), and that restarting controllers resumes reconciliation to converge
// new desired state.
func TestControllerIsolation_AllControllersRestart(testHandle *testing.T) {
	cluster := helperSetupIsolationCluster(testHandle, defaultIsolationGroups())
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(testHandle, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	for _, groupName := range []string{"instance", "scheduler", "endpoint", "failure", "node-failure", "network"} {
		cluster.killControllerGroup(groupName)
	}
	time.Sleep(100 * time.Millisecond)

	webDuringOutage := helperCountRunningInstances(ctx, cluster.factStore, "web")
	if webDuringOutage != 6 {
		testHandle.Fatalf("workloads disrupted during controller outage: got %d, want 6", webDuringOutage)
	}

	helperDeployService(ctx, cluster.factStore, "api", "myapp:v1", "2")
	time.Sleep(300 * time.Millisecond)

	apiDuringOutage := helperCountRunningInstances(ctx, cluster.factStore, "api")
	if apiDuringOutage > 0 {
		testHandle.Fatalf("api instances appeared without controllers: %d", apiDuringOutage)
	}

	for _, group := range defaultIsolationGroups() {
		cluster.restartControllerGroup(group.name, group.controllers)
	}

	waitFor(testHandle, 5*time.Second, "web still 6 and api converges to 2", func() bool {
		webCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
		apiCount := helperCountRunningInstances(ctx, cluster.factStore, "api")
		return webCount == 6 && apiCount == 2
	})
}

// TestConcurrentControllerRecovery verifies that killing the scheduler,
// instance, and endpoint controllers simultaneously, then restarting them all,
// produces no conflicts. Optimistic concurrency (store transactions) prevents
// double-writes when multiple controllers reconcile concurrently after restart.
func TestConcurrentControllerRecovery(testHandle *testing.T) {
	cluster := helperSetupIsolationCluster(testHandle, defaultIsolationGroups())
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")
	helperDeployService(ctx, cluster.factStore, "api", "myapp:v1", "4")

	waitFor(testHandle, 5*time.Second, "web=6 and api=4 running", func() bool {
		webCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
		apiCount := helperCountRunningInstances(ctx, cluster.factStore, "api")
		return webCount == 6 && apiCount == 4
	})

	cluster.killControllerGroup("scheduler")
	cluster.killControllerGroup("instance")
	cluster.killControllerGroup("endpoint")
	time.Sleep(50 * time.Millisecond)

	cluster.restartControllerGroup("scheduler", []controllers.Controller{scheduler.NewScheduler()})
	cluster.restartControllerGroup("instance", []controllers.Controller{controllers.NewInstanceController()})
	cluster.restartControllerGroup("endpoint", []controllers.Controller{controllers.NewEndpointController()})

	helperDeployService(ctx, cluster.factStore, "db", "postgres:16", "2")

	waitFor(testHandle, 5*time.Second, "all 3 services at desired count", func() bool {
		webCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
		apiCount := helperCountRunningInstances(ctx, cluster.factStore, "api")
		dbCount := helperCountRunningInstances(ctx, cluster.factStore, "db")
		return webCount == 6 && apiCount == 4 && dbCount == 2
	})

	allInstances, listError := types.ListInstances(ctx, cluster.factStore)
	if listError != nil {
		testHandle.Fatal(listError)
	}
	webInstanceCount := 0
	apiInstanceCount := 0
	dbInstanceCount := 0
	for _, instance := range allInstances {
		if instance.State == types.InstanceStopped {
			continue
		}
		switch instance.Service {
		case "web":
			webInstanceCount++
		case "api":
			apiInstanceCount++
		case "db":
			dbInstanceCount++
		}
	}
	if webInstanceCount != 6 {
		testHandle.Errorf("web has %d non-stopped instances (expected 6) — possible duplicates", webInstanceCount)
	}
	if apiInstanceCount != 4 {
		testHandle.Errorf("api has %d non-stopped instances (expected 4) — possible duplicates", apiInstanceCount)
	}
	if dbInstanceCount != 2 {
		testHandle.Errorf("db has %d non-stopped instances (expected 2) — possible duplicates", dbInstanceCount)
	}
}

// helperCountPlacedInstances returns the number of instances for a service that
// have a placement fact (assigned to a node), regardless of running state.
func helperCountPlacedInstances(ctx context.Context, factStore store.StateStore, serviceName string) int {
	allInstances, listError := types.ListInstances(ctx, factStore)
	if listError != nil {
		return 0
	}
	placedCount := 0
	for _, instance := range allInstances {
		if instance.Service == serviceName && instance.Node != "" {
			placedCount++
		}
	}
	return placedCount
}
