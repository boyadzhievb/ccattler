// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// ---------------------------------------------------------------------------
// Phase 67e — Runtime, Observability, API & Ecosystem Evidence
// ---------------------------------------------------------------------------

// TestContainerRuntime_ImagePullFailure verifies that when a runtime returns a
// start error (simulating a non-existent image), the agent reports the instance
// as failed in the observed state — not a crash loop. The instance should stay
// in InstanceFailed with a clear error, not repeatedly restart.
func TestContainerRuntime_ImagePullFailure(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	// Start controllers to create and place instances.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Deploy the service and wait for placement — agent not running yet.
	helperDeployService(ctx, factStore, "broken", "nonexistent-registry.example.com/bad:latest", "1")

	var placedInstanceID string
	waitFor(t, 5*time.Second, "instance placed on node-1", func() bool {
		placements, _ := factStore.Scan(ctx, "placement/instance/")
		for _, placement := range placements {
			if string(placement.Value) == "node-1" {
				placedInstanceID = strings.TrimPrefix(placement.Key, "placement/instance/")
				return true
			}
		}
		return false
	})

	// Inject start failure for the placed instance before starting the agent.
	simulatorRuntime := runtime.NewSimulatorRuntime()
	simulatorRuntime.StartFailures = map[string]string{
		placedInstanceID: "image pull failed: nonexistent-registry.example.com/bad:latest",
	}

	nodeAgent := agent.New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
	go nodeAgent.Run(ctx)

	waitFor(t, 5*time.Second, "instance reported as failed", func() bool {
		fact, err := factStore.Get(ctx, types.KeyObservedInstanceState(placedInstanceID))
		return err == nil && types.InstanceState(fact.Value) == types.InstanceFailed
	})
}

// TestContainerRuntime_OOMKill verifies that when a workload is killed (e.g.
// OOM with exit code 137), the agent detects the killed container via runtime
// Status and reports it as InstanceFailed.
func TestContainerRuntime_OOMKill(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
	go nodeAgent.Run(ctx)

	helperDeployService(ctx, factStore, "memhog", "memhog:latest", "1")

	waitFor(t, 5*time.Second, "memhog instance running", func() bool {
		return helperCountRunningInstances(ctx, factStore, "memhog") == 1
	})

	// Find the instance ID and kill it with OOM exit code.
	var killedInstanceID string
	instances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range instances {
		if instance.Service == "memhog" && instance.State == types.InstanceRunning {
			killedInstanceID = instance.ID
			break
		}
	}
	if killedInstanceID == "" {
		t.Fatal("no running memhog instance found to kill")
	}

	// Kill the workload and block restarts so the failed state persists.
	simulatorRuntime.KillWorkload(killedInstanceID, 137, "OOMKilled")
	simulatorRuntime.StartFailures = map[string]string{
		killedInstanceID: "OOMKilled",
	}

	waitFor(t, 5*time.Second, "OOM-killed instance detected as failed", func() bool {
		fact, getError := factStore.Get(ctx, types.KeyObservedInstanceState(killedInstanceID))
		return getError == nil && types.InstanceState(fact.Value) == types.InstanceFailed
	})
}

// TestContainerRuntime_GracefulShutdown verifies that when the agent stops an
// instance (because it is no longer placed on the node), the runtime's Stop
// method is called and the instance transitions through correct state changes.
// The SimulatorRuntime Stop produces a clean exit; the agent should report the
// instance as stopped and clean up observed state.
func TestContainerRuntime_GracefulShutdown(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
	go nodeAgent.Run(ctx)

	helperDeployService(ctx, factStore, "graceful", "app:v1", "2")

	waitFor(t, 5*time.Second, "2 running graceful instances", func() bool {
		return helperCountRunningInstances(ctx, factStore, "graceful") == 2
	})

	// Scale down to 0 — all instances should be stopped gracefully.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("graceful"), []byte("0"))

	waitFor(t, 5*time.Second, "all graceful instances stopped", func() bool {
		return helperCountRunningInstances(ctx, factStore, "graceful") == 0
	})

	// Wait for the agent to complete its cleanup cycle (Stop calls on runtime).
	waitFor(t, 5*time.Second, "runtime reports no running graceful workloads", func() bool {
		allStatuses, _ := simulatorRuntime.List(ctx)
		for _, status := range allStatuses {
			if status.Running {
				return false
			}
		}
		return true
	})
}

