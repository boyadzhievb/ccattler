// Package scheduler implements the CCattler placement scheduler. It assigns
// pending service instances to alive nodes using a least-loaded, resource-aware
// scoring strategy. The scheduler is a pure function of facts: given the current
// set of nodes, instances, placements, and service resource requirements, it
// produces placement changes without side effects.
package scheduler

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Scheduler is the placement controller that assigns pending instances to
// alive nodes. It implements the controllers.Controller interface so it can
// participate in the reconciliation loop.
type Scheduler struct{}

// NewScheduler creates and returns a new Scheduler instance.
func NewScheduler() *Scheduler { return &Scheduler{} }

// Name returns the controller identifier used for logging and registration.
func (s *Scheduler) Name() string { return "scheduler" }

// Watch returns the fact-store key prefixes that the scheduler observes.
// Any change under these prefixes triggers a new reconciliation cycle.
func (s *Scheduler) Watch() []string {
	return []string{
		types.ScanPlacements,
		types.ScanObservedNodes,
		types.ScanObservedInstances,
		types.ScanDesiredServices,
	}
}

// Reconcile examines the current facts to find pending instances that lack a
// placement, then assigns each one to the alive node with the lowest load and
// sufficient available resources. It returns a list of proposed placement
// changes (one per newly placed instance) or nil if no work is needed.
func (s *Scheduler) Reconcile(_ context.Context, facts []store.Fact) ([]controllers.Change, error) {
	nodes := extractNodeInfoFromFacts(facts)
	instances := extractInstanceInfoFromFacts(facts)
	placements := extractPlacementsFromFacts(facts)
	svcResources := extractServiceResourcesFromFacts(facts)

	// Find pending instances that have no placement.
	var unplaced []string
	for id, inst := range instances {
		if inst.state == types.InstancePending && placements[id] == "" {
			unplaced = append(unplaced, id)
		}
	}
	if len(unplaced) == 0 {
		return nil, nil
	}
	sort.Strings(unplaced)

	// Count existing placements per node and track consumed resources.
	loadPerNode := make(map[string]int)
	usedCPU := make(map[string]int64)
	usedMemory := make(map[string]int64)
	for instID, nodeID := range placements {
		loadPerNode[nodeID]++
		if inst := instances[instID]; inst != nil {
			if resource, ok := svcResources[inst.service]; ok {
				usedCPU[nodeID] += resource.cpu
				usedMemory[nodeID] += resource.memory
			}
		}
	}

	// Build alive node list with available resources.
	var alive []candidateNode
	for _, node := range nodes {
		if node.state != types.NodeAlive {
			continue
		}
		alive = append(alive, candidateNode{
			id:          node.id,
			availCPU:    node.availCPU - usedCPU[node.id],
			availMemory: node.availMemory - usedMemory[node.id],
		})
	}
	if len(alive) == 0 {
		return nil, nil
	}
	sort.Slice(alive, func(i, j int) bool {
		return alive[i].id < alive[j].id
	})

	var changes []controllers.Change
	for _, instID := range unplaced {
		inst := instances[instID]
		var reqCPU, reqMemory int64
		if inst != nil {
			if resource, ok := svcResources[inst.service]; ok {
				reqCPU = resource.cpu
				reqMemory = resource.memory
			}
		}

		best := selectLeastLoadedNode(alive, loadPerNode, reqCPU, reqMemory)
		if best == "" {
			continue
		}
		changes = append(changes, controllers.Change{
			Type:  store.OpPut,
			Key:   types.KeyPlacementInstance(instID),
			Value: []byte(best),
		})
		loadPerNode[best]++
		// Update available resources for subsequent placements in this cycle.
		for i := range alive {
			if alive[i].id == best {
				alive[i].availCPU -= reqCPU
				alive[i].availMemory -= reqMemory
				break
			}
		}
	}

	return changes, nil
}

// candidateNode represents a node that is eligible for instance placement,
// carrying the remaining resource capacity after accounting for existing
// placements.
type candidateNode struct {
	// id is the unique identifier of the node (e.g. "node-1").
	id string
	// availCPU is the remaining CPU capacity in millicores after subtracting
	// resources consumed by already-placed instances.
	availCPU int64
	// availMemory is the remaining memory capacity in MiB after subtracting
	// resources consumed by already-placed instances.
	availMemory int64
}

// selectLeastLoadedNode picks the alive node with sufficient resources and the
// lowest current instance count (load). It returns the node ID of the best
// candidate, or an empty string if no node can satisfy the requirements.
func selectLeastLoadedNode(alive []candidateNode, load map[string]int, reqCPU, reqMemory int64) string {
	best := ""
	bestLoad := math.MaxInt
	for _, node := range alive {
		if reqCPU > 0 && node.availCPU < reqCPU {
			continue
		}
		if reqMemory > 0 && node.availMemory < reqMemory {
			continue
		}
		nodeLoad := load[node.id]
		if nodeLoad < bestLoad {
			bestLoad = nodeLoad
			best = node.id
		}
	}
	return best
}

