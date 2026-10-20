// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestDataPlaneReconcilesBuildVIPConfigsFromStore(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorDataPlane := network.NewSimulatorDataPlane()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(simulatorDataPlane)

	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "web", VIP: "10.200.0.1", Port: 80,
	})

	factStore.Put(ctx, types.KeyEndpoint("web", "instance-aaa", 80), []byte("10.100.1.2:80"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("instance-aaa"), []byte("node-1"))

	factStore.Put(ctx, types.KeyEndpoint("web", "instance-bbb", 80), []byte("10.100.2.2:80"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("instance-bbb"), []byte("node-2"))
	factStore.Put(ctx, types.KeyObservedNodeAddress("node-2"), []byte("192.168.100.215"))
	factStore.Put(ctx, types.KeyObservedInstanceHostPort("instance-bbb"), []byte("80"))

	nodeAgent.dataPlaneReconciler.Reconcile(ctx)

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 1 {
		t.Fatalf("expected 1 VIP config, got %d", len(lastReconciled))
	}

	vipConfig := lastReconciled[0]
	if vipConfig.ServiceName != "web" {
		t.Errorf("ServiceName = %q, want %q", vipConfig.ServiceName, "web")
	}
	if vipConfig.VirtualIP != "10.200.0.1" {
		t.Errorf("VirtualIP = %q, want %q", vipConfig.VirtualIP, "10.200.0.1")
	}
	if vipConfig.Port != 80 {
		t.Errorf("Port = %d, want 80", vipConfig.Port)
	}
	if len(vipConfig.Backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(vipConfig.Backends))
	}

	foundLocal := false
	foundRemote := false
	for _, backend := range vipConfig.Backends {
		if backend.Address == "10.100.1.2" && backend.Port == 80 {
			foundLocal = true
		}
		if backend.Address == "192.168.100.215" && backend.Port == 80 {
			foundRemote = true
		}
	}
	if !foundLocal {
		t.Error("local backend (container IP) not found")
	}
	if !foundRemote {
		t.Error("remote backend (host address) not found")
	}
}

func TestDataPlaneNoProviderIsNoop(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)

	nodeAgent.dataPlaneReconciler.Reconcile(ctx)
}

func TestDataPlaneIncludesServicesWithNoPort(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorDataPlane := network.NewSimulatorDataPlane()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(simulatorDataPlane)

	factStore.Put(ctx, types.KeyNetworkVIPService("orphan"), []byte("10.200.0.5"))

	nodeAgent.dataPlaneReconciler.Reconcile(ctx)

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 1 {
		t.Fatalf("expected 1 VIP config (port 0, no backends), got %d", len(lastReconciled))
	}
	if lastReconciled[0].Port != 0 {
		t.Errorf("expected port 0, got %d", lastReconciled[0].Port)
	}
	if lastReconciled[0].VirtualIP != "10.200.0.5" {
		t.Errorf("expected VIP 10.200.0.5, got %s", lastReconciled[0].VirtualIP)
	}
}

func TestDataPlaneRemoteBackendSkippedWithoutHostAddress(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorDataPlane := network.NewSimulatorDataPlane()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(simulatorDataPlane)

	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "web", VIP: "10.200.0.1", Port: 80,
	})

	// Remote instance on node-3, but node-3 has no advertise address and
	// the instance has no host port — container IP is not routable cross-node.
	factStore.Put(ctx, types.KeyEndpoint("web", "instance-ccc", 80), []byte("10.100.3.2:80"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("instance-ccc"), []byte("node-3"))

	nodeAgent.dataPlaneReconciler.Reconcile(ctx)

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 1 {
		t.Fatalf("expected 1 VIP config, got %d", len(lastReconciled))
	}
	if len(lastReconciled[0].Backends) != 0 {
		t.Fatalf("expected 0 backends for unreachable remote instance, got %d", len(lastReconciled[0].Backends))
	}
}

