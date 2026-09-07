package types

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
)

// WriteService writes a service definition as flat key-value pairs under the
// desired/ prefix in the fact store. Each field of the Service struct becomes
// a separate key, allowing controllers to watch individual fields independently.
func WriteService(ctx context.Context, stateStore store.StateStore, service Service) error {
	puts := []struct{ key, val string }{
		{KeyDesiredService(service.Name), ""},
		{KeyDesiredServiceImage(service.Name), service.Image},
		{KeyDesiredServiceInstances(service.Name), strconv.Itoa(service.Instances)},
		{KeyDesiredServiceResourcesCPU(service.Name), service.CPU},
		{KeyDesiredServiceResourcesMemory(service.Name), service.Memory},
	}
	for _, port := range service.Ports {
		puts = append(puts, struct{ key, val string }{
			KeyDesiredServiceExpose(service.Name, port), "",
		})
	}
	for _, putEntry := range puts {
		if _, err := stateStore.Put(ctx, putEntry.key, []byte(putEntry.val)); err != nil {
			return fmt.Errorf("writing %s: %w", putEntry.key, err)
		}
	}
	return nil
}

// ReadService assembles a Service struct by reading flat keys under
// desired/service/{name}/ from the fact store. It returns an error if the
// service's root marker key does not exist.
func ReadService(ctx context.Context, stateStore store.StateStore, name string) (*Service, error) {
	prefix := KeyDesiredService(name) + "/"
	facts, err := stateStore.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if _, err := stateStore.Get(ctx, KeyDesiredService(name)); err != nil {
		return nil, err
	}

	service := &Service{Name: name}
	for _, factEntry := range facts {
		suffix := strings.TrimPrefix(factEntry.Key, KeyDesiredService(name)+"/")
		val := string(factEntry.Value)
		switch suffix {
		case "image":
			service.Image = val
		case "instances":
			service.Instances, _ = strconv.Atoi(val)
		case "resources/cpu":
			service.CPU = val
		case "resources/memory":
			service.Memory = val
		default:
			if strings.HasPrefix(suffix, "expose/") {
				portStr := strings.TrimPrefix(suffix, "expose/")
				if port, err := strconv.Atoi(portStr); err == nil {
					service.Ports = append(service.Ports, port)
				}
			}
		}
	}
	return service, nil
}

// WriteInstance writes an instance as flat key-value pairs under the
// observed/instance/{id}/ prefix. Only non-empty optional fields (Node,
// Image, IP, Health) are written, so newly created pending instances
// carry minimal initial state.
func WriteInstance(ctx context.Context, stateStore store.StateStore, instance Instance) error {
	puts := []struct{ key, val string }{
		{KeyObservedInstance(instance.ID), ""},
		{KeyObservedInstanceService(instance.ID), instance.Service},
		{KeyObservedInstanceState(instance.ID), string(instance.State)},
	}
	if instance.Node != "" {
		puts = append(puts, struct{ key, val string }{KeyObservedInstanceNode(instance.ID), instance.Node})
	}
	if instance.Image != "" {
		puts = append(puts, struct{ key, val string }{KeyObservedInstanceImage(instance.ID), instance.Image})
	}
	if instance.IP != "" {
		puts = append(puts, struct{ key, val string }{KeyObservedInstanceIP(instance.ID), instance.IP})
	}
	if instance.Health != "" {
		puts = append(puts, struct{ key, val string }{KeyObservedInstanceHealth(instance.ID), string(instance.Health)})
	}
	for _, putEntry := range puts {
		if _, err := stateStore.Put(ctx, putEntry.key, []byte(putEntry.val)); err != nil {
			return fmt.Errorf("writing %s: %w", putEntry.key, err)
		}
	}
	return nil
}

// ReadInstance assembles an Instance struct by reading flat keys under
// observed/instance/{id}/ from the fact store. It returns an error if the
// instance's root marker key does not exist.
func ReadInstance(ctx context.Context, stateStore store.StateStore, id string) (*Instance, error) {
	if _, err := stateStore.Get(ctx, KeyObservedInstance(id)); err != nil {
		return nil, err
	}
	prefix := KeyObservedInstance(id) + "/"
	facts, err := stateStore.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}

	instance := &Instance{ID: id}
	for _, factEntry := range facts {
		suffix := strings.TrimPrefix(factEntry.Key, KeyObservedInstance(id)+"/")
		val := string(factEntry.Value)
		switch suffix {
		case "service":
			instance.Service = val
		case "node":
			instance.Node = val
		case "state":
			instance.State = InstanceState(val)
		case "image":
			instance.Image = val
		case "ip":
			instance.IP = val
		case "health":
			instance.Health = HealthStatus(val)
		}
	}
	return instance, nil
}

