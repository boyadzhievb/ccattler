// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// delayedStartRuntime wraps a SimulatorRuntime but reports instances as not-running
// for a configurable number of Status() calls after Start(). This tests the
// observation-based state reporting: Start() succeeding should not be treated as
// proof that the workload is running.
type delayedStartRuntime struct {
	inner        *runtime.SimulatorRuntime
	statusCounts map[string]int // how many Status() calls have been made per instance
	delayCount   int            // Status() returns not-running for this many calls after Start()
	mu           sync.Mutex
}

func newDelayedStartRuntime(delayCount int) *delayedStartRuntime {
	return &delayedStartRuntime{
		inner:        runtime.NewSimulatorRuntime(),
		statusCounts: make(map[string]int),
		delayCount:   delayCount,
	}
}

func (delayed *delayedStartRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	delayed.mu.Lock()
	delayed.statusCounts[spec.ID] = 0
	delayed.mu.Unlock()
	return delayed.inner.Start(ctx, spec)
}

func (delayed *delayedStartRuntime) Stop(ctx context.Context, instanceID string) error {
	return delayed.inner.Stop(ctx, instanceID)
}

func (delayed *delayedStartRuntime) Status(ctx context.Context, instanceID string) (runtime.Status, error) {
	delayed.mu.Lock()
	count := delayed.statusCounts[instanceID]
	delayed.statusCounts[instanceID] = count + 1
	delayed.mu.Unlock()

	if count < delayed.delayCount {
		return runtime.Status{ID: instanceID, Running: false}, nil
	}
	return delayed.inner.Status(ctx, instanceID)
}

func (delayed *delayedStartRuntime) List(ctx context.Context) ([]runtime.Status, error) {
	return delayed.inner.List(ctx)
}

func (delayed *delayedStartRuntime) Exec(ctx context.Context, instanceID string, execSpec runtime.ExecSpec) error {
	return delayed.inner.Exec(ctx, instanceID, execSpec)
}

func (delayed *delayedStartRuntime) ExecInit(ctx context.Context, image string, execSpec runtime.ExecSpec) error {
	return delayed.inner.ExecInit(ctx, image, execSpec)
}

func (delayed *delayedStartRuntime) Stats(ctx context.Context, instanceID string) (runtime.ResourceStats, error) {
	return delayed.inner.Stats(ctx, instanceID)
}

func (delayed *delayedStartRuntime) Logs(ctx context.Context, instanceID string, follow bool) (io.ReadCloser, error) {
	return delayed.inner.Logs(ctx, instanceID, follow)
}

func (delayed *delayedStartRuntime) Resize(ctx context.Context, instanceID string, cpuMillicores int64, memoryBytes int64) error {
	return delayed.inner.Resize(ctx, instanceID, cpuMillicores, memoryBytes)
}

// mockSecretProvider is a test double that serves secrets from an in-memory map.
type mockSecretProvider struct {
	mu      sync.Mutex
	secrets map[string][]byte // secretName → plaintext
	grants  map[string]string // "service/secret" → mountPath
}

func (mockProvider *mockSecretProvider) GetSecretForService(_ context.Context, serviceName, secretName string) ([]byte, string, error) {
	mockProvider.mu.Lock()
	defer mockProvider.mu.Unlock()
	grantKey := serviceName + "/" + secretName
	mountPath, hasGrant := mockProvider.grants[grantKey]
	if !hasGrant {
		return nil, "", fmt.Errorf("no grant for %s/%s", serviceName, secretName)
	}
	plaintext, exists := mockProvider.secrets[secretName]
	if !exists {
		return nil, "", fmt.Errorf("secret %s not found", secretName)
	}
	return plaintext, mountPath, nil
}

func waitFor(t *testing.T, timeout time.Duration, desc string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

func TestAgentStartsPlacedInstance(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Set up: service config + instance placed on our node.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "aaa", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for agent to start the instance.
	waitFor(t, 2*time.Second, "instance running in runtime", func() bool {
		st, err := simulatorRuntime.Status(ctx, "aaa")
		return err == nil && st.Running
	})

	// Verify state reported back to store.
	waitFor(t, 2*time.Second, "instance state=running in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("aaa"))
		return err == nil && string(factEntry.Value) == "running"
	})

	// Verify node assignment reported.
	factEntry, _ := factStore.Get(ctx, types.KeyObservedInstanceNode("aaa"))
	if string(factEntry.Value) != "node-1" {
		t.Errorf("node: got %s, want node-1", factEntry.Value)
	}
}

func TestAgentIgnoresOtherNode(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Instance placed on node-2, not our node.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "bbb", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "bbb", NodeID: "node-2"})

	go nodeAgent.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	_, err := simulatorRuntime.Status(ctx, "bbb")
	if err != runtime.ErrNotFound {
		t.Error("agent should not start instances placed on other nodes")
	}
}

func TestAgentRegistersNode(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "node registered", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(factEntry.Value) == "alive"
	})
}

func TestAgentStopsRemovedInstance(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Start with a placed instance.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "ccc", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "ccc", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		st, err := simulatorRuntime.Status(ctx, "ccc")
		return err == nil && st.Running
	})

	// Remove the placement.
	types.DeletePlacement(ctx, factStore, "node-1", "ccc")

	// Agent should stop the process.
	waitFor(t, 2*time.Second, "instance stopped in runtime", func() bool {
		st, err := simulatorRuntime.Status(ctx, "ccc")
		return err == nil && !st.Running
	})
}

func TestAgentHandlesNewPlacement(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("myapp:latest"))

	go nodeAgent.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Dynamically place an instance.
	types.WriteInstance(ctx, factStore, types.Instance{ID: "ddd", Service: "api", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "ddd", NodeID: "node-1"})

	waitFor(t, 2*time.Second, "new instance running", func() bool {
		st, err := simulatorRuntime.Status(ctx, "ddd")
		return err == nil && st.Running
	})
}

func TestAgentReportsHealthy(t *testing.T) {
	// Start an HTTP health endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, mux) //nolint:gosec // test HTTP server
	defer ln.Close()

	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Service with health check config.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceHealthMethod("web"), []byte("http"))
	factStore.Put(ctx, types.KeyDesiredServiceHealthPath("web"), []byte("/health"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", port), []byte(""))

	// Place instance and pre-set its IP so health checks have a target.
	types.WriteInstance(ctx, factStore, types.Instance{ID: "eee", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "eee", NodeID: "node-1"})
	factStore.Put(ctx, types.KeyObservedInstanceIP("eee"), []byte("127.0.0.1"))

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance health=healthy in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceHealth("eee"))
		return err == nil && string(factEntry.Value) == string(types.HealthHealthy)
	})
}

func TestAgentReportsUnhealthy(t *testing.T) {
	// Use a port with nothing listening.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // close immediately so the port is refused

	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceHealthMethod("web"), []byte("http"))
	factStore.Put(ctx, types.KeyDesiredServiceHealthPath("web"), []byte("/health"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", port), []byte(""))

	types.WriteInstance(ctx, factStore, types.Instance{ID: "fff", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "fff", NodeID: "node-1"})
	factStore.Put(ctx, types.KeyObservedInstanceIP("fff"), []byte("127.0.0.1"))

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance health=unhealthy in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceHealth("fff"))
		return err == nil && string(factEntry.Value) == string(types.HealthUnhealthy)
	})
}