func TestDataPlaneMultipleServices(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorDataPlane := network.NewSimulatorDataPlane()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(simulatorDataPlane)

	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "web", VIP: "10.200.0.1", Port: 80,
	})
	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "redis", VIP: "10.200.0.2", Port: 6379,
	})

	factStore.Put(ctx, types.KeyEndpoint("web", "w1", 80), []byte("10.100.1.2:80"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("w1"), []byte("node-1"))

	factStore.Put(ctx, types.KeyEndpoint("redis", "r1", 6379), []byte("10.100.1.3:6379"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("r1"), []byte("node-1"))

	nodeAgent.dataPlaneReconciler.Reconcile(ctx)

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 2 {
		t.Fatalf("expected 2 VIP configs, got %d", len(lastReconciled))
	}

	serviceNames := map[string]bool{}
	for _, config := range lastReconciled {
		serviceNames[config.ServiceName] = true
	}
	if !serviceNames["web"] || !serviceNames["redis"] {
		t.Errorf("expected both web and redis services, got %v", serviceNames)
	}
}

func TestDataPlanePublishesNodeAdvertiseAddress(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetAdvertiseAddress("192.168.100.43")

	nodeAgent.nodeReporter.PublishAdvertiseAddress(ctx)

	addressFact, err := factStore.Get(ctx, types.KeyObservedNodeAddress("node-1"))
	if err != nil {
		t.Fatalf("node address not published: %v", err)
	}
	if string(addressFact.Value) != "192.168.100.43" {
		t.Errorf("node address = %q, want %q", string(addressFact.Value), "192.168.100.43")
	}
}

func TestDataPlaneRemoteBackendUsesPerPortHostPort(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorDataPlane := network.NewSimulatorDataPlane()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(simulatorDataPlane)

	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "db", VIP: "10.200.0.3", Port: 5432,
	})

	// Remote instance with per-port hostport fact (no legacy key).
	factStore.Put(ctx, types.KeyEndpoint("db", "db-inst", 5432), []byte("10.100.2.5:5432"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("db-inst"), []byte("node-2"))
	factStore.Put(ctx, types.KeyObservedNodeAddress("node-2"), []byte("192.168.124.10"))
	factStore.Put(ctx, types.KeyObservedInstanceHostPortForPort("db-inst", 5432), []byte("5432"))

	nodeAgent.dataPlaneReconciler.Reconcile(ctx)

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 1 {
		t.Fatalf("expected 1 VIP config, got %d", len(lastReconciled))
	}
	if len(lastReconciled[0].Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(lastReconciled[0].Backends))
	}

	backend := lastReconciled[0].Backends[0]
	if backend.Address != "192.168.124.10" || backend.Port != 5432 {
		t.Errorf("backend = %s:%d, want 192.168.124.10:5432", backend.Address, backend.Port)
	}
}

func TestDataPlaneLocalBackendUsesContainerIPNotAdvertiseAddress(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorDataPlane := network.NewSimulatorDataPlane()
	ctx := context.Background()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(simulatorDataPlane)
	nodeAgent.SetAdvertiseAddress("192.168.100.43")

	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "db", VIP: "10.200.0.1", Port: 5432,
	})

	// Endpoint value uses the advertise address (as the endpoint controller
	// writes it), but the observed instance IP stores the container bridge IP.
	// The dataplane must use the container IP for local backends to avoid
	// hairpin NAT.
	factStore.Put(ctx, types.KeyEndpoint("db", "db-local", 5432), []byte("192.168.100.43:5432"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("db-local"), []byte("node-1"))
	factStore.Put(ctx, types.KeyObservedInstanceIP("db-local"), []byte("172.17.0.5"))
	factStore.Put(ctx, types.KeyObservedInstanceHostPortForPort("db-local", 5432), []byte("5432"))

	nodeAgent.dataPlaneReconciler.Reconcile(ctx)

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 1 {
		t.Fatalf("expected 1 VIP config, got %d", len(lastReconciled))
	}
	if len(lastReconciled[0].Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(lastReconciled[0].Backends))
	}

	backend := lastReconciled[0].Backends[0]
	if backend.Address != "172.17.0.5" || backend.Port != 5432 {
		t.Errorf("local backend = %s:%d, want 172.17.0.5:5432 (container bridge IP, not advertise address)", backend.Address, backend.Port)
	}
}