// serviceResourceRequirements holds the CPU and memory resources that a
// service declares it needs per instance. These values come from the
// desired-services facts in the store.
type serviceResourceRequirements struct {
	// cpu is the CPU requirement in millicores (e.g. 500 for 500m).
	cpu int64
	// memory is the memory requirement in MiB (e.g. 512 for 512Mi).
	memory int64
}

// schedulerInstanceInfo captures the service affiliation and lifecycle state
// of an observed instance. It is used internally by the scheduler to decide
// which instances need placement.
type schedulerInstanceInfo struct {
	// service is the name of the service this instance belongs to.
	service string
	// state is the current lifecycle state (e.g. pending, running, failed).
	state types.InstanceState
}

// schedulerNodeInfo captures the identity, health state, and available resource
// capacity of an observed node. It is used internally by the scheduler to
// filter alive nodes and compute resource fit.
type schedulerNodeInfo struct {
	// id is the unique identifier of this node.
	id string
	// state is the node's health state (e.g. alive, unreachable).
	state types.NodeState
	// availCPU is the total available CPU in millicores as reported by the node.
	availCPU int64
	// availMemory is the total available memory in MiB as reported by the node.
	availMemory int64
}

// extractInstanceInfoFromFacts parses the flat list of store facts and returns
// a map of instance ID to schedulerInstanceInfo, extracting each instance's
// service name and lifecycle state from the observed-instances key prefix.
func extractInstanceInfoFromFacts(facts []store.Fact) map[string]*schedulerInstanceInfo {
	instances := make(map[string]*schedulerInstanceInfo)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedInstances) {
			continue
		}
		rel := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(rel, "/", 2)
		if len(parts) != 2 {
			continue
		}
		id := parts[0]
		if instances[id] == nil {
			instances[id] = &schedulerInstanceInfo{}
		}
		switch parts[1] {
		case "service":
			instances[id].service = string(fact.Value)
		case "state":
			instances[id].state = types.InstanceState(fact.Value)
		}
	}
	return instances
}

// extractNodeInfoFromFacts parses the flat list of store facts and returns a
// map of node ID to schedulerNodeInfo, extracting each node's state and
// available CPU/memory from the observed-nodes key prefix.
func extractNodeInfoFromFacts(facts []store.Fact) map[string]schedulerNodeInfo {
	nodes := make(map[string]schedulerNodeInfo)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedNodes) {
			continue
		}
		rel := strings.TrimPrefix(fact.Key, types.ScanObservedNodes)
		parts := strings.SplitN(rel, "/", 2)
		if len(parts) == 0 {
			continue
		}
		id := parts[0]
		node := nodes[id]
		node.id = id
		if len(parts) == 2 {
			val := string(fact.Value)
			switch parts[1] {
			case "state":
				node.state = types.NodeState(val)
			case "available/cpu":
				node.availCPU, _ = strconv.ParseInt(val, 10, 64)
			case "available/memory":
				node.availMemory, _ = strconv.ParseInt(val, 10, 64)
			}
		}
		nodes[id] = node
	}
	return nodes
}

// extractPlacementsFromFacts parses the flat list of store facts and returns a
// map of instance ID to the node ID where it is currently placed, using the
// placements key prefix.
func extractPlacementsFromFacts(facts []store.Fact) map[string]string {
	placements := make(map[string]string)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanPlacements) {
			continue
		}
		rel := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		if rel != "" && !strings.Contains(rel, "/") {
			placements[rel] = string(fact.Value)
		}
	}
	return placements
}

// extractServiceResourcesFromFacts parses the flat list of store facts and
// returns a map of service name to serviceResourceRequirements, extracting the
// CPU and memory requirements from the desired-services key prefix.
func extractServiceResourcesFromFacts(facts []store.Fact) map[string]serviceResourceRequirements {
	resources := make(map[string]serviceResourceRequirements)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		rel := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		// rel = "{name}/resources/cpu" or "{name}/resources/memory"
		parts := strings.SplitN(rel, "/", 3)
		if len(parts) != 3 || parts[1] != "resources" {
			continue
		}
		name := parts[0]
		resource := resources[name]
		val, _ := strconv.ParseInt(string(fact.Value), 10, 64)
		switch parts[2] {
		case "cpu":
			resource.cpu = val
		case "memory":
			resource.memory = val
		}
		resources[name] = resource
	}
	return resources
}
