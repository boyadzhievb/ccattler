// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// helperSetupServiceGroupCluster creates a 2-node simulated cluster with the
// full controller set needed for service group integration testing.
func helperSetupServiceGroupCluster(t *testing.T) (store.StateStore, context.CancelFunc) {
	t.Helper()

	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	controllerRunner := controllers.NewRunner(factStore,
		controllers.NewInstanceController(),
		scheduler.NewScheduler(),
		controllers.NewEndpointController(),
		controllers.NewFailureController(),
		controllers.NewIntentResolverController(),
	)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go func() {
		_ = controllerRunner.Run(ctx)
	}()

	for _, nodeID := range []string{"node-1", "node-2"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		nodeAgent.SetHeartbeatInterval(100 * time.Millisecond)
		go func() {
			_ = nodeAgent.Run(ctx)
		}()
	}

	return factStore, cancel
}

// TestServiceGroupCoScheduled verifies the full pipeline: DSL group block →
// desired/group/ facts → scheduler co-places group member instances on the
// same node.
func TestServiceGroupCoScheduled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	factStore, cancel := helperSetupServiceGroupCluster(t)
	defer cancel()
	defer factStore.Close()

	ctx := context.Background()

	dslInput := `
service proxy {
    image "envoy:1.30"
    instances 1
}

service web {
    image "nginx:1.27"
    instances 1
}

group frontend {
    process proxy
    process web
    share network
}
`
	applyErr := lang.Apply(ctx, factStore, dslInput)
	if applyErr != nil {
		t.Fatalf("apply error: %v", applyErr)
	}

	// Wait for both instances to be running and co-located on the same node.
	waitFor(t, 5*time.Second, "both group instances running and co-located", func() bool {
		allFacts, _ := factStore.Scan(ctx, types.ScanObservedInstances)
		runningCount := 0
		for _, fact := range allFacts {
			if strings.HasSuffix(fact.Key, "/state") && string(fact.Value) == string(types.InstanceRunning) {
				runningCount++
			}
		}
		if runningCount < 2 {
			return false
		}

		placements, _ := factStore.Scan(ctx, types.ScanPlacements)
		nodesByInstance := make(map[string]string)
		for _, fact := range placements {
			instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
			if instanceID != "" && !strings.Contains(instanceID, "/") {
				nodesByInstance[instanceID] = string(fact.Value)
			}
		}

		observedInstances, _ := factStore.Scan(ctx, types.ScanObservedInstances)
		serviceByInstance := make(map[string]string)
		for _, fact := range observedInstances {
			relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
			parts := strings.SplitN(relativePath, "/", 2)
			if len(parts) == 2 && parts[1] == "service" {
				serviceByInstance[parts[0]] = string(fact.Value)
			}
		}

		var proxyNode, webNode string
		for instanceID, serviceName := range serviceByInstance {
			switch serviceName {
			case "proxy":
				proxyNode = nodesByInstance[instanceID]
			case "web":
				webNode = nodesByInstance[instanceID]
			}
		}

		return proxyNode != "" && webNode != "" && proxyNode == webNode
	})
}

// TestServiceGroupWithUngroupedService verifies that ungrouped services are
// placed independently while grouped services are co-located.
func TestServiceGroupWithUngroupedService(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	factStore, cancel := helperSetupServiceGroupCluster(t)
	defer cancel()
	defer factStore.Close()

	ctx := context.Background()

	dslInput := `
service proxy {
    image "envoy:1.30"
    instances 1
}

service web {
    image "nginx:1.27"
    instances 1
}

service api {
    image "myapi:v1"
    instances 1
}

group frontend {
    process proxy
    process web
    share network
}
`
	applyErr := lang.Apply(ctx, factStore, dslInput)
	if applyErr != nil {
		t.Fatalf("apply error: %v", applyErr)
	}

	// Wait for all 3 instances running and grouped services co-located.
	waitFor(t, 5*time.Second, "all three instances running and group co-located", func() bool {
		allFacts, _ := factStore.Scan(ctx, types.ScanObservedInstances)
		runningCount := 0
		for _, fact := range allFacts {
			if strings.HasSuffix(fact.Key, "/state") && string(fact.Value) == string(types.InstanceRunning) {
				runningCount++
			}
		}
		if runningCount < 3 {
			return false
		}

		placements, _ := factStore.Scan(ctx, types.ScanPlacements)
		nodesByInstance := make(map[string]string)
		for _, fact := range placements {
			instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
			if instanceID != "" && !strings.Contains(instanceID, "/") {
				nodesByInstance[instanceID] = string(fact.Value)
			}
		}

		observedInstances, _ := factStore.Scan(ctx, types.ScanObservedInstances)
		serviceByInstance := make(map[string]string)
		for _, fact := range observedInstances {
			relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
			parts := strings.SplitN(relativePath, "/", 2)
			if len(parts) == 2 && parts[1] == "service" {
				serviceByInstance[parts[0]] = string(fact.Value)
			}
		}

		var proxyNode, webNode string
		for instanceID, serviceName := range serviceByInstance {
			switch serviceName {
			case "proxy":
				proxyNode = nodesByInstance[instanceID]
			case "web":
				webNode = nodesByInstance[instanceID]
			}
		}

		return proxyNode != "" && webNode != "" && proxyNode == webNode
	})
}