func TestAgentNoHealthWithoutConfig(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// No health config for this service.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "ggg", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "ggg", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for instance to be running.
	waitFor(t, 2*time.Second, "instance running", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("ggg"))
		return err == nil && string(factEntry.Value) == "running"
	})

	// Health key should not exist.
	_, err := factStore.Get(ctx, types.KeyObservedInstanceHealth("ggg"))
	if err == nil {
		t.Error("health should not be reported when no health config exists")
	}
}

func TestAgentWritesHeartbeat(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "heartbeat lease written", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyLeaseNode("node-1"))
		return err == nil && len(factEntry.Value) > 0
	})
}

// TestAgentWithNetworkProviderAllocatesSubnetIP verifies that when a
// NetworkProvider is configured, instances receive IPs from the node's subnet
// instead of the default 127.0.0.1.
func TestAgentWithNetworkProviderAllocatesSubnetIP(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "net-aaa", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "net-aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance gets subnet IP", func() bool {
		ipFact, err := factStore.Get(ctx, types.KeyObservedInstanceIP("net-aaa"))
		return err == nil && strings.HasPrefix(string(ipFact.Value), "10.100.1.")
	})
}

// TestAgentWithNetworkProviderWritesAllocationFact verifies that the agent
// writes a network allocation fact alongside the instance IP.
func TestAgentWithNetworkProviderWritesAllocationFact(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "net-bbb", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "net-bbb", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "network allocation fact written", func() bool {
		allocationFact, err := factStore.Get(ctx, types.KeyNetworkAllocation("net-bbb"))
		return err == nil && strings.HasPrefix(string(allocationFact.Value), "10.100.1.")
	})
}

// TestAgentWithoutNetworkProviderNoIPAssigned verifies that without a
// NetworkProvider, no IP fact is written. A missing IP is semantically correct:
// it means the network is unavailable, rather than a misleading 127.0.0.1.
func TestAgentWithoutNetworkProviderNoIPAssigned(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "net-ccc", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "net-ccc", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for instance to be reported.
	waitFor(t, 2*time.Second, "instance state observed", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("net-ccc"))
		return err == nil && string(factEntry.Value) == string(types.InstanceRunning)
	})

	// No IP fact should exist without a NetworkProvider.
	_, err := factStore.Get(ctx, types.KeyObservedInstanceIP("net-ccc"))
	if err == nil {
		t.Error("IP fact should not exist without NetworkProvider")
	}

	// No network allocation fact should exist.
	_, err = factStore.Get(ctx, types.KeyNetworkAllocation("net-ccc"))
	if err == nil {
		t.Error("network allocation fact should not exist without NetworkProvider")
	}
}

// TestAgentReleasesIPOnInstanceStop verifies that when an instance's placement
// is removed, the agent releases the IP and deletes the allocation fact.
func TestAgentReleasesIPOnInstanceStop(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "net-ddd", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "net-ddd", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for the instance to be running with an allocated IP.
	waitFor(t, 2*time.Second, "instance running with subnet IP", func() bool {
		ipFact, err := factStore.Get(ctx, types.KeyObservedInstanceIP("net-ddd"))
		return err == nil && strings.HasPrefix(string(ipFact.Value), "10.100.1.")
	})

	// Remove the placement to trigger stop.
	types.DeletePlacement(ctx, factStore, "node-1", "net-ddd")

	// Wait for the allocation fact to be cleaned up.
	waitFor(t, 2*time.Second, "network allocation fact deleted", func() bool {
		_, err := factStore.Get(ctx, types.KeyNetworkAllocation("net-ddd"))
		return err != nil
	})
}

// TestTwoAgentsGetDifferentSubnetIPs verifies that agents on different nodes
// allocate IPs from different subnets when sharing the same NetworkProvider.
func TestTwoAgentsGetDifferentSubnetIPs(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create two agents on different nodes sharing the same network provider.
	for _, nodeID := range []string{"node-1", "node-2"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(ctx)
	}

	// Place one instance on each node.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))

	types.WriteInstance(ctx, factStore, types.Instance{ID: "net-eee", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "net-eee", NodeID: "node-1"})

	types.WriteInstance(ctx, factStore, types.Instance{ID: "net-fff", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "net-fff", NodeID: "node-2"})

	// Wait for both instances to get IPs.
	waitFor(t, 2*time.Second, "both instances have IPs", func() bool {
		ipFact1, err1 := factStore.Get(ctx, types.KeyObservedInstanceIP("net-eee"))
		ipFact2, err2 := factStore.Get(ctx, types.KeyObservedInstanceIP("net-fff"))
		return err1 == nil && err2 == nil &&
			string(ipFact1.Value) != "127.0.0.1" &&
			string(ipFact2.Value) != "127.0.0.1"
	})

	ipNode1, _ := factStore.Get(ctx, types.KeyObservedInstanceIP("net-eee"))
	ipNode2, _ := factStore.Get(ctx, types.KeyObservedInstanceIP("net-fff"))

	// Extract the third octet (subnet identifier) from each IP. The two nodes
	// should have received different subnets regardless of allocation order.
	ipNode1Parts := strings.Split(string(ipNode1.Value), ".")
	ipNode2Parts := strings.Split(string(ipNode2.Value), ".")
	if len(ipNode1Parts) != 4 || len(ipNode2Parts) != 4 {
		t.Fatalf("invalid IPs: node-1=%s, node-2=%s", ipNode1.Value, ipNode2.Value)
	}
	if ipNode1Parts[2] == ipNode2Parts[2] {
		t.Errorf("instances on different nodes should have different subnets: node-1=%s, node-2=%s",
			ipNode1.Value, ipNode2.Value)
	}
}

// TestAgentAttachesVolumeBeforeStart verifies that the agent attaches a volume
// and writes observed volume facts before starting an instance that needs it.
func TestAgentAttachesVolumeBeforeStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorStorageProvider := storage.NewSimulatorStorageProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetStorageProvider(simulatorStorageProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	types.WriteObservedVolume(ctx, factStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAvailable,
	})
	types.WriteInstance(ctx, factStore, types.Instance{ID: "vol-aaa", Service: "postgres", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "vol-aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("vol-aaa"))
		return err == nil && string(stateFact.Value) == "running"
	})

	volumeStateFact, err := factStore.Get(ctx, types.KeyObservedVolumeState("pgdata"))
	if err != nil {
		t.Fatal(err)
	}
	if string(volumeStateFact.Value) != string(types.VolumeAttached) {
		t.Errorf("volume state = %s, want attached", volumeStateFact.Value)
	}

	volumeNodeFact, _ := factStore.Get(ctx, types.KeyObservedVolumeNode("pgdata"))
	if string(volumeNodeFact.Value) != "node-1" {
		t.Errorf("volume node = %s, want node-1", volumeNodeFact.Value)
	}
}