// TestObservability_DecisionAuditTrail verifies that deploying a service
// produces semantic events in the event log explaining each decision: instance
// creation, placement, and reaching running state.
func TestObservability_DecisionAuditTrail(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventLog := types.NewEventLog(factStore, 100)

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Let the EventProjector establish its watches before writing facts,
	// otherwise the initial effective/service write can race the watch setup.
	time.Sleep(100 * time.Millisecond)

	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
	go nodeAgent.Run(ctx)

	helperDeployService(ctx, factStore, "web", "nginx:1.28", "2")

	waitFor(t, 5*time.Second, "2 running web instances", func() bool {
		return helperCountRunningInstances(ctx, factStore, "web") == 2
	})

	// Wait for all three event types to appear in the log.
	waitFor(t, 5*time.Second, "all audit trail events emitted", func() bool {
		allEvents, queryError := eventLog.Query(ctx, "", 0)
		if queryError != nil {
			return false
		}
		hasPlacementEvent := false
		hasRunningEvent := false
		hasScaledEvent := false
		for _, event := range allEvents {
			if event.Kind == "instance.placed" {
				hasPlacementEvent = true
			}
			if event.Kind == "instance.running" {
				hasRunningEvent = true
			}
			if event.Kind == "service.scaled" && strings.Contains(event.Target, "web") {
				hasScaledEvent = true
			}
		}
		return hasPlacementEvent && hasRunningEvent && hasScaledEvent
	})

	// Final verification with explicit error messages.
	allEvents, queryError := eventLog.Query(ctx, "", 0)
	if queryError != nil {
		t.Fatalf("query events: %v", queryError)
	}

	hasPlacementEvent := false
	hasRunningEvent := false
	hasScaledEvent := false

	for _, event := range allEvents {
		if event.Kind == "instance.placed" {
			hasPlacementEvent = true
		}
		if event.Kind == "instance.running" {
			hasRunningEvent = true
		}
		if event.Kind == "service.scaled" && strings.Contains(event.Target, "web") {
			hasScaledEvent = true
		}
	}

	if !hasPlacementEvent {
		t.Error("expected instance.placed event in audit trail")
	}
	if !hasRunningEvent {
		t.Error("expected instance.running event in audit trail")
	}
	if !hasScaledEvent {
		t.Error("expected service.scaled event in audit trail")
	}

	if len(allEvents) < 3 {
		t.Errorf("expected at least 3 events (placed, running, scaled), got %d", len(allEvents))
	}
}

