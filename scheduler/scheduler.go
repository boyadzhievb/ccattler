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
func (placementScheduler *Scheduler) Name() string { return "scheduler" }

// Watch returns the fact-store key prefixes that the scheduler observes.
// Any change under these prefixes triggers a new reconciliation cycle.
func (placementScheduler *Scheduler) Watch() []string {
	return []string{
		types.ScanPlacements,
		types.ScanObservedNodes,
		types.ScanObservedInstances,
		types.ScanDesiredServices,
		types.ScanDesiredGroups,
	}
}

// Reconcile examines the current facts to find pending instances that lack a
// placement, then assigns each one to the alive node with the lowest load and
// sufficient available resources. Placement constraints (architecture, zone
// spread) are applied as filters before selecting the least-loaded node.
// Service group members are co-scheduled onto the same node.
func (placementScheduler *Scheduler) Reconcile(_ context.Context, facts []store.Fact) ([]controllers.Change, error) {
	nodes := extractNodeInfoFromFacts(facts)
	instances := extractInstanceInfoFromFacts(facts)
	placements := extractPlacementsFromFacts(facts)
	serviceResources := extractServiceResourcesFromFacts(facts)
	placementConstraints := extractPlacementConstraints(facts)
	serviceToGroup := extractServiceGroupMemberships(facts)

	unplaced := findUnplacedPendingInstances(instances, placements)
	if len(unplaced) == 0 {
		return nil, nil
	}

	loadPerNode, usedCPU, usedMemory := computeNodeLoadAndResourceUsage(placements, instances, serviceResources)
	alive := buildAliveCandidateNodes(nodes, usedCPU, usedMemory)
	if len(alive) == 0 {
		return nil, nil
	}

	serviceZoneCounts := computeServiceZonePlacements(placements, instances, alive)
	schedulingState := &placementState{
		alive: alive, loadPerNode: loadPerNode,
		serviceZoneCounts: serviceZoneCounts, serviceResources: serviceResources,
		placementConstraints: placementConstraints, instances: instances, placements: placements,
		serviceToGroup: serviceToGroup,
	}

	groupedUnplaced, ungroupedUnplaced := partitionUnplacedByGroup(unplaced, instances, serviceToGroup)

	var changes []controllers.Change
	changes = append(changes, placeGroupedInstances(groupedUnplaced, serviceToGroup, schedulingState)...)
	changes = append(changes, placeUngroupedInstances(ungroupedUnplaced, schedulingState)...)
	return changes, nil
}

// placementState bundles the mutable scheduling state passed between the
// group and ungrouped placement functions within a single reconciliation.
type placementState struct {
	alive                []candidateNode
	loadPerNode          map[string]int
	serviceZoneCounts    map[string]map[string]int
	serviceResources     map[string]serviceResourceRequirements
	placementConstraints map[string]*servicePlacementConstraint
	instances            map[string]*schedulerInstanceInfo
	placements           map[string]string
	serviceToGroup       map[string]string
}

// placeUngroupedInstances runs the standard per-instance placement loop for
// instances that do not belong to any service group.
func placeUngroupedInstances(unplaced []string, state *placementState) []controllers.Change {
	var changes []controllers.Change
	for _, instanceID := range unplaced {
		change := placeSingleInstance(instanceID, state)
		if change != nil {
			changes = append(changes, *change)
		}
	}
	return changes
}

// placeSingleInstance selects the best node for a single instance and returns
// the placement change, or nil if no suitable node is available.
func placeSingleInstance(instanceID string, state *placementState) *controllers.Change {
	instanceInfo := state.instances[instanceID]
	serviceName, reqCPU, reqMemory := resolveInstanceResourceNeeds(instanceInfo, state.serviceResources)

	candidates := filterByConstraints(state.alive, serviceName, state.placementConstraints)
	constraint := state.placementConstraints[serviceName]
	if constraint != nil && constraint.zonePolicy == "spread" {
		candidates = selectZoneSpreadCandidates(candidates, serviceName, state.serviceZoneCounts)
	}
	if constraint != nil && len(constraint.prefer) > 0 {
		candidates = rankByPreferences(candidates, constraint.prefer, state.loadPerNode, reqCPU, reqMemory)
	}

	best := selectLeastLoadedNode(candidates, state.loadPerNode, reqCPU, reqMemory)
	if best == "" {
		return nil
	}
	state.loadPerNode[best]++
	updateNodeResourcesAfterPlacement(state.alive, best, reqCPU, reqMemory, serviceName, state.serviceZoneCounts)
	return &controllers.Change{
		Type: store.OpPut, Key: types.KeyPlacementInstance(instanceID), Value: []byte(best),
	}
}