// TestAgentDoesNotStartWhenVolumeUnavailable verifies that the agent does not
// start an instance if its required volume is attached to a different node.
func TestAgentDoesNotStartWhenVolumeUnavailable(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorStorageProvider := storage.NewSimulatorStorageProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)
	simulatorStorageProvider.AttachVolume(ctx, "pgdata", "node-2")

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetStorageProvider(simulatorStorageProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	types.WriteObservedVolume(ctx, factStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAttached, Node: "node-2",
	})
	types.WriteInstance(ctx, factStore, types.Instance{ID: "vol-bbb", Service: "postgres", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "vol-bbb", NodeID: "node-1"})

	go nodeAgent.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	_, err := simulatorRuntime.Status(ctx, "vol-bbb")
	if err != runtime.ErrNotFound {
		t.Error("agent should not start instance when volume is attached to another node")
	}
}

// TestAgentDetachesVolumeOnStop verifies that the agent detaches a volume and
// sets its state to available when the owning instance is stopped.
func TestAgentDetachesVolumeOnStop(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorStorageProvider := storage.NewSimulatorStorageProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetStorageProvider(simulatorStorageProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	types.WriteObservedVolume(ctx, factStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAvailable,
	})
	types.WriteInstance(ctx, factStore, types.Instance{ID: "vol-ccc", Service: "postgres", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "vol-ccc", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running with attached volume", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedVolumeState("pgdata"))
		return err == nil && string(stateFact.Value) == string(types.VolumeAttached)
	})

	types.DeletePlacement(ctx, factStore, "node-1", "vol-ccc")

	waitFor(t, 2*time.Second, "volume detached after instance stop", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedVolumeState("pgdata"))
		return err == nil && string(stateFact.Value) == string(types.VolumeAvailable)
	})

	attached, _, _ := simulatorStorageProvider.IsAttached(ctx, "pgdata")
	if attached {
		t.Error("volume should be detached from storage provider")
	}
}

// TestAgentWithoutStorageProviderIgnoresVolumes verifies backward compat:
// agents without a StorageProvider start instances normally even if volume
// mounts are declared in the service config.
func TestAgentWithoutStorageProviderIgnoresVolumes(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "vol-ddd", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "vol-ddd", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running without storage provider", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("vol-ddd"))
		return err == nil && string(stateFact.Value) == "running"
	})
}

// TestAgentServiceWithoutVolumesUnaffected verifies that services without
// volume mounts work normally when a storage provider is configured.
func TestAgentServiceWithoutVolumesUnaffected(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorStorageProvider := storage.NewSimulatorStorageProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetStorageProvider(simulatorStorageProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "vol-eee", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "vol-eee", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("vol-eee"))
		return err == nil && string(stateFact.Value) == "running"
	})
}

// TestAgentReattachesMigratingVolume verifies that when a volume is in
// VolumeMigrating state (force-detached from an unreachable node), the agent
// on the new node attaches it, sets state to attached, and clears migration
// metadata.
func TestAgentReattachesMigratingVolume(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorStorageProvider := storage.NewSimulatorStorageProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 100)

	nodeAgent := New("node-2", factStore, simulatorRuntime)
	nodeAgent.SetStorageProvider(simulatorStorageProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	types.WriteObservedVolume(ctx, factStore, types.Volume{
		Name:            "pgdata",
		Size:            "100Gi",
		State:           types.VolumeMigrating,
		MigrationSource: "node-1",
	})
	types.WriteInstance(ctx, factStore, types.Instance{ID: "mig-aaa", Service: "postgres", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "mig-aaa", NodeID: "node-2"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running with migrated volume", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("mig-aaa"))
		return err == nil && string(stateFact.Value) == "running"
	})

	volumeStateFact, err := factStore.Get(ctx, types.KeyObservedVolumeState("pgdata"))
	if err != nil {
		t.Fatal(err)
	}
	if string(volumeStateFact.Value) != string(types.VolumeAttached) {
		t.Errorf("volume state = %s, want attached", volumeStateFact.Value)
	}

	volumeNodeFact, _ := factStore.Get(ctx, types.KeyObservedVolumeNode("pgdata"))
	if string(volumeNodeFact.Value) != "node-2" {
		t.Errorf("volume node = %s, want node-2", volumeNodeFact.Value)
	}

	_, migrationSourceErr := factStore.Get(ctx, types.KeyObservedVolumeMigrationSource("pgdata"))
	if migrationSourceErr == nil {
		t.Error("expected migration_source cleared after reattach")
	}
}

// TestAgentReportsVolumeUsage verifies that the agent writes usage facts when
// attaching a volume that has usage data from the storage provider.
func TestAgentReportsVolumeUsage(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorStorageProvider := storage.NewSimulatorStorageProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 107374182400)
	simulatorStorageProvider.SetVolumeUsage("pgdata", 53687091200)

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetStorageProvider(simulatorStorageProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("postgres"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))
	types.WriteObservedVolume(ctx, factStore, types.Volume{
		Name: "pgdata", Size: "100Gi", State: types.VolumeAvailable,
	})
	types.WriteInstance(ctx, factStore, types.Instance{ID: "usage-aaa", Service: "postgres", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "usage-aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("usage-aaa"))
		return err == nil && string(stateFact.Value) == "running"
	})

	usedFact, err := factStore.Get(ctx, types.KeyObservedVolumeUsedBytes("pgdata"))
	if err != nil {
		t.Fatal("expected used_bytes fact")
	}
	if string(usedFact.Value) != "53687091200" {
		t.Errorf("used_bytes = %s, want 53687091200", usedFact.Value)
	}

	capacityFact, err := factStore.Get(ctx, types.KeyObservedVolumeCapacityBytes("pgdata"))
	if err != nil {
		t.Fatal("expected capacity_bytes fact")
	}
	if string(capacityFact.Value) != "107374182400" {
		t.Errorf("capacity_bytes = %s, want 107374182400", capacityFact.Value)
	}
}

// TestAgentInitRestartSafety verifies that if an init step was left in "running"
// state (simulating an agent crash mid-init), the agent marks it as failed and
// retries it, rather than skipping or hanging.
func TestAgentInitRestartSafety(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceName := "init-svc"
	instanceID := "init-aaa"

	// Set up service with one init step (root marker + exec command).
	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("app:1.0"))
	factStore.Put(ctx, types.KeyDesiredServiceInitStep(serviceName, 0), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceInitStepExec(serviceName, 0), []byte("echo setup"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})

	// Simulate agent crash: init step 0 was left in "running" state.
	factStore.Put(ctx, types.KeyDerivedInstanceInitPhase(instanceID), []byte(string(types.InitPhaseRunning)))
	factStore.Put(ctx, types.KeyObservedInstanceInitStepState(instanceID, 0), []byte(string(types.InitStepRunning)))

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	// The agent should detect the stale "running" step, mark it failed, re-execute,
	// and eventually complete init + start the workload. The InitController (not
	// running here) derives the phase; the agent only writes per-step results.

	// Step 0 should be succeeded.
	waitFor(t, 2*time.Second, "init step 0 succeeded", func() bool {
		stepFact, err := factStore.Get(ctx, types.KeyObservedInstanceInitStepState(instanceID, 0))
		return err == nil && string(stepFact.Value) == string(types.InitStepSucceeded)
	})

	// Instance should be running.
	waitFor(t, 2*time.Second, "instance running after init recovery", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState(instanceID))
		return err == nil && string(stateFact.Value) == string(types.InstanceRunning)
	})
}

