package agent

import (
	"context"
	"fmt"
	"io"
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

func (delayed *delayedStartRuntime) Logs(ctx context.Context, instanceID string, follow bool) (io.ReadCloser, error) {
	return delayed.inner.Logs(ctx, instanceID, follow)
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

	waitFor(t, 2*time.Second, "instance running", func() bool {
		stateFact, getError := factStore.Get(ctx, types.KeyObservedInstanceState(instanceID))
		return getError == nil && string(stateFact.Value) == string(types.InstanceRunning)
	})

	imageFact, imageError := factStore.Get(ctx, types.KeyObservedInstanceImage(instanceID))
	if imageError != nil {
		t.Fatal("expected observed image fact")
	}
	if string(imageFact.Value) != "nginx:1.27" {
		t.Errorf("image: got %s, want nginx:1.27", imageFact.Value)
	}
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
	factStore.Delete(ctx, types.KeyPlacementInstance(instanceID))

	waitFor(t, 2*time.Second, "instance stopped after placement removed", func() bool {
		runtimeStatus, statusError := simulatorRuntime.Status(ctx, instanceID)
		return statusError == nil && !runtimeStatus.Running
	})
}

