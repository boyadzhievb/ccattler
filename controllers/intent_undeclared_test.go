// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestIntentResolverStopsUndeclaredServices: spec service-scaling, "Only
// declared services keep instances". A name with a user intent but no
// desired/service/{name} declaration gets effective count 0, whatever its
// intents say; a declared service keeps its count.
func TestIntentResolverStopsUndeclaredServices(t *testing.T) {
	intentResolver := NewIntentResolverController()
	facts := buildFacts(
		kv(types.KeyDesiredService("web"), ""),
		kv(types.KeyIntentUserServiceInstances("web"), "3"),
		kv(types.KeyIntentUserServiceInstances("nosuch"), "2"),
		kv(types.KeyIntentAutoscalerServiceInstances("nosuch"), "4"),
		kv(types.KeyEffectiveServiceInstances("nosuch"), "2"),
	)
	changes, reconcileError := intentResolver.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	changedValues := make(map[string]string)
	for _, change := range changes {
		changedValues[change.Key] = string(change.Value)
	}

	if undeclaredCount, changed := changedValues[types.KeyEffectiveServiceInstances("nosuch")]; !changed || undeclaredCount != "0" {
		t.Errorf("undeclared nosuch: effective instances change %q (changed=%v), want \"0\"", undeclaredCount, changed)
	}
	if declaredCount := changedValues[types.KeyEffectiveServiceInstances("web")]; declaredCount != "3" {
		t.Errorf("declared web: effective instances %q, want \"3\"", declaredCount)
	}
}
