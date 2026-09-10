package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
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
	factStore.Delete(ctx, types.KeyPlacementInstance("ccc"))

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
	go http.Serve(ln, mux)
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

	// Place instance and set its IP.
	types.WriteInstance(ctx, factStore, types.Instance{ID: "eee", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "eee", NodeID: "node-1"})

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

// TestAgentWithoutNetworkProviderUsesLoopback verifies backward compatibility:
// agents without a NetworkProvider still assign 127.0.0.1.
func TestAgentWithoutNetworkProviderUsesLoopback(t *testing.T) {
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

	waitFor(t, 2*time.Second, "instance gets 127.0.0.1", func() bool {
		ipFact, err := factStore.Get(ctx, types.KeyObservedInstanceIP("net-ccc"))
		return err == nil && string(ipFact.Value) == "127.0.0.1"
	})

	// No network allocation fact should exist.
	_, err := factStore.Get(ctx, types.KeyNetworkAllocation("net-ccc"))
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
	factStore.Delete(ctx, types.KeyPlacementInstance("net-ddd"))

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

	factStore.Delete(ctx, types.KeyPlacementInstance("vol-ccc"))

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

func TestAgentMaterializesSecretsBeforeStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	secretMountDir := t.TempDir()
	secretMountPath := secretMountDir + "/db-password"

	secretProvider := &mockSecretProvider{
		secrets: map[string][]byte{"db-password": []byte("hunter2")},
		grants:  map[string]string{"web/db-password": secretMountPath},
	}

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetSecretProvider(secretProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceSecret("web", "db-password"), []byte(secretMountPath))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "sec-aaa", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "sec-aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running with secret materialized", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("sec-aaa"))
		return err == nil && string(stateFact.Value) == "running"
	})

	// Verify the secret file was written.
	fileContent, err := os.ReadFile(secretMountPath)
	if err != nil {
		t.Fatalf("secret file not written: %v", err)
	}
	if string(fileContent) != "hunter2" {
		t.Fatalf("expected hunter2, got %s", string(fileContent))
	}
}

func TestAgentCleansUpSecretsOnStop(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	secretMountDir := t.TempDir()
	secretMountPath := secretMountDir + "/api-key"

	secretProvider := &mockSecretProvider{
		secrets: map[string][]byte{"api-key": []byte("sk-12345")},
		grants:  map[string]string{"web/api-key": secretMountPath},
	}

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetSecretProvider(secretProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceSecret("web", "api-key"), []byte(secretMountPath))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "sec-bbb", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "sec-bbb", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("sec-bbb"))
		return err == nil && string(stateFact.Value) == "running"
	})

	// Verify the secret was materialized.
	if _, err := os.Stat(secretMountPath); err != nil {
		t.Fatalf("secret file not found before stop: %v", err)
	}

	// Remove the placement to trigger a stop.
	factStore.Delete(ctx, types.KeyPlacementInstance("sec-bbb"))

	waitFor(t, 2*time.Second, "secret cleaned up after stop", func() bool {
		_, err := os.Stat(secretMountPath)
		return os.IsNotExist(err)
	})
}

func TestAgentRotatesSecretWithoutRestart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	simulatorRuntime := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	secretMountDir := t.TempDir()
	secretMountPath := secretMountDir + "/db-password"

	secretProvider := &mockSecretProvider{
		secrets: map[string][]byte{"db-password": []byte("original-password")},
		grants:  map[string]string{"web/db-password": secretMountPath},
	}

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetSecretProvider(secretProvider)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceSecret("web", "db-password"), []byte(secretMountPath))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "rot-aaa", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "rot-aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		stateFact, err := factStore.Get(ctx, types.KeyObservedInstanceState("rot-aaa"))
		return err == nil && string(stateFact.Value) == "running"
	})

	// Verify original secret.
	content, _ := os.ReadFile(secretMountPath)
	if string(content) != "original-password" {
		t.Fatalf("expected original-password, got %s", string(content))
	}

	// Rotate the secret in the provider.
	secretProvider.mu.Lock()
	secretProvider.secrets["db-password"] = []byte("rotated-password")
	secretProvider.mu.Unlock()

	// Wait for agent to detect and update the file.
	waitFor(t, 2*time.Second, "secret rotated on disk", func() bool {
		newContent, err := os.ReadFile(secretMountPath)
		return err == nil && string(newContent) == "rotated-password"
	})

	// Instance should still be running (no restart).
	stateFact, _ := factStore.Get(ctx, types.KeyObservedInstanceState("rot-aaa"))
	if string(stateFact.Value) != "running" {
		t.Fatalf("instance should still be running, got %s", string(stateFact.Value))
	}
}

