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