// resolveInstanceResourceNeeds extracts the service name and resource
// requirements for an instance.
func resolveInstanceResourceNeeds(
	instanceInfo *schedulerInstanceInfo,
	serviceResources map[string]serviceResourceRequirements,
) (string, int64, int64) {
	if instanceInfo == nil {
		return "", 0, 0
	}
	serviceName := instanceInfo.service
	if resource, ok := serviceResources[serviceName]; ok {
		return serviceName, resource.cpu, resource.memory
	}
	return serviceName, 0, 0
}

// findUnplacedPendingInstances returns the sorted list of instance IDs that are
// in the pending state and have no existing placement assignment.
func findUnplacedPendingInstances(instances map[string]*schedulerInstanceInfo, placements map[string]string) []string {
	var unplaced []string
	for instanceID, instanceInfo := range instances {
		if instanceInfo.state == types.InstancePending && placements[instanceID] == "" {
			unplaced = append(unplaced, instanceID)
		}
	}
	sort.Strings(unplaced)
	return unplaced
}

// computeNodeLoadAndResourceUsage iterates over existing placements and computes
// the instance count, consumed CPU, and consumed memory per node. These totals
// are used to determine remaining capacity on each node during scheduling.
func computeNodeLoadAndResourceUsage(
	placements map[string]string,
	instances map[string]*schedulerInstanceInfo,
	serviceResources map[string]serviceResourceRequirements,
) (map[string]int, map[string]int64, map[string]int64) {
	loadPerNode := make(map[string]int)
	usedCPU := make(map[string]int64)
	usedMemory := make(map[string]int64)
	for instanceID, nodeID := range placements {
		loadPerNode[nodeID]++
		if instanceInfo := instances[instanceID]; instanceInfo != nil {
			if resource, ok := serviceResources[instanceInfo.service]; ok {
				usedCPU[nodeID] += resource.cpu
				usedMemory[nodeID] += resource.memory
			}
		}
	}
	return loadPerNode, usedCPU, usedMemory
}

// buildAliveCandidateNodes filters the node map to only alive nodes and computes
// each node's remaining CPU and memory after subtracting already-consumed
// resources. The result is sorted by node ID for deterministic scheduling.
func buildAliveCandidateNodes(nodes map[string]schedulerNodeInfo, usedCPU map[string]int64, usedMemory map[string]int64) []candidateNode {
	var alive []candidateNode
	for _, node := range nodes {
		if node.state != types.NodeAlive {
			continue
		}
		alive = append(alive, candidateNode{
			id:           node.id,
			availCPU:     node.availCPU - usedCPU[node.id],
			availMemory:  node.availMemory - usedMemory[node.id],
			architecture: node.architecture,
			zone:         node.zone,
			labels:       node.labels,
			restrictions: node.restrictions,
		})
	}
	sort.Slice(alive, func(i, j int) bool {
		return alive[i].id < alive[j].id
	})
	return alive
}

// computeServiceZonePlacements counts the number of existing placements per
// zone for each service. This information is used by the zone-spread placement
// strategy to prefer zones with fewer instances.
func computeServiceZonePlacements(
	placements map[string]string,
	instances map[string]*schedulerInstanceInfo,
	aliveNodes []candidateNode,
) map[string]map[string]int {
	serviceZoneCounts := make(map[string]map[string]int)
	for instanceID, nodeID := range placements {
		instanceInfo := instances[instanceID]
		if instanceInfo == nil || instanceInfo.state == types.InstanceStopped {
			continue
		}
		for _, node := range aliveNodes {
			if node.id == nodeID && node.zone != "" {
				if serviceZoneCounts[instanceInfo.service] == nil {
					serviceZoneCounts[instanceInfo.service] = make(map[string]int)
				}
				serviceZoneCounts[instanceInfo.service][node.zone]++
			}
		}
	}
	return serviceZoneCounts
}