// ListInstances returns all instances by scanning the observed/instance/ prefix.
// It groups flat keys by instance ID and reconstructs each Instance struct from
// its constituent fields.
func ListInstances(ctx context.Context, stateStore store.StateStore) ([]Instance, error) {
	facts, err := stateStore.Scan(ctx, ScanObservedInstances)
	if err != nil {
		return nil, err
	}

	grouped := make(map[string]map[string]string)
	for _, factEntry := range facts {
		rel := strings.TrimPrefix(factEntry.Key, ScanObservedInstances)
		parts := strings.SplitN(rel, "/", 2)
		id := parts[0]
		if _, ok := grouped[id]; !ok {
			grouped[id] = make(map[string]string)
		}
		if len(parts) == 2 {
			grouped[id][parts[1]] = string(factEntry.Value)
		}
	}

	instances := make([]Instance, 0, len(grouped))
	for id, fields := range grouped {
		instance := Instance{ID: id}
		instance.Service = fields["service"]
		instance.Node = fields["node"]
		instance.State = InstanceState(fields["state"])
		instance.Image = fields["image"]
		instance.IP = fields["ip"]
		instance.Health = HealthStatus(fields["health"])
		instances = append(instances, instance)
	}
	return instances, nil
}

// WriteNode writes a node's observed state as flat key-value pairs under
// observed/node/{id}/. This includes the node's lifecycle state, resource
// capacity and availability, and optional architecture and zone metadata.
func WriteNode(ctx context.Context, stateStore store.StateStore, node Node) error {
	puts := []struct{ key, val string }{
		{KeyObservedNode(node.ID), ""},
		{KeyObservedNodeState(node.ID), string(node.State)},
		{KeyObservedNodeCapacityCPU(node.ID), strconv.FormatInt(node.CapacityCPU, 10)},
		{KeyObservedNodeCapacityMemory(node.ID), strconv.FormatInt(node.CapacityMemory, 10)},
		{KeyObservedNodeAvailableCPU(node.ID), strconv.FormatInt(node.AvailableCPU, 10)},
		{KeyObservedNodeAvailableMemory(node.ID), strconv.FormatInt(node.AvailableMemory, 10)},
	}
	if node.Architecture != "" {
		puts = append(puts, struct{ key, val string }{KeyObservedNodeArchitecture(node.ID), node.Architecture})
	}
	if node.Zone != "" {
		puts = append(puts, struct{ key, val string }{KeyObservedNodeZone(node.ID), node.Zone})
	}
	for _, putEntry := range puts {
		if _, err := stateStore.Put(ctx, putEntry.key, []byte(putEntry.val)); err != nil {
			return fmt.Errorf("writing %s: %w", putEntry.key, err)
		}
	}
	return nil
}

// ReadNode assembles a Node struct by reading flat keys under
// observed/node/{id}/ from the fact store. It returns an error if the
// node's root marker key does not exist.
func ReadNode(ctx context.Context, stateStore store.StateStore, id string) (*Node, error) {
	if _, err := stateStore.Get(ctx, KeyObservedNode(id)); err != nil {
		return nil, err
	}
	prefix := KeyObservedNode(id) + "/"
	facts, err := stateStore.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}

	node := &Node{ID: id}
	for _, factEntry := range facts {
		suffix := strings.TrimPrefix(factEntry.Key, KeyObservedNode(id)+"/")
		val := string(factEntry.Value)
		switch suffix {
		case "state":
			node.State = NodeState(val)
		case "capacity/cpu":
			node.CapacityCPU, _ = strconv.ParseInt(val, 10, 64)
		case "capacity/memory":
			node.CapacityMemory, _ = strconv.ParseInt(val, 10, 64)
		case "available/cpu":
			node.AvailableCPU, _ = strconv.ParseInt(val, 10, 64)
		case "available/memory":
			node.AvailableMemory, _ = strconv.ParseInt(val, 10, 64)
		case "architecture":
			node.Architecture = val
		case "zone":
			node.Zone = val
		}
	}
	return node, nil
}

