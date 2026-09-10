package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// setupProbeTestAgent creates a memory store, simulator runtime, and agent
// wired together for probe tests. It returns all three so callers can
// manipulate the store directly and inspect the runtime.
func setupProbeTestAgent(nodeID string) (*store.MemoryStore, *runtime.SimulatorRuntime, *Agent) {
	factStore := store.NewMemoryStore()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := New(nodeID, factStore, simulatorRuntime)
	return factStore, simulatorRuntime, nodeAgent
}

// writeHTTPProbeConfig writes the probe configuration facts for an HTTP probe
// into the store for the given service and probe type. Only the fields that
// are non-zero/non-empty are written, so callers can omit optional fields.
func writeHTTPProbeConfig(ctx context.Context, factStore *store.MemoryStore, serviceName string, probeType string, port int, path string, interval time.Duration, failureThreshold int, successThreshold int) {
	factStore.Put(ctx, types.KeyDesiredServiceProbeMethod(serviceName, probeType), []byte("http"))
	if path != "" {
		factStore.Put(ctx, types.KeyDesiredServiceProbePath(serviceName, probeType), []byte(path))
	}
	if port != 0 {
		factStore.Put(ctx, types.KeyDesiredServiceProbePort(serviceName, probeType), []byte(fmt.Sprintf("%d", port)))
	}
	if interval > 0 {
		factStore.Put(ctx, types.KeyDesiredServiceProbeInterval(serviceName, probeType), []byte(interval.String()))
	}
	if failureThreshold > 0 {
		factStore.Put(ctx, types.KeyDesiredServiceProbeFailureThreshold(serviceName, probeType), []byte(fmt.Sprintf("%d", failureThreshold)))
	}
	if successThreshold > 0 {
		factStore.Put(ctx, types.KeyDesiredServiceProbeSuccessThreshold(serviceName, probeType), []byte(fmt.Sprintf("%d", successThreshold)))
	}
	// Use zero initial delay for tests so probes fire immediately.
	factStore.Put(ctx, types.KeyDesiredServiceProbeInitialDelay(serviceName, probeType), []byte("0s"))
}

// startTestHTTPServer starts a test HTTP server that responds with the given
// status code on all requests. It returns the listener (for cleanup) and the
// port number. The caller must close the listener when done.
func startTestHTTPServer(t *testing.T, statusCode int) (net.Listener, int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(statusCode)
	})
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatalf("failed to start test HTTP server: %v", listenErr)
	}
	go http.Serve(listener, mux)
	return listener, listener.Addr().(*net.TCPAddr).Port
}

// getUnusedPort allocates and immediately releases a TCP port so it is free
// but guaranteed to have nothing listening (connection-refused behavior).
func getUnusedPort(t *testing.T) int {
	t.Helper()
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatalf("failed to get unused port: %v", listenErr)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// setupInstanceInStore writes the minimal set of facts needed for the agent to
// recognize an instance as placed on its node: the service image, the instance
// record, the placement, and the instance IP.
func setupInstanceInStore(ctx context.Context, factStore *store.MemoryStore, serviceName string, instanceID string, nodeID string, instanceIP string) {
	factStore.Put(ctx, types.KeyDesiredServiceImage(serviceName), []byte("test-image:latest"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: serviceName, State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: nodeID})
	factStore.Put(ctx, types.KeyObservedInstanceIP(instanceID), []byte(instanceIP))
	// Write the instance service fact so the agent can map instance to service.
	factStore.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte(serviceName))
}

