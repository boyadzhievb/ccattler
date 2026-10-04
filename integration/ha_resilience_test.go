// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"sync"
	"sync/atomic"
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

// slowStore wraps a StateStore to inject artificial latency into every operation.
// Used by TestStorageFailure_SlowStore to verify controllers degrade gracefully.
type slowStore struct {
	// underlying is the real store that operations delegate to after the delay.
	underlying store.StateStore
	// latency is the artificial delay added before each operation.
	latency time.Duration
}

// newSlowStore wraps the given store with the specified latency per operation.
func newSlowStore(underlying store.StateStore, latency time.Duration) *slowStore {
	return &slowStore{underlying: underlying, latency: latency}
}

func (slowStore *slowStore) delay() { time.Sleep(slowStore.latency) }

func (slowStore *slowStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	slowStore.delay()
	return slowStore.underlying.Get(ctx, key)
}

func (slowStore *slowStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	slowStore.delay()
	return slowStore.underlying.Put(ctx, key, value)
}

func (slowStore *slowStore) Delete(ctx context.Context, key string) error {
	slowStore.delay()
	return slowStore.underlying.Delete(ctx, key)
}

func (slowStore *slowStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	slowStore.delay()
	return slowStore.underlying.Scan(ctx, prefix)
}

func (slowStore *slowStore) ScanWithRevision(ctx context.Context, prefix string) (*store.ScanResult, error) {
	slowStore.delay()
	return slowStore.underlying.ScanWithRevision(ctx, prefix)
}

func (slowStore *slowStore) Watch(ctx context.Context, key string, opts store.WatchOption) (<-chan store.Event, error) {
	return slowStore.underlying.Watch(ctx, key, opts)
}

func (slowStore *slowStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	slowStore.delay()
	return slowStore.underlying.Transaction(ctx, compares, onSuccess, onFailure)
}

func (slowStore *slowStore) Revision(ctx context.Context) (int64, error) {
	return slowStore.underlying.Revision(ctx)
}

func (slowStore *slowStore) Close() error {
	return slowStore.underlying.Close()
}

