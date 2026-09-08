package tenant

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// FairScheduler advises the placement scheduler on tenant-weighted priorities.
// When cluster resources are scarce, tenants with higher weights receive
// proportionally more capacity. Unused guarantees become borrowable — a tenant
// can exceed its guarantee if no other tenant needs the capacity.
type FairScheduler struct {
	factStore store.StateStore
	registry  *TenantRegistry
}

// NewFairScheduler creates a fair scheduler backed by the given store.
func NewFairScheduler(factStore store.StateStore, registry *TenantRegistry) *FairScheduler {
	return &FairScheduler{
		factStore: factStore,
		registry:  registry,
	}
}

// TenantShare describes a tenant's resource allocation and current usage.
type TenantShare struct {
	TenantName     string  // tenant identifier
	Weight         int     // scheduling weight (higher = more resources)
	GuaranteedCPU  int     // guaranteed CPU in millicores based on weight proportion
	CurrentCPU     int     // currently consumed CPU in millicores
	InstanceCount  int     // current number of active instances
	ShareFraction  float64 // fraction of total weight this tenant holds
	Borrowing      int     // CPU borrowed above guarantee (0 if at or below guarantee)
}

// ComputeFairShares calculates the guaranteed resource share for each tenant
// based on their weights and the total cluster capacity. Returns shares
// sorted by priority — tenants furthest below their guarantee come first.
func (fairScheduler *FairScheduler) ComputeFairShares(ctx context.Context, totalClusterCPU int) ([]TenantShare, error) {
	tenants, err := fairScheduler.registry.ListTenants(ctx)
	if err != nil {
		return nil, err
	}

	totalWeight := 0
	for _, tenant := range tenants {
		weight := tenant.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
	}

	if totalWeight == 0 {
		return nil, nil
	}

	shares := make([]TenantShare, 0, len(tenants))
	for _, tenant := range tenants {
		weight := tenant.Weight
		if weight <= 0 {
			weight = 1
		}

		shareFraction := float64(weight) / float64(totalWeight)
		guaranteedCPU := int(shareFraction * float64(totalClusterCPU))
		currentCPU := fairScheduler.computeTenantCPU(ctx, tenant.Name)
		instanceCount := fairScheduler.countTenantInstances(ctx, tenant.Name)

		borrowing := 0
		if currentCPU > guaranteedCPU {
			borrowing = currentCPU - guaranteedCPU
		}

		shares = append(shares, TenantShare{
			TenantName:    tenant.Name,
			Weight:        weight,
			GuaranteedCPU: guaranteedCPU,
			CurrentCPU:    currentCPU,
			InstanceCount: instanceCount,
			ShareFraction: shareFraction,
			Borrowing:     borrowing,
		})
	}

	// Sort by priority: tenants furthest below their guarantee first.
	sort.Slice(shares, func(i, j int) bool {
		deficitI := shares[i].GuaranteedCPU - shares[i].CurrentCPU
		deficitJ := shares[j].GuaranteedCPU - shares[j].CurrentCPU
		return deficitI > deficitJ
	})

	return shares, nil
}

// PrioritizeTenant returns the priority score for a tenant's pending instance.
// Higher scores mean the instance should be scheduled sooner. Tenants below
// their guarantee get the highest priority; tenants borrowing above their
// guarantee get lower priority.
func (fairScheduler *FairScheduler) PrioritizeTenant(ctx context.Context, tenantName string, totalClusterCPU int) int {
	shares, err := fairScheduler.ComputeFairShares(ctx, totalClusterCPU)
	if err != nil {
		return 0
	}

	for _, share := range shares {
		if share.TenantName == tenantName {
			deficit := share.GuaranteedCPU - share.CurrentCPU
			if deficit > 0 {
				return 1000 + deficit
			}
			return 500 - share.Borrowing
		}
	}
	return 0
}

// IsBorrowing returns true if the tenant is using resources above its
// guaranteed share, and the amount borrowed.
func (fairScheduler *FairScheduler) IsBorrowing(ctx context.Context, tenantName string, totalClusterCPU int) (bool, int) {
	shares, err := fairScheduler.ComputeFairShares(ctx, totalClusterCPU)
	if err != nil {
		return false, 0
	}

	for _, share := range shares {
		if share.TenantName == tenantName {
			return share.Borrowing > 0, share.Borrowing
		}
	}
	return false, 0
}

// computeTenantCPU sums the CPU requirements for all services owned by the tenant.
func (fairScheduler *FairScheduler) computeTenantCPU(ctx context.Context, tenantName string) int {
	serviceFacts, err := fairScheduler.factStore.Scan(ctx, types.ScanDesiredServices)
	if err != nil {
		return 0
	}

	serviceCPU := make(map[string]int)
	serviceInstances := make(map[string]int)
	serviceExists := make(map[string]bool)

	for _, fact := range serviceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		serviceName, fieldSuffix := extractServiceNameAndField(relativePath)
		if serviceName == "" {
			continue
		}

		switch fieldSuffix {
		case "":
			serviceExists[serviceName] = true
		case "resources/cpu":
			cpuValue := strings.TrimSuffix(string(fact.Value), "m")
			parsed, _ := strconv.Atoi(cpuValue)
			serviceCPU[serviceName] = parsed
			serviceExists[serviceName] = true
		case "instances":
			parsed, _ := strconv.Atoi(string(fact.Value))
			serviceInstances[serviceName] = parsed
			serviceExists[serviceName] = true
		}
	}

	totalCPU := 0
	for serviceName := range serviceExists {
		owner, err := fairScheduler.registry.ResolveTenantForService(ctx, serviceName)
		if err != nil || owner != tenantName {
			continue
		}
		totalCPU += serviceCPU[serviceName] * serviceInstances[serviceName]
	}
	return totalCPU
}

// extractServiceNameAndField splits a relative path from the service scan prefix
// into a service name and field suffix. Handles hierarchical names with slashes
// by matching known field suffixes from the end.
func extractServiceNameAndField(relativePath string) (string, string) {
	knownSuffixes := []string{
		"/resources/cpu", "/resources/memory",
		"/scale/horizontal/", "/scale/vertical/",
		"/placement/architecture", "/placement/zone",
		"/update/max_unavailable", "/update/max_extra",
		"/health/method", "/health/path", "/health/interval",
		"/config/env/", "/config/file/",
		"/secret/", "/volume/", "/expose/",
		"/quota/instances",
		"/instances", "/image", "/owner",
	}

	for _, suffix := range knownSuffixes {
		if idx := strings.Index(relativePath, suffix); idx > 0 {
			return relativePath[:idx], relativePath[idx+1:]
		}
	}

	return relativePath, ""
}

// countTenantInstances counts active instances for services owned by the tenant.
func (fairScheduler *FairScheduler) countTenantInstances(ctx context.Context, tenantName string) int {
	instanceFacts, err := fairScheduler.factStore.Scan(ctx, types.ScanObservedInstances)
	if err != nil {
		return 0
	}

	instanceServices := make(map[string]string)
	instanceStates := make(map[string]string)
	for _, fact := range instanceFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[1] {
		case "service":
			instanceServices[parts[0]] = string(fact.Value)
		case "state":
			instanceStates[parts[0]] = string(fact.Value)
		}
	}

	count := 0
	for instanceID, serviceName := range instanceServices {
		if instanceStates[instanceID] == "stopped" {
			continue
		}
		owner, err := fairScheduler.registry.ResolveTenantForService(ctx, serviceName)
		if err == nil && owner == tenantName {
			count++
		}
	}
	return count
}