func TestDataPlaneRunsOnReconciliationTick(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorDataPlane := network.NewSimulatorDataPlane()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(simulatorDataPlane)
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	factStore.Put(ctx, fmt.Sprintf("%s/service/web", types.PrefixDesired), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, fmt.Sprintf("%s/service/web/instances", types.PrefixDesired), []byte("0"))

	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "web", VIP: "10.200.0.1", Port: 80,
	})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "dataplane reconcile called", func() bool {
		return simulatorDataPlane.ReconcileCallCount() > 0
	})

	cancel()
}

// ciliumCapableDataPlane is a test mock that implements both DataPlaneProvider
// and all optional Cilium interfaces (DataPlaneInitializer,
// DataPlaneNodeRegistrar, EndpointIdentityRegistrar, DataPlanePolicyReconciler).
// All fields are guarded by a mutex for race-detector safety.
type ciliumCapableDataPlane struct {
	mutex                  sync.Mutex
	initializeCalled       bool
	registerNodeCalled     bool
	registerNodeIP         string
	registerNodeCIDR       string
	endpointRegCalled      bool
	endpointRegContainerIP string
	endpointRegService     string
	policyReconcileCalled  bool
}

func (mockDP *ciliumCapableDataPlane) ReconcileVIPDataPlane(_ context.Context, _ []network.ServiceVIPConfig) error {
	return nil
}
func (mockDP *ciliumCapableDataPlane) ReconcileRouteDataPlane(_ context.Context, _ []network.RouteDataPlaneConfig) error {
	return nil
}
func (mockDP *ciliumCapableDataPlane) Cleanup(_ context.Context) error { return nil }

func (mockDP *ciliumCapableDataPlane) Initialize(_ context.Context) error {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	mockDP.initializeCalled = true
	return nil
}

func (mockDP *ciliumCapableDataPlane) RegisterNode(_ context.Context, nodeIP string, podCIDR string) error {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	mockDP.registerNodeCalled = true
	mockDP.registerNodeIP = nodeIP
	mockDP.registerNodeCIDR = podCIDR
	return nil
}

func (mockDP *ciliumCapableDataPlane) RegisterEndpointIdentity(_ context.Context, containerIP string, _ string, serviceName string, _ int) error {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	mockDP.endpointRegCalled = true
	mockDP.endpointRegContainerIP = containerIP
	mockDP.endpointRegService = serviceName
	return nil
}

func (mockDP *ciliumCapableDataPlane) ReconcilePolicies(_ context.Context) error {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	mockDP.policyReconcileCalled = true
	return nil
}

func (mockDP *ciliumCapableDataPlane) isInitializeCalled() bool {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.initializeCalled
}

func (mockDP *ciliumCapableDataPlane) isRegisterNodeCalled() bool {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.registerNodeCalled
}

func (mockDP *ciliumCapableDataPlane) isEndpointRegCalled() bool {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.endpointRegCalled
}

func (mockDP *ciliumCapableDataPlane) isPolicyReconcileCalled() bool {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.policyReconcileCalled
}

func (mockDP *ciliumCapableDataPlane) getEndpointRegContainerIP() string {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.endpointRegContainerIP
}

func (mockDP *ciliumCapableDataPlane) getEndpointRegService() string {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.endpointRegService
}

func (mockDP *ciliumCapableDataPlane) getRegisterNodeIP() string {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.registerNodeIP
}

func (mockDP *ciliumCapableDataPlane) getRegisterNodeCIDR() string {
	mockDP.mutex.Lock()
	defer mockDP.mutex.Unlock()
	return mockDP.registerNodeCIDR
}

// TestDataPlaneInitializerCalledOnStartup verifies that the agent calls
// Initialize and RegisterNode on data plane providers that implement those
// optional interfaces.
func TestDataPlaneInitializerCalledOnStartup(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &ciliumCapableDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	nodeAgent.SetAdvertiseAddress("192.168.1.10")
	nodeAgent.SetNodeSubnet("10.100.1.0/24")
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "initializer and node registrar called", func() bool {
		return mockDP.isInitializeCalled() && mockDP.isRegisterNodeCalled()
	})

	cancel()

	if mockDP.getRegisterNodeIP() != "192.168.1.10" {
		t.Errorf("expected RegisterNode IP '192.168.1.10', got %q", mockDP.getRegisterNodeIP())
	}
	if mockDP.getRegisterNodeCIDR() != "10.100.1.0/24" {
		t.Errorf("expected RegisterNode CIDR '10.100.1.0/24', got %q", mockDP.getRegisterNodeCIDR())
	}
}