// updateNodeResourcesAfterPlacement adjusts the available resources of the
// selected node after a placement decision, and increments the zone count for
// the placed service. This keeps subsequent placement decisions in the same
// reconciliation cycle aware of resources already committed.
func updateNodeResourcesAfterPlacement(
	aliveNodes []candidateNode,
	selectedNodeID string,
	requiredCPU int64,
	requiredMemory int64,
	serviceName string,
	serviceZoneCounts map[string]map[string]int,
) {
	for i := range aliveNodes {
		if aliveNodes[i].id == selectedNodeID {
			aliveNodes[i].availCPU -= requiredCPU
			aliveNodes[i].availMemory -= requiredMemory
			if aliveNodes[i].zone != "" && serviceName != "" {
				if serviceZoneCounts[serviceName] == nil {
					serviceZoneCounts[serviceName] = make(map[string]int)
				}
				serviceZoneCounts[serviceName][aliveNodes[i].zone]++
			}
			break
		}
	}
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
	// architecture is the CPU architecture of the node (e.g. "amd64", "arm64").
	architecture string
	// zone is the availability zone the node resides in.
	zone string
	// labels holds key-value pairs assigned to the node for placement matching.
	labels map[string]string
	// restrictions holds labels that prevent scheduling unless the service accepts them.
	restrictions map[string]string
}

// selectLeastLoadedNode picks the alive node with sufficient resources and the
// lowest current instance count (load). Uses a min-heap for O(log n) selection
// instead of linear scan.
func selectLeastLoadedNode(alive []candidateNode, load map[string]int, reqCPU, reqMemory int64) string {
	heapData, _ := buildNodeHeap(alive, load, reqCPU, reqMemory)
	return selectFromHeap(&heapData)
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
	// architecture is the CPU architecture of this node (e.g. "amd64", "arm64").
	architecture string
	// zone is the availability zone this node resides in.
	zone string
	// labels holds key-value pairs assigned to the node for placement matching.
	labels map[string]string
	// restrictions holds labels that prevent scheduling unless the service accepts them.
	restrictions map[string]string
}

// extractInstanceInfoFromFacts parses the flat list of store facts and returns
// a map of instance ID to schedulerInstanceInfo, extracting each instance's
// service name and lifecycle state from the observed-instances key prefix.
func extractInstanceInfoFromFacts(facts []store.Fact) map[string]*schedulerInstanceInfo {
	instances := make(map[string]*schedulerInstanceInfo)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		instanceID := parts[0]
		if instances[instanceID] == nil {
			instances[instanceID] = &schedulerInstanceInfo{}
		}
		switch parts[1] {
		case "service":
			instances[instanceID].service = string(fact.Value)
		case "state":
			instances[instanceID].state = types.InstanceState(fact.Value)
		}
	}
	return instances
}

// extractNodeInfoFromFacts parses the flat list of store facts and returns a
// map of node ID to schedulerNodeInfo, extracting each node's state and
// available CPU/memory from the observed-nodes key prefix.
func extractNodeInfoFromFacts(facts []store.Fact) map[string]schedulerNodeInfo {
	nodes := make(map[string]schedulerNodeInfo)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedNodes) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedNodes)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 0 {
			continue
		}
		nodeID := parts[0]
		node := nodes[nodeID]
		node.id = nodeID
		if len(parts) == 2 {
			fieldValue := string(fact.Value)
			switch {
			case parts[1] == "state":
				node.state = types.NodeState(fieldValue)
			case parts[1] == "available/cpu":
				node.availCPU, _ = strconv.ParseInt(fieldValue, 10, 64)
			case parts[1] == "available/memory":
				node.availMemory, _ = strconv.ParseInt(fieldValue, 10, 64)
			case parts[1] == "architecture":
				node.architecture = fieldValue
			case parts[1] == "zone":
				node.zone = fieldValue
			case strings.HasPrefix(parts[1], "label/"):
				labelName := strings.TrimPrefix(parts[1], "label/")
				if node.labels == nil {
					node.labels = make(map[string]string)
				}
				node.labels[labelName] = fieldValue
			case strings.HasPrefix(parts[1], "restrict/"):
				restrictLabel := strings.TrimPrefix(parts[1], "restrict/")
				if node.restrictions == nil {
					node.restrictions = make(map[string]string)
				}
				node.restrictions[restrictLabel] = fieldValue
			}
		}
		nodes[nodeID] = node
	}
	return nodes
}

