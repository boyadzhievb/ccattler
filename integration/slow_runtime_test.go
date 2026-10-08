// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// slowStartRuntime wraps a SimulatorRuntime and adds a configurable delay to
// Start() calls. This simulates the real-world cost of container image pulls,
// cgroup setup, and network configuration that can take tens of seconds.
type slowStartRuntime struct {
	delegate   runtime.Runtime
	startDelay time.Duration
}

// Start delays for the configured duration before delegating to the wrapped runtime.
func (slowRuntime *slowStartRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	select {
	case <-time.After(slowRuntime.startDelay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return slowRuntime.delegate.Start(ctx, spec)
}

// Stop delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) Stop(ctx context.Context, id string) error {
	return slowRuntime.delegate.Stop(ctx, id)
}

// Status delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) Status(ctx context.Context, id string) (runtime.Status, error) {
	return slowRuntime.delegate.Status(ctx, id)
}

// List delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) List(ctx context.Context) ([]runtime.Status, error) {
	return slowRuntime.delegate.List(ctx)
}

// Exec delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) Exec(ctx context.Context, id string, execSpec runtime.ExecSpec) error {
	return slowRuntime.delegate.Exec(ctx, id, execSpec)
}

// ExecInit delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) ExecInit(ctx context.Context, image string, execSpec runtime.ExecSpec) error {
	return slowRuntime.delegate.ExecInit(ctx, image, execSpec)
}

// Stats delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) Stats(ctx context.Context, id string) (runtime.ResourceStats, error) {
	return slowRuntime.delegate.Stats(ctx, id)
}

// Logs delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) Logs(ctx context.Context, id string, follow bool) (io.ReadCloser, error) {
	return slowRuntime.delegate.Logs(ctx, id, follow)
}

// Resize delegates to the wrapped runtime.
func (slowRuntime *slowStartRuntime) Resize(ctx context.Context, id string, cpuMillicores int64, memoryBytes int64) error {
	return slowRuntime.delegate.Resize(ctx, id, cpuMillicores, memoryBytes)
}

// TestSlowStartDoesNotCauseNodeUnreachable verifies that a node remains alive
// when container starts are slow (simulating image pulls). This test would have
// caught the 5-second lease timeout bug: the old timeout expired during the
// initial reconciliation while the agent was blocked starting containers,
// causing the node failure controller to mark the node unreachable.
func TestSlowStartDoesNotCauseNodeUnreachable(t *testing.T) {
	memStore := store.NewMemoryStore()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	simulatorRuntime := runtime.NewSimulatorRuntime()

	agentRuntime := &slowStartRuntime{
		delegate:   simulatorRuntime,
		startDelay: 3 * time.Second,
	}

	nodeFailureController := controllers.NewNodeFailureController()
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()

	controllerRunner := controllers.NewRunner(memStore,
		instanceController, schedulerController, nodeFailureController)
	controllerRunner.SetDebounce(50 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Register the node in the store (agent does this on startup, but the
	// simulator needs the node known for capacity).
	memStore.Put(ctx, types.KeyObservedNodeCapacityCPU("slow-node"), []byte("4000"))
	memStore.Put(ctx, types.KeyObservedNodeCapacityMemory("slow-node"), []byte("8192"))
	memStore.Put(ctx, types.KeyObservedNodeAvailableCPU("slow-node"), []byte("4000"))
	memStore.Put(ctx, types.KeyObservedNodeAvailableMemory("slow-node"), []byte("8192"))

	nodeAgent := agent.New("slow-node", memStore, agentRuntime)
	nodeAgent.SetInterval(500 * time.Millisecond)
	nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
	go nodeAgent.Run(ctx)

	// Deploy a service — the agent will try to start instances, each taking 3s.
	memStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	memStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))

	// Wait long enough for the slow starts to complete (2 instances * 3s each
	// sequential in the reconcile loop = ~6s). During this time, the old 5s
	// lease timeout would have expired and marked the node unreachable.
	time.Sleep(8 * time.Second)

	// Check node state — it should be "alive", not "unreachable".
	nodeFact, getError := memStore.Get(ctx, types.KeyObservedNodeState("slow-node"))
	if getError != nil {
		t.Fatalf("failed to read node state: %v", getError)
	}

	nodeState := string(nodeFact.Value)
	if nodeState != string(types.NodeAlive) {
		t.Errorf("node state after slow starts: got %q, want %q", nodeState, types.NodeAlive)
	}
}

