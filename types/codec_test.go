package types

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

// ctx is a shared background context used across all codec tests.
var ctx = context.Background()

func TestServiceRoundTrip(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	service := Service{
		Name:      "web",
		Image:     "nginx:1.28",
		Instances: 3,
		Ports:     []int{8080},
		CPU:       "500m",
		Memory:    "512Mi",
	}
	if err := WriteService(ctx, stateStore, service); err != nil {
		t.Fatal(err)
	}

	got, err := ReadService(ctx, stateStore, "web")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web" {
		t.Fatalf("name: got %s, want web", got.Name)
	}
	if got.Image != "nginx:1.28" {
		t.Fatalf("image: got %s, want nginx:1.28", got.Image)
	}
	if got.Instances != 3 {
		t.Fatalf("instances: got %d, want 3", got.Instances)
	}
	if len(got.Ports) != 1 || got.Ports[0] != 8080 {
		t.Fatalf("ports: got %v, want [8080]", got.Ports)
	}
	if got.CPU != "500m" {
		t.Fatalf("cpu: got %s, want 500m", got.CPU)
	}
	if got.Memory != "512Mi" {
		t.Fatalf("memory: got %s, want 512Mi", got.Memory)
	}
}

func TestServiceNotFound(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	_, err := ReadService(ctx, stateStore, "missing")
	if err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestInstanceRoundTrip(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	instance := Instance{
		ID:      "a8f31",
		Service: "web",
		Node:    "node-1",
		State:   InstanceRunning,
		Image:   "nginx:1.28",
		IP:      "10.0.1.4",
		Health:  HealthHealthy,
	}
	if err := WriteInstance(ctx, stateStore, instance); err != nil {
		t.Fatal(err)
	}

	got, err := ReadInstance(ctx, stateStore, "a8f31")
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "web" {
		t.Fatalf("service: got %s, want web", got.Service)
	}
	if got.Node != "node-1" {
		t.Fatalf("node: got %s, want node-1", got.Node)
	}
	if got.State != InstanceRunning {
		t.Fatalf("state: got %s, want running", got.State)
	}
	if got.IP != "10.0.1.4" {
		t.Fatalf("ip: got %s, want 10.0.1.4", got.IP)
	}
	if got.Health != HealthHealthy {
		t.Fatalf("health: got %s, want healthy", got.Health)
	}
}

func TestInstancePendingMinimalFields(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	instance := Instance{
		ID:      "b72c9",
		Service: "api",
		State:   InstancePending,
	}
	if err := WriteInstance(ctx, stateStore, instance); err != nil {
		t.Fatal(err)
	}

	got, err := ReadInstance(ctx, stateStore, "b72c9")
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "api" {
		t.Fatalf("service: got %s, want api", got.Service)
	}
	if got.State != InstancePending {
		t.Fatalf("state: got %s, want pending", got.State)
	}
	if got.Node != "" {
		t.Fatalf("node should be empty, got %s", got.Node)
	}
}

func TestListInstances(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	for _, instance := range []Instance{
		{ID: "aaa", Service: "web", State: InstanceRunning},
		{ID: "bbb", Service: "web", State: InstancePending},
		{ID: "ccc", Service: "api", State: InstanceRunning},
	} {
		if err := WriteInstance(ctx, stateStore, instance); err != nil {
			t.Fatal(err)
		}
	}

	instances, err := ListInstances(ctx, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 3 {
		t.Fatalf("expected 3 instances, got %d", len(instances))
	}

	byID := make(map[string]Instance)
	for _, instance := range instances {
		byID[instance.ID] = instance
	}
	if byID["aaa"].Service != "web" || byID["aaa"].State != InstanceRunning {
		t.Fatalf("instance aaa: %+v", byID["aaa"])
	}
	if byID["bbb"].State != InstancePending {
		t.Fatalf("instance bbb: %+v", byID["bbb"])
	}
	if byID["ccc"].Service != "api" {
		t.Fatalf("instance ccc: %+v", byID["ccc"])
	}
}

func TestNodeRoundTrip(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	node := Node{
		ID:              "node-1",
		State:           NodeAlive,
		CapacityCPU:     4000,
		CapacityMemory:  8192,
		AvailableCPU:    2500,
		AvailableMemory: 4096,
		Architecture:    "amd64",
		Zone:            "us-east-1a",
	}
	if err := WriteNode(ctx, stateStore, node); err != nil {
		t.Fatal(err)
	}

	got, err := ReadNode(ctx, stateStore, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != NodeAlive {
		t.Fatalf("state: got %s, want alive", got.State)
	}
	if got.CapacityCPU != 4000 {
		t.Fatalf("capacity cpu: got %d, want 4000", got.CapacityCPU)
	}
	if got.AvailableMemory != 4096 {
		t.Fatalf("available memory: got %d, want 4096", got.AvailableMemory)
	}
	if got.Architecture != "amd64" {
		t.Fatalf("arch: got %s, want amd64", got.Architecture)
	}
	if got.Zone != "us-east-1a" {
		t.Fatalf("zone: got %s, want us-east-1a", got.Zone)
	}
}

func TestListNodes(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	for _, node := range []Node{
		{ID: "node-1", State: NodeAlive, CapacityCPU: 4000, CapacityMemory: 8192, AvailableCPU: 4000, AvailableMemory: 8192},
		{ID: "node-2", State: NodeAlive, CapacityCPU: 8000, CapacityMemory: 16384, AvailableCPU: 8000, AvailableMemory: 16384},
	} {
		if err := WriteNode(ctx, stateStore, node); err != nil {
			t.Fatal(err)
		}
	}

	nodes, err := ListNodes(ctx, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
}

func TestPlacementWrite(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	_, err := WritePlacement(ctx, stateStore, Placement{InstanceID: "a8f31", NodeID: "node-2"})
	if err != nil {
		t.Fatal(err)
	}

	factEntry, err := stateStore.Get(ctx, KeyPlacementInstance("a8f31"))
	if err != nil {
		t.Fatal(err)
	}
	if string(factEntry.Value) != "node-2" {
		t.Fatalf("expected node-2, got %s", factEntry.Value)
	}
}

func TestEndpointWriteAndDelete(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	_, err := WriteEndpoint(ctx, stateStore, Endpoint{
		Service:    "web",
		InstanceID: "a8f31",
		IP:         "10.0.1.4",
		Port:       8080,
	})
	if err != nil {
		t.Fatal(err)
	}

	factEntry, err := stateStore.Get(ctx, KeyEndpoint("web", "a8f31"))
	if err != nil {
		t.Fatal(err)
	}
	if string(factEntry.Value) != "10.0.1.4:8080" {
		t.Fatalf("expected 10.0.1.4:8080, got %s", factEntry.Value)
	}

	if err := DeleteEndpoint(ctx, stateStore, "web", "a8f31"); err != nil {
		t.Fatal(err)
	}
	_, err = stateStore.Get(ctx, KeyEndpoint("web", "a8f31"))
	if err != store.ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

func TestKeyPaths(t *testing.T) {
	tests := []struct {
		got, want string
	}{
		{KeyDesiredService("web"), "/ccattler/desired/service/web"},
		{KeyDesiredServiceImage("web"), "/ccattler/desired/service/web/image"},
		{KeyDesiredServiceInstances("web"), "/ccattler/desired/service/web/instances"},
		{KeyDesiredServiceExpose("web", 8080), "/ccattler/desired/service/web/expose/8080"},
		{KeyEffectiveServiceInstances("web"), "/ccattler/effective/service/web/instances"},
		{KeyObservedNode("node-1"), "/ccattler/observed/node/node-1"},
		{KeyObservedNodeState("node-1"), "/ccattler/observed/node/node-1/state"},
		{KeyObservedInstance("a8f31"), "/ccattler/observed/instance/a8f31"},
		{KeyObservedInstanceState("a8f31"), "/ccattler/observed/instance/a8f31/state"},
		{KeyPlacementInstance("a8f31"), "/ccattler/placement/instance/a8f31"},
		{KeyEndpoint("web", "a8f31"), "/ccattler/endpoint/service/web/a8f31"},
		{KeyLeaseNode("node-1"), "/ccattler/lease/node/node-1"},
		{KeyNetworkNodeSubnet("node-1"), "/ccattler/network/node/node-1/subnet"},
		{KeyNetworkAllocation("a8f31"), "/ccattler/network/allocation/a8f31"},
		{KeyNetworkVIPService("web"), "/ccattler/network/vip/service/web"},
		{KeyNetworkVIPServicePort("web"), "/ccattler/network/vip/service/web/port"},
		{KeyNetworkDNS("web"), "/ccattler/network/dns/web"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %s, want %s", tt.got, tt.want)
		}
	}
}

// TestServiceVIPRoundTrip verifies that a ServiceVIP can be written to and
// read back from the fact store with all fields preserved.
func TestServiceVIPRoundTrip(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	serviceVIP := ServiceVIP{
		Service: "web",
		VIP:     "10.200.0.1",
		Port:    8080,
	}
	_, err := WriteServiceVIP(ctx, stateStore, serviceVIP)
	if err != nil {
		t.Fatal(err)
	}

	readBackVIP, err := ReadServiceVIP(ctx, stateStore, "web")
	if err != nil {
		t.Fatal(err)
	}
	if readBackVIP.Service != "web" {
		t.Errorf("service: got %s, want web", readBackVIP.Service)
	}
	if readBackVIP.VIP != "10.200.0.1" {
		t.Errorf("vip: got %s, want 10.200.0.1", readBackVIP.VIP)
	}
	if readBackVIP.Port != 8080 {
		t.Errorf("port: got %d, want 8080", readBackVIP.Port)
	}
}

// TestServiceVIPDeleteRemovesAllFacts verifies that DeleteServiceVIP removes
// the VIP address, port, and DNS mapping facts.
func TestServiceVIPDeleteRemovesAllFacts(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	WriteServiceVIP(ctx, stateStore, ServiceVIP{Service: "web", VIP: "10.200.0.1", Port: 8080})
	stateStore.Put(ctx, KeyNetworkDNS("web"), []byte("10.200.0.1"))

	DeleteServiceVIP(ctx, stateStore, "web")

	_, err := stateStore.Get(ctx, KeyNetworkVIPService("web"))
	if err != store.ErrKeyNotFound {
		t.Errorf("VIP should be deleted, got err: %v", err)
	}
	_, err = stateStore.Get(ctx, KeyNetworkVIPServicePort("web"))
	if err != store.ErrKeyNotFound {
		t.Errorf("VIP port should be deleted, got err: %v", err)
	}
	_, err = stateStore.Get(ctx, KeyNetworkDNS("web"))
	if err != store.ErrKeyNotFound {
		t.Errorf("DNS should be deleted, got err: %v", err)
	}
}

// TestNetworkAllocationWriteAndDelete verifies that an instance IP allocation
// can be written and then removed from the fact store.
func TestNetworkAllocationWriteAndDelete(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	_, err := WriteNetworkAllocation(ctx, stateStore, "instance-aaa", "10.100.1.2")
	if err != nil {
		t.Fatal(err)
	}

	factEntry, err := stateStore.Get(ctx, KeyNetworkAllocation("instance-aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if string(factEntry.Value) != "10.100.1.2" {
		t.Errorf("allocation: got %s, want 10.100.1.2", factEntry.Value)
	}

	if err := DeleteNetworkAllocation(ctx, stateStore, "instance-aaa"); err != nil {
		t.Fatal(err)
	}
	_, err = stateStore.Get(ctx, KeyNetworkAllocation("instance-aaa"))
	if err != store.ErrKeyNotFound {
		t.Errorf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

// TestReadServiceVIPNotFound verifies that reading a VIP for a service that
// has none returns an error.
func TestReadServiceVIPNotFound(t *testing.T) {
	stateStore := store.NewMemoryStore()
	defer stateStore.Close()

	_, err := ReadServiceVIP(ctx, stateStore, "nonexistent")
	if err == nil {
		t.Error("expected error for missing VIP")
	}
}
