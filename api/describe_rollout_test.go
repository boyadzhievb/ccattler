// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestDescribeServiceShowsRolloutReason: spec service-rollout, "Shown to the
// user": describe carries the state, failures, reason and rollback reason.
func TestDescribeServiceShowsRolloutReason(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { _ = factStore.Close() })
	serviceFacts := map[string]string{
		types.KeyDesiredService("web"):                      "",
		types.KeyDesiredServiceImage("web"):                 "app:a",
		types.KeyDesiredServiceInstances("web"):             "1",
		types.KeyDerivedServiceRolloutState("web"):          string(types.RolloutStalled),
		types.KeyDerivedServiceRolloutFailures("web"):       "2",
		types.KeyDerivedServiceRolloutReason("web"):         "waiting for web-3 (pending: unplaced: insufficient_capacity)",
		types.KeyDerivedServiceRolloutRollbackReason("web"): "3 failures ≥ 3 on app:b: exited with code 1",
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
	if serviceDescribe.Rollout == nil {
		t.Fatal("describe has no rollout section")
	}
	wantRollout := DescribeRollout{
		State:          string(types.RolloutStalled),
		Failures:       "2",
		Reason:         serviceFacts[types.KeyDerivedServiceRolloutReason("web")],
		RollbackReason: serviceFacts[types.KeyDerivedServiceRolloutRollbackReason("web")],
	}
	if *serviceDescribe.Rollout != wantRollout {
		t.Errorf("rollout = %+v, want %+v", *serviceDescribe.Rollout, wantRollout)
	}
}