// extractPlacementsFromFacts parses the flat list of store facts and returns a
// map of instance ID to the node ID where it is currently placed, using the
// placements key prefix.
func extractPlacementsFromFacts(facts []store.Fact) map[string]string {
	placements := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		if relativePath != "" && !strings.Contains(relativePath, "/") {
			placements[relativePath] = string(fact.Value)
		}
	}
	return placements
}

// servicePlacementConstraint holds parsed placement constraints for a service.
type servicePlacementConstraint struct {
	// architecture is the required CPU architecture (empty means any).
	architecture string
	// zonePolicy is "spread" for zone-aware distribution, or a specific zone name.
	zonePolicy string
	// require holds hard node label requirements — the node must have each label with the specified value.
	require map[string]string
	// prefer holds soft node label preferences — matching nodes get a scoring bonus.
	prefer map[string]string
	// accept holds node restriction labels this service tolerates — allowing placement on restricted nodes.
	accept map[string]bool
}

// extractPlacementConstraints parses placement constraint facts per service.
func extractPlacementConstraints(facts []store.Fact) map[string]*servicePlacementConstraint {
	constraints := make(map[string]*servicePlacementConstraint)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		serviceName := parts[0]
		suffix := parts[1]

		if constraints[serviceName] == nil {
			constraints[serviceName] = &servicePlacementConstraint{}
		}
		constraint := constraints[serviceName]
		switch {
		case suffix == "placement/architecture":
			constraint.architecture = string(fact.Value)
		case suffix == "placement/zone":
			constraint.zonePolicy = string(fact.Value)
		case strings.HasPrefix(suffix, "placement/require/"):
			labelName := strings.TrimPrefix(suffix, "placement/require/")
			if constraint.require == nil {
				constraint.require = make(map[string]string)
			}
			constraint.require[labelName] = string(fact.Value)
		case strings.HasPrefix(suffix, "placement/prefer/"):
			labelName := strings.TrimPrefix(suffix, "placement/prefer/")
			if constraint.prefer == nil {
				constraint.prefer = make(map[string]string)
			}
			constraint.prefer[labelName] = string(fact.Value)
		case strings.HasPrefix(suffix, "placement/accept/"):
			labelName := strings.TrimPrefix(suffix, "placement/accept/")
			if constraint.accept == nil {
				constraint.accept = make(map[string]bool)
			}
			constraint.accept[labelName] = true
		}
	}
	return constraints
}

// filterByConstraints returns only the candidate nodes that satisfy the
// placement constraints for the given service. This enforces architecture,
// zone, require (hard label match), and restrict/accept (node restriction
// tolerance) constraints.
func filterByConstraints(candidates []candidateNode, serviceName string, constraints map[string]*servicePlacementConstraint) []candidateNode {
	constraint := constraints[serviceName]
	if constraint == nil {
		return filterByRestrictions(candidates, nil)
	}

	var filtered []candidateNode
	for _, candidate := range candidates {
		if constraint.architecture != "" && candidate.architecture != "" && candidate.architecture != constraint.architecture {
			continue
		}
		if constraint.zonePolicy != "" && constraint.zonePolicy != "spread" && candidate.zone != "" && candidate.zone != constraint.zonePolicy {
			continue
		}
		if !satisfiesRequireLabels(candidate, constraint.require) {
			continue
		}
		if !toleratesRestrictions(candidate, constraint.accept) {
			continue
		}
		filtered = append(filtered, candidate)
	}

	if len(filtered) == 0 {
		return candidates
	}
	return filtered
}

// satisfiesRequireLabels checks whether a candidate node has all the required
// label key-value pairs. Returns true if all requirements are met.
func satisfiesRequireLabels(candidate candidateNode, requireLabels map[string]string) bool {
	for label, requiredValue := range requireLabels {
		nodeValue, hasLabel := candidate.labels[label]
		if !hasLabel || nodeValue != requiredValue {
			return false
		}
	}
	return true
}

// toleratesRestrictions checks whether a service accepts all restrictions on a
// candidate node. A node with restrictions can only host services that explicitly
// accept each restriction label. Returns true if the node has no restrictions
// or if the service accepts all of them.
func toleratesRestrictions(candidate candidateNode, acceptLabels map[string]bool) bool {
	for restrictLabel := range candidate.restrictions {
		if !acceptLabels[restrictLabel] {
			return false
		}
	}
	return true
}