// TestLoadProbeConfigFromStore verifies that loadProbeConfig correctly reads all
// probe configuration fields from the fact store, and returns nil when no method
// fact exists.
func TestLoadProbeConfigFromStore(t *testing.T) {
	factStore, _, nodeAgent := setupProbeTestAgent("test-node")
	defer factStore.Close()

	ctx := context.Background()
	serviceName := "webapp"

	// Subtest: returns nil when no method fact is present.
	t.Run("returns nil when no method fact exists", func(t *testing.T) {
		loadedConfig := nodeAgent.loadProbeConfig(ctx, serviceName, "startup")
		if loadedConfig != nil {
			t.Error("expected nil config when no probe method fact is set")
		}
	})

	// Write a full probe configuration into the store.
	factStore.Put(ctx, types.KeyDesiredServiceProbeMethod(serviceName, "startup"), []byte("http"))
	factStore.Put(ctx, types.KeyDesiredServiceProbePath(serviceName, "startup"), []byte("/healthz"))
	factStore.Put(ctx, types.KeyDesiredServiceProbePort(serviceName, "startup"), []byte("9090"))
	factStore.Put(ctx, types.KeyDesiredServiceProbeInterval(serviceName, "startup"), []byte("5s"))
	factStore.Put(ctx, types.KeyDesiredServiceProbeTimeout(serviceName, "startup"), []byte("3s"))
	factStore.Put(ctx, types.KeyDesiredServiceProbeFailureThreshold(serviceName, "startup"), []byte("4"))
	factStore.Put(ctx, types.KeyDesiredServiceProbeSuccessThreshold(serviceName, "startup"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredServiceProbeInitialDelay(serviceName, "startup"), []byte("10s"))

	// Subtest: all fields read correctly.
	t.Run("reads all fields correctly", func(t *testing.T) {
		loadedConfig := nodeAgent.loadProbeConfig(ctx, serviceName, "startup")
		if loadedConfig == nil {
			t.Fatal("expected non-nil config when method fact is set")
		}
		if loadedConfig.method != "http" {
			t.Errorf("method = %q, want %q", loadedConfig.method, "http")
		}
		if loadedConfig.path != "/healthz" {
			t.Errorf("path = %q, want %q", loadedConfig.path, "/healthz")
		}
		if loadedConfig.port != 9090 {
			t.Errorf("port = %d, want %d", loadedConfig.port, 9090)
		}
		if loadedConfig.interval != 5*time.Second {
			t.Errorf("interval = %v, want %v", loadedConfig.interval, 5*time.Second)
		}
		if loadedConfig.timeout != 3*time.Second {
			t.Errorf("timeout = %v, want %v", loadedConfig.timeout, 3*time.Second)
		}
		if loadedConfig.failureThreshold != 4 {
			t.Errorf("failureThreshold = %d, want %d", loadedConfig.failureThreshold, 4)
		}
		if loadedConfig.successThreshold != 2 {
			t.Errorf("successThreshold = %d, want %d", loadedConfig.successThreshold, 2)
		}
		if loadedConfig.initialDelay != 10*time.Second {
			t.Errorf("initialDelay = %v, want %v", loadedConfig.initialDelay, 10*time.Second)
		}
	})

	// Subtest: defaults are applied when optional fields are missing.
	t.Run("uses defaults for missing optional fields", func(t *testing.T) {
		otherService := "minimal-svc"
		factStore.Put(ctx, types.KeyDesiredServiceProbeMethod(otherService, "liveness"), []byte("tcp"))
		// Write an exposed port so deriveProbePortFromService can find one.
		factStore.Put(ctx, types.KeyDesiredServiceExpose(otherService, 3000), []byte(""))

		loadedConfig := nodeAgent.loadProbeConfig(ctx, otherService, "liveness")
		if loadedConfig == nil {
			t.Fatal("expected non-nil config")
		}
		if loadedConfig.method != "tcp" {
			t.Errorf("method = %q, want %q", loadedConfig.method, "tcp")
		}
		// Default interval is 10s.
		if loadedConfig.interval != 10*time.Second {
			t.Errorf("default interval = %v, want %v", loadedConfig.interval, 10*time.Second)
		}
		// Default timeout is 2s.
		if loadedConfig.timeout != 2*time.Second {
			t.Errorf("default timeout = %v, want %v", loadedConfig.timeout, 2*time.Second)
		}
		// Default failureThreshold is 3.
		if loadedConfig.failureThreshold != 3 {
			t.Errorf("default failureThreshold = %d, want %d", loadedConfig.failureThreshold, 3)
		}
		// Default successThreshold is 1.
		if loadedConfig.successThreshold != 1 {
			t.Errorf("default successThreshold = %d, want %d", loadedConfig.successThreshold, 1)
		}
		// Port should be derived from exposed port.
		if loadedConfig.port != 3000 {
			t.Errorf("derived port = %d, want %d", loadedConfig.port, 3000)
		}
	})
}

// TestStartupProbeGatesLivenessAndReadiness verifies that liveness and
// readiness probes do not execute while the startup probe is still pending,
// and that they start running after the startup probe succeeds.
func TestStartupProbeGatesLivenessAndReadiness(t *testing.T) {
	factStore, _, nodeAgent := setupProbeTestAgent("test-node")
	defer factStore.Close()

	ctx := context.Background()
	serviceName := "gated-svc"
	instanceID := "gate-inst-001"

	// Start a healthy HTTP server for all probes to target.
	healthyListener, healthyPort := startTestHTTPServer(t, 200)
	defer healthyListener.Close()

	setupInstanceInStore(ctx, factStore, serviceName, instanceID, "test-node", "127.0.0.1")

	// Configure all three probes pointing at the healthy server.
	// Startup needs 1 success, liveness needs 1 success, readiness needs 1 success.
	writeHTTPProbeConfig(ctx, factStore, serviceName, "startup", healthyPort, "/", 0, 3, 1)
	writeHTTPProbeConfig(ctx, factStore, serviceName, "liveness", healthyPort, "/", 0, 3, 1)
	writeHTTPProbeConfig(ctx, factStore, serviceName, "readiness", healthyPort, "/", 0, 3, 1)

	instanceInfo := placedInstanceInfo{id: instanceID, service: serviceName}

	// Execute probes once. Startup should succeed on first call since the
	// server is healthy and success_threshold=1. But startup has not yet
	// been recorded as succeeded before this first call, so liveness and
	// readiness depend on startup returning true from this call.
	nodeAgent.executeProbesForInstance(ctx, instanceInfo)

	// After one successful call, startup should have written "succeeded".
	startupStateFact, startupErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "startup"))
	if startupErr != nil {
		t.Fatalf("startup probe state not written: %v", startupErr)
	}
	if string(startupStateFact.Value) != string(types.StartupProbeSucceeded) {
		t.Fatalf("startup state = %q, want %q", startupStateFact.Value, types.StartupProbeSucceeded)
	}

	// Now execute probes again. This time startup is already succeeded, so
	// liveness and readiness should execute.
	nodeAgent.executeProbesForInstance(ctx, instanceInfo)

	// Verify liveness probe executed and wrote a state.
	livenessStateFact, livenessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "liveness"))
	if livenessErr != nil {
		t.Fatalf("liveness probe state not written after startup succeeded: %v", livenessErr)
	}
	if string(livenessStateFact.Value) != string(types.LivenessProbeHealthy) {
		t.Errorf("liveness state = %q, want %q", livenessStateFact.Value, types.LivenessProbeHealthy)
	}

	// Verify readiness probe executed and wrote a state.
	readinessStateFact, readinessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "readiness"))
	if readinessErr != nil {
		t.Fatalf("readiness probe state not written after startup succeeded: %v", readinessErr)
	}
	if string(readinessStateFact.Value) != string(types.ReadinessProbeReady) {
		t.Errorf("readiness state = %q, want %q", readinessStateFact.Value, types.ReadinessProbeReady)
	}

	// Now verify the gating behavior: set up a fresh scenario where the
	// startup probe is still pending (failing target).
	freshServiceName := "gated-svc-2"
	freshInstanceID := "gate-inst-002"

	failingPort := getUnusedPort(t)

	setupInstanceInStore(ctx, factStore, freshServiceName, freshInstanceID, "test-node", "127.0.0.1")

	// Configure startup to require 3 successes (will never reach it since the
	// port is closed), and configure liveness and readiness too.
	writeHTTPProbeConfig(ctx, factStore, freshServiceName, "startup", failingPort, "/", 0, 10, 3)
	writeHTTPProbeConfig(ctx, factStore, freshServiceName, "liveness", healthyPort, "/", 0, 3, 1)
	writeHTTPProbeConfig(ctx, factStore, freshServiceName, "readiness", healthyPort, "/", 0, 3, 1)

	freshInstanceInfo := placedInstanceInfo{id: freshInstanceID, service: freshServiceName}

	// Execute probes. Startup should fail, so liveness and readiness should
	// not be executed at all.
	nodeAgent.executeProbesForInstance(ctx, freshInstanceInfo)

	// Liveness and readiness should not have any state written.
	_, livenessErrFresh := factStore.Get(ctx, types.KeyObservedInstanceProbeState(freshInstanceID, "liveness"))
	if livenessErrFresh == nil {
		t.Error("liveness probe should not execute while startup is pending")
	}
	_, readinessErrFresh := factStore.Get(ctx, types.KeyObservedInstanceProbeState(freshInstanceID, "readiness"))
	if readinessErrFresh == nil {
		t.Error("readiness probe should not execute while startup is pending")
	}
}