// TestObservability_ReconciliationExplainability verifies that after a node
// failure, the event stream shows the causal chain: node unreachable → instances
// failed → replacements created → placed on surviving nodes.
func TestObservability_ReconciliationExplainability(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventLog := types.NewEventLog(factStore, 200)

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 300 * time.Millisecond
	endpointController := controllers.NewEndpointController()

	controllerRunner := controllers.NewRunner(factStore, instanceController,
		schedulerController, failureController, nodeFailureController, endpointController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	runtimes := make(map[string]*runtime.SimulatorRuntime)
	agentCancels := make(map[string]context.CancelFunc)
	for _, nodeID := range []string{"node-1", "node-2"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		runtimes[nodeID] = simulatorRuntime
		nodeCtx, nodeCancel := context.WithCancel(ctx)
		agentCancels[nodeID] = nodeCancel
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetInterval(50 * time.Millisecond)
		nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
		go nodeAgent.Run(nodeCtx)
	}

	helperDeployService(ctx, factStore, "web", "nginx:1.28", "4")

	waitFor(t, 5*time.Second, "4 running web instances", func() bool {
		return helperCountRunningInstances(ctx, factStore, "web") == 4
	})

	beforeFailure := time.Now()

	// Kill node-1 agent.
	agentCancels["node-1"]()

	// Wait for node-1 to become unreachable.
	waitFor(t, 3*time.Second, "node-1 unreachable", func() bool {
		fact, err := factStore.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(fact.Value) == string(types.NodeUnreachable)
	})

	// Wait for recovery — instances rescheduled to node-2.
	waitFor(t, 10*time.Second, "4 running after node-1 death", func() bool {
		return helperCountRunningInstances(ctx, factStore, "web") == 4
	})

	time.Sleep(300 * time.Millisecond)

	eventsSinceFailure, queryError := eventLog.Since(ctx, beforeFailure, 0)
	if queryError != nil {
		t.Fatalf("query events: %v", queryError)
	}

	hasNodeUnreachable := false
	hasInstanceFailed := false
	hasReplacementPlaced := false

	for _, event := range eventsSinceFailure {
		if event.Kind == "node.unreachable" && strings.Contains(event.Target, "node-1") {
			hasNodeUnreachable = true
		}
		if event.Kind == "instance.failed" || event.Kind == "instance.stopped" {
			hasInstanceFailed = true
		}
		if event.Kind == "instance.placed" {
			hasReplacementPlaced = true
		}
	}

	if !hasNodeUnreachable {
		t.Error("causal chain missing: node.unreachable event for node-1")
	}
	if !hasInstanceFailed {
		t.Error("causal chain missing: instance.failed or instance.stopped event")
	}
	if !hasReplacementPlaced {
		t.Error("causal chain missing: instance.placed event for replacement")
	}

	if len(eventsSinceFailure) < 3 {
		t.Errorf("expected at least 3 events in causal chain, got %d", len(eventsSinceFailure))
		for _, event := range eventsSinceFailure {
			t.Logf("  event: kind=%s target=%s detail=%s", event.Kind, event.Target, event.Detail)
		}
	}
}

// TestAPIVersioning_BackwardsCompatibility verifies that every .cca file from
// examples/ and test/e2e/workloads/ still parses and compiles without error.
// This is a regression suite ensuring the DSL grammar never breaks existing
// configs as it evolves.
func TestAPIVersioning_BackwardsCompatibility(t *testing.T) {
	ccaDirectories := []string{
		filepath.Join("..", "examples"),
		filepath.Join("..", "test", "e2e", "workloads"),
	}

	totalFileCount := 0

	for _, ccaDirectory := range ccaDirectories {
		ccaFiles, globError := filepath.Glob(filepath.Join(ccaDirectory, "*.cca"))
		if globError != nil {
			t.Fatalf("glob %s: %v", ccaDirectory, globError)
		}

		for _, ccaFilePath := range ccaFiles {
			totalFileCount++
			fileName := filepath.Base(ccaFilePath)
			t.Run(fileName, func(t *testing.T) {
				fileContents, readError := os.ReadFile(ccaFilePath) //nolint:gosec
				if readError != nil {
					t.Fatalf("read %s: %v", fileName, readError)
				}

				factStore := store.NewMemoryStore()
				defer factStore.Close()
				ctx := context.Background()

				fileBaseDir := filepath.Dir(ccaFilePath)
				if applyError := lang.ApplyWithBaseDir(ctx, factStore, string(fileContents), fileBaseDir); applyError != nil {
					t.Fatalf("backwards compatibility broken for %s: %v", fileName, applyError)
				}

				// Verify at least one fact was produced.
				desiredFacts, _ := factStore.Scan(ctx, "desired/")
				authFacts, _ := factStore.Scan(ctx, "auth/")
				if len(desiredFacts) == 0 && len(authFacts) == 0 {
					t.Errorf("%s produced zero facts", fileName)
				}
			})
		}
	}

	if totalFileCount < 15 {
		t.Errorf("expected at least 15 .cca files for compatibility testing, found %d", totalFileCount)
	}
}

// statusEndpointContractFields lists the required top-level JSON fields in the
// /api/status response. A missing field would be a breaking API change.
var statusEndpointContractFields = []string{
	"services", "instances", "nodes",
}

// serviceContractFields lists the required JSON fields in each service entry.
var serviceContractFields = []string{
	"name", "image", "desired", "running",
}

// instanceContractFields lists the required JSON fields in each instance entry.
var instanceContractFields = []string{
	"id", "service", "state", "node",
}

// nodeContractFields lists the required JSON fields in each node entry.
var nodeContractFields = []string{
	"id", "state", "instances", "available_cpu", "capacity_cpu",
	"available_memory", "capacity_memory",
}

// TestAPIStability_StatusEndpointContract verifies that the /api/status JSON
// schema has not changed its required field names or types. This snapshot
// comparison catches accidental renames or type changes that would break
// existing consumers.
func TestAPIStability_StatusEndpointContract(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})
	helperDeployService(ctx, factStore, "web", "nginx:1.28", "1")
	factStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte(string(types.InstanceRunning)))
	factStore.Put(ctx, types.KeyObservedInstanceService("web-1"), []byte("web"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("web-1"), []byte("node-1"))

	apiServer := api.NewServer(factStore)
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var rawResponse map[string]json.RawMessage
	if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &rawResponse); unmarshalError != nil {
		t.Fatalf("unmarshal status response: %v", unmarshalError)
	}

	for _, requiredField := range statusEndpointContractFields {
		if _, exists := rawResponse[requiredField]; !exists {
			t.Errorf("CONTRACT VIOLATION: required top-level field %q missing from /api/status", requiredField)
		}
	}

	// Check service entry fields.
	var services []map[string]json.RawMessage
	if servicesRaw, ok := rawResponse["services"]; ok {
		json.Unmarshal(servicesRaw, &services)
	}
	if len(services) > 0 {
		for _, requiredField := range serviceContractFields {
			if _, exists := services[0][requiredField]; !exists {
				t.Errorf("CONTRACT VIOLATION: required service field %q missing", requiredField)
			}
		}
	}

	// Check instance entry fields.
	var instances []map[string]json.RawMessage
	if instancesRaw, ok := rawResponse["instances"]; ok {
		json.Unmarshal(instancesRaw, &instances)
	}
	if len(instances) > 0 {
		for _, requiredField := range instanceContractFields {
			if _, exists := instances[0][requiredField]; !exists {
				t.Errorf("CONTRACT VIOLATION: required instance field %q missing", requiredField)
			}
		}
	}

	// Check node entry fields.
	var nodes []map[string]json.RawMessage
	if nodesRaw, ok := rawResponse["nodes"]; ok {
		json.Unmarshal(nodesRaw, &nodes)
	}
	if len(nodes) > 0 {
		for _, requiredField := range nodeContractFields {
			if _, exists := nodes[0][requiredField]; !exists {
				t.Errorf("CONTRACT VIOLATION: required node field %q missing", requiredField)
			}
		}
	}

	// Verify struct field types match expectations by round-tripping through
	// the typed struct and comparing keys.
	var typedStatus api.ClusterStatus
	if unmarshalError := json.Unmarshal(recorder.Body.Bytes(), &typedStatus); unmarshalError != nil {
		t.Errorf("CONTRACT VIOLATION: response does not unmarshal into ClusterStatus: %v", unmarshalError)
	}

	// Verify the typed struct has the same JSON shape as the raw map.
	reSerializedBytes, _ := json.Marshal(typedStatus)
	var reSerializedMap map[string]json.RawMessage
	json.Unmarshal(reSerializedBytes, &reSerializedMap)

	for _, topLevelField := range statusEndpointContractFields {
		if _, exists := reSerializedMap[topLevelField]; !exists {
			t.Errorf("CONTRACT VIOLATION: ClusterStatus struct missing JSON field %q", topLevelField)
		}
	}
}