// filterByRestrictions removes restricted nodes from candidates when the service
// has no accept declarations. Used when there are no placement constraints at all.
func filterByRestrictions(candidates []candidateNode, acceptLabels map[string]bool) []candidateNode {
	var filtered []candidateNode
	for _, candidate := range candidates {
		if toleratesRestrictions(candidate, acceptLabels) {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		return candidates
	}
	return filtered
}

// rankByPreferences reorders candidates so that nodes matching more prefer
// labels appear first. Among nodes with equal preference scores, the original
// order (which feeds into least-loaded selection) is preserved. This is a soft
// constraint — non-matching nodes remain eligible, they just rank lower.
func rankByPreferences(candidates []candidateNode, preferLabels map[string]string, loadPerNode map[string]int, reqCPU, reqMemory int64) []candidateNode {
	type scoredCandidate struct {
		candidate     candidateNode
		preferScore   int
		originalIndex int
	}

	scored := make([]scoredCandidate, len(candidates))
	for index, candidate := range candidates {
		preferScore := 0
		for label, preferredValue := range preferLabels {
			if candidate.labels[label] == preferredValue {
				preferScore++
			}
		}
		scored[index] = scoredCandidate{
			candidate:     candidate,
			preferScore:   preferScore,
			originalIndex: index,
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].preferScore > scored[j].preferScore
	})

	reordered := make([]candidateNode, len(candidates))
	for index, entry := range scored {
		reordered[index] = entry.candidate
	}
	return reordered
}

// selectZoneSpreadCandidates filters candidates to prefer nodes in the zone
// with the fewest existing instances for this service.
func selectZoneSpreadCandidates(candidates []candidateNode, serviceName string, serviceZoneCounts map[string]map[string]int) []candidateNode {
	if len(candidates) == 0 {
		return candidates
	}

	zoneCounts := serviceZoneCounts[serviceName]
	if zoneCounts == nil {
		zoneCounts = make(map[string]int)
	}

	minZoneCount := math.MaxInt
	for _, candidate := range candidates {
		zone := candidate.zone
		if zone == "" {
			zone = "_default"
		}
		count := zoneCounts[zone]
		if count < minZoneCount {
			minZoneCount = count
		}
	}

	var preferred []candidateNode
	for _, candidate := range candidates {
		zone := candidate.zone
		if zone == "" {
			zone = "_default"
		}
		if zoneCounts[zone] == minZoneCount {
			preferred = append(preferred, candidate)
		}
	}

	if len(preferred) == 0 {
		return candidates
	}
	return preferred
}

// extractServiceGroupMemberships parses desired/group/ facts and returns a map
// from service name to the group name it belongs to. A service can only belong
// to one group.
func extractServiceGroupMemberships(facts []store.Fact) map[string]string {
	serviceToGroup := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredGroups) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredGroups)
		// relativePath = "{groupName}/process/{serviceName}"
		parts := strings.SplitN(relativePath, "/", 3)
		if len(parts) != 3 || parts[1] != "process" {
			continue
		}
		groupName := parts[0]
		serviceName := parts[2]
		serviceToGroup[serviceName] = groupName
	}
	return serviceToGroup
}

// partitionUnplacedByGroup splits unplaced instance IDs into two collections:
// grouped (keyed by group name) and ungrouped (plain list). Deterministic
// ordering is preserved within each group.
func partitionUnplacedByGroup(
	unplaced []string,
	instances map[string]*schedulerInstanceInfo,
	serviceToGroup map[string]string,
) (map[string][]string, []string) {
	groupedUnplaced := make(map[string][]string)
	var ungroupedUnplaced []string
	for _, instanceID := range unplaced {
		instanceInfo := instances[instanceID]
		if instanceInfo == nil {
			ungroupedUnplaced = append(ungroupedUnplaced, instanceID)
			continue
		}
		groupName, inGroup := serviceToGroup[instanceInfo.service]
		if inGroup {
			groupedUnplaced[groupName] = append(groupedUnplaced[groupName], instanceID)
		} else {
			ungroupedUnplaced = append(ungroupedUnplaced, instanceID)
		}
	}
	return groupedUnplaced, ungroupedUnplaced
}

