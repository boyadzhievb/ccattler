package tenant

import (
	"context"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// resourceNode represents a tenant-owned resource in the ownership graph.
// BFS traversal from the tenant root discovers all transitively owned
// resources for garbage collection.
type resourceNode struct {
	// resourceType identifies the kind of resource (e.g., "service", "volume").
	resourceType string
	// resourceName is the unique name within its type.
	resourceName string
	// factKeys lists the store keys that comprise this resource's state.
	factKeys []string
}

// GarbageCollector performs BFS-based garbage collection of tenant resources.
// Starting from the tenant root, it discovers all owned resources layer by
// layer: tenant → services/volumes → instances → endpoints/placements.
type GarbageCollector struct {
	factStore store.StateStore
	registry  *TenantRegistry
}

// NewGarbageCollector creates a garbage collector backed by the given store.
func NewGarbageCollector(factStore store.StateStore, registry *TenantRegistry) *GarbageCollector {
	return &GarbageCollector{factStore: factStore, registry: registry}
}

// GarbageCollectionResult describes what was discovered and deleted.
type GarbageCollectionResult struct {
	// ResourcesByLevel groups discovered resources by their BFS depth level.
	ResourcesByLevel [][]resourceNode
	// TotalKeysDeleted is the count of fact keys removed from the store.
	TotalKeysDeleted int
}

// CollectTenantResources performs a BFS traversal starting from the tenant
// root to discover all transitively owned resources. Returns the resources
// grouped by depth level without deleting anything.
func (garbageCollector *GarbageCollector) CollectTenantResources(ctx context.Context, tenantName string) ([][]resourceNode, error) {
	var resourceLevels [][]resourceNode

	// Level 0: the tenant root itself.
	tenantRoot := garbageCollector.discoverTenantRootKeys(ctx, tenantName)
	if len(tenantRoot) == 0 {
		return nil, nil
	}
	resourceLevels = append(resourceLevels, tenantRoot)

	// Level 1: direct children — services and volumes owned by this tenant.
	directChildren := garbageCollector.discoverDirectChildren(ctx, tenantName)
	if len(directChildren) > 0 {
		resourceLevels = append(resourceLevels, directChildren)
	}

	// Level 2: instances belonging to discovered services.
	serviceNames := extractServiceNames(directChildren)
	instances := garbageCollector.discoverInstances(ctx, serviceNames)
	if len(instances) > 0 {
		resourceLevels = append(resourceLevels, instances)
	}

	// Level 3: endpoints and placements for discovered services/instances.
	instanceIDs := extractInstanceIDs(instances)
	leafResources := garbageCollector.discoverLeafResources(ctx, serviceNames, instanceIDs)
	if len(leafResources) > 0 {
		resourceLevels = append(resourceLevels, leafResources)
	}

	return resourceLevels, nil
}

// DeleteTenantResources performs BFS collection then deletes all discovered
// resources in reverse level order (leaves first, root last) to maintain
// referential integrity.
func (garbageCollector *GarbageCollector) DeleteTenantResources(ctx context.Context, tenantName string) (*GarbageCollectionResult, error) {
	resourceLevels, collectError := garbageCollector.CollectTenantResources(ctx, tenantName)
	if collectError != nil {
		return nil, collectError
	}

	result := &GarbageCollectionResult{
		ResourcesByLevel: resourceLevels,
	}

	// Delete in reverse BFS order: deepest level first.
	for levelIndex := len(resourceLevels) - 1; levelIndex >= 0; levelIndex-- {
		for _, resource := range resourceLevels[levelIndex] {
			for _, factKey := range resource.factKeys {
				garbageCollector.factStore.Delete(ctx, factKey)
				result.TotalKeysDeleted++
			}
		}
	}

	return result, nil
}

// discoverTenantRootKeys finds all fact keys directly belonging to the tenant.
func (garbageCollector *GarbageCollector) discoverTenantRootKeys(ctx context.Context, tenantName string) []resourceNode {
	tenantPrefix := types.PrefixDesiredTenant + tenantName + "/"
	tenantFacts, scanError := garbageCollector.factStore.Scan(ctx, tenantPrefix)
	if scanError != nil || len(tenantFacts) == 0 {
		return nil
	}

	tenantKeys := make([]string, 0, len(tenantFacts)+1)
	tenantMarkerKey := types.KeyDesiredTenant(tenantName)
	if _, getError := garbageCollector.factStore.Get(ctx, tenantMarkerKey); getError == nil {
		tenantKeys = append(tenantKeys, tenantMarkerKey)
	}
	for _, fact := range tenantFacts {
		tenantKeys = append(tenantKeys, fact.Key)
	}

	// Also collect observed usage facts.
	observedPrefix := types.PrefixObservedTenant + tenantName + "/"
	usageFacts, _ := garbageCollector.factStore.Scan(ctx, observedPrefix)
	for _, fact := range usageFacts {
		tenantKeys = append(tenantKeys, fact.Key)
	}

	// Collect tenant infrastructure facts (network boundary, secrets, audit).
	infraPrefix := "tenant/" + tenantName + "/"
	infraFacts, _ := garbageCollector.factStore.Scan(ctx, infraPrefix)
	for _, fact := range infraFacts {
		tenantKeys = append(tenantKeys, fact.Key)
	}

	return []resourceNode{{
		resourceType: "tenant",
		resourceName: tenantName,
		factKeys:     tenantKeys,
	}}
}

// discoverDirectChildren finds services and volumes owned by the tenant.
func (garbageCollector *GarbageCollector) discoverDirectChildren(ctx context.Context, tenantName string) []resourceNode {
	var children []resourceNode

	// Discover services owned by this tenant via the registry.
	serviceFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanDesiredServices)
	serviceKeysMap := make(map[string][]string)
	for _, fact := range serviceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		serviceName, _ := extractServiceNameAndField(relativePath)
		owner, ownerError := garbageCollector.registry.ResolveTenantForService(ctx, serviceName)
		if ownerError != nil || owner != tenantName {
			continue
		}
		serviceKeysMap[serviceName] = append(serviceKeysMap[serviceName], fact.Key)
	}

	for serviceName, factKeys := range serviceKeysMap {
		// Also collect effective and intent facts for this service.
		effectiveFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanEffectiveServices+serviceName)
		for _, fact := range effectiveFacts {
			factKeys = append(factKeys, fact.Key)
		}
		intentUserFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanIntentUserServices+serviceName)
		for _, fact := range intentUserFacts {
			factKeys = append(factKeys, fact.Key)
		}
		intentAutoFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanIntentAutoscalerServices+serviceName)
		for _, fact := range intentAutoFacts {
			factKeys = append(factKeys, fact.Key)
		}
		derivedFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanDerivedServices+serviceName)
		for _, fact := range derivedFacts {
			factKeys = append(factKeys, fact.Key)
		}
		observedFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanObservedServices+serviceName)
		for _, fact := range observedFacts {
			factKeys = append(factKeys, fact.Key)
		}

		children = append(children, resourceNode{
			resourceType: "service",
			resourceName: serviceName,
			factKeys:     factKeys,
		})
	}

	// Discover volumes owned by this tenant (hierarchical name prefix).
	volumeFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanDesiredVolumes)
	volumeKeysMap := make(map[string][]string)
	for _, fact := range volumeFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredVolumes)
		firstSegment := strings.SplitN(relativePath, "/", 2)[0]
		if firstSegment == tenantName {
			volumeKeysMap[firstSegment] = append(volumeKeysMap[firstSegment], fact.Key)
		}
	}
	for volumeName, factKeys := range volumeKeysMap {
		children = append(children, resourceNode{
			resourceType: "volume",
			resourceName: volumeName,
			factKeys:     factKeys,
		})
	}

	return children
}

