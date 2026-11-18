// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestDescribeServiceShowsKindPacing: design D14 and review round 2 L5. A
// stateful service with an explicit max_extra shows it ignored, and a forced
// max_unavailable 1 is marked.
func TestDescribeServiceShowsKindPacing(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	serviceFacts := map[string]string{
		types.KeyDesiredService("db"):                     "",
		types.KeyDesiredServiceImage("db"):                "postgres:17",
		types.KeyDesiredServiceInstances("db"):            "3",
		types.KeyEffectiveServiceStateful("db"):           "true",
		types.KeyDesiredServiceUpdateMaxUnavailable("db"): "0",
		types.KeyDesiredServiceUpdateMaxExtra("db"):       "2",
	}
	for factKey, factValue := range serviceFacts {
		if _, putError := factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
			t.Fatal(putError)
		}
	}
	serviceDescribe, describeError := buildServiceDescribe(ctx, factStore, nil, "db")
	if describeError != nil {
		t.Fatal(describeError)
	}
	wantStrategy := DescribeUpdateStrategy{MaxUnavailable: "1", MaxExtra: "0", MaxUnavailableForced: true, WorkloadKind: "stateful", MaxExtraIgnored: true}
	if serviceDescribe.UpdateStrategy == nil || *serviceDescribe.UpdateStrategy != wantStrategy {
		t.Errorf("update strategy = %+v, want %+v", serviceDescribe.UpdateStrategy, wantStrategy)
	}
}

// TestDescribeReadsEffectivePlacementModeLikeTheControllers: review round 3
// (2e.4). The kind describe shows comes from the effective placement mode, the
// fact the rollout and instance controllers read; a desired mode the intent
// controller has not made effective yet does not change it.
func TestDescribeReadsEffectivePlacementModeLikeTheControllers(t *testing.T) {
	testCases := []struct {
		name      string            // name describes the case.
		modeFacts map[string]string // modeFacts are the placement mode facts.
		wantKind  string            // wantKind is the kind describe shows.
	}{
		{name: "effective per-node", wantKind: string(types.WorkloadPerNode),
			modeFacts: map[string]string{types.KeyEffectiveServicePlacementMode("agentd"): string(types.PlacementModePerNode)}},
		{name: "desired per-node not effective yet", wantKind: string(types.WorkloadStateless),
			modeFacts: map[string]string{types.KeyDesiredServicePlacementMode("agentd"): string(types.PlacementModePerNode)}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			factStore := store.NewMemoryStore()
			t.Cleanup(func() { _ = factStore.Close() })
			serviceFacts := map[string]string{
				types.KeyDesiredService("agentd"):            "",
				types.KeyDesiredServiceImage("agentd"):       "monitor:1.0",
				types.KeyEffectiveServiceInstances("agentd"): "3",
			}
			for factKey, factValue := range testCase.modeFacts {
				serviceFacts[factKey] = factValue
			}
			for factKey, factValue := range serviceFacts {
				if _, putError := factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
					t.Fatal(putError)
				}
			}
			serviceDescribe, describeError := buildServiceDescribe(ctx, factStore, nil, "agentd")
			if describeError != nil {
				t.Fatal(describeError)
			}
			if serviceDescribe.UpdateStrategy == nil || serviceDescribe.UpdateStrategy.WorkloadKind != testCase.wantKind {
				t.Errorf("update strategy = %+v, want kind %s", serviceDescribe.UpdateStrategy, testCase.wantKind)
			}
		})
	}
}

// TestDescribePerNodeServiceUsesExpectedCount: review round 4 (M241 2g.2). A
// per-node service has no desired instance count: describe shows the
// effective count, and the disruption budget's min_available is measured
// against it.
func TestDescribePerNodeServiceUsesExpectedCount(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	serviceFacts := map[string]string{
		types.KeyDesiredService("agentd"):                       "",
		types.KeyDesiredServiceImage("agentd"):                  "monitor:1.0",
		types.KeyDesiredServicePlacementMode("agentd"):          string(types.PlacementModePerNode),
		types.KeyEffectiveServicePlacementMode("agentd"):        string(types.PlacementModePerNode),
		types.KeyEffectiveServiceInstances("agentd"):            "3",
		types.KeyDesiredServiceUpdateMaxUnavailable("agentd"):   "2",
		types.KeyDesiredServiceDisruptionMinAvailable("agentd"): "2",
	}
	for factKey, factValue := range serviceFacts {
		if _, putError := factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
			t.Fatal(putError)
		}
	}
	serviceDescribe, describeError := buildServiceDescribe(ctx, factStore, nil, "agentd")
	if describeError != nil {
		t.Fatal(describeError)
	}
	if serviceDescribe.DesiredInstances != 3 {
		t.Errorf("desired instances = %d, want the effective count 3", serviceDescribe.DesiredInstances)
	}
	if serviceDescribe.UpdateStrategy == nil || serviceDescribe.UpdateStrategy.MaxUnavailable != "1" {
		t.Errorf("update strategy = %+v, want max_unavailable 1 (3 expected, min_available 2)", serviceDescribe.UpdateStrategy)
	}
}
