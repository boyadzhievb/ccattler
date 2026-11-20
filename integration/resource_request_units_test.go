// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// resourceUnitsTestTimeout bounds each wait of the resource-unit tests.
const resourceUnitsTestTimeout = 10 * time.Second

// resourceUnitsResyncInterval makes the runner reconcile without new events,
// so a moved test clock is seen.
const resourceUnitsResyncInterval = 50 * time.Millisecond

// scanControllerFacts returns the facts under every prefix the controller
// watches, as the runner passes them to Reconcile.
func scanControllerFacts(ctx context.Context, t *testing.T, factStore store.StateStore, controller controllers.Controller) []store.Fact {
	t.Helper()
	var watchedFacts []store.Fact
	for _, prefix := range controller.Watch() {
		prefixFacts, scanError := factStore.Scan(ctx, prefix)
		if scanError != nil {
			t.Fatalf("scan %s: %v", prefix, scanError)
		}
		watchedFacts = append(watchedFacts, prefixFacts...)
	}
	store.SortFacts(watchedFacts)
	return watchedFacts
}

// proposesCapacityRequestFor reports whether the cluster-scale controller,
// given the store's current facts, proposes a capacity request for instanceID.
func proposesCapacityRequestFor(ctx context.Context, t *testing.T, factStore store.StateStore, instanceID string) bool {
	t.Helper()
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore), factStore)
	changes, reconcileError := clusterAutoscaleController.Reconcile(ctx, scanControllerFacts(ctx, t, factStore, clusterAutoscaleController))
	if reconcileError != nil {
		t.Fatalf("cluster-scale reconcile: %v", reconcileError)
	}
	for _, change := range changes {
		if change.Key == types.KeyDerivedCapacityRequestState(instanceID) {
			return true
		}
	}
	return false
}

// findPendingInstance returns the ID of a pending instance of serviceName, or
// "" when there is none.
func findPendingInstance(ctx context.Context, factStore store.StateStore, serviceName string) string {
	instances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range instances {
		if instance.Service == serviceName && instance.State == types.InstancePending {
			return instance.ID
		}
	}
	return ""
}

// TestClusterScaleCountsUnitRequestShortage: spec resource-requests, "Capacity
// shortage reaches cluster scaling and the rollout". A running service rolls
// to a configuration requesting 64Gi: the new instance is unplaced with
// insufficient_capacity, cluster scaling asks for a node, the rollout reason
// names it, and after progress_deadline the rollback reason names it too.
func TestClusterScaleCountsUnitRequestShortage(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	registerTestNodes(ctx, factStore, 1)

	var clockOffset atomic.Int64
	rolloutController := controllers.NewRolloutController()
	rolloutController.Now = func() time.Time { return time.Now().Add(time.Duration(clockOffset.Load())) }
	controllerRunner := controllers.NewRunner(factStore,
		controllers.NewInstanceController(), newTestScheduler(),
		controllers.NewIntentResolverController(), rolloutController,
	)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	controllerRunner.SetResyncInterval(resourceUnitsResyncInterval)
	startTestAgents(ctx, factStore, 1)
	startRunner(t, factStore, ctx, cancel, controllerRunner)

	const webConfiguration = `
service web {
    image whoami:1.0
    instances 1
    resources {
        memory 512Mi
    }
    update {
        progress_deadline 2m
        min_healthy_time 0s
    }
}
`
	if applyError := lang.Apply(ctx, factStore, webConfiguration); applyError != nil {
		t.Fatalf("apply failed: %v", applyError)
	}
	waitFor(t, resourceUnitsTestTimeout, "web running", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") == 1
	})

	largerConfiguration := strings.NewReplacer("whoami:1.0", "whoami:2.0", "512Mi", "64Gi").Replace(webConfiguration)
	if applyError := lang.Apply(ctx, factStore, largerConfiguration); applyError != nil {
		t.Fatalf("apply of the 64Gi configuration failed: %v", applyError)
	}

	// Every async fact is checked inside waitFor (CLAUDE.md integration rule):
	// unplaced with insufficient_capacity, no placement, and counted as demand.
	waitFor(t, resourceUnitsTestTimeout, "the 64Gi instance is unplaced with insufficient_capacity, has no placement and is demand for a node", func() bool {
		newInstanceID := findPendingInstance(ctx, factStore, "web")
		if newInstanceID == "" {
			return false
		}
		reasonFact, reasonError := factStore.Get(ctx, types.KeyDerivedSchedulerUnplacedReason(newInstanceID))
		_, placementError := factStore.Get(ctx, types.KeyPlacementInstance(newInstanceID))
		return reasonError == nil && string(reasonFact.Value) == string(types.UnplacedInsufficientCapacity) &&
			placementError != nil && proposesCapacityRequestFor(ctx, t, factStore, newInstanceID)
	})
	waitFor(t, resourceUnitsTestTimeout, "rollout reason names insufficient_capacity", func() bool {
		reasonFact, reasonError := factStore.Get(ctx, types.KeyDerivedServiceRolloutReason("web"))
		return reasonError == nil && strings.Contains(string(reasonFact.Value), string(types.UnplacedInsufficientCapacity))
	})

	clockOffset.Store(int64(3 * time.Minute))
	waitFor(t, resourceUnitsTestTimeout, "rollback reason names insufficient_capacity", func() bool {
		reasonFact, reasonError := factStore.Get(ctx, types.KeyDerivedServiceRolloutRollbackReason("web"))
		return reasonError == nil && strings.Contains(string(reasonFact.Value), string(types.UnplacedInsufficientCapacity))
	})
}