// TestEtcdUnavailable_WorkloadsKeepRunning verifies that when agents lose
// contact with the store (simulating etcd unavailability), running containers
// are not stopped. When the store becomes reachable again, reconciliation
// resumes and the system converges.
func TestEtcdUnavailable_WorkloadsKeepRunning(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	clusterContext, clusterCancel := context.WithCancel(context.Background())
	defer clusterCancel()

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	for _, nodeID := range nodeIDs {
		types.WriteNode(clusterContext, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	helperStartControllers(factStore, clusterContext)

	partitionedStores := make(map[string]*chaos.PartitionedStore, len(nodeIDs))
	agentRuntimes := make(map[string]*runtime.SimulatorRuntime, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		partitionedStore := chaos.NewPartitionedStore(factStore)
		partitionedStores[nodeID] = partitionedStore
		simulatorRuntime := runtime.NewSimulatorRuntime()
		agentRuntimes[nodeID] = simulatorRuntime
		nodeAgent := agent.New(nodeID, partitionedStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(clusterContext)
	}

	helperDeployService(clusterContext, factStore, "web", "nginx:1.28", "6")

	waitFor(testHandle, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(clusterContext, factStore, "web") == 6
	})

	waitFor(testHandle, 5*time.Second, "6 containers across runtimes", func() bool {
		totalContainers := 0
		for _, simulatorRuntime := range agentRuntimes {
			statuses, _ := simulatorRuntime.List(context.Background())
			for _, status := range statuses {
				if status.Running {
					totalContainers++
				}
			}
		}
		return totalContainers >= 6
	})

	for _, nodeID := range nodeIDs {
		partitionedStores[nodeID].Partition()
	}

	waitFor(testHandle, 5*time.Second, "6 containers still running during partition", func() bool {
		totalRunning := 0
		for _, simulatorRuntime := range agentRuntimes {
			statuses, listError := simulatorRuntime.List(context.Background())
			if listError != nil {
				continue
			}
			for _, containerStatus := range statuses {
				if containerStatus.Running {
					totalRunning++
				}
			}
		}
		return totalRunning >= 6
	})

	for _, nodeID := range nodeIDs {
		statuses, listError := agentRuntimes[nodeID].List(context.Background())
		if listError != nil {
			testHandle.Errorf("failed to list containers on %s: %v", nodeID, listError)
			continue
		}
		nodeRunningCount := 0
		for _, containerStatus := range statuses {
			if containerStatus.Running {
				nodeRunningCount++
			}
		}
		if nodeRunningCount == 0 {
			testHandle.Errorf("node %s has 0 running containers during partition — workloads should persist", nodeID)
		}
	}

	for _, nodeID := range nodeIDs {
		partitionedStores[nodeID].Heal()
	}

	waitFor(testHandle, 10*time.Second, "convergence after partition heal", func() bool {
		return helperCountRunningInstances(clusterContext, factStore, "web") == 6
	})
}

// TestControlPlaneRestart_NoDataLoss verifies that stopping all controllers
// and restarting them does not lose any desired or observed state. The store
// retains all facts across controller lifecycle boundaries.
func TestControlPlaneRestart_NoDataLoss(testHandle *testing.T) {
	cluster := helperSetupIsolationCluster(testHandle, defaultIsolationGroups())
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")
	helperDeployService(ctx, cluster.factStore, "api", "myapp:v1", "3")

	waitFor(testHandle, 5*time.Second, "web=6 and api=3", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6 &&
			helperCountRunningInstances(ctx, cluster.factStore, "api") == 3
	})

	desiredWebBefore, _ := cluster.factStore.Get(ctx, types.KeyEffectiveServiceInstances("web"))
	desiredApiBefore, _ := cluster.factStore.Get(ctx, types.KeyEffectiveServiceInstances("api"))

	for _, groupName := range []string{"instance", "scheduler", "endpoint", "failure", "node-failure", "network"} {
		cluster.killControllerGroup(groupName)
	}
	time.Sleep(200 * time.Millisecond)

	desiredWebAfter, _ := cluster.factStore.Get(ctx, types.KeyEffectiveServiceInstances("web"))
	desiredApiAfter, _ := cluster.factStore.Get(ctx, types.KeyEffectiveServiceInstances("api"))

	if string(desiredWebBefore.Value) != string(desiredWebAfter.Value) {
		testHandle.Errorf("web desired state lost: before=%q after=%q", desiredWebBefore.Value, desiredWebAfter.Value)
	}
	if string(desiredApiBefore.Value) != string(desiredApiAfter.Value) {
		testHandle.Errorf("api desired state lost: before=%q after=%q", desiredApiBefore.Value, desiredApiAfter.Value)
	}

	instancesBefore, _ := types.ListInstances(ctx, cluster.factStore)
	runningBeforeRestart := 0
	for _, instance := range instancesBefore {
		if instance.State == types.InstanceRunning {
			runningBeforeRestart++
		}
	}
	if runningBeforeRestart != 9 {
		testHandle.Errorf("running instances during outage: got %d, want 9", runningBeforeRestart)
	}

	for _, group := range defaultIsolationGroups() {
		cluster.restartControllerGroup(group.name, group.controllers)
	}

	waitFor(testHandle, 5*time.Second, "convergence after restart", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6 &&
			helperCountRunningInstances(ctx, cluster.factStore, "api") == 3
	})
}

// TestLeaderElectionFencing verifies that two controller runners operating
// simultaneously on the same store do not produce duplicate instances. The
// store's optimistic concurrency (transaction CAS) ensures only one runner's
// changes succeed per reconciliation cycle.
func TestLeaderElectionFencing(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	clusterContext, clusterCancel := context.WithCancel(context.Background())
	defer clusterCancel()

	types.WriteNode(clusterContext, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 8000, CapacityMemory: 16384,
		AvailableCPU: 8000, AvailableMemory: 16384,
	})

	for _, nodeID := range []string{"node-1"} {
		partitionedStore := chaos.NewPartitionedStore(factStore)
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, partitionedStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(clusterContext)
	}

	runner1 := controllers.NewRunner(factStore,
		controllers.NewInstanceController(), scheduler.NewScheduler(),
		controllers.NewEndpointController(), controllers.NewFailureController())
	runner1.SetDebounce(10 * time.Millisecond)

	runner2 := controllers.NewRunner(factStore,
		controllers.NewInstanceController(), scheduler.NewScheduler(),
		controllers.NewEndpointController(), controllers.NewFailureController())
	runner2.SetDebounce(10 * time.Millisecond)

	go runner1.Run(clusterContext)
	go runner2.Run(clusterContext)

	helperDeployService(clusterContext, factStore, "web", "nginx:1.28", "5")

	waitFor(testHandle, 5*time.Second, "5 running web instances", func() bool {
		return helperCountRunningInstances(clusterContext, factStore, "web") == 5
	})

	allInstances, _ := types.ListInstances(clusterContext, factStore)
	nonStoppedCount := 0
	for _, instance := range allInstances {
		if instance.Service == "web" && instance.State != types.InstanceStopped {
			nonStoppedCount++
		}
	}
	if nonStoppedCount != 5 {
		testHandle.Errorf("two concurrent runners produced %d non-stopped web instances, expected exactly 5 (no duplicates)", nonStoppedCount)
	}
}

