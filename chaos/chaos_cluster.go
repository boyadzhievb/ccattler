package chaos

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// SimulatedChaosCluster implements ChaosCluster for the in-process simulated
// cluster used by integration tests and the CLI demo.
type SimulatedChaosCluster struct {
	// factStore is the shared backing memory store.
	factStore *store.MemoryStore
	// partitionedStores maps nodeID to its PartitionedStore wrapper.
	partitionedStores map[string]*PartitionedStore
	// nodeContextCancels maps nodeID to its agent's cancel function.
	nodeContextCancels map[string]context.CancelFunc
	// nodeAlive tracks which nodes have running agents.
	nodeAlive map[string]bool
	// agentRuntimes maps nodeID to its SimulatorRuntime.
	agentRuntimes map[string]*runtime.SimulatorRuntime
	// controllerCancel cancels the current controller runner.
	controllerCancel context.CancelFunc
	// nodeIDs is the ordered list of node identifiers.
	nodeIDs []string
	// agentInterval is the reconciliation interval for agents.
	agentInterval time.Duration
	// controllerDebounce is the debounce interval for the controller runner.
	controllerDebounce time.Duration
	// leaseTimeout is the NodeFailureController's lease timeout.
	leaseTimeout time.Duration
	// services tracks deployed service names and their current desired counts.
	services map[string]int
	// clusterContext is the parent context for everything in this cluster.
	clusterContext context.Context
	// mutex guards concurrent access to cluster state.
	mutex sync.Mutex
}

// NewSimulatedChaosCluster creates a simulated cluster for chaos testing.
// The cluster is not started until Start is called.
func NewSimulatedChaosCluster(factStore *store.MemoryStore, nodeIDs []string) *SimulatedChaosCluster {
	return &SimulatedChaosCluster{
		factStore:          factStore,
		nodeIDs:            nodeIDs,
		partitionedStores:  make(map[string]*PartitionedStore),
		nodeContextCancels: make(map[string]context.CancelFunc),
		nodeAlive:          make(map[string]bool),
		agentRuntimes:      make(map[string]*runtime.SimulatorRuntime),
		services:           make(map[string]int),
		agentInterval:      50 * time.Millisecond,
		controllerDebounce: 10 * time.Millisecond,
		leaseTimeout:       300 * time.Millisecond,
	}
}

// Start initializes and starts all agents and the controller runner.
func (simulatedCluster *SimulatedChaosCluster) Start(ctx context.Context) {
	simulatedCluster.clusterContext = ctx

	for _, nodeID := range simulatedCluster.nodeIDs {
		types.WriteNode(ctx, simulatedCluster.factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	simulatedCluster.startControllers(ctx)

	for _, nodeID := range simulatedCluster.nodeIDs {
		partitionedStore := NewPartitionedStore(simulatedCluster.factStore)
		simulatedCluster.partitionedStores[nodeID] = partitionedStore

		simulatorRuntime := runtime.NewSimulatorRuntime()
		simulatedCluster.agentRuntimes[nodeID] = simulatorRuntime

		simulatedCluster.startAgent(ctx, nodeID)
	}
}

// DeployService writes desired state for a service with the given instance count.
func (simulatedCluster *SimulatedChaosCluster) DeployService(ctx context.Context, serviceName string, image string, desiredInstances int) {
	simulatedCluster.mutex.Lock()
	simulatedCluster.services[serviceName] = desiredInstances
	simulatedCluster.mutex.Unlock()

	simulatedCluster.factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte(image))
	simulatedCluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances(serviceName), []byte(strconv.Itoa(desiredInstances)))
}

// NodeIDs returns all node identifiers in the cluster.
func (simulatedCluster *SimulatedChaosCluster) NodeIDs() []string {
	return simulatedCluster.nodeIDs
}

// IsNodeAlive reports whether a node's agent is currently running.
func (simulatedCluster *SimulatedChaosCluster) IsNodeAlive(nodeID string) bool {
	simulatedCluster.mutex.Lock()
	defer simulatedCluster.mutex.Unlock()
	return simulatedCluster.nodeAlive[nodeID]
}

// KillNode stops a node's agent by cancelling its context.
func (simulatedCluster *SimulatedChaosCluster) KillNode(nodeID string) {
	simulatedCluster.mutex.Lock()
	defer simulatedCluster.mutex.Unlock()
	if cancelFunc, exists := simulatedCluster.nodeContextCancels[nodeID]; exists {
		cancelFunc()
		simulatedCluster.nodeAlive[nodeID] = false
	}
}

// RestartNode starts a new agent for the given node.
func (simulatedCluster *SimulatedChaosCluster) RestartNode(ctx context.Context, nodeID string) {
	simulatedCluster.startAgent(ctx, nodeID)
}