// placeGroupedInstances co-schedules all instances belonging to the same service
// group onto the same node. If any group member is already placed, the remaining
// members join it. Otherwise the node with the most remaining resources that
// fits the combined group requirements is selected.
func placeGroupedInstances(
	groupedUnplaced map[string][]string,
	serviceToGroup map[string]string,
	state *placementState,
) []controllers.Change {
	sortedGroupNames := make([]string, 0, len(groupedUnplaced))
	for groupName := range groupedUnplaced {
		sortedGroupNames = append(sortedGroupNames, groupName)
	}
	sort.Strings(sortedGroupNames)

	var changes []controllers.Change
	for _, groupName := range sortedGroupNames {
		instanceIDs := groupedUnplaced[groupName]
		groupChanges := placeOneGroup(groupName, instanceIDs, state)
		changes = append(changes, groupChanges...)
	}
	return changes
}

// placeOneGroup places all unplaced instances of a single service group onto
// the same node. It first checks if any group member is already placed and
// reuses that node; otherwise it picks the node that fits the combined resource
// requirements of all unplaced members.
func placeOneGroup(
	groupName string,
	instanceIDs []string,
	state *placementState,
) []controllers.Change {
	targetNode := findExistingGroupNode(groupName, state)

	if targetNode == "" {
		totalCPU, totalMemory := computeGroupResourceTotal(instanceIDs, state)
		targetNode = selectLeastLoadedNode(state.alive, state.loadPerNode, totalCPU, totalMemory)
	}
	if targetNode == "" {
		return nil
	}

	var changes []controllers.Change
	for _, instanceID := range instanceIDs {
		instanceInfo := state.instances[instanceID]
		_, reqCPU, reqMemory := resolveInstanceResourceNeeds(instanceInfo, state.serviceResources)
		state.loadPerNode[targetNode]++
		serviceName := ""
		if instanceInfo != nil {
			serviceName = instanceInfo.service
		}
		updateNodeResourcesAfterPlacement(state.alive, targetNode, reqCPU, reqMemory, serviceName, state.serviceZoneCounts)
		changes = append(changes, controllers.Change{
			Type:  store.OpPut,
			Key:   types.KeyPlacementInstance(instanceID),
			Value: []byte(targetNode),
		})
	}
	return changes
}

// findExistingGroupNode looks through already-placed instances to see if any
// instance whose service belongs to the given group is already placed on a node.
// Returns the node ID or empty string if no group member is placed yet. When
// multiple nodes host group members (split state), the first node in sorted
// order is returned for determinism.
func findExistingGroupNode(groupName string, state *placementState) string {
	var candidateNodeIDs []string
	for instanceID, nodeID := range state.placements {
		instanceInfo := state.instances[instanceID]
		if instanceInfo == nil {
			continue
		}
		if state.serviceToGroup[instanceInfo.service] == groupName {
			candidateNodeIDs = append(candidateNodeIDs, nodeID)
		}
	}
	if len(candidateNodeIDs) == 0 {
		return ""
	}
	sort.Strings(candidateNodeIDs)
	return candidateNodeIDs[0]
}

// computeGroupResourceTotal sums the CPU and memory requirements across all
// instances in a group, used for selecting a node with sufficient capacity.
func computeGroupResourceTotal(instanceIDs []string, state *placementState) (int64, int64) {
	var totalCPU, totalMemory int64
	for _, instanceID := range instanceIDs {
		instanceInfo := state.instances[instanceID]
		_, reqCPU, reqMemory := resolveInstanceResourceNeeds(instanceInfo, state.serviceResources)
		totalCPU += reqCPU
		totalMemory += reqMemory
	}
	return totalCPU, totalMemory
}

// extractServiceResourcesFromFacts parses the flat list of store facts and
// returns a map of service name to serviceResourceRequirements, extracting the
// CPU and memory requirements from the desired-services key prefix.
func extractServiceResourcesFromFacts(facts []store.Fact) map[string]serviceResourceRequirements {
	resources := make(map[string]serviceResourceRequirements)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		// relativePath = "{name}/resources/cpu" or "{name}/resources/memory"
		parts := strings.SplitN(relativePath, "/", 3)
		if len(parts) != 3 || parts[1] != "resources" {
			continue
		}
		name := parts[0]
		resource := resources[name]
		parsedValue, _ := strconv.ParseInt(string(fact.Value), 10, 64)
		switch parts[2] {
		case "cpu":
			resource.cpu = parsedValue
		case "memory":
			resource.memory = parsedValue
		}
		resources[name] = resource
	}
	return resources
}
