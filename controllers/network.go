package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// NetworkController watches endpoint facts and desired service configurations
// to produce service-level networking facts: a stable virtual IP (VIP) per
// service and a DNS name-to-VIP mapping. The existing EndpointController
// handles instance-to-endpoint mapping; this controller handles the
// service-to-VIP aggregation layer above it.
type NetworkController struct {
	// vipBaseFirstOctet is the first octet of the VIP CIDR pool (default 10).
	vipBaseFirstOctet int
	// vipBaseSecondOctet is the second octet of the VIP CIDR pool (default 200).
	vipBaseSecondOctet int
	// vipBaseThirdOctet is the third octet of the VIP CIDR pool (default 0).
	vipBaseThirdOctet int
}

// NewNetworkController returns a NetworkController that allocates VIPs from
// the default 10.200.0.0/24 pool.
func NewNetworkController() *NetworkController {
	return &NetworkController{
		vipBaseFirstOctet:  10,
		vipBaseSecondOctet: 200,
		vipBaseThirdOctet:  0,
	}
}

// Name returns "network", identifying this controller in logs and runner
// bookkeeping.
func (networkController *NetworkController) Name() string { return "network" }

// Watch returns the fact prefixes the network controller monitors: existing
// endpoints (to know which services have backends), desired services (for
// exposed port info), existing VIPs (to avoid re-allocation), and DNS
// mappings (for staleness detection).
func (networkController *NetworkController) Watch() []string {
	return []string{
		types.ScanEndpoints,
		types.ScanDesiredServices,
		types.ScanNetworkVIPs,
		types.ScanNetworkDNS,
	}
}

// Reconcile examines endpoint facts, desired service port configurations,
// and existing VIP/DNS facts, then emits changes to create missing VIPs and
// DNS records for services with endpoints, and remove stale VIPs and DNS
// records for services that no longer have any endpoints.
func (networkController *NetworkController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	// Collect services that have at least one endpoint.
	servicesWithEndpoints := make(map[string]bool)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanEndpoints) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) >= 1 {
			servicesWithEndpoints[pathParts[0]] = true
		}
	}

	// Collect service exposed ports: serviceName -> first exposed port.
	servicePorts := make(map[string]int)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanDesiredServices) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 3 && pathParts[1] == "expose" {
			portNumber, _ := strconv.Atoi(pathParts[2])
			if portNumber > 0 {
				if _, alreadySet := servicePorts[pathParts[0]]; !alreadySet {
					servicePorts[pathParts[0]] = portNumber
				}
			}
		}
	}

	// Collect existing VIPs: serviceName -> VIP address.
	existingVIPs := make(map[string]string)
	existingVIPPorts := make(map[string]string)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanNetworkVIPs) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, types.ScanNetworkVIPs)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 1 {
			existingVIPs[pathParts[0]] = string(fact.Value)
		} else if len(pathParts) == 2 && pathParts[1] == "port" {
			existingVIPPorts[pathParts[0]] = string(fact.Value)
		}
	}

	// Collect existing DNS mappings: serviceName -> VIP.
	existingDNS := make(map[string]string)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanNetworkDNS) {
			continue
		}
		serviceName := strings.TrimPrefix(fact.Key, types.ScanNetworkDNS)
		existingDNS[serviceName] = string(fact.Value)
	}

	// Determine the next VIP address to allocate by finding the highest
	// fourth-octet value among existing VIPs and incrementing.
	nextVIPFourthOctet := 1
	for _, vipAddress := range existingVIPs {
		vipParts := strings.Split(vipAddress, ".")
		if len(vipParts) == 4 {
			fourthOctet, _ := strconv.Atoi(vipParts[3])
			if fourthOctet >= nextVIPFourthOctet {
				nextVIPFourthOctet = fourthOctet + 1
			}
		}
	}

	var changes []Change

	// Create VIP + DNS for services that have endpoints and an exposed port
	// but don't yet have a VIP.
	for serviceName := range servicesWithEndpoints {
		exposedPort := servicePorts[serviceName]
		if exposedPort == 0 {
			continue
		}

		if _, hasVIP := existingVIPs[serviceName]; !hasVIP {
			vipAddress := fmt.Sprintf("%d.%d.%d.%d",
				networkController.vipBaseFirstOctet,
				networkController.vipBaseSecondOctet,
				networkController.vipBaseThirdOctet,
				nextVIPFourthOctet,
			)
			nextVIPFourthOctet++

			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyNetworkVIPService(serviceName),
				Value: []byte(vipAddress),
			})
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyNetworkVIPServicePort(serviceName),
				Value: []byte(strconv.Itoa(exposedPort)),
			})
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyNetworkDNS(serviceName),
				Value: []byte(vipAddress),
			})
		}
	}

	// Remove VIPs and DNS records for services that no longer have any endpoints.
	for serviceName := range existingVIPs {
		if !servicesWithEndpoints[serviceName] {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyNetworkVIPService(serviceName),
			})
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyNetworkVIPServicePort(serviceName),
			})
		}
	}
	for serviceName := range existingDNS {
		if !servicesWithEndpoints[serviceName] {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyNetworkDNS(serviceName),
			})
		}
	}

	return changes, nil
}