// PartitionNode activates the network partition for a node's store.
func (simulatedCluster *SimulatedChaosCluster) PartitionNode(nodeID string) {
	if partitionedStore, exists := simulatedCluster.partitionedStores[nodeID]; exists {
		partitionedStore.Partition()
	}
}

// HealNode deactivates the network partition for a node's store.
func (simulatedCluster *SimulatedChaosCluster) HealNode(nodeID string) {
	if partitionedStore, exists := simulatedCluster.partitionedStores[nodeID]; exists {
		partitionedStore.Heal()
	}
}

// IsNodePartitioned reports whether a node is currently partitioned.
func (simulatedCluster *SimulatedChaosCluster) IsNodePartitioned(nodeID string) bool {
	if partitionedStore, exists := simulatedCluster.partitionedStores[nodeID]; exists {
		return partitionedStore.IsPartitioned()
	}
	return false
}

// RestartControllers cancels and restarts the controller runner.
func (simulatedCluster *SimulatedChaosCluster) RestartControllers(ctx context.Context) {
	simulatedCluster.mutex.Lock()
	if simulatedCluster.controllerCancel != nil {
		simulatedCluster.controllerCancel()
	}
	simulatedCluster.mutex.Unlock()

	time.Sleep(10 * time.Millisecond)
	simulatedCluster.startControllers(ctx)
}

// SetServiceScale changes the effective instance count for a service.
func (simulatedCluster *SimulatedChaosCluster) SetServiceScale(ctx context.Context, serviceName string, instanceCount int) {
	simulatedCluster.mutex.Lock()
	simulatedCluster.services[serviceName] = instanceCount
	simulatedCluster.mutex.Unlock()

	simulatedCluster.factStore.Put(ctx, types.KeyEffectiveServiceInstances(serviceName), []byte(strconv.Itoa(instanceCount)))
}

// ServiceNames returns all deployed service names.
func (simulatedCluster *SimulatedChaosCluster) ServiceNames() []string {
	simulatedCluster.mutex.Lock()
	defer simulatedCluster.mutex.Unlock()
	names := make([]string, 0, len(simulatedCluster.services))
	for name := range simulatedCluster.services {
		names = append(names, name)
	}
	return names
}

// CheckConvergence verifies all services have desired == running instance
// counts on alive, non-partitioned nodes. Returns convergence status and a
// human-readable description.
func (simulatedCluster *SimulatedChaosCluster) CheckConvergence(ctx context.Context) (bool, string) {
	simulatedCluster.mutex.Lock()
	servicesCopy := make(map[string]int, len(simulatedCluster.services))
	for name, count := range simulatedCluster.services {
		servicesCopy[name] = count
	}
	simulatedCluster.mutex.Unlock()

	allInstances, err := types.ListInstances(ctx, simulatedCluster.factStore)
	if err != nil {
		return false, "failed to list instances"
	}

	description := ""
	allConverged := true
	for serviceName, desiredCount := range servicesCopy {
		runningCount := 0
		for _, instance := range allInstances {
			if instance.Service == serviceName && instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		serviceStatus := fmt.Sprintf("%s: %d/%d", serviceName, runningCount, desiredCount)
		if description != "" {
			description += ", "
		}
		description += serviceStatus
		if runningCount != desiredCount {
			allConverged = false
		}
	}

	return allConverged, description
}

// startControllers creates and starts all controllers on a new Runner.
func (simulatedCluster *SimulatedChaosCluster) startControllers(ctx context.Context) {
	controllerContext, controllerCancel := context.WithCancel(ctx)

	simulatedCluster.mutex.Lock()
	simulatedCluster.controllerCancel = controllerCancel
	simulatedCluster.mutex.Unlock()

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = simulatedCluster.leaseTimeout
	networkController := controllers.NewNetworkController()

	controllerRunner := controllers.NewRunner(simulatedCluster.factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, networkController)
	controllerRunner.SetDebounce(simulatedCluster.controllerDebounce)
	go controllerRunner.Run(controllerContext)
}

// startAgent creates and starts a new agent for the given node.
func (simulatedCluster *SimulatedChaosCluster) startAgent(ctx context.Context, nodeID string) {
	nodeContext, nodeCancel := context.WithCancel(ctx)

	simulatedCluster.mutex.Lock()
	simulatedCluster.nodeContextCancels[nodeID] = nodeCancel
	simulatedCluster.nodeAlive[nodeID] = true
	simulatedCluster.mutex.Unlock()

	nodeAgent := agent.New(nodeID, simulatedCluster.partitionedStores[nodeID], simulatedCluster.agentRuntimes[nodeID])
	nodeAgent.SetInterval(simulatedCluster.agentInterval)
	go nodeAgent.Run(nodeContext)
}
