// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestColocatePlacesNearTargetService verifies that a service with
// `colocate "redis"` prefers nodes already running redis when loads are equal.
func TestColocatePlacesNearTargetService(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := newTestScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)
	startRunner(t, factStore, ctx, cancel, runner)

	// Deploy redis (1 instance) and filler (1 instance) together.
	// Spread places redis on node-1, filler on node-2 → equal load.
	setupInput := `
service redis {
    image redis:7
    instances 1
}

service filler {
    image filler:1.0
    instances 1
}
`
	if err := lang.Apply(ctx, factStore, setupInput); err != nil {
		t.Fatalf("apply setup failed: %v", err)
	}

	waitFor(t, 3*time.Second, "redis and filler placed", func() bool {
		return countPlacementsForService(ctx, factStore, "redis") == 1 &&
			countPlacementsForService(ctx, factStore, "filler") == 1
	})

	// Find which node has redis.
	redisNodes := findNodesForService(ctx, factStore, "redis")
	if len(redisNodes) != 1 {
		t.Fatalf("expected redis on 1 node, got %d", len(redisNodes))
	}

	// Deploy web with colocate "redis" — 1 instance.
	// Both nodes have load=1, colocate hint breaks the tie toward the redis node.
	webInput := `
service web {
    image nginx:1.28
    instances 1
    placement {
        colocate "redis"
    }
}
`
	if err := lang.Apply(ctx, factStore, webInput); err != nil {
		t.Fatalf("apply web failed: %v", err)
	}

	waitFor(t, 3*time.Second, "1 web instance placed", func() bool {
		return countPlacementsForService(ctx, factStore, "web") == 1
	})

	// Web should land on the same node as redis (colocate hint).
	webNodes := findNodesForService(ctx, factStore, "web")
	for webNode := range webNodes {
		if !redisNodes[webNode] {
			t.Errorf("web placed on %s which has no redis (colocate hint violated)", webNode)
		}
	}
}

// TestSeparatePlacesAwayFromTargetService verifies that a service with
// `separate "redis"` avoids nodes running redis when an alternative exists.
func TestSeparatePlacesAwayFromTargetService(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	for _, nodeID := range []string{"node-1", "node-2"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := newTestScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)
	startRunner(t, factStore, ctx, cancel, runner)

	// Deploy redis (1 instance) and filler (1 instance) to equalize load.
	setupInput := `
service redis {
    image redis:7
    instances 1
}

service filler {
    image filler:1.0
    instances 1
}
`
	if err := lang.Apply(ctx, factStore, setupInput); err != nil {
		t.Fatalf("apply setup failed: %v", err)
	}

	waitFor(t, 3*time.Second, "redis and filler placed", func() bool {
		return countPlacementsForService(ctx, factStore, "redis") == 1 &&
			countPlacementsForService(ctx, factStore, "filler") == 1
	})

	redisNodes := findNodesForService(ctx, factStore, "redis")

	// Deploy web with separate "redis" — 1 instance.
	// Both nodes have load=1, separate hint breaks the tie away from the redis node.
	webInput := `
service web {
    image nginx:1.28
    instances 1
    placement {
        separate "redis"
    }
}
`
	if err := lang.Apply(ctx, factStore, webInput); err != nil {
		t.Fatalf("apply web failed: %v", err)
	}

	waitFor(t, 3*time.Second, "1 web instance placed", func() bool {
		return countPlacementsForService(ctx, factStore, "web") == 1
	})

	// Web should be on the node WITHOUT redis (separate hint).
	webNodes := findNodesForService(ctx, factStore, "web")
	for webNode := range webNodes {
		if redisNodes[webNode] {
			t.Errorf("web placed on %s which has redis (separate hint violated)", webNode)
		}
	}
}

// findNodesForService returns the set of node IDs hosting active instances of a service.
func findNodesForService(ctx context.Context, factStore store.StateStore, serviceName string) map[string]bool {
	nodeSet := make(map[string]bool)
	placements, _ := factStore.Scan(ctx, types.ScanPlacements)
	for _, placement := range placements {
		instanceID := extractInstanceIDFromPlacementKey(placement.Key)
		serviceKey := types.KeyObservedInstanceService(instanceID)
		serviceFact, err := factStore.Get(ctx, serviceKey)
		if err != nil || string(serviceFact.Value) != serviceName {
			continue
		}
		stoppedKey := types.KeyDerivedInstanceControllerStopped(instanceID)
		stoppedFact, err := factStore.Get(ctx, stoppedKey)
		if err == nil && string(stoppedFact.Value) == "true" {
			continue
		}
		nodeSet[string(placement.Value)] = true
	}
	return nodeSet
}