// TestClusterScaleIgnoresInvalidResourceRequest: spec resource-requests, "No
// node asked for". With an unreadable stored request and no alive node, the
// reason is invalid_resource_request (not no_nodes) and cluster scaling writes
// no capacity request.
func TestClusterScaleIgnoresInvalidResourceRequest(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("web"), []byte("512MB"))
	factStore.Put(ctx, types.KeyObservedInstanceService("web-1"), []byte("web"))
	factStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte(string(types.InstancePending)))

	placementScheduler := newTestScheduler()
	schedulerChanges, reconcileError := placementScheduler.Reconcile(ctx, scanControllerFacts(ctx, t, factStore, placementScheduler))
	if reconcileError != nil {
		t.Fatalf("scheduler reconcile: %v", reconcileError)
	}
	for _, change := range schedulerChanges {
		if change.Type == store.OpPut {
			factStore.Put(ctx, change.Key, change.Value)
		}
	}

	reasonFact, reasonError := factStore.Get(ctx, types.KeyDerivedSchedulerUnplacedReason("web-1"))
	if reasonError != nil {
		t.Fatalf("no unplaced reason for web-1: %v", reasonError)
	}
	if string(reasonFact.Value) != string(types.UnplacedInvalidResourceRequest) {
		t.Fatalf("unplaced reason = %q, want %s", reasonFact.Value, types.UnplacedInvalidResourceRequest)
	}
	if proposesCapacityRequestFor(ctx, t, factStore, "web-1") {
		t.Errorf("cluster scaling asked for a node for an unreadable request")
	}
}

// TestRemovingVerticalScalingRestoresDesiredRequest: spec resource-requests,
// "Vertical scaling removed". With no scale/vertical/ facts left, the
// autoscaler deletes its 8Gi recommendation, the intent resolver deletes the
// effective 8Gi, and the scheduler places web with its desired 256Mi on a node
// with 4096 MiB.
func TestRemovingVerticalScalingRestoresDesiredRequest(t *testing.T) {
	t.Parallel()
	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 4096, AvailableCPU: 4000, AvailableMemory: 4096,
	})
	factStore.Put(ctx, types.KeyIntentAutoscalerServiceResourcesMemory("web"), []byte("8Gi"))
	factStore.Put(ctx, types.KeyEffectiveServiceResourcesMemory("web"), []byte("8Gi"))

	controllerRunner := controllers.NewRunner(factStore,
		controllers.NewInstanceController(), newTestScheduler(),
		controllers.NewIntentResolverController(), controllers.NewAutoscaleController(),
	)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	startRunner(t, factStore, ctx, cancel, controllerRunner)
	if applyError := lang.Apply(ctx, factStore, `
service web {
    image whoami:1.0
    instances 1
    resources {
        memory 256Mi
    }
}
`); applyError != nil {
		t.Fatalf("apply failed: %v", applyError)
	}

	waitFor(t, resourceUnitsTestTimeout, "web placed with its desired 256Mi", func() bool {
		instances, _ := types.ListInstances(ctx, factStore)
		for _, instance := range instances {
			if instance.Service != "web" {
				continue
			}
			if _, placementError := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); placementError == nil {
				return true
			}
		}
		return false
	})
	waitFor(t, resourceUnitsTestTimeout, "recommendation and effective memory deleted", func() bool {
		_, intentError := factStore.Get(ctx, types.KeyIntentAutoscalerServiceResourcesMemory("web"))
		_, effectiveError := factStore.Get(ctx, types.KeyEffectiveServiceResourcesMemory("web"))
		return intentError != nil && effectiveError != nil
	})
}
