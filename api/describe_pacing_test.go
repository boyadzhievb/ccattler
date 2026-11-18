// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestDescribeServiceShowsEffectivePacing: design D12. Describe shows the
// effective max_unavailable and max_extra and marks the defaulted ones.
func TestDescribeServiceShowsEffectivePacing(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	serviceFacts := map[string]string{
		types.KeyDesiredService("web"):                     "",
		types.KeyDesiredServiceImage("web"):                "app:a",
		types.KeyDesiredServiceInstances("web"):            "8",
		types.KeyDesiredServiceUpdateMaxUnavailable("web"): "3",
	}
	for factKey, factValue := range serviceFacts {
		if _, putError := factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
			t.Fatal(putError)
		}
	}
	serviceDescribe, describeError := buildServiceDescribe(ctx, factStore, nil, "web")
	if describeError != nil {
		t.Fatal(describeError)
	}
	if serviceDescribe.UpdateStrategy == nil {
		t.Fatal("describe has no update strategy")
	}
	wantStrategy := DescribeUpdateStrategy{MaxUnavailable: "3", MaxExtra: "2", MaxExtraIsDefault: true, WorkloadKind: "stateless"}
	if *serviceDescribe.UpdateStrategy != wantStrategy {
		t.Errorf("update strategy = %+v, want %+v", *serviceDescribe.UpdateStrategy, wantStrategy)
	}
}