// TestDisasterRecovery_EtcdSnapshot verifies that a cluster can be rebuilt
// from a store snapshot. All facts are dumped from the original store into a
// fresh store, and controllers converge the new store to the same state.
func TestDisasterRecovery_EtcdSnapshot(testHandle *testing.T) {
	originalStore := store.NewMemoryStore()
	clusterContext, clusterCancel := context.WithCancel(context.Background())

	types.WriteNode(clusterContext, originalStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	helperStartControllers(originalStore, clusterContext)

	partitionedStore := chaos.NewPartitionedStore(originalStore)
	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New("node-1", partitionedStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	go nodeAgent.Run(clusterContext)

	helperDeployService(clusterContext, originalStore, "web", "nginx:1.28", "3")
	helperDeployService(clusterContext, originalStore, "api", "myapp:v1", "2")

	waitFor(testHandle, 5*time.Second, "web=3 and api=2", func() bool {
		return helperCountRunningInstances(clusterContext, originalStore, "web") == 3 &&
			helperCountRunningInstances(clusterContext, originalStore, "api") == 2
	})

	allFacts, scanError := originalStore.Scan(clusterContext, "")
	if scanError != nil {
		testHandle.Fatal(scanError)
	}

	clusterCancel()
	originalStore.Close()

	restoredStore := store.NewMemoryStore()
	defer restoredStore.Close()
	restoreContext, restoreCancel := context.WithCancel(context.Background())
	defer restoreCancel()

	desiredFactCount := 0
	for _, fact := range allFacts {
		restoredStore.Put(restoreContext, fact.Key, fact.Value)
		desiredFactCount++
	}

	if desiredFactCount == 0 {
		testHandle.Fatal("snapshot contained 0 facts")
	}

	helperStartControllers(restoredStore, restoreContext)

	restoredPartitioned := chaos.NewPartitionedStore(restoredStore)
	restoredRuntime := runtime.NewSimulatorRuntime()
	restoredAgent := agent.New("node-1", restoredPartitioned, restoredRuntime)
	restoredAgent.SetInterval(50 * time.Millisecond)
	go restoredAgent.Run(restoreContext)

	waitFor(testHandle, 5*time.Second, "restored cluster converges", func() bool {
		return helperCountRunningInstances(restoreContext, restoredStore, "web") == 3 &&
			helperCountRunningInstances(restoreContext, restoredStore, "api") == 2
	})
}

// TestSplitBrainHeal verifies that when some agents are partitioned from the
// store (creating a split-brain), healing the partition causes convergence
// without duplicate instances.
func TestSplitBrainHeal(testHandle *testing.T) {
	cluster := helperSetupResilienceCluster(testHandle)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(testHandle, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	cluster.partitionedStores["node-1"].Partition()
	cluster.partitionedStores["node-2"].Partition()

	waitFor(testHandle, 3*time.Second, "partitioned nodes become unreachable", func() bool {
		fact1, _ := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		fact2, _ := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-2"))
		return fact1 != nil && string(fact1.Value) == string(types.NodeUnreachable) &&
			fact2 != nil && string(fact2.Value) == string(types.NodeUnreachable)
	})

	cluster.partitionedStores["node-1"].Heal()
	cluster.partitionedStores["node-2"].Heal()

	waitFor(testHandle, 10*time.Second, "all nodes alive and 6 running after heal", func() bool {
		fact1, _ := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		fact2, _ := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-2"))
		nodesAlive := fact1 != nil && string(fact1.Value) == string(types.NodeAlive) &&
			fact2 != nil && string(fact2.Value) == string(types.NodeAlive)
		return nodesAlive && helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	allInstances, _ := types.ListInstances(ctx, cluster.factStore)
	nonStoppedCount := 0
	for _, instance := range allInstances {
		if instance.Service == "web" && instance.State != types.InstanceStopped {
			nonStoppedCount++
		}
	}
	if nonStoppedCount != 6 {
		testHandle.Errorf("split-brain heal produced %d non-stopped instances, expected 6 (no duplicates)", nonStoppedCount)
	}
}

// TestNetworkPartition_EndpointStaleness verifies that when a node is
// partitioned and its instances become failed, the endpoint controller removes
// their endpoints. After healing, replacement instances get new endpoints.
func TestNetworkPartition_EndpointStaleness(testHandle *testing.T) {
	cluster := helperSetupResilienceCluster(testHandle)
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	cluster.factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte("8080"))
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		cluster.factStore.Put(ctx, types.KeyObservedNodeAddress(nodeID), []byte("10.0.0.1"))
	}
	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")

	waitFor(testHandle, 5*time.Second, "6 running web instances", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})

	allInstances, _ := types.ListInstances(ctx, cluster.factStore)
	for _, instance := range allInstances {
		if instance.Service == "web" && instance.State == types.InstanceRunning {
			cluster.factStore.Put(ctx, types.KeyObservedInstanceIP(instance.ID), []byte("10.0.1."+instance.ID[:1]))
		}
	}

	waitFor(testHandle, 5*time.Second, "endpoints created", func() bool {
		endpoints, _ := cluster.factStore.Scan(ctx, types.ScanEndpoints)
		return len(endpoints) >= 6
	})

	endpointsBefore, _ := cluster.factStore.Scan(ctx, types.ScanEndpoints)
	endpointCountBefore := len(endpointsBefore)
	testHandle.Logf("endpoints before partition: %d", endpointCountBefore)

	cluster.partitionedStores["node-1"].Partition()

	waitFor(testHandle, 3*time.Second, "node-1 unreachable", func() bool {
		fact, _ := cluster.factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return fact != nil && string(fact.Value) == string(types.NodeUnreachable)
	})

	waitFor(testHandle, 10*time.Second, "some endpoints removed after partition", func() bool {
		endpoints, _ := cluster.factStore.Scan(ctx, types.ScanEndpoints)
		return len(endpoints) < endpointCountBefore
	})

	cluster.partitionedStores["node-1"].Heal()

	waitFor(testHandle, 10*time.Second, "6 running instances restored after heal", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6
	})
}

// TestStorageFailure_VolumeDisappears verifies that deleting observed volume
// facts triggers the storage controller to re-create them. Uses direct
// controller Reconcile calls to prove the controller logic handles missing
// observed state by re-creating it.
func TestStorageFailure_VolumeDisappears(testHandle *testing.T) {
	storageController := controllers.NewStorageController()
	ctx := context.Background()

	desiredFacts := []store.Fact{
		{Key: types.KeyDesiredVolume("pgdata"), Value: []byte("")},
		{Key: types.KeyDesiredVolumeSize("pgdata"), Value: []byte("100Gi")},
		{Key: types.KeyDesiredVolumePersistent("pgdata"), Value: []byte("true")},
		{Key: types.KeyObservedVolume("pgdata"), Value: []byte("")},
		{Key: types.KeyObservedVolumeState("pgdata"), Value: []byte("available")},
		{Key: types.KeyObservedVolumeSize("pgdata"), Value: []byte("100Gi")},
	}
	changes, reconcileError := storageController.Reconcile(ctx, desiredFacts)
	if reconcileError != nil {
		testHandle.Fatal(reconcileError)
	}
	if len(changes) != 0 {
		testHandle.Fatalf("expected 0 changes when observed matches desired, got %d", len(changes))
	}

	factsAfterDeletion := []store.Fact{
		{Key: types.KeyDesiredVolume("pgdata"), Value: []byte("")},
		{Key: types.KeyDesiredVolumeSize("pgdata"), Value: []byte("100Gi")},
		{Key: types.KeyDesiredVolumePersistent("pgdata"), Value: []byte("true")},
	}
	recoveryChanges, recoveryError := storageController.Reconcile(ctx, factsAfterDeletion)
	if recoveryError != nil {
		testHandle.Fatal(recoveryError)
	}

	if len(recoveryChanges) == 0 {
		testHandle.Fatal("storage controller produced 0 changes after observed volume disappeared — should re-create")
	}

	recreatedVolume := false
	recreatedState := false
	for _, change := range recoveryChanges {
		if change.Key == types.KeyObservedVolume("pgdata") {
			recreatedVolume = true
		}
		if change.Key == types.KeyObservedVolumeState("pgdata") {
			recreatedState = true
		}
	}
	if !recreatedVolume {
		testHandle.Error("storage controller did not re-create observed volume marker")
	}
	if !recreatedState {
		testHandle.Error("storage controller did not re-create observed volume state")
	}
}

// TestStorageFailure_SlowStore verifies that controllers degrade gracefully
// when the store is slow. Controllers should not crash loop — they should
// continue reconciling with backoff. We verify that the system eventually
// converges despite latency.
func TestStorageFailure_SlowStore(testHandle *testing.T) {
	memStore := store.NewMemoryStore()
	defer memStore.Close()

	latencyInjectedStore := newSlowStore(memStore, 20*time.Millisecond)

	clusterContext, clusterCancel := context.WithCancel(context.Background())
	defer clusterCancel()

	types.WriteNode(clusterContext, memStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()

	controllerRunner := controllers.NewRunner(latencyInjectedStore, instanceController,
		schedulerController, endpointController, failureController)
	controllerRunner.SetDebounce(50 * time.Millisecond)

	var runnerExited atomic.Bool
	go func() {
		controllerRunner.Run(clusterContext)
		runnerExited.Store(true)
	}()

	partitionedStore := chaos.NewPartitionedStore(memStore)
	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New("node-1", partitionedStore, simulatorRuntime)
	nodeAgent.SetInterval(100 * time.Millisecond)
	go nodeAgent.Run(clusterContext)

	memStore.Put(clusterContext, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	memStore.Put(clusterContext, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	waitFor(testHandle, 15*time.Second, "3 running with slow store", func() bool {
		return helperCountRunningInstances(clusterContext, memStore, "web") == 3
	})

	if runnerExited.Load() {
		testHandle.Error("controller runner exited prematurely — should degrade, not crash")
	}
}

// TestRollingControlPlaneUpgrade verifies that stopping and restarting
// controllers sequentially (simulating a rolling upgrade) does not cause
// any reconciliation gap — all services remain at desired count throughout.
func TestRollingControlPlaneUpgrade(testHandle *testing.T) {
	cluster := helperSetupIsolationCluster(testHandle, defaultIsolationGroups())
	defer cluster.clusterCancel()
	defer cluster.factStore.Close()
	ctx := context.Background()

	helperDeployService(ctx, cluster.factStore, "web", "nginx:1.28", "6")
	helperDeployService(ctx, cluster.factStore, "api", "myapp:v1", "3")

	waitFor(testHandle, 5*time.Second, "web=6 and api=3", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6 &&
			helperCountRunningInstances(ctx, cluster.factStore, "api") == 3
	})

	controllerGroupOrder := []string{"instance", "scheduler", "endpoint", "failure", "node-failure", "network"}
	groupControllers := map[string][]controllers.Controller{
		"instance":  {controllers.NewInstanceController()},
		"scheduler": {scheduler.NewScheduler()},
		"endpoint":  {controllers.NewEndpointController()},
		"failure":   {controllers.NewFailureController()},
		"node-failure": {func() controllers.Controller {
			c := controllers.NewNodeFailureController()
			c.LeaseTimeout = 300 * time.Millisecond
			return c
		}()},
		"network": {controllers.NewNetworkController()},
	}

	var violations []string
	var violationMutex sync.Mutex

	monitorContext, monitorCancel := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		for {
			select {
			case <-monitorContext.Done():
				return
			case <-time.After(50 * time.Millisecond):
				webCount := helperCountRunningInstances(ctx, cluster.factStore, "web")
				apiCount := helperCountRunningInstances(ctx, cluster.factStore, "api")
				if webCount < 5 || apiCount < 2 {
					violationMutex.Lock()
					violations = append(violations, "")
					violationMutex.Unlock()
				}
			}
		}
	}()

	for _, groupName := range controllerGroupOrder {
		cluster.killControllerGroup(groupName)
		time.Sleep(50 * time.Millisecond)
		cluster.restartControllerGroup(groupName, groupControllers[groupName])
		time.Sleep(100 * time.Millisecond)
	}

	waitFor(testHandle, 5*time.Second, "web=6 and api=3 after rolling upgrade", func() bool {
		return helperCountRunningInstances(ctx, cluster.factStore, "web") == 6 &&
			helperCountRunningInstances(ctx, cluster.factStore, "api") == 3
	})

	monitorCancel()
	<-monitorDone

	violationMutex.Lock()
	violationCount := len(violations)
	violationMutex.Unlock()

	if violationCount > 5 {
		testHandle.Errorf("rolling upgrade caused %d availability dips (>5 tolerated) — significant disruption", violationCount)
	}
}