// TestStartupProbeFailureThreshold verifies that when the startup probe fails
// consecutively and reaches its failure threshold, the store records
// StartupProbeFailed.
func TestStartupProbeFailureThreshold(t *testing.T) {
	factStore, _, nodeAgent := setupProbeTestAgent("test-node")
	defer factStore.Close()

	ctx := context.Background()
	serviceName := "failing-svc"
	instanceID := "fail-inst-001"

	// Get a port with nothing listening to guarantee probe failure.
	failingPort := getUnusedPort(t)

	setupInstanceInStore(ctx, factStore, serviceName, instanceID, "test-node", "127.0.0.1")

	// Configure startup probe with failure_threshold=2 and a short timeout.
	writeHTTPProbeConfig(ctx, factStore, serviceName, "startup", failingPort, "/health", 0, 2, 1)
	factStore.Put(ctx, types.KeyDesiredServiceProbeTimeout(serviceName, "startup"), []byte("100ms"))

	instanceInfo := placedInstanceInfo{id: instanceID, service: serviceName}

	// First probe execution: 1 consecutive failure. Should write "pending".
	nodeAgent.executeProbesForInstance(ctx, instanceInfo)

	startupStateFact, _ := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "startup"))
	if string(startupStateFact.Value) != string(types.StartupProbePending) {
		t.Errorf("after 1 failure: state = %q, want %q", startupStateFact.Value, types.StartupProbePending)
	}

	// Reset the tracker's lastCheckTime so the interval gate does not block
	// the next execution. The interval defaults to 10s, but we need to run
	// probes back-to-back in the test.
	probeState := nodeAgent.getOrCreateProbeState(instanceID)
	probeState.startup.lastCheckTime = time.Time{}

	// Second probe execution: 2 consecutive failures = threshold reached.
	nodeAgent.executeProbesForInstance(ctx, instanceInfo)

	startupStateFact, startupErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "startup"))
	if startupErr != nil {
		t.Fatalf("startup probe state not found after reaching failure threshold: %v", startupErr)
	}
	if string(startupStateFact.Value) != string(types.StartupProbeFailed) {
		t.Errorf("after 2 failures: state = %q, want %q", startupStateFact.Value, types.StartupProbeFailed)
	}
}

