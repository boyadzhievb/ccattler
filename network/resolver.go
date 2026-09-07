package network

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// ServiceResolver translates service names into their network addresses.
// Both the DNS server and the load balancer proxy use this interface to
// discover service backends.
type ServiceResolver interface {
	// ResolveEndpoints returns all active backend endpoints for the named
	// service. Returns an empty slice if the service has no endpoints.
	ResolveEndpoints(ctx context.Context, serviceName string) ([]types.Endpoint, error)

	// ResolveVIP returns the virtual IP address assigned to the named service.
	// Returns an error if the service has no VIP.
	ResolveVIP(ctx context.Context, serviceName string) (string, error)
}

// StoreBackedResolver implements ServiceResolver by reading endpoint and VIP
// facts directly from the state store. It always returns the current live
// state — no caching, no stale data.
type StoreBackedResolver struct {
	// factStore is the backing state store used to look up endpoint and VIP facts.
	factStore store.StateStore
}

// NewStoreBackedResolver creates a resolver that reads service networking
// facts from the provided state store.
func NewStoreBackedResolver(factStore store.StateStore) *StoreBackedResolver {
	return &StoreBackedResolver{factStore: factStore}
}

// ResolveEndpoints scans the endpoint prefix for the named service and returns
// all active endpoints. Each endpoint fact value is expected to be in
// "ip:port" format.
func (storeBackedResolver *StoreBackedResolver) ResolveEndpoints(ctx context.Context, serviceName string) ([]types.Endpoint, error) {
	endpointPrefix := fmt.Sprintf("%s/service/%s/", types.PrefixEndpoint, serviceName)
	endpointFacts, err := storeBackedResolver.factStore.Scan(ctx, endpointPrefix)
	if err != nil {
		return nil, fmt.Errorf("scanning endpoints for %s: %w", serviceName, err)
	}

	var resolvedEndpoints []types.Endpoint
	for _, endpointFact := range endpointFacts {
		relativePath := strings.TrimPrefix(endpointFact.Key, endpointPrefix)
		instanceID := relativePath

		addressAndPort := string(endpointFact.Value)
		colonIndex := strings.LastIndex(addressAndPort, ":")
		if colonIndex < 0 {
			continue
		}

		endpointIP := addressAndPort[:colonIndex]
		endpointPort, _ := strconv.Atoi(addressAndPort[colonIndex+1:])

		resolvedEndpoints = append(resolvedEndpoints, types.Endpoint{
			Service:    serviceName,
			InstanceID: instanceID,
			IP:         endpointIP,
			Port:       endpointPort,
		})
	}

	return resolvedEndpoints, nil
}

// ResolveVIP reads the VIP fact for the named service. Returns an error if
// no VIP has been allocated.
func (storeBackedResolver *StoreBackedResolver) ResolveVIP(ctx context.Context, serviceName string) (string, error) {
	vipFact, err := storeBackedResolver.factStore.Get(ctx, types.KeyNetworkVIPService(serviceName))
	if err != nil {
		return "", fmt.Errorf("no VIP for service %s: %w", serviceName, err)
	}
	return string(vipFact.Value), nil
}
