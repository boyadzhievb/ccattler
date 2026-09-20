package security

import "context"

// PolicyCycle describes a circular dependency chain found in network policies.
type PolicyCycle struct {
	// Services lists the services forming the cycle, in traversal order.
	// The last element connects back to the first.
	Services []string
}

// DetectPolicyCycles finds circular communication patterns in the network
// policy graph. It builds a directed graph from allow rules (source → target)
// and uses DFS with coloring to detect back edges indicating cycles.
func (policyEngine *NetworkPolicyEngine) DetectPolicyCycles(ctx context.Context) ([]PolicyCycle, error) {
	rules, listError := policyEngine.ListRules(ctx)
	if listError != nil {
		return nil, listError
	}

	// Build adjacency list from allow rules. Wildcard sources/targets are
	// excluded since they don't form meaningful directional edges.
	adjacencyList := make(map[string][]string)
	allServices := make(map[string]bool)
	for _, rule := range rules {
		if rule.Action != PolicyAllow {
			continue
		}
		if rule.SourceService == "*" || rule.TargetService == "*" {
			continue
		}
		adjacencyList[rule.SourceService] = append(adjacencyList[rule.SourceService], rule.TargetService)
		allServices[rule.SourceService] = true
		allServices[rule.TargetService] = true
	}

	// DFS with three-color marking to detect back edges.
	// white (unvisited), gray (in current path), black (fully explored).
	const (
		colorWhite = 0
		colorGray  = 1
		colorBlack = 2
	)

	serviceColor := make(map[string]int)
	parentPath := make(map[string]string)
	var detectedCycles []PolicyCycle

	var depthFirstSearch func(service string)
	depthFirstSearch = func(service string) {
		serviceColor[service] = colorGray

		for _, neighbor := range adjacencyList[service] {
			switch serviceColor[neighbor] {
			case colorWhite:
				parentPath[neighbor] = service
				depthFirstSearch(neighbor)

			case colorGray:
				// Back edge found — extract the cycle by walking the path.
				cycle := extractCyclePath(parentPath, service, neighbor)
				detectedCycles = append(detectedCycles, PolicyCycle{Services: cycle})
			}
		}

		serviceColor[service] = colorBlack
	}

	for service := range allServices {
		if serviceColor[service] == colorWhite {
			depthFirstSearch(service)
		}
	}

	return detectedCycles, nil
}

// extractCyclePath walks the parent chain from cycleEnd back to cycleStart
// to reconstruct the cycle path.
func extractCyclePath(parentPath map[string]string, cycleEnd string, cycleStart string) []string {
	var cyclePath []string
	current := cycleEnd
	for current != cycleStart {
		cyclePath = append(cyclePath, current)
		current = parentPath[current]
	}
	cyclePath = append(cyclePath, cycleStart)

	// Reverse to get forward order.
	for left, right := 0, len(cyclePath)-1; left < right; left, right = left+1, right-1 {
		cyclePath[left], cyclePath[right] = cyclePath[right], cyclePath[left]
	}

	return cyclePath
}