// TestDataPlanePolicyReconcilerCalledOnTick verifies that the data plane
// reconciler calls ReconcilePolicies on providers that implement it.
func TestDataPlanePolicyReconcilerCalledOnTick(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &ciliumCapableDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	factStore.Put(ctx, fmt.Sprintf("%s/service/web", types.PrefixDesired), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, fmt.Sprintf("%s/service/web/instances", types.PrefixDesired), []byte("0"))

	types.WriteServiceVIP(ctx, factStore, types.ServiceVIP{
		Service: "web", VIP: "10.200.0.1", Port: 80,
	})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "policy reconciler called", func() bool {
		return mockDP.isPolicyReconcileCalled()
	})

	cancel()
}

// failingInitDataPlane is a mock that fails on Initialize to verify the agent
// handles initialization failures gracefully.
type failingInitDataPlane struct {
	initCalled         atomic.Bool
	registerNodeCalled atomic.Bool
}

func (mockDP *failingInitDataPlane) ReconcileVIPDataPlane(_ context.Context, _ []network.ServiceVIPConfig) error {
	return nil
}
func (mockDP *failingInitDataPlane) ReconcileRouteDataPlane(_ context.Context, _ []network.RouteDataPlaneConfig) error {
	return nil
}
func (mockDP *failingInitDataPlane) Cleanup(_ context.Context) error { return nil }

func (mockDP *failingInitDataPlane) Initialize(_ context.Context) error {
	mockDP.initCalled.Store(true)
	return fmt.Errorf("simulated initialization failure")
}

func (mockDP *failingInitDataPlane) RegisterNode(_ context.Context, _ string, _ string) error {
	mockDP.registerNodeCalled.Store(true)
	return nil
}

func (mockDP *failingInitDataPlane) ReconcilePolicies(_ context.Context) error {
	return nil
}

// TestDataPlaneInitializeFailureSkipsNodeRegistration verifies that when
// Initialize returns an error, RegisterNode is not called but the agent
// continues running.
func TestDataPlaneInitializeFailureSkipsNodeRegistration(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &failingInitDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	nodeAgent.SetAdvertiseAddress("192.168.1.10")
	nodeAgent.SetNodeSubnet("10.100.1.0/24")
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	// Write a service so the agent has work to do.
	factStore.Put(ctx, fmt.Sprintf("%s/service/web", types.PrefixDesired), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, fmt.Sprintf("%s/service/web/instances", types.PrefixDesired), []byte("0"))

	go nodeAgent.Run(ctx)

	// Agent should still reconcile despite init failure.
	waitFor(t, 2*time.Second, "agent continues after init failure", func() bool {
		return mockDP.initCalled.Load()
	})

	cancel()

	if mockDP.registerNodeCalled.Load() {
		t.Error("RegisterNode should not be called when Initialize fails")
	}
}

// TestDataPlaneNoSubnetSkipsNodeRegistration verifies that when no node subnet
// is set, RegisterNode is not called even if the provider supports it.
func TestDataPlaneNoSubnetSkipsNodeRegistration(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &ciliumCapableDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	nodeAgent.SetAdvertiseAddress("192.168.1.10")
	// Deliberately NOT setting node subnet.
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "initializer called", func() bool {
		return mockDP.isInitializeCalled()
	})

	cancel()

	if mockDP.isRegisterNodeCalled() {
		t.Error("RegisterNode should not be called when node subnet is empty")
	}
}

// TestDataPlaneNoAdvertiseAddressSkipsNodeRegistration verifies that when no
// advertise address is set, RegisterNode is not called.
func TestDataPlaneNoAdvertiseAddressSkipsNodeRegistration(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &ciliumCapableDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	// Deliberately NOT setting advertise address.
	nodeAgent.SetNodeSubnet("10.100.1.0/24")
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "initializer called", func() bool {
		return mockDP.isInitializeCalled()
	})

	cancel()

	if mockDP.isRegisterNodeCalled() {
		t.Error("RegisterNode should not be called when advertise address is empty")
	}
}