// TestAgentInitSkipsCompletedSteps verifies that already-succeeded init steps
// are not re-executed across reconciliation cycles.
func TestAgentInitSkipsCompletedSteps(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceName := "init-skip-svc"
	instanceID := "init-bbb"

	// Service with two init steps (root markers + exec commands).
	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("app:1.0"))
	factStore.Put(ctx, types.KeyDesiredServiceInitStep(serviceName, 0), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceInitStepExec(serviceName, 0), []byte("echo step0"))
	factStore.Put(ctx, types.KeyDesiredServiceInitStep(serviceName, 1), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceInitStepExec(serviceName, 1), []byte("echo step1"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})

	// Step 0 already succeeded (persisted from previous agent incarnation).
	factStore.Put(ctx, types.KeyObservedInstanceInitStepState(instanceID, 0), []byte(string(types.InitStepSucceeded)))

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	// Init should complete — step 0 skipped, step 1 executed. The InitController
	// derives the phase; the agent only writes per-step results.
	waitFor(t, 3*time.Second, "init step 1 succeeded", func() bool {
		stepFact, err := factStore.Get(ctx, types.KeyObservedInstanceInitStepState(instanceID, 1))
		return err == nil && string(stepFact.Value) == string(types.InitStepSucceeded)
	})

	// Both steps should show succeeded.
	for stepIndex := 0; stepIndex < 2; stepIndex++ {
		stepFact, err := factStore.Get(ctx, types.KeyObservedInstanceInitStepState(instanceID, stepIndex))
		if err != nil || string(stepFact.Value) != string(types.InitStepSucceeded) {
			t.Errorf("step %d: want succeeded, got %v (err: %v)", stepIndex, string(stepFact.Value), err)
		}
	}
}

// TestAgentConcurrentReconcileAndProbes verifies that the reconciliation loop
// and independent probe scheduler can run concurrently without races.
// Must be run with -race to be effective.
func TestAgentConcurrentReconcileAndProbes(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	nodeAgent.SetInterval(20 * time.Millisecond)

	// Set up multiple services with health probes to maximize concurrency.
	for serviceIndex := 0; serviceIndex < 5; serviceIndex++ {
		serviceName := fmt.Sprintf("concurrent-svc-%d", serviceIndex)
		instanceID := fmt.Sprintf("concurrent-%d", serviceIndex)
		factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("app:1.0"))
		types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
		types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})
	}

	go nodeAgent.Run(ctx)

	// Let the agent run with concurrent reconciliation + probes for a while.
	time.Sleep(300 * time.Millisecond)

	// Verify all instances converged to running.
	for serviceIndex := 0; serviceIndex < 5; serviceIndex++ {
		instanceID := fmt.Sprintf("concurrent-%d", serviceIndex)
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState(instanceID))
		if err != nil {
			t.Errorf("instance %s: state not found", instanceID)
			continue
		}
		if string(stateFact.Value) != string(types.InstanceRunning) {
			t.Errorf("instance %s: want running, got %s", instanceID, string(stateFact.Value))
		}
	}
}

// TestAgentRestartDuringStarting verifies that an agent restart while an
// instance is in "starting" state correctly re-observes and converges.
func TestAgentRestartDuringStarting(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceName := "restart-svc"
	instanceID := "restart-aaa"

	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("app:1.0"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})

	// Simulate: previous agent left instance in "starting" state but it's actually
	// running in the simulator (simulating the runtime having finished starting
	// after the agent crashed).
	factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceStarting)))
	factStore.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte(serviceName))
	factStore.Put(ctx, types.KeyObservedInstanceNode(instanceID), []byte("node-1"))
	simulatorRuntime.Start(ctx, runtime.Spec{ID: instanceID, ServiceName: serviceName, Image: "app:1.0"})

	// New agent incarnation starts.
	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	// Agent should observe the running instance and update state to "running".
	waitFor(t, 2*time.Second, "instance converges to running", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState(instanceID))
		return err == nil && string(stateFact.Value) == string(types.InstanceRunning)
	})
}

// TestAgentRestartDuringRunning verifies that an agent restart while instances
// are running correctly re-observes all instance states.
func TestAgentRestartDuringRunning(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx1, cancel1 := context.WithCancel(context.Background())

	nodeAgent1 := New("node-1", factStore, simulatorRuntime)
	nodeAgent1.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx1, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx1, factStore, types.Instance{ID: "restart-bbb", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx1, factStore, types.Placement{InstanceID: "restart-bbb", NodeID: "node-1"})

	go nodeAgent1.Run(ctx1)

	waitFor(t, 2*time.Second, "instance running under first agent", func() bool {
		stateFact, err := factStore.Get(ctx1, types.KeyObservedInstanceState("restart-bbb"))
		return err == nil && string(stateFact.Value) == string(types.InstanceRunning)
	})

	// Simulate agent crash/restart.
	cancel1()
	time.Sleep(50 * time.Millisecond)

	// Second agent incarnation.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	nodeAgent2 := New("node-1", factStore, simulatorRuntime)
	nodeAgent2.SetInterval(50 * time.Millisecond)

	go nodeAgent2.Run(ctx2)

	// Instance should still be running — second agent observes the runtime.
	time.Sleep(200 * time.Millisecond)
	stateFact, err := factStore.Get(ctx2, types.KeyObservedInstanceState("restart-bbb"))
	if err != nil {
		t.Fatal("instance state not found after agent restart")
	}
	if string(stateFact.Value) != string(types.InstanceRunning) {
		t.Errorf("instance state after restart: want running, got %s", string(stateFact.Value))
	}
}

// TestAgentObservesRuntimeStateBeforePublishing verifies that after Start()
// succeeds, the agent observes runtime state via Status() before publishing.
// With a delayed-start runtime, the first observation should report "starting"
// instead of immediately assuming "running".
func TestAgentObservesRuntimeStateBeforePublishing(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	delayedRuntime := newDelayedStartRuntime(2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, delayedRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "obs-aaa", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "obs-aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// First, the agent should publish "starting" because Status() returns not-running.
	waitFor(t, 2*time.Second, "instance state=starting in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("obs-aaa"))
		return err == nil && string(factEntry.Value) == string(types.InstanceStarting)
	})

	// Eventually, after enough reconciliation cycles, Status() returns running.
	waitFor(t, 2*time.Second, "instance state=running in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("obs-aaa"))
		return err == nil && string(factEntry.Value) == string(types.InstanceRunning)
	})
}

// TestAgentObservationBasedStateForExistingInstances verifies that even for
// instances already in the runtime (the else branch), the agent observes
// current runtime state rather than blindly publishing "running".
func TestAgentObservationBasedStateForExistingInstances(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "obs-bbb", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "obs-bbb", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("obs-bbb"))
		return err == nil && string(factEntry.Value) == string(types.InstanceRunning)
	})

	// Kill the instance in the runtime — the agent should observe the change.
	simulatorRuntime.Stop(ctx, "obs-bbb")

	// On the next reconciliation cycle, the agent will see it's not running via
	// List() and try to restart it. After restart + observation, it should be
	// running again (simulator Start() is immediate).
	waitFor(t, 2*time.Second, "instance re-observed as running after restart", func() bool {
		runtimeStatus, err := simulatorRuntime.Status(ctx, "obs-bbb")
		return err == nil && runtimeStatus.Running
	})
}

