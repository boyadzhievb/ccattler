// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// agentResourceTestTimeout bounds the wait for the agent to start the container.
const agentResourceTestTimeout = 2 * time.Second

// TestAgentReadsEffectiveResourceQuantities: spec resource-requests, "The
// agent applies the effective request". The effective values written by the
// vertical autoscaler (750m, 768Mi) win over the desired ones and are read
// with their units.
func TestAgentReadsEffectiveResourceQuantities(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	simulatorRuntime := runtime.NewSimulatorRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("web"), []byte("250m"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("web"), []byte("256Mi"))
	factStore.Put(ctx, types.KeyEffectiveServiceResourcesCPU("web"), []byte("750m"))
	factStore.Put(ctx, types.KeyEffectiveServiceResourcesMemory("web"), []byte("768Mi"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "web-1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "web-1", NodeID: "node-1"})

	nodeAgent := New("node-1", factStore, simulatorRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)
	go nodeAgent.Run(ctx)

	waitFor(t, agentResourceTestTimeout, "web-1 started", func() bool {
		workloadStatus, statusError := simulatorRuntime.Status(ctx, "web-1")
		return statusError == nil && workloadStatus.Running
	})
	resourceStats, statsError := simulatorRuntime.Stats(ctx, "web-1")
	if statsError != nil {
		t.Fatalf("stats: %v", statsError)
	}
	if resourceStats.CPUMillicores != 750 {
		t.Errorf("cpu limit = %d millicores, want 750", resourceStats.CPUMillicores)
	}
	if resourceStats.MemoryBytes != 805306368 {
		t.Errorf("memory limit = %d bytes, want 805306368", resourceStats.MemoryBytes)
	}
}

// TestLookupServiceResourcesReadsKubernetesForms checks the agent reads
// requests with the shared parser: cores, millicores and decimal memory
// suffixes, and an unreadable value as 0 (no limit) instead of guessing.
func TestLookupServiceResourcesReadsKubernetesForms(t *testing.T) {
	testCases := []struct {
		cpuText        string // cpuText is the stored desired cpu
		memoryText     string // memoryText is the stored desired memory
		wantMillicores int64  // wantMillicores is the expected CPU limit
		wantBytes      int64  // wantBytes is the expected memory limit
	}{
		{cpuText: "0.5", memoryText: "1G", wantMillicores: 500, wantBytes: 1000000000},
		{cpuText: "2", memoryText: "1.5Gi", wantMillicores: 2000, wantBytes: 1610612736},
		{cpuText: "lots", memoryText: "512MB", wantMillicores: 0, wantBytes: 0},
	}
	for _, testCase := range testCases {
		factStore := store.NewMemoryStore()
		ctx := context.Background()
		factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("web"), []byte(testCase.cpuText))
		factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("web"), []byte(testCase.memoryText))
		nodeReporter := &NodeReporter{factStore: factStore}
		gotMillicores, gotBytes := nodeReporter.lookupServiceResourcesFromStore(ctx, "web")
		if gotMillicores != testCase.wantMillicores || gotBytes != testCase.wantBytes {
			t.Errorf("cpu %q memory %q: got %d / %d, want %d / %d", testCase.cpuText, testCase.memoryText,
				gotMillicores, gotBytes, testCase.wantMillicores, testCase.wantBytes)
		}
		_ = factStore.Close()
	}
}
