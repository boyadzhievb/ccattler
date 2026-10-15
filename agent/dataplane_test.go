// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"fmt"
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

func TestDataPlaneSkipsServicesWithNoPort(t *testing.T) {
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
	if len(lastReconciled) != 0 {
		t.Errorf("expected 0 VIP configs for service with no port, got %d", len(lastReconciled))
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