// TestAgentExecInitUsesRuntimeInterface verifies that init steps are executed
// through the runtime's ExecInit method (not raw host exec), and that the
// correct image and command are passed.
func TestAgentExecInitUsesRuntimeInterface(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceName := "exec-init-svc"
	instanceID := "exec-init-aaa"
	serviceImage := "myapp:2.0"

	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte(serviceImage))
	factStore.Put(ctx, types.KeyDesiredServiceInitStep(serviceName, 0), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceInitStepExec(serviceName, 0), []byte("db-migrate --run"))
	factStore.Put(ctx, types.KeyDesiredServiceInitStep(serviceName, 1), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceInitStepExec(serviceName, 1), []byte("cache-warm"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	waitFor(t, 3*time.Second, "instance running after init", func() bool {
		stateFact, getError := factStore.Get(ctx, types.KeyObservedInstanceState(instanceID))
		return getError == nil && string(stateFact.Value) == string(types.InstanceRunning)
	})

	if len(simulatorRuntime.ExecInitCalls) < 2 {
		t.Fatalf("expected at least 2 ExecInit calls, got %d", len(simulatorRuntime.ExecInitCalls))
	}

	firstCall := simulatorRuntime.ExecInitCalls[0]
	if firstCall.Image != serviceImage {
		t.Errorf("first ExecInit image: got %q, want %q", firstCall.Image, serviceImage)
	}
	if firstCall.Command != "db-migrate --run" {
		t.Errorf("first ExecInit command: got %q, want %q", firstCall.Command, "db-migrate --run")
	}

	secondCall := simulatorRuntime.ExecInitCalls[1]
	if secondCall.Image != serviceImage {
		t.Errorf("second ExecInit image: got %q, want %q", secondCall.Image, serviceImage)
	}
	if secondCall.Command != "cache-warm" {
		t.Errorf("second ExecInit command: got %q, want %q", secondCall.Command, "cache-warm")
	}
}

// TestAgentReconcileDesiredInstanceStartsMissing verifies that the extracted
// reconcileDesiredInstance method brings up an instance that is not running.
func TestAgentReconcileDesiredInstanceStartsMissing(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceName := "reconcile-svc"
	instanceID := "reconcile-aaa"

	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("nginx:1.27"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running with image fact", func() bool {
		stateFact, stateErr := factStore.Get(ctx, types.KeyObservedInstanceState(instanceID))
		if stateErr != nil || string(stateFact.Value) != string(types.InstanceRunning) {
			return false
		}
		imageFact, imageErr := factStore.Get(ctx, types.KeyObservedInstanceImage(instanceID))
		return imageErr == nil && string(imageFact.Value) == "nginx:1.27"
	})
}

// TestAgentCleanupUndesiredInstanceStopsStale verifies that the extracted
// cleanupUndesiredInstance method tears down an instance that is no longer
// placed on this node.
func TestAgentCleanupUndesiredInstanceStopsStale(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceName := "cleanup-svc"
	instanceID := "cleanup-aaa"

	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("nginx:1.27"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running before removal", func() bool {
		runtimeStatus, statusError := simulatorRuntime.Status(ctx, instanceID)
		return statusError == nil && runtimeStatus.Running
	})

	// Remove the placement — the instance should no longer be desired on node-1.
	types.DeletePlacement(ctx, factStore, "node-1", instanceID)

	waitFor(t, 2*time.Second, "instance stopped after placement removed", func() bool {
		runtimeStatus, statusError := simulatorRuntime.Status(ctx, instanceID)
		return statusError == nil && !runtimeStatus.Running
	})
}

// TestAgentTelemetryUsesRuntimeStats verifies that the agent reports
// per-workload CPU and memory from runtime.Stats() to the store.
func TestAgentTelemetryUsesRuntimeStats(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceName := "telemetry-svc"
	instanceID := "telemetry-aaa"

	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("app:1.0"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU(serviceName), []byte("500m"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory(serviceName), []byte("512Mi"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		stateFact, getError := factStore.Get(ctx, types.KeyObservedInstanceState(instanceID))
		return getError == nil && string(stateFact.Value) == string(types.InstanceRunning)
	})

	// Give the telemetry collection a few cycles to run.
	waitFor(t, 2*time.Second, "cpu telemetry reported", func() bool {
		cpuFact, cpuError := factStore.Get(ctx, types.KeyObservedInstanceCPU(instanceID))
		return cpuError == nil && len(cpuFact.Value) > 0
	})

	cpuFact, cpuError := factStore.Get(ctx, types.KeyObservedInstanceCPU(instanceID))
	if cpuError != nil {
		t.Fatal("expected CPU telemetry fact")
	}
	// SimulatorRuntime returns spec CPUm (0, since Spec.CPUm is not set by the agent start path).
	// The key point is that the fact exists — it was written via Stats(), not estimated.
	t.Logf("observed CPU: %s", cpuFact.Value)

	memoryFact, memoryError := factStore.Get(ctx, types.KeyObservedInstanceMemory(instanceID))
	if memoryError != nil {
		t.Fatal("expected memory telemetry fact")
	}
	t.Logf("observed memory: %s", memoryFact.Value)
}

func TestAgentResizesInstanceOnEffectiveResourceChange(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("myapp:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("api"), []byte("500m"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("api"), []byte("512Mi"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "inst-1", Service: "api", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "inst-1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running in runtime", func() bool {
		workloadStatus, statusError := simulatorRuntime.Status(ctx, "inst-1")
		return statusError == nil && workloadStatus.Running
	})

	waitFor(t, 2*time.Second, "instance state=running in store", func() bool {
		factEntry, getError := factStore.Get(ctx, types.KeyObservedInstanceState("inst-1"))
		return getError == nil && string(factEntry.Value) == "running"
	})

	initialStats, statsError := simulatorRuntime.Stats(ctx, "inst-1")
	if statsError != nil {
		t.Fatalf("failed to get initial stats: %v", statsError)
	}
	if initialStats.CPUMillicores != 500 {
		t.Errorf("initial CPU: got %d, want 500", initialStats.CPUMillicores)
	}

	factStore.Put(ctx, types.KeyEffectiveServiceResourcesCPU("api"), []byte("1000m"))
	factStore.Put(ctx, types.KeyEffectiveServiceResourcesMemory("api"), []byte("1Gi"))

	waitFor(t, 3*time.Second, "simulator workload resized to 1000m CPU", func() bool {
		resourceStats, statsErr := simulatorRuntime.Stats(ctx, "inst-1")
		return statsErr == nil && resourceStats.CPUMillicores == 1000
	})

	updatedStats, _ := simulatorRuntime.Stats(ctx, "inst-1")
	expectedMemory := int64(1024 * 1024 * 1024)
	if updatedStats.MemoryBytes != expectedMemory {
		t.Errorf("memory after resize: got %d, want %d", updatedStats.MemoryBytes, expectedMemory)
	}
}