// discoverInstances finds all instances belonging to the given services.
func (garbageCollector *GarbageCollector) discoverInstances(ctx context.Context, serviceNames map[string]bool) []resourceNode {
	if len(serviceNames) == 0 {
		return nil
	}

	instanceFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanObservedInstances)
	instanceServices := make(map[string]string)
	instanceKeys := make(map[string][]string)

	for _, fact := range instanceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		instanceID := parts[0]
		instanceKeys[instanceID] = append(instanceKeys[instanceID], fact.Key)
		if parts[1] == "service" {
			instanceServices[instanceID] = string(fact.Value)
		}
	}

	// Also collect derived instance facts.
	derivedInstanceFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanDerivedInstances)
	for _, fact := range derivedInstanceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) >= 1 {
			instanceID := parts[0]
			instanceKeys[instanceID] = append(instanceKeys[instanceID], fact.Key)
		}
	}

	var instances []resourceNode
	for instanceID, serviceName := range instanceServices {
		if serviceNames[serviceName] {
			instances = append(instances, resourceNode{
				resourceType: "instance",
				resourceName: instanceID,
				factKeys:     instanceKeys[instanceID],
			})
		}
	}
	return instances
}

// discoverLeafResources finds endpoints and placements for given services/instances.
func (garbageCollector *GarbageCollector) discoverLeafResources(ctx context.Context, serviceNames map[string]bool, instanceIDs map[string]bool) []resourceNode {
	var leaves []resourceNode

	// Endpoints: endpoint/service/{serviceName}/{instanceID}/{port}
	// Service names may contain slashes (e.g., "payments/checkout"), so we
	// match each endpoint key against known service names by prefix.
	for serviceName := range serviceNames {
		serviceEndpointPrefix := types.ScanEndpoints + serviceName + "/"
		endpointFacts, _ := garbageCollector.factStore.Scan(ctx, serviceEndpointPrefix)
		if len(endpointFacts) > 0 {
			factKeys := make([]string, len(endpointFacts))
			for index, fact := range endpointFacts {
				factKeys[index] = fact.Key
			}
			leaves = append(leaves, resourceNode{
				resourceType: "endpoint",
				resourceName: serviceName,
				factKeys:     factKeys,
			})
		}
	}

	// Placements: placement/instance/{instance}
	placementFacts, _ := garbageCollector.factStore.Scan(ctx, types.ScanPlacements)
	for _, fact := range placementFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		instanceID := strings.SplitN(relativePath, "/", 2)[0]
		if instanceIDs[instanceID] {
			leaves = append(leaves, resourceNode{
				resourceType: "placement",
				resourceName: instanceID,
				factKeys:     []string{fact.Key},
			})
		}
	}

	return leaves
}

// extractServiceNames returns a set of service names from resource nodes.
func extractServiceNames(nodes []resourceNode) map[string]bool {
	services := make(map[string]bool)
	for _, node := range nodes {
		if node.resourceType == "service" {
			services[node.resourceName] = true
		}
	}
	return services
}

// extractInstanceIDs returns a set of instance IDs from resource nodes.
func extractInstanceIDs(nodes []resourceNode) map[string]bool {
	instances := make(map[string]bool)
	for _, node := range nodes {
		if node.resourceType == "instance" {
			instances[node.resourceName] = true
		}
	}
	return instances
}
