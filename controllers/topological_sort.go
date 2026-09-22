package controllers

// topologicalSortResult holds the sorted ordering and any controllers
// that could not be ordered due to cyclic dependencies.
type topologicalSortResult struct {
	// sortedControllers is the topologically sorted list of controller names.
	sortedControllers []string
	// cyclicControllers lists controllers involved in dependency cycles.
	cyclicControllers []string
}

// topologicalSort performs Kahn's algorithm on the dependency graph to produce
// a stable execution ordering for controllers. Each node is a controller name;
// edges represent "must run before" relationships. Returns the sorted order
// and any nodes that could not be placed (due to cycles).
func topologicalSort(controllerNames []string, dependencyEdges map[string][]string) topologicalSortResult {
	incomingDegree := make(map[string]int)
	outgoingEdges := make(map[string][]string)

	for _, controllerName := range controllerNames {
		incomingDegree[controllerName] = 0
	}

	for dependent, dependencies := range dependencyEdges {
		for _, dependency := range dependencies {
			if _, exists := incomingDegree[dependency]; !exists {
				continue
			}
			if _, exists := incomingDegree[dependent]; !exists {
				continue
			}
			outgoingEdges[dependency] = append(outgoingEdges[dependency], dependent)
			incomingDegree[dependent]++
		}
	}

	// Seed the queue with controllers that have no incoming dependencies.
	var processingQueue []string
	for _, controllerName := range controllerNames {
		if incomingDegree[controllerName] == 0 {
			processingQueue = append(processingQueue, controllerName)
		}
	}

	var sortedResult []string
	for len(processingQueue) > 0 {
		currentController := processingQueue[0]
		processingQueue = processingQueue[1:]
		sortedResult = append(sortedResult, currentController)

		for _, dependent := range outgoingEdges[currentController] {
			incomingDegree[dependent]--
			if incomingDegree[dependent] == 0 {
				processingQueue = append(processingQueue, dependent)
			}
		}
	}

	var cyclicNodes []string
	if len(sortedResult) < len(controllerNames) {
		sortedSet := make(map[string]bool, len(sortedResult))
		for _, name := range sortedResult {
			sortedSet[name] = true
		}
		for _, controllerName := range controllerNames {
			if !sortedSet[controllerName] {
				cyclicNodes = append(cyclicNodes, controllerName)
			}
		}
	}

	return topologicalSortResult{
		sortedControllers: sortedResult,
		cyclicControllers: cyclicNodes,
	}
}

// controllerOutputPrefixes returns the known output fact prefixes for
// built-in controllers. This is the authoritative write-domain declaration:
// each controller may only write keys that fall under its declared prefixes.
// The runner enforces this at commit time, and the dependency graph uses it
// to order controllers (if B watches a prefix that A writes, B depends on A).
func controllerOutputPrefixes() map[string][]string {
	return map[string][]string{
		"intent-resolver":      {"effective/service/"},
		"instance":             {"observed/instance/"},
		"scheduler":            {"placement/"},
		"endpoint":             {"endpoint/"},
		"failure":              {"observed/instance/", "derived/instance/"},
		"node-failure":         {"observed/node/", "observed/instance/"},
		"network":              {"network/vip/", "network/dns/"},
		"autoscale":            {"intent/autoscaler/"},
		"rollout":              {"observed/instance/", "derived/service/", "desired/service/"},
		"storage":              {"observed/volume/"},
		"warm-zero":            {"derived/service/"},
		"init":                 {"derived/instance/"},
		"credential-broker":    {"derived/credential/"},
		"cloud-node-lifecycle": {"observed/cloud/instance/", "observed/node/"},
		"cloud-loadbalancer":   {"observed/cloud/loadbalancer/"},
		"cloud-routes":         {"observed/cloud/route/"},
	}
}

// buildControllerDependencyGraph creates a dependency map from a set of
// controllers by matching each controller's Watch prefixes against other
// controllers' known output prefixes. Returns a map of controller name to
// the names of controllers it depends on (must run after).
func buildControllerDependencyGraph(controllers []Controller) map[string][]string {
	outputPrefixes := controllerOutputPrefixes()
	dependencyGraph := make(map[string][]string)

	for _, consumer := range controllers {
		consumerName := consumer.Name()
		watchedPrefixes := consumer.Watch()

		for _, producer := range controllers {
			producerName := producer.Name()
			if producerName == consumerName {
				continue
			}
			producerOutputs := outputPrefixes[producerName]
			if hasOverlappingPrefix(watchedPrefixes, producerOutputs) {
				dependencyGraph[consumerName] = append(
					dependencyGraph[consumerName], producerName)
			}
		}
	}

	return dependencyGraph
}

// hasOverlappingPrefix checks if any watched prefix matches or is a prefix
// of any output prefix (or vice versa).
func hasOverlappingPrefix(watchedPrefixes []string, outputPrefixes []string) bool {
	for _, watched := range watchedPrefixes {
		for _, output := range outputPrefixes {
			if prefixOverlaps(watched, output) {
				return true
			}
		}
	}
	return false
}

// prefixOverlaps returns true if either string is a prefix of the other.
func prefixOverlaps(prefixA string, prefixB string) bool {
	if len(prefixA) <= len(prefixB) {
		return prefixB[:len(prefixA)] == prefixA
	}
	return prefixA[:len(prefixB)] == prefixB
}

// SortControllersByDependency returns an execution ordering for the given
// controllers that respects data dependencies. Controllers that produce
// facts consumed by others are scheduled first.
func SortControllersByDependency(controllers []Controller) ([]Controller, []string) {
	controllerNames := make([]string, len(controllers))
	controllerByName := make(map[string]Controller, len(controllers))
	for index, controller := range controllers {
		controllerNames[index] = controller.Name()
		controllerByName[controller.Name()] = controller
	}

	dependencyGraph := buildControllerDependencyGraph(controllers)
	sortResult := topologicalSort(controllerNames, dependencyGraph)

	sortedControllers := make([]Controller, 0, len(sortResult.sortedControllers))
	for _, name := range sortResult.sortedControllers {
		sortedControllers = append(sortedControllers, controllerByName[name])
	}

	return sortedControllers, sortResult.cyclicControllers
}