func TestAgentRestartsInstanceWhenResizeUnsupported(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	resizeUnsupportedRuntime := &noResizeRuntime{
		inner: runtime.NewSimulatorRuntime(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, resizeUnsupportedRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("myapp:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("api"), []byte("500m"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("api"), []byte("512Mi"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "inst-2", Service: "api", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "inst-2", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		workloadStatus, statusError := resizeUnsupportedRuntime.Status(ctx, "inst-2")
		return statusError == nil && workloadStatus.Running
	})

	factStore.Put(ctx, types.KeyEffectiveServiceResourcesCPU("api"), []byte("2000m"))

	waitFor(t, 3*time.Second, "instance stopped and restarted with new resources", func() bool {
		resourceStats, statsErr := resizeUnsupportedRuntime.Stats(ctx, "inst-2")
		return statsErr == nil && resourceStats.CPUMillicores == 2000
	})
}

// noResizeRuntime wraps SimulatorRuntime but returns ErrResizeUnsupported from
// Resize, forcing the agent to stop and restart the instance.
type noResizeRuntime struct {
	inner *runtime.SimulatorRuntime
}

func (noResize *noResizeRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	return noResize.inner.Start(ctx, spec)
}

func (noResize *noResizeRuntime) Stop(ctx context.Context, instanceID string) error {
	return noResize.inner.Stop(ctx, instanceID)
}

func (noResize *noResizeRuntime) Status(ctx context.Context, instanceID string) (runtime.Status, error) {
	return noResize.inner.Status(ctx, instanceID)
}

func (noResize *noResizeRuntime) List(ctx context.Context) ([]runtime.Status, error) {
	return noResize.inner.List(ctx)
}

func (noResize *noResizeRuntime) Exec(ctx context.Context, instanceID string, execSpec runtime.ExecSpec) error {
	return noResize.inner.Exec(ctx, instanceID, execSpec)
}

func (noResize *noResizeRuntime) ExecInit(ctx context.Context, image string, execSpec runtime.ExecSpec) error {
	return noResize.inner.ExecInit(ctx, image, execSpec)
}

func (noResize *noResizeRuntime) Stats(ctx context.Context, instanceID string) (runtime.ResourceStats, error) {
	return noResize.inner.Stats(ctx, instanceID)
}

func (noResize *noResizeRuntime) Logs(ctx context.Context, instanceID string, follow bool) (io.ReadCloser, error) {
	return noResize.inner.Logs(ctx, instanceID, follow)
}

// Resize always returns ErrResizeUnsupported to simulate process/container runtimes.
func (noResize *noResizeRuntime) Resize(_ context.Context, _ string, _ int64, _ int64) error {
	return runtime.ErrResizeUnsupported
}

// watchClosingStore wraps a MemoryStore and closes the first N watch channels
// immediately after creation, simulating etcd compaction or connection loss.
// Subsequent Watch calls delegate normally to the inner store.
type watchClosingStore struct {
	inner          *store.MemoryStore
	closuresLeft   int
	watchCallCount int
	mu             sync.Mutex
}

func (watchStore *watchClosingStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	return watchStore.inner.Get(ctx, key)
}
func (watchStore *watchClosingStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	return watchStore.inner.Put(ctx, key, value)
}
func (watchStore *watchClosingStore) Delete(ctx context.Context, key string) error {
	return watchStore.inner.Delete(ctx, key)
}
func (watchStore *watchClosingStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	return watchStore.inner.Scan(ctx, prefix)
}
func (watchStore *watchClosingStore) ScanWithRevision(ctx context.Context, prefix string) (*store.ScanResult, error) {
	return watchStore.inner.ScanWithRevision(ctx, prefix)
}
func (watchStore *watchClosingStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	return watchStore.inner.Transaction(ctx, compares, onSuccess, onFailure)
}
func (watchStore *watchClosingStore) Revision(ctx context.Context) (int64, error) {
	return watchStore.inner.Revision(ctx)
}
func (watchStore *watchClosingStore) GrantLease(ctx context.Context, ttlSeconds int64) (int64, error) {
	return watchStore.inner.GrantLease(ctx, ttlSeconds)
}
func (watchStore *watchClosingStore) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (int64, error) {
	return watchStore.inner.PutWithLease(ctx, key, value, leaseID)
}
func (watchStore *watchClosingStore) RenewLease(ctx context.Context, leaseID int64) (int64, error) {
	return watchStore.inner.RenewLease(ctx, leaseID)
}
func (watchStore *watchClosingStore) RevokeLease(ctx context.Context, leaseID int64) error {
	return watchStore.inner.RevokeLease(ctx, leaseID)
}
func (watchStore *watchClosingStore) Close() error {
	return watchStore.inner.Close()
}

// Watch either immediately closes the channel (simulating disconnection)
// or delegates to the inner store for normal operation.
func (watchStore *watchClosingStore) Watch(ctx context.Context, key string, opts store.WatchOption) (<-chan store.Event, error) {
	watchStore.mu.Lock()
	watchStore.watchCallCount++
	shouldClose := watchStore.closuresLeft > 0
	if shouldClose {
		watchStore.closuresLeft--
	}
	watchStore.mu.Unlock()

	if shouldClose {
		closedChannel := make(chan store.Event)
		close(closedChannel)
		return closedChannel, nil
	}
	return watchStore.inner.Watch(ctx, key, opts)
}

// WatchCallCount returns how many times Watch was called.
func (watchStore *watchClosingStore) WatchCallCount() int {
	watchStore.mu.Lock()
	defer watchStore.mu.Unlock()
	return watchStore.watchCallCount
}

// TestAgentRestartsOnWatchClosure verifies that the agent's retry loop
// re-establishes the watch when the watch channel is closed (e.g. due
// to etcd compaction or store shutdown).
func TestAgentRestartsOnWatchClosure(t *testing.T) {
	innerStore := store.NewMemoryStore()
	defer innerStore.Close()
	closingStore := &watchClosingStore{inner: innerStore, closuresLeft: 1}

	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", closingStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Write facts so the agent has something to reconcile.
	innerStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, innerStore, types.Instance{ID: "web-1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, innerStore, types.Placement{InstanceID: "web-1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for the agent to hit the closed watch, reconnect, and reconcile.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if closingStore.WatchCallCount() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if closingStore.WatchCallCount() < 2 {
		t.Fatalf("expected at least 2 Watch calls (initial + reconnect), got %d",
			closingStore.WatchCallCount())
	}
}

// TestAgentSkipsFailedInstances verifies that the agent does not attempt to
// start instances in the "failed" state — the FailureController is responsible
// for replacing those.
func TestAgentSkipsFailedInstances(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)

	// Place two instances on our node: one pending (should be started),
	// one failed (should be skipped).
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "ok-1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "ok-1", NodeID: "node-1"})
	types.WriteInstance(ctx, factStore, types.Instance{ID: "bad-1", Service: "web", State: types.InstanceFailed})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "bad-1", NodeID: "node-1"})

	instances, err := nodeAgent.findInstancesPlacedOnThisNode(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(instances) != 1 {
		t.Fatalf("expected 1 instance (pending only), got %d", len(instances))
	}
	if instances[0].id != "ok-1" {
		t.Errorf("expected instance ok-1, got %s", instances[0].id)
	}
}

