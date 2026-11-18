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

// rolloutKindsTimeout bounds each wait of the rollout-kind tests.
const rolloutKindsTimeout = 10 * time.Second

// startRolloutKindsCluster runs the instance, scheduler, intent and rollout
// controllers and simulator agents on three nodes, applies configuration and
// returns the store and context.
func startRolloutKindsCluster(t *testing.T, configuration string) (store.StateStore, context.Context) {
	t.Helper()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	registerTestNodes(ctx, factStore, 3)
	controllerRunner := controllers.NewRunner(factStore,
		controllers.NewInstanceController(), newTestScheduler(),
		controllers.NewIntentResolverController(), controllers.NewRolloutController(),
	)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	startTestAgents(ctx, factStore, 3)
	startRunner(t, factStore, ctx, cancel, controllerRunner)
	if applyError := lang.Apply(ctx, factStore, configuration); applyError != nil {
		t.Fatalf("apply failed: %v", applyError)
	}
	return factStore, ctx
}

// serviceRolledOutTo reports whether the service's rollout is complete with
// wantCount running instances, each started by its agent on image.
func serviceRolledOutTo(ctx context.Context, factStore store.StateStore, serviceName string, image string, wantCount int) bool {
	stateFact, stateError := factStore.Get(ctx, types.KeyDerivedServiceRolloutState(serviceName))
	if stateError != nil || types.RolloutState(stateFact.Value) != types.RolloutComplete {
		return false
	}
	instances, listError := types.ListInstances(ctx, factStore)
	if listError != nil {
		return false
	}
	runningOnImage := 0
	for _, instance := range instances {
		if instance.Service != serviceName || instance.State == types.InstanceStopped {
			continue
		}
		observedImage, imageError := factStore.Get(ctx, types.KeyObservedInstanceImage(instance.ID))
		if instance.State != types.InstanceRunning || imageError != nil || string(observedImage.Value) != image {
			return false
		}
		runningOnImage++
	}
	return runningOnImage == wantCount
}

// TestPerNodeRolloutReplacesEveryNode: testbed T2 (M241 2e.2). A per-node
// service on three nodes rolls out to a new image: each replacement is placed
// on the node its stopped predecessor left, and the rollout completes.
func TestPerNodeRolloutReplacesEveryNode(t *testing.T) {
	t.Parallel()
	factStore, ctx := startRolloutKindsCluster(t, `
service agentd {
    image monitor:1.0
    placement {
        mode per-node
    }
    update {
        min_healthy_time 0s
    }
}
`)
	waitFor(t, rolloutKindsTimeout, "3 running agentd instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "agentd") == 3
	})
	factStore.Put(ctx, types.KeyDesiredServiceImage("agentd"), []byte("monitor:2.0"))
	waitFor(t, rolloutKindsTimeout, "agentd rollout complete on monitor:2.0 on every node", func() bool {
		return serviceRolledOutTo(ctx, factStore, "agentd", "monitor:2.0", 3)
	})
}

// TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage: testbed T1 (M241
// 2e.1). A 1-instance stateful service rolls out to a new image: the rollout
// completes only once the agent runs db-0 on the new image.
func TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage(t *testing.T) {
	t.Parallel()
	factStore, ctx := startRolloutKindsCluster(t, `
service db {
    image whoami:1.0
    instances 1
    stateful
    update {
        min_healthy_time 0s
    }
}
`)
	waitFor(t, rolloutKindsTimeout, "db-0 running", func() bool {
		return countRunningInstancesForService(ctx, factStore, "db") == 1
	})
	factStore.Put(ctx, types.KeyDesiredServiceImage("db"), []byte("mariadb:11"))
	waitFor(t, rolloutKindsTimeout, "db rollout complete with db-0 started on mariadb:11", func() bool {
		return serviceRolledOutTo(ctx, factStore, "db", "mariadb:11", 1)
	})
}
