package agent

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/types"
)

// reconcileDataPlane reads VIP and endpoint facts from the store, resolves
// each endpoint to a host-accessible backend address, and calls the data
// plane provider to program iptables DNAT rules. For instances running on
// this node, the backend is the container IP:port (reachable via Docker
// bridge). For instances on other nodes, the backend is the remote node's
// advertise address + host port (reachable via LAN).
func (nodeAgent *Agent) reconcileDataPlane(ctx context.Context) {
	if nodeAgent.dataPlaneProvider == nil {
		return
	}

	serviceVIPConfigs, err := nodeAgent.buildServiceVIPConfigs(ctx)
	if err != nil {
		log.Printf("agent %s: dataplane: failed to build VIP configs: %v", nodeAgent.nodeID, err)
		return
	}

	if err := nodeAgent.dataPlaneProvider.ReconcileVIPDataPlane(ctx, serviceVIPConfigs); err != nil {
		log.Printf("agent %s: dataplane: reconcile error: %v", nodeAgent.nodeID, err)
	}
}

// buildServiceVIPConfigs scans VIP facts and endpoint facts to construct
// the complete set of service VIP configurations for the data plane.
func (nodeAgent *Agent) buildServiceVIPConfigs(ctx context.Context) ([]network.ServiceVIPConfig, error) {
	vipFacts, err := nodeAgent.store.Scan(ctx, types.ScanNetworkVIPs)
	if err != nil {
		return nil, fmt.Errorf("scanning VIP facts: %w", err)
	}

	serviceVIPMap := make(map[string]*network.ServiceVIPConfig)
	for _, vipFact := range vipFacts {
		relativePath := strings.TrimPrefix(vipFact.Key, types.ScanNetworkVIPs)

		if strings.HasSuffix(relativePath, "/port") {
			serviceName := strings.TrimSuffix(relativePath, "/port")
			if existingConfig, exists := serviceVIPMap[serviceName]; exists {
				parsedPort, _ := strconv.Atoi(string(vipFact.Value))
				existingConfig.Port = parsedPort
			}
			continue
		}

		serviceName := relativePath
		parsedPort := 0
		portFact, portErr := nodeAgent.store.Get(ctx, types.KeyNetworkVIPServicePort(serviceName))
		if portErr == nil {
			parsedPort, _ = strconv.Atoi(string(portFact.Value))
		}

		serviceVIPMap[serviceName] = &network.ServiceVIPConfig{
			ServiceName: serviceName,
			VirtualIP:   string(vipFact.Value),
			Port:        parsedPort,
		}
	}

	var serviceVIPConfigs []network.ServiceVIPConfig
	for serviceName, vipConfig := range serviceVIPMap {
		if vipConfig.Port == 0 {
			continue
		}

		backends, err := nodeAgent.resolveServiceBackends(ctx, serviceName)
		if err != nil {
			log.Printf("agent %s: dataplane: failed to resolve backends for %s: %v", nodeAgent.nodeID, serviceName, err)
			continue
		}
		vipConfig.Backends = backends
		serviceVIPConfigs = append(serviceVIPConfigs, *vipConfig)
	}

	return serviceVIPConfigs, nil
}

// resolveServiceBackends reads endpoint facts for a service and resolves
// each endpoint to a host-accessible backend address. Local instances use
// the container IP:port; remote instances use the node's advertise address
// and host port.
func (nodeAgent *Agent) resolveServiceBackends(ctx context.Context, serviceName string) ([]network.DataPlaneBackend, error) {
	endpointPrefix := fmt.Sprintf("%s/service/%s/", types.PrefixEndpoint, serviceName)
	endpointFacts, err := nodeAgent.store.Scan(ctx, endpointPrefix)
	if err != nil {
		return nil, fmt.Errorf("scanning endpoints for %s: %w", serviceName, err)
	}

	var backends []network.DataPlaneBackend
	for _, endpointFact := range endpointFacts {
		instanceID := strings.TrimPrefix(endpointFact.Key, endpointPrefix)
		endpointValue := string(endpointFact.Value)

		colonIndex := strings.LastIndex(endpointValue, ":")
		if colonIndex < 0 {
			continue
		}
		endpointIP := endpointValue[:colonIndex]
		endpointPort, _ := strconv.Atoi(endpointValue[colonIndex+1:])

		instanceNodeFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceNode(instanceID))
		if err != nil {
			backends = append(backends, network.DataPlaneBackend{
				Address: endpointIP,
				Port:    endpointPort,
			})
			continue
		}

		instanceNodeID := string(instanceNodeFact.Value)

		if instanceNodeID == nodeAgent.nodeID {
			backends = append(backends, network.DataPlaneBackend{
				Address: endpointIP,
				Port:    endpointPort,
			})
		} else {
			remoteBackend := nodeAgent.resolveRemoteBackend(ctx, instanceID, instanceNodeID, endpointIP, endpointPort)
			backends = append(backends, remoteBackend)
		}
	}

	return backends, nil
}

// resolveRemoteBackend determines the host-accessible address for an
// instance running on a different node. It looks up the remote node's
// advertise address and the instance's host port mapping. Falls back to
// the container IP:port if the node address or host port is unavailable.
func (nodeAgent *Agent) resolveRemoteBackend(ctx context.Context, instanceID string, remoteNodeID string, fallbackIP string, fallbackPort int) network.DataPlaneBackend {
	nodeAddressFact, err := nodeAgent.store.Get(ctx, types.KeyObservedNodeAddress(remoteNodeID))
	if err != nil {
		return network.DataPlaneBackend{Address: fallbackIP, Port: fallbackPort}
	}
	nodeAddress := string(nodeAddressFact.Value)

	hostPortFact, err := nodeAgent.store.Get(ctx, types.KeyObservedInstanceHostPort(instanceID))
	if err != nil {
		return network.DataPlaneBackend{Address: fallbackIP, Port: fallbackPort}
	}
	hostPort, err := strconv.Atoi(string(hostPortFact.Value))
	if err != nil {
		return network.DataPlaneBackend{Address: fallbackIP, Port: fallbackPort}
	}

	return network.DataPlaneBackend{Address: nodeAddress, Port: hostPort}
}

// publishNodeAdvertiseAddress writes this node's routable address to the
// store so other nodes' data plane providers can resolve cross-host
// backend addresses.
func (nodeAgent *Agent) publishNodeAdvertiseAddress(ctx context.Context) {
	if nodeAgent.advertiseAddress == "" {
		return
	}
	nodeAgent.store.Put(ctx, types.KeyObservedNodeAddress(nodeAgent.nodeID), []byte(nodeAgent.advertiseAddress))
}

// publishInstanceHostPort writes the host port mapping for an instance to
// the store. This is called after starting a container so other nodes can
// resolve the host-accessible port for cross-host data plane DNAT.
func (nodeAgent *Agent) publishInstanceHostPort(ctx context.Context, instanceID string, hostPort int) {
	if hostPort <= 0 {
		return
	}
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceHostPort(instanceID), []byte(strconv.Itoa(hostPort)))
}