// ListNodes returns all nodes by scanning the observed/node/ prefix.
// It groups flat keys by node ID and reconstructs each Node struct from
// its constituent fields.
func ListNodes(ctx context.Context, stateStore store.StateStore) ([]Node, error) {
	facts, err := stateStore.Scan(ctx, ScanObservedNodes)
	if err != nil {
		return nil, err
	}

	grouped := make(map[string]map[string]string)
	for _, factEntry := range facts {
		rel := strings.TrimPrefix(factEntry.Key, ScanObservedNodes)
		parts := strings.SplitN(rel, "/", 2)
		id := parts[0]
		if _, ok := grouped[id]; !ok {
			grouped[id] = make(map[string]string)
		}
		if len(parts) == 2 {
			grouped[id][parts[1]] = string(factEntry.Value)
		}
	}

	nodes := make([]Node, 0, len(grouped))
	for id, fields := range grouped {
		node := Node{ID: id}
		node.State = NodeState(fields["state"])
		node.CapacityCPU, _ = strconv.ParseInt(fields["capacity/cpu"], 10, 64)
		node.CapacityMemory, _ = strconv.ParseInt(fields["capacity/memory"], 10, 64)
		node.AvailableCPU, _ = strconv.ParseInt(fields["available/cpu"], 10, 64)
		node.AvailableMemory, _ = strconv.ParseInt(fields["available/memory"], 10, 64)
		node.Architecture = fields["architecture"]
		node.Zone = fields["zone"]
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// WritePlacement writes a scheduler placement decision to the fact store.
// It stores the target node ID at the placement key for the given instance.
func WritePlacement(ctx context.Context, stateStore store.StateStore, placement Placement) (int64, error) {
	return stateStore.Put(ctx, KeyPlacementInstance(placement.InstanceID), []byte(placement.NodeID))
}

// WriteEndpoint writes a derived endpoint fact for a running service instance.
// The value is stored as "IP:port" at the endpoint key for the service/instance pair.
func WriteEndpoint(ctx context.Context, stateStore store.StateStore, endpoint Endpoint) (int64, error) {
	val := fmt.Sprintf("%s:%d", endpoint.IP, endpoint.Port)
	return stateStore.Put(ctx, KeyEndpoint(endpoint.Service, endpoint.InstanceID), []byte(val))
}

// DeleteEndpoint removes an endpoint fact for a service instance. This is
// called when an instance stops running and should no longer receive traffic.
func DeleteEndpoint(ctx context.Context, stateStore store.StateStore, serviceName, instanceID string) error {
	return stateStore.Delete(ctx, KeyEndpoint(serviceName, instanceID))
}

// WriteServiceVIP writes a service virtual IP assignment as flat key-value pairs
// to the fact store. The VIP address and port are stored separately so
// controllers can watch each independently.
func WriteServiceVIP(ctx context.Context, stateStore store.StateStore, serviceVIP ServiceVIP) (int64, error) {
	revision, err := stateStore.Put(ctx, KeyNetworkVIPService(serviceVIP.Service), []byte(serviceVIP.VIP))
	if err != nil {
		return 0, fmt.Errorf("writing VIP for %s: %w", serviceVIP.Service, err)
	}
	if _, err := stateStore.Put(ctx, KeyNetworkVIPServicePort(serviceVIP.Service), []byte(strconv.Itoa(serviceVIP.Port))); err != nil {
		return 0, fmt.Errorf("writing VIP port for %s: %w", serviceVIP.Service, err)
	}
	return revision, nil
}

// ReadServiceVIP reads a service's VIP assignment from the fact store.
// Returns an error if the service has no VIP assigned.
func ReadServiceVIP(ctx context.Context, stateStore store.StateStore, serviceName string) (*ServiceVIP, error) {
	vipFact, err := stateStore.Get(ctx, KeyNetworkVIPService(serviceName))
	if err != nil {
		return nil, err
	}
	serviceVIP := &ServiceVIP{
		Service: serviceName,
		VIP:     string(vipFact.Value),
	}
	portFact, err := stateStore.Get(ctx, KeyNetworkVIPServicePort(serviceName))
	if err == nil {
		serviceVIP.Port, _ = strconv.Atoi(string(portFact.Value))
	}
	return serviceVIP, nil
}

// DeleteServiceVIP removes all VIP-related facts for a service (the VIP
// address, port, and DNS mapping).
func DeleteServiceVIP(ctx context.Context, stateStore store.StateStore, serviceName string) error {
	stateStore.Delete(ctx, KeyNetworkVIPService(serviceName))
	stateStore.Delete(ctx, KeyNetworkVIPServicePort(serviceName))
	stateStore.Delete(ctx, KeyNetworkDNS(serviceName))
	return nil
}

// WriteNetworkAllocation records an instance's allocated IP address in the
// networking section of the fact store.
func WriteNetworkAllocation(ctx context.Context, stateStore store.StateStore, instanceID string, allocatedIP string) (int64, error) {
	return stateStore.Put(ctx, KeyNetworkAllocation(instanceID), []byte(allocatedIP))
}

// DeleteNetworkAllocation removes an instance's IP allocation record from
// the fact store. Called when an instance is stopped and its IP is released.
func DeleteNetworkAllocation(ctx context.Context, stateStore store.StateStore, instanceID string) error {
	return stateStore.Delete(ctx, KeyNetworkAllocation(instanceID))
}