// staticNetworkProvider is a mock NetworkProvider that always allocates a
// fixed IP, enabling endpoint identity registration tests.
type staticNetworkProvider struct {
	allocatedIP string
}

func (mockNP *staticNetworkProvider) AllocateIP(_ context.Context, _ string, _ string) (string, error) {
	return mockNP.allocatedIP, nil
}
func (mockNP *staticNetworkProvider) ReleaseIP(_ context.Context, _ string, _ string) error {
	return nil
}
func (mockNP *staticNetworkProvider) NodeSubnet(_ string) string { return "10.100.1.0/24" }

// TestDataPlaneEndpointRegistrationCalledWithNetworkProvider verifies that
// when a network provider allocates an IP, registerEndpointIdentityIfSupported
// is called with the correct container IP.
func TestDataPlaneEndpointRegistrationCalledWithNetworkProvider(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &ciliumCapableDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	nodeAgent.SetNetworkProvider(&staticNetworkProvider{allocatedIP: "10.100.1.42"})
	nodeAgent.SetAdvertiseAddress("192.168.1.10")
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "ep-net-1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "ep-net-1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 3*time.Second, "endpoint identity registration called", func() bool {
		return mockDP.isEndpointRegCalled()
	})

	cancel()

	if mockDP.getEndpointRegContainerIP() != "10.100.1.42" {
		t.Errorf("expected container IP '10.100.1.42', got %q", mockDP.getEndpointRegContainerIP())
	}
	if mockDP.getEndpointRegService() != "web" {
		t.Errorf("expected service 'web', got %q", mockDP.getEndpointRegService())
	}
}

// TestDataPlaneEndpointRegistrationSkippedWithoutNetworkProvider verifies
// that when no network provider is configured and the runtime doesn't
// report an IP, endpoint identity registration is skipped (empty IP).
func TestDataPlaneEndpointRegistrationSkippedWithoutNetworkProvider(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &ciliumCapableDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	// No network provider — simulator runtime doesn't report container IPs.
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "ep-skip-1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "ep-skip-1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for the instance to start (agent reconciles it).
	waitFor(t, 2*time.Second, "instance started", func() bool {
		st, err := simulatorRuntime.Status(ctx, "ep-skip-1")
		return err == nil && st.Running
	})

	// Small delay for any post-start processing.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Without a network provider, container IP is empty so registration is skipped.
	if mockDP.isEndpointRegCalled() {
		t.Error("RegisterEndpointIdentity should not be called when container IP is empty")
	}
}

// nonIdentityDataPlane is a mock that implements DataPlaneProvider but NOT
// EndpointIdentityRegistrar, verifying the agent skips identity registration.
type nonIdentityDataPlane struct {
	reconcileCallCount atomic.Int32
}

func (mockDP *nonIdentityDataPlane) ReconcileVIPDataPlane(_ context.Context, _ []network.ServiceVIPConfig) error {
	mockDP.reconcileCallCount.Add(1)
	return nil
}
func (mockDP *nonIdentityDataPlane) ReconcileRouteDataPlane(_ context.Context, _ []network.RouteDataPlaneConfig) error {
	return nil
}
func (mockDP *nonIdentityDataPlane) Cleanup(_ context.Context) error { return nil }

// TestDataPlaneNonIdentityProviderSkipsRegistration verifies that data plane
// providers that don't implement EndpointIdentityRegistrar gracefully skip
// identity registration without error.
func TestDataPlaneNonIdentityProviderSkipsRegistration(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	mockDP := &nonIdentityDataPlane{}
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetDataPlaneProvider(mockDP)
	nodeAgent.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "non-id-1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "non-id-1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Should still reconcile VIP data plane without crashing.
	waitFor(t, 2*time.Second, "dataplane reconcile runs", func() bool {
		return mockDP.reconcileCallCount.Load() > 0
	})

	cancel()
}