// TestEcosystem_ControllerSDKPlugin verifies that an external controller
// written using the Controller SDK (CustomController + ReconcileFunc) can be
// registered in a Runner, receive facts via the standard Watch/Reconcile loop,
// and write derived facts back to the store.
func TestEcosystem_ControllerSDKPlugin(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build a custom "alert-controller" that watches observed instance states
	// and writes a derived alert fact when any instance is failed.
	alertController := controllers.NewCustomController(
		"alert-controller",
		[]string{"observed/instance/"},
		func(reconcileCtx context.Context, facts *controllers.FactMap) ([]controllers.Change, error) {
			var changes []controllers.Change
			for _, entityName := range facts.Entities() {
				instanceState := facts.GetField(entityName, "state")
				if instanceState == string(types.InstanceFailed) {
					alertKey := fmt.Sprintf("derived/alert/%s", entityName)
					changes = append(changes, controllers.PutChange(alertKey, "instance-failed"))
				}
			}
			return changes, nil
		},
	)

	// Run it alongside the standard instance controller.
	instanceController := controllers.NewInstanceController()
	controllerRunner := controllers.NewRunner(factStore, instanceController, alertController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Write a failed instance into observed state.
	factStore.Put(ctx, types.KeyObservedInstanceState("api-1"), []byte(string(types.InstanceFailed)))
	factStore.Put(ctx, types.KeyObservedInstanceService("api-1"), []byte("api"))
	factStore.Put(ctx, types.KeyObservedInstanceNode("api-1"), []byte("node-1"))

	// Wait for the custom controller to produce the derived alert fact.
	waitFor(t, 5*time.Second, "derived alert fact created by SDK plugin", func() bool {
		fact, err := factStore.Get(ctx, "derived/alert/api-1")
		return err == nil && string(fact.Value) == "instance-failed"
	})

	// Verify the custom controller's name is accessible via the interface.
	if alertController.Name() != "alert-controller" {
		t.Errorf("expected controller name 'alert-controller', got %q", alertController.Name())
	}

	// Verify Watch() returns the declared prefix.
	watchPrefixes := alertController.Watch()
	if len(watchPrefixes) != 1 || watchPrefixes[0] != "observed/instance/" {
		t.Errorf("unexpected watch prefixes: %v", watchPrefixes)
	}

	// Verify the FactMap organized facts correctly.
	allFacts, _ := factStore.Scan(ctx, "observed/instance/")
	factMap := controllers.NewFactMap(allFacts, "observed/instance/")
	if !factMap.HasEntity("api-1") {
		t.Error("FactMap should have entity 'api-1'")
	}
	if factMap.GetField("api-1", "state") != string(types.InstanceFailed) {
		t.Errorf("FactMap field mismatch: got %q", factMap.GetField("api-1", "state"))
	}
}

// TestEcosystem_WatchIntegration verifies that an external consumer can connect
// to the /api/watch SSE endpoint, receive real-time fact changes, and that the
// event format is a stable JSON structure with type, key, value, and revision
// fields.
func TestEcosystem_WatchIntegration(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	apiServer := api.NewServer(factStore)
	testServer := httptest.NewServer(apiServer.Handler())
	defer testServer.Close()

	// Start a goroutine that will write facts after a brief delay.
	writeContext, writeCancel := context.WithCancel(context.Background())
	defer writeCancel()
	go func() {
		time.Sleep(200 * time.Millisecond)
		factStore.Put(writeContext, "desired/service/web/image", []byte("nginx:1.28"))
		time.Sleep(50 * time.Millisecond)
		factStore.Put(writeContext, "desired/service/web/instances", []byte("3"))
		time.Sleep(50 * time.Millisecond)
		factStore.Put(writeContext, "desired/service/api/image", []byte("myapi:v2"))
	}()

	watchURL := testServer.URL + "/api/watch?prefix=desired/service/"
	watchCtx, watchCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer watchCancel()

	watchRequest, _ := http.NewRequestWithContext(watchCtx, http.MethodGet, watchURL, nil)
	watchResponse, requestError := http.DefaultClient.Do(watchRequest)
	if requestError != nil {
		t.Fatalf("watch request failed: %v", requestError)
	}
	defer watchResponse.Body.Close()

	if watchResponse.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", watchResponse.Header.Get("Content-Type"))
	}

	// Read SSE events from the stream.
	type watchEvent struct {
		Type     string `json:"type"`
		Key      string `json:"key"`
		Value    string `json:"value"`
		Revision int64  `json:"revision"`
	}

	receivedEvents := make([]watchEvent, 0)
	scanner := bufio.NewScanner(watchResponse.Body)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonPayload := strings.TrimPrefix(line, "data: ")

		var event watchEvent
		if unmarshalError := json.Unmarshal([]byte(jsonPayload), &event); unmarshalError != nil {
			t.Errorf("failed to unmarshal SSE event JSON: %v (payload: %s)", unmarshalError, jsonPayload)
			continue
		}
		receivedEvents = append(receivedEvents, event)

		// Verify the event has all required fields.
		eventFields := reflect.TypeOf(event)
		for fieldIndex := 0; fieldIndex < eventFields.NumField(); fieldIndex++ {
			jsonTag := eventFields.Field(fieldIndex).Tag.Get("json")
			rawMap := make(map[string]interface{})
			json.Unmarshal([]byte(jsonPayload), &rawMap)
			if _, exists := rawMap[jsonTag]; !exists {
				t.Errorf("SSE event missing required field %q", jsonTag)
			}
		}

		if len(receivedEvents) >= 3 {
			break
		}
	}

	if len(receivedEvents) < 3 {
		t.Fatalf("expected at least 3 watch events, received %d", len(receivedEvents))
	}

	// Verify event content matches what was written.
	foundWebImage := false
	foundWebInstances := false
	foundAPIImage := false

	for _, event := range receivedEvents {
		if event.Type != "put" {
			t.Errorf("expected event type 'put', got %q", event.Type)
		}
		if event.Key == "desired/service/web/image" && event.Value == "nginx:1.28" {
			foundWebImage = true
		}
		if event.Key == "desired/service/web/instances" && event.Value == "3" {
			foundWebInstances = true
		}
		if event.Key == "desired/service/api/image" && event.Value == "myapi:v2" {
			foundAPIImage = true
		}
		if event.Revision <= 0 {
			t.Errorf("event revision should be positive, got %d for key %s", event.Revision, event.Key)
		}
	}

	if !foundWebImage {
		t.Error("missed watch event for desired/service/web/image")
	}
	if !foundWebInstances {
		t.Error("missed watch event for desired/service/web/instances")
	}
	if !foundAPIImage {
		t.Error("missed watch event for desired/service/api/image")
	}
}
