package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// DataPlaneReconciler encapsulates data plane reconciliation logic, reading
// VIP and endpoint facts from the store and programming DNAT rules via the
// data plane provider. Extracted from Agent to isolate the data plane concern.
type DataPlaneReconciler struct {
	nodeID            string                   // nodeID is the unique identifier for the node this reconciler manages.
	factStore         store.StateStore         // factStore is the fact store used to read VIP, endpoint, and node facts.
	dataPlaneProvider network.DataPlaneProvider // dataPlaneProvider programs VIP DNAT rules on the host.
	advertiseAddress  string                   // advertiseAddress is this node's LAN-routable IP for cross-host data plane.
}

// NewDataPlaneReconciler creates a DataPlaneReconciler wired to the given
// fact store and data plane provider for the specified node.
func NewDataPlaneReconciler(nodeID string, factStore store.StateStore, dataPlaneProvider network.DataPlaneProvider, advertiseAddress string) *DataPlaneReconciler {
	return &DataPlaneReconciler{
		nodeID:            nodeID,
		factStore:         factStore,
		dataPlaneProvider: dataPlaneProvider,
		advertiseAddress:  advertiseAddress,
	}
}

// Reconcile reads VIP and endpoint facts from the store, resolves
// each endpoint to a host-accessible backend address, and calls the data
// plane provider to program iptables DNAT rules. For instances running on
// this node, the backend is the container IP:port (reachable via Docker
// bridge). For instances on other nodes, the backend is the remote node's
// advertise address + host port (reachable via LAN).
func (dataPlaneReconciler *DataPlaneReconciler) Reconcile(ctx context.Context) {
	if dataPlaneReconciler.dataPlaneProvider == nil {
		return
	}

	serviceVIPConfigs, err := dataPlaneReconciler.buildServiceVIPConfigs(ctx)
	if err != nil {
		logging.Default().Error("dataplane: failed to build VIP configs", "agent", dataPlaneReconciler.nodeID, "error", err.Error())
		return
	}

	if err := dataPlaneReconciler.dataPlaneProvider.ReconcileVIPDataPlane(ctx, serviceVIPConfigs); err != nil {
		logging.Default().Error("dataplane: reconcile error", "agent", dataPlaneReconciler.nodeID, "error", err.Error())
	}
}

