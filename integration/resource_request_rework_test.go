// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// legacyMigrationRounds is how many deterministic controller rounds the legacy
// recommendation test runs after the migration.
const legacyMigrationRounds = 5

// reconcileAndApply runs one controller on the facts it watches, as the runner
// would, and writes its proposed puts and deletes to the store.
func reconcileAndApply(ctx context.Context, t *testing.T, factStore store.StateStore, controller controllers.Controller) {
	t.Helper()
	changes, reconcileError := controller.Reconcile(ctx, scanControllerFacts(ctx, t, factStore, controller))
	if reconcileError != nil {
		t.Fatalf("%s reconcile: %v", controller.Name(), reconcileError)
	}
	for _, change := range changes {
		if change.Type == store.OpDelete {
			_ = factStore.Delete(ctx, change.Key)
			continue
		}
		if _, putError := factStore.Put(ctx, change.Key, change.Value); putError != nil {
			t.Fatal(putError)
		}
	}
}

// TestLegacyBareRecommendationAfterMigrationKeepsItsSize: spec
// resource-requests, "Legacy recommendation without a unit" (B1, D9). The
// review's legacy state: intent and effective cpu "750" (millicores, written
// by an older version), metric at the target. After the startup migration, no
// order of the controllers jumps the recommendation to the policy max or makes
// web insufficient_capacity on a 1000m node. The autoscaler runs twice before
// the intent resolver, the order in which it read the bare effective "750" as
// 750 cores before the migration existed.
func TestLegacyBareRecommendationAfterMigrationKeepsItsSize(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 1000, CapacityMemory: 8192, AvailableCPU: 1000, AvailableMemory: 8192,
	})
	if applyError := lang.Apply(ctx, factStore, `
service web {
    image whoami:1.0
    instances 1
    resources {
        cpu 500m
    }
    scale {
        vertical {
            cpu {
                min 250m
                max 4
            }
        }
    }
}
`); applyError != nil {
		t.Fatalf("apply failed: %v", applyError)
	}
	factStore.Put(ctx, types.KeyIntentAutoscalerServiceResourcesCPU("web"), []byte("750"))
	factStore.Put(ctx, types.KeyEffectiveServiceResourcesCPU("web"), []byte("750"))
	factStore.Put(ctx, types.KeyObservedMetric("web", "cpu"), []byte("70"))

	if _, migrationError := controllers.MigrateUnitlessResourceRecommendations(ctx, factStore); migrationError != nil {
		t.Fatal(migrationError)
	}

	placementScheduler := newTestScheduler()
	autoscaleController := controllers.NewAutoscaleController()
	roundOrder := []controllers.Controller{
		controllers.NewInstanceController(), placementScheduler,
		autoscaleController, autoscaleController, controllers.NewIntentResolverController(),
	}
	for round := 0; round < legacyMigrationRounds; round++ {
		for _, controller := range roundOrder {
			reconcileAndApply(ctx, t, factStore, controller)
			if cpuIntent := store.ReadValueOrEmpty(ctx, factStore, types.KeyIntentAutoscalerServiceResourcesCPU("web")); cpuIntent != "750m" {
				t.Fatalf("round %d after %s: intent cpu = %q, want 750m (no jump to the 4-core max)", round, controller.Name(), cpuIntent)
			}
			assertNoInsufficientCapacity(ctx, t, factStore)
		}
	}
	if countPlacementsForService(ctx, factStore, "web") != 1 {
		t.Errorf("web has %d placements, want 1 (750m fits the 1000m node)", countPlacementsForService(ctx, factStore, "web"))
	}
}

// assertNoInsufficientCapacity fails when any instance is unplaced with
// insufficient_capacity.
func assertNoInsufficientCapacity(ctx context.Context, t *testing.T, factStore store.StateStore) {
	t.Helper()
	unplacedFacts, _ := factStore.Scan(ctx, types.ScanDerivedSchedulerUnplaced)
	for _, fact := range unplacedFacts {
		if strings.HasSuffix(fact.Key, "/reason") && string(fact.Value) == string(types.UnplacedInsufficientCapacity) {
			t.Fatalf("%s = insufficient_capacity: the bare effective value was read as cores", fact.Key)
		}
	}
}

// TestSchedulerWritesInvalidResourceRequestShownByStatus: spec
// resource-requests, "Legacy bad value" (T2). The scheduler itself writes
// derived/scheduler/invalid_resource_request/web, and GET /api/status shows it
// on the web service.
func TestSchedulerWritesInvalidResourceRequestShownByStatus(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	registerTestNodes(ctx, factStore, 1)
	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("1"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("web"), []byte("512MB"))
	factStore.Put(ctx, types.KeyObservedInstanceService("web-1"), []byte("web"))
	factStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte(string(types.InstancePending)))

	reconcileAndApply(ctx, t, factStore, newTestScheduler())
	wantValue := types.KeyDesiredServiceResourcesMemory("web") + "=512MB"
	if storedValue := store.ReadValueOrEmpty(ctx, factStore, types.KeyDerivedSchedulerInvalidResourceRequest("web")); storedValue != wantValue {
		t.Fatalf("scheduler wrote %q, want %q", storedValue, wantValue)
	}

	apiServer := api.NewServer(factStore)
	statusAuthorizer := security.NewAPIAuthorizer()
	statusAuthorizer.Grant("user:status-reader", security.CapabilityWorkloadRead, security.ScopeCluster)
	apiServer.SetAPIAuthorizer(statusAuthorizer)
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	readerPrincipal := security.Principal{Kind: security.PrincipalKindUser, Name: "status-reader"}
	request = request.WithContext(security.WithPrincipalStruct(security.WithPrincipal(request.Context(), "user:status-reader"), readerPrincipal))
	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var clusterStatus api.ClusterStatus
	if decodeError := json.Unmarshal(recorder.Body.Bytes(), &clusterStatus); decodeError != nil {
		t.Fatal(decodeError)
	}
	for _, serviceStatus := range clusterStatus.Services {
		if serviceStatus.Name == "web" {
			if serviceStatus.InvalidResourceRequest != wantValue {
				t.Errorf("/api/status web invalid_resource_request = %q, want %q", serviceStatus.InvalidResourceRequest, wantValue)
			}
			return
		}
	}
	t.Errorf("/api/status has no web service: %s", recorder.Body.String())
}