// TestLeaseTimeoutDefaultIsReasonableForContainers verifies that the default
// node failure lease timeout is long enough for typical container operations.
// Container image pulls can take 30-60 seconds; the timeout must exceed this.
func TestLeaseTimeoutDefaultIsReasonableForContainers(t *testing.T) {
	nodeFailureController := controllers.NewNodeFailureController()

	minimumReasonableTimeout := 15 * time.Second
	if nodeFailureController.LeaseTimeout < minimumReasonableTimeout {
		t.Errorf("default LeaseTimeout is %v, but container operations "+
			"(image pulls, cgroup setup) routinely take 15-60s; "+
			"minimum reasonable timeout is %v",
			nodeFailureController.LeaseTimeout, minimumReasonableTimeout)
	}
}

// TestMemoryBytesPassedDirectlyToRuntime verifies that the agent passes memory
// values in bytes without double-conversion. The agent reads "512Mi" from the
// store (parsed to 536870912 bytes), and the runtime spec should contain that
// exact byte value — verified via the simulator's Stats().
func TestMemoryBytesPassedDirectlyToRuntime(t *testing.T) {
	memStore := store.NewMemoryStore()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	simulatorRuntime := runtime.NewSimulatorRuntime()

	// Register node capacity.
	memStore.Put(ctx, types.KeyObservedNodeCapacityCPU("mem-node"), []byte("4000"))
	memStore.Put(ctx, types.KeyObservedNodeCapacityMemory("mem-node"), []byte("8192"))
	memStore.Put(ctx, types.KeyObservedNodeAvailableCPU("mem-node"), []byte("4000"))
	memStore.Put(ctx, types.KeyObservedNodeAvailableMemory("mem-node"), []byte("8192"))

	nodeAgent := agent.New("mem-node", memStore, simulatorRuntime)
	nodeAgent.SetInterval(200 * time.Millisecond)
	nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
	go nodeAgent.Run(ctx)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	controllerRunner := controllers.NewRunner(memStore, instanceController, schedulerController)
	controllerRunner.SetDebounce(50 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Deploy a service with 512Mi memory.
	memStore.Put(ctx, types.KeyDesiredServiceImage("memtest"), []byte("nginx:1.27"))
	memStore.Put(ctx, types.KeyEffectiveServiceInstances("memtest"), []byte("1"))
	memStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("memtest"), []byte("512Mi"))

	// Wait for the instance to be running.
	var instanceID string
	waitFor(t, 5*time.Second, "instance running", func() bool {
		facts, scanError := memStore.Scan(ctx, "observed/instance/")
		if scanError != nil {
			return false
		}
		for _, fact := range facts {
			if strings.HasSuffix(fact.Key, "/state") && string(fact.Value) == "running" {
				// Extract instance ID: "observed/instance/<id>/state"
				trimmedKey := strings.TrimPrefix(fact.Key, "observed/instance/")
				instanceID = strings.TrimSuffix(trimmedKey, "/state")
				return true
			}
		}
		return false
	})

	if instanceID == "" {
		t.Fatal("could not find running instance ID")
	}

	// Check that the simulator recorded 536870912 bytes (512 MiB), not
	// 562949953421312 (the double-converted value).
	stats, statsError := simulatorRuntime.Stats(ctx, instanceID)
	if statsError != nil {
		t.Fatalf("failed to get stats for instance %s: %v", instanceID, statsError)
	}

	expectedMemoryBytes := int64(536870912) // 512 MiB
	if stats.MemoryBytes != expectedMemoryBytes {
		t.Errorf("runtime memory: got %d bytes, want %d bytes (512 MiB)",
			stats.MemoryBytes, expectedMemoryBytes)
		if stats.MemoryBytes == 562949953421312 {
			t.Error("memory value is double-converted: bytes * 1024 * 1024")
		}
	}
}