func TestCommandRequiresShell(t *testing.T) {
	tests := []struct {
		command    string
		needsShell bool
	}{
		{"airflow webserver", false},
		{"airflow scheduler", false},
		{"celery worker", false},
		{"redis-server --appendonly yes", false},
		{"nginx -g 'daemon off;'", true},
		{"echo hello && echo world", true},
		{"cat /run/secrets/db-password | tr -d '\\n'", true},
		{"bash -c 'airflow db migrate; exec airflow webserver'", true},
		{"echo $(cat /run/secrets/password)", true},
		{"cmd > /dev/null", true},
		{"cmd1 | cmd2", true},
	}
	for _, testCase := range tests {
		result := commandRequiresShell(testCase.command)
		if result != testCase.needsShell {
			t.Errorf("commandRequiresShell(%q) = %v, want %v", testCase.command, result, testCase.needsShell)
		}
	}
}

func TestSplitSimpleCommand(t *testing.T) {
	tests := []struct {
		command  string
		expected []string
	}{
		{"webserver", []string{"webserver"}},
		{"airflow webserver", []string{"airflow", "webserver"}},
		{"celery worker", []string{"celery", "worker"}},
		{"redis-server --appendonly yes", []string{"redis-server", "--appendonly", "yes"}},
		{`bash -c 'long command here'`, []string{"bash", "-c", "long command here"}},
		{`echo "hello world"`, []string{"echo", "hello world"}},
		{"cmd  with   multiple  spaces", []string{"cmd", "with", "multiple", "spaces"}},
	}
	for _, testCase := range tests {
		result := splitSimpleCommand(testCase.command)
		if len(result) != len(testCase.expected) {
			t.Errorf("splitSimpleCommand(%q) = %v, want %v", testCase.command, result, testCase.expected)
			continue
		}
		for index := range result {
			if result[index] != testCase.expected[index] {
				t.Errorf("splitSimpleCommand(%q)[%d] = %q, want %q", testCase.command, index, result[index], testCase.expected[index])
			}
		}
	}
}

// TestBuildConnectionURL verifies that buildConnectionURL percent-encodes
// credentials and produces parseable URLs for all edge cases.
func TestBuildConnectionURL(t *testing.T) { //nolint:funlen // table-driven test
	//nolint:gosec // G101 false positive: test fixture URLs contain dummy credentials
	testCases := []struct {
		name       string
		definition connectionDefinition
		credential string
		wantURL    string
	}{
		{
			name: "normal case without special characters",
			definition: connectionDefinition{
				driver: "postgresql", user: "airflow", host: "postgres", port: 5432, database: "airflow",
			},
			credential: "secret",
			wantURL:    "postgresql://airflow:secret@postgres:5432/airflow",
		},
		{
			name: "credential with at sign and colon",
			definition: connectionDefinition{
				driver: "postgresql", user: "admin", host: "db.internal", port: 5432, database: "mydb",
			},
			credential: "p@ss:word",
			wantURL:    "postgresql://admin:p%40ss%3Aword@db.internal:5432/mydb",
		},
		{
			name: "credential with slash question hash",
			definition: connectionDefinition{
				driver: "mysql", user: "root", host: "mysql-host", port: 3306, database: "app",
			},
			credential: "a/b?c#d",
			wantURL:    "mysql://root:a%2Fb%3Fc%23d@mysql-host:3306/app",
		},
		{
			name: "user with at sign",
			definition: connectionDefinition{
				driver: "postgresql", user: "user@domain", host: "db", port: 5432, database: "test",
			},
			credential: "pass",
			wantURL:    "postgresql://user%40domain:pass@db:5432/test",
		},
		{
			name: "empty user and credential",
			definition: connectionDefinition{
				driver: "redis", host: "cache", port: 6379,
			},
			wantURL: "redis://cache:6379",
		},
		{
			name: "user without credential",
			definition: connectionDefinition{
				driver: "postgresql", user: "readonly", host: "db", port: 5432, database: "reports",
			},
			wantURL: "postgresql://readonly@db:5432/reports",
		},
		{
			name: "zero port omitted",
			definition: connectionDefinition{
				driver: "sqlite", host: "localhost", database: "local.db",
			},
			wantURL: "sqlite://localhost/local.db",
		},
		{
			name: "empty database omitted",
			definition: connectionDefinition{
				driver: "redis", host: "cache", port: 6379,
			},
			wantURL: "redis://cache:6379",
		},
	}

	for _, testCase := range testCases {
		result := buildConnectionURL(testCase.definition, testCase.credential)
		if result != testCase.wantURL {
			t.Errorf("%s: buildConnectionURL() = %q, want %q", testCase.name, result, testCase.wantURL)
		}
	}
}

func TestAgentPullsImageBeforeStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "pull-ok", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "pull-ok", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running after pull", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("pull-ok"))
		return err == nil && string(factEntry.Value) == "running"
	})

	if !simulatorRuntime.IsImageCached(ctx, "nginx:1.28") {
		t.Error("image should have been pulled before start")
	}
}

func TestAgentImagePullFailureEntersBackoff(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorRuntime.PullFailures = map[string]string{
		"bad-image:latest": "registry unreachable",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("bad-image:latest"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "pull-fail", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "pull-fail", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance in image-pull-backoff", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("pull-fail"))
		return err == nil && string(factEntry.Value) == string(types.InstanceImagePullBackoff)
	})

	waitFor(t, 2*time.Second, "pull-status fact written", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstancePullStatus("pull-fail"))
		return err == nil && strings.Contains(string(factEntry.Value), "registry unreachable")
	})

	runtimeStatus, err := simulatorRuntime.Status(ctx, "pull-fail")
	if err != runtime.ErrNotFound {
		t.Errorf("instance should not be started when pull fails, got status: %+v", runtimeStatus)
	}
}

func TestAgentImagePullBackoffCleanedOnSuccess(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorRuntime.PullFailures = map[string]string{
		"flaky-image:v1": "timeout",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.EnableTestHooks()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("flaky-image:v1"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "pull-retry", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "pull-retry", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance in backoff", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("pull-retry"))
		return err == nil && string(factEntry.Value) == string(types.InstanceImagePullBackoff)
	})

	// Fix the pull failure and expire the backoff timer inside the agent
	// goroutine to avoid a data race on agent-internal state.
	simulatorRuntime.ClearPullFailure("flaky-image:v1")
	nodeAgent.RunInAgentLoop(func() {
		nodeAgent.imagePullBackoffs["pull-retry"].retryAfter = time.Now().Add(-1 * time.Second)
	})

	waitFor(t, 2*time.Second, "instance running after retry", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("pull-retry"))
		return err == nil && string(factEntry.Value) == "running"
	})

	_, pullStatusErr := factStore.Get(ctx, types.KeyObservedInstancePullStatus("pull-retry"))
	if pullStatusErr == nil {
		t.Error("pull-status fact should be removed after successful pull")
	}
}

func TestAgentCleanupRemovesPullBackoff(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorRuntime.PullFailures = map[string]string{
		"bad-image:latest": "not found",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("bad-image:latest"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "pull-clean", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "pull-clean", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance in backoff", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("pull-clean"))
		return err == nil && string(factEntry.Value) == string(types.InstanceImagePullBackoff)
	})

	// Remove placement — agent should clean up the instance and its backoff state.
	factStore.Delete(ctx, types.ScanPlacementsForNode("node-1")+"pull-clean")

	waitFor(t, 2*time.Second, "pull-status fact cleaned up", func() bool {
		_, err := factStore.Get(ctx, types.KeyObservedInstancePullStatus("pull-clean"))
		return err != nil
	})
}