// buildServiceVIPConfigs scans VIP facts and endpoint facts to construct
// the complete set of service VIP configurations for the data plane.
func (dataPlaneReconciler *DataPlaneReconciler) buildServiceVIPConfigs(ctx context.Context) ([]network.ServiceVIPConfig, error) {
	vipFacts, err := dataPlaneReconciler.factStore.Scan(ctx, types.ScanNetworkVIPs)
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
		portFact, portErr := dataPlaneReconciler.factStore.Get(ctx, types.KeyNetworkVIPServicePort(serviceName))
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

		backends, err := dataPlaneReconciler.resolveServiceBackends(ctx, serviceName)
		if err != nil {
			logging.Default().Error("dataplane: failed to resolve backends", "agent", dataPlaneReconciler.nodeID, "service", serviceName, "error", err.Error())
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
func (dataPlaneReconciler *DataPlaneReconciler) resolveServiceBackends(ctx context.Context, serviceName string) ([]network.DataPlaneBackend, error) {
	endpointPrefix := fmt.Sprintf("%s/service/%s/", types.PrefixEndpoint, serviceName)
	endpointFacts, err := dataPlaneReconciler.factStore.Scan(ctx, endpointPrefix)
	if err != nil {
		return nil, fmt.Errorf("scanning endpoints for %s: %w", serviceName, err)
	}

	var backends []network.DataPlaneBackend
	for _, endpointFact := range endpointFacts {
		endpointSuffix := strings.TrimPrefix(endpointFact.Key, endpointPrefix)
		instanceID := strings.SplitN(endpointSuffix, "/", 2)[0]
		endpointValue := string(endpointFact.Value)

		colonIndex := strings.LastIndex(endpointValue, ":")
		if colonIndex < 0 {
			continue
		}
		endpointIP := endpointValue[:colonIndex]
		endpointPort, _ := strconv.Atoi(endpointValue[colonIndex+1:])

		instanceNodeFact, err := dataPlaneReconciler.factStore.Get(ctx, types.KeyObservedInstanceNode(instanceID))
		if err != nil {
			backends = append(backends, network.DataPlaneBackend{
				Address: endpointIP,
				Port:    endpointPort,
			})
			continue
		}

		instanceNodeID := string(instanceNodeFact.Value)

		if instanceNodeID == dataPlaneReconciler.nodeID {
			hostPortFact, hostPortErr := dataPlaneReconciler.factStore.Get(ctx, types.KeyObservedInstanceHostPort(instanceID))
			if hostPortErr == nil {
				hostPort, parseErr := strconv.Atoi(string(hostPortFact.Value))
				if parseErr == nil && hostPort > 0 {
					backends = append(backends, network.DataPlaneBackend{
						Address: "127.0.0.1",
						Port:    hostPort,
					})
					continue
				}
			}
			backends = append(backends, network.DataPlaneBackend{
				Address: endpointIP,
				Port:    endpointPort,
			})
		} else {
			remoteBackend := dataPlaneReconciler.resolveRemoteBackend(ctx, instanceID, instanceNodeID, endpointIP, endpointPort)
			backends = append(backends, remoteBackend)
		}
	}

	return backends, nil
}

// resolveRemoteBackend determines the host-accessible address for an
// instance running on a different node. It looks up the remote node's
// advertise address and the instance's host port mapping. Falls back to
// the container IP:port if the node address or host port is unavailable.
func (dataPlaneReconciler *DataPlaneReconciler) resolveRemoteBackend(ctx context.Context, instanceID string, remoteNodeID string, fallbackIP string, fallbackPort int) network.DataPlaneBackend {
	nodeAddressFact, err := dataPlaneReconciler.factStore.Get(ctx, types.KeyObservedNodeAddress(remoteNodeID))
	if err != nil {
		return network.DataPlaneBackend{Address: fallbackIP, Port: fallbackPort}
	}
	nodeAddress := string(nodeAddressFact.Value)

	hostPortFact, err := dataPlaneReconciler.factStore.Get(ctx, types.KeyObservedInstanceHostPort(instanceID))
	if err != nil {
		return network.DataPlaneBackend{Address: fallbackIP, Port: fallbackPort}
	}
	hostPort, err := strconv.Atoi(string(hostPortFact.Value))
	if err != nil {
		return network.DataPlaneBackend{Address: fallbackIP, Port: fallbackPort}
	}

	return network.DataPlaneBackend{Address: nodeAddress, Port: hostPort}
}

// PublishAdvertiseAddress writes this node's routable address to the
// store so other nodes' data plane providers can resolve cross-host
// backend addresses.
func (dataPlaneReconciler *DataPlaneReconciler) PublishAdvertiseAddress(ctx context.Context) {
	if dataPlaneReconciler.advertiseAddress == "" {
		return
	}
	dataPlaneReconciler.factStore.Put(ctx, types.KeyObservedNodeAddress(dataPlaneReconciler.nodeID), []byte(dataPlaneReconciler.advertiseAddress))
}

// publishInstanceHostPort writes the host port mapping for an instance to
// the store. This is called after starting a container so other nodes can
// resolve the host-accessible port for cross-host data plane DNAT. This is
// a package-level function because it is called from both the DataPlaneReconciler
// and the Agent's instance reconciliation logic.
func publishInstanceHostPort(ctx context.Context, factStore store.StateStore, instanceID string, hostPort int) {
	if hostPort <= 0 {
		return
	}
	factStore.Put(ctx, types.KeyObservedInstanceHostPort(instanceID), []byte(strconv.Itoa(hostPort)))
}