// TestReadinessProbeStateTransitions verifies the full transition cycle of a
// readiness probe: from unknown (no state) to ready after reaching the success
// threshold, and then back to not-ready after reaching the failure threshold.
func TestReadinessProbeStateTransitions(t *testing.T) {
	factStore, _, nodeAgent := setupProbeTestAgent("test-node")
	defer factStore.Close()

	ctx := context.Background()
	serviceName := "ready-svc"
	instanceID := "ready-inst-001"

	// Start a healthy HTTP server.
	healthyListener, healthyPort := startTestHTTPServer(t, 200)
	defer healthyListener.Close()

	setupInstanceInStore(ctx, factStore, serviceName, instanceID, "test-node", "127.0.0.1")

	// Configure readiness with success_threshold=2 and failure_threshold=1
	// (no startup probe, so readiness can run immediately).
	writeHTTPProbeConfig(ctx, factStore, serviceName, "readiness", healthyPort, "/", 0, 1, 2)
	factStore.Put(ctx, types.KeyDesiredServiceProbeTimeout(serviceName, "readiness"), []byte("1s"))

	instanceInfo := placedInstanceInfo{id: instanceID, service: serviceName}

	// First execution: 1 success, threshold is 2, so no state change yet.
	nodeAgent.executeProbesForInstance(ctx, instanceInfo)

	_, readinessErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "readiness"))
	if readinessErr == nil {
		// The probe should not have written "ready" yet because we need 2
		// consecutive successes. It might have written nothing, or it might be
		// "unknown". Either way, it should NOT be "ready" yet.
		readinessFact, _ := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "readiness"))
		if string(readinessFact.Value) == string(types.ReadinessProbeReady) {
			t.Error("readiness should not be 'ready' after only 1 success (threshold=2)")
		}
	}

	// Reset the lastCheckTime to allow immediate re-execution.
	probeState := nodeAgent.getOrCreateProbeState(instanceID)
	probeState.readiness.lastCheckTime = time.Time{}

	// Second execution: 2 consecutive successes = threshold reached.
	nodeAgent.executeProbesForInstance(ctx, instanceInfo)

	readinessStateFact, readinessStateErr := factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "readiness"))
	if readinessStateErr != nil {
		t.Fatalf("readiness probe state not written after 2 successes: %v", readinessStateErr)
	}
	if string(readinessStateFact.Value) != string(types.ReadinessProbeReady) {
		t.Errorf("after 2 successes: readiness = %q, want %q", readinessStateFact.Value, types.ReadinessProbeReady)
	}

	// Now switch to a failing target. Close the healthy listener and point
	// probes at a port that refuses connections.
	healthyListener.Close()
	failingPort := getUnusedPort(t)

	// Reconfigure probes to point at the failing port. We need to update
	// the port fact in the store and also recreate the probe state tracker
	// to clear the success counter.
	factStore.Put(ctx, types.KeyDesiredServiceProbePort(serviceName, "readiness"), []byte(fmt.Sprintf("%d", failingPort)))
	factStore.Put(ctx, types.KeyDesiredServiceProbeTimeout(serviceName, "readiness"), []byte("100ms"))

	// Reset the lastCheckTime and the consecutive success counter to allow
	// failures to accumulate fresh.
	probeState.readiness.lastCheckTime = time.Time{}
	probeState.readiness.consecutiveSuccesses = 0

	// One failure should reach the failure_threshold=1.
	nodeAgent.executeProbesForInstance(ctx, instanceInfo)

	readinessStateFact, readinessStateErr = factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, "readiness"))
	if readinessStateErr != nil {
		t.Fatalf("readiness probe state not updated after failure: %v", readinessStateErr)
	}
	if string(readinessStateFact.Value) != string(types.ReadinessProbeNotReady) {
		t.Errorf("after failure: readiness = %q, want %q", readinessStateFact.Value, types.ReadinessProbeNotReady)
	}
}

// TestCleanupProbeState verifies that cleanupProbeState removes the in-memory
// probe tracking state for a given instance.
func TestCleanupProbeState(t *testing.T) {
	factStore, _, nodeAgent := setupProbeTestAgent("test-node")
	defer factStore.Close()

	instanceID := "cleanup-inst-001"

	// Create probe state by calling getOrCreateProbeState.
	probeState := nodeAgent.getOrCreateProbeState(instanceID)
	probeState.startup = &probeTracker{started: true, consecutiveSuccesses: 1}
	probeState.liveness = &probeTracker{started: true}

	// Verify the state exists.
	if nodeAgent.probeStates[instanceID] == nil {
		t.Fatal("probe state should exist before cleanup")
	}

	// Clean up.
	nodeAgent.cleanupProbeState(instanceID)

	// Verify the state is gone.
	if nodeAgent.probeStates[instanceID] != nil {
		t.Error("probe state should be nil after cleanup")
	}

	// Verify that cleaning up a non-existent ID does not panic.
	nodeAgent.cleanupProbeState("non-existent-instance")
}