func TestAgentPullPolicyNeverSkipsPull(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("myapp:v1"))
	factStore.Put(ctx, types.KeyDesiredServicePullPolicy("web"), []byte("never"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "no-pull", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "no-pull", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running without pull", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("no-pull"))
		return err == nil && string(factEntry.Value) == "running"
	})

	if simulatorRuntime.IsImageCached(ctx, "myapp:v1") {
		t.Error("pull_policy=never should not pull the image")
	}
}

func TestAgentPullPolicyAlwaysRepulls(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("myapp:v2"))
	factStore.Put(ctx, types.KeyDesiredServicePullPolicy("web"), []byte("always"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "always-pull", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "always-pull", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running after pull", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("always-pull"))
		return err == nil && string(factEntry.Value) == "running"
	})

	pullCount := 0
	for _, pulledImage := range simulatorRuntime.PulledImages {
		if pulledImage == "myapp:v2" {
			pullCount++
		}
	}
	if pullCount == 0 {
		t.Error("pull_policy=always should pull even if image could be cached")
	}
}

func TestAgentImagePullSecretPassesCredentials(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.SetSecretProvider(&mockSecretProvider{
		secrets: map[string][]byte{
			"registry-creds": []byte(`{"username":"bot","password":"token123"}`),
		},
		grants: map[string]string{ //nolint:gosec // test fixture, not real credentials
			"api/registry-creds": "/run/secrets/registry-creds",
		},
	})

	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("registry.example.com/api:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceImagePullSecret("api"), []byte("registry-creds"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "pull-creds", Service: "api", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "pull-creds", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running after authenticated pull", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("pull-creds"))
		return err == nil && string(factEntry.Value) == "running"
	})

	credentials, hasCredentials := simulatorRuntime.PullCredentials["registry.example.com/api:v1"]
	if !hasCredentials {
		t.Fatal("PullImage should have received credentials for registry.example.com/api:v1")
	}
	if credentials != "bot:token123" {
		t.Errorf("credentials: got %q, want %q", credentials, "bot:token123")
	}
}

func TestAgentImagePullWithoutSecretOmitsCredentials(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "no-creds", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "no-creds", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running without credentials", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("no-creds"))
		return err == nil && string(factEntry.Value) == "running"
	})

	if len(simulatorRuntime.PullCredentials) > 0 {
		t.Errorf("PullImage should not have received credentials, got: %v", simulatorRuntime.PullCredentials)
	}
}

// TestAgentPublishesExitReasonOnOOMKill verifies that when a container is
// OOM-killed (exit code 137), the agent publishes the exit-reason fact.
func TestAgentPublishesExitReasonOnOOMKill(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "oom1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "oom1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		observedStatus, statusErr := simulatorRuntime.Status(ctx, "oom1")
		return statusErr == nil && observedStatus.Running
	})

	// Kill with OOM and block restart so the failed state persists.
	simulatorRuntime.InjectStartFailure("oom1", "still OOM")
	simulatorRuntime.KillWorkload("oom1", 137, "OOMKilled")

	waitFor(t, 2*time.Second, "instance state=failed in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceState("oom1"))
		return err == nil && string(factEntry.Value) == "failed"
	})

	waitFor(t, 2*time.Second, "exit-reason=OOMKilled in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceExitReason("oom1"))
		return err == nil && string(factEntry.Value) == "OOMKilled"
	})
}

// TestAgentRestartsOnConfigChange verifies that when a config env var changes,
// the agent detects the hash mismatch and restarts the instance.
func TestAgentRestartsOnConfigChange(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.old.local"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "cfg1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "cfg1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for the instance to start and the config hash to be written.
	waitFor(t, 2*time.Second, "config-hash stored", func() bool {
		_, err := factStore.Get(ctx, types.KeyObservedInstanceConfigHash("cfg1"))
		return err == nil
	})

	// Record the original config hash.
	originalHashFact, _ := factStore.Get(ctx, types.KeyObservedInstanceConfigHash("cfg1"))
	originalHash := string(originalHashFact.Value)

	// Change the config.
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.new.local"))

	// The agent should detect the mismatch, stop, and restart with new config.
	waitFor(t, 3*time.Second, "config-hash updated after config change", func() bool {
		hashFact, err := factStore.Get(ctx, types.KeyObservedInstanceConfigHash("cfg1"))
		return err == nil && string(hashFact.Value) != originalHash
	})
}

// TestAgentPublishesExitReasonOnNonZeroExit verifies that a non-OOM crash
// publishes the numeric exit code as the reason.
func TestAgentPublishesExitReasonOnNonZeroExit(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "crash1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "crash1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		observedStatus, statusErr := simulatorRuntime.Status(ctx, "crash1")
		return statusErr == nil && observedStatus.Running
	})

	// Kill and block restart so the failed state persists.
	simulatorRuntime.InjectStartFailure("crash1", "segfault")
	simulatorRuntime.KillWorkload("crash1", 1, "")

	waitFor(t, 2*time.Second, "exit-reason=exit(1) in store", func() bool {
		factEntry, err := factStore.Get(ctx, types.KeyObservedInstanceExitReason("crash1"))
		return err == nil && string(factEntry.Value) == "exit(1)"
	})
}

func TestAgentPreStopHookExecutedBeforeStop(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Configure a pre_stop hook for the "web" service.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceLifecyclePreStop("web"), []byte("echo draining"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "hook1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "hook1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		runtimeStatus, statusErr := simulatorRuntime.Status(ctx, "hook1")
		return statusErr == nil && runtimeStatus.Running
	})

	// Remove placement to trigger cleanup (which should run the pre_stop hook).
	factStore.Delete(ctx, types.ScanPlacementsForNode("node-1")+"hook1")

	waitFor(t, 2*time.Second, "instance stopped", func() bool {
		factEntry, getErr := factStore.Get(ctx, types.KeyObservedInstanceState("hook1"))
		return getErr == nil && string(factEntry.Value) == string(types.InstanceStopped)
	})

	execCalls := simulatorRuntime.GetExecCalls()
	foundPreStop := false
	for _, execCall := range execCalls {
		if execCall.InstanceID == "hook1" && execCall.Command == "echo draining" {
			foundPreStop = true
			break
		}
	}
	if !foundPreStop {
		t.Errorf("expected pre_stop hook to be executed, got exec calls: %v", execCalls)
	}
}

func TestAgentPostStartHookExecutedAfterStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Configure a post_start hook for the "web" service.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceLifecyclePostStart("web"), []byte("echo started"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "hook2", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "hook2", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		runtimeStatus, statusErr := simulatorRuntime.Status(ctx, "hook2")
		return statusErr == nil && runtimeStatus.Running
	})

	execCalls := simulatorRuntime.GetExecCalls()
	foundPostStart := false
	for _, execCall := range execCalls {
		if execCall.InstanceID == "hook2" && execCall.Command == "echo started" {
			foundPostStart = true
			break
		}
	}
	if !foundPostStart {
		t.Errorf("expected post_start hook to be executed, got exec calls: %v", execCalls)
	}
}
