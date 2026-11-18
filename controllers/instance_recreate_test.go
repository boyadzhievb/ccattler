// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"slices"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestInstanceControllerClearsStaleIncarnationFactsOnRecreate: review B4 and
// D13. A recreated stateful ordinal drops the probe results, created-at,
// started_at and last_exit of its previous incarnation in the creation group.
func TestInstanceControllerClearsStaleIncarnationFactsOnRecreate(t *testing.T) {
	inputFacts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("db"), "1"),
		kv(types.KeyEffectiveServiceStateful("db"), "true"),
		kv(types.KeyDesiredServiceImage("db"), "postgres:17"),
		kv(types.KeyObservedInstance("db-0"), ""),
		kv(types.KeyObservedInstanceService("db-0"), "db"),
		kv(types.KeyObservedInstanceState("db-0"), string(types.InstanceStopped)),
		kv(types.KeyObservedInstanceOrdinal("db-0"), "0"),
		kv(types.KeyObservedInstanceProbeState("db-0", types.ProbeReadiness), string(types.ReadinessProbeReady)),
		kv(types.KeyObservedInstanceProbeState("db-0", types.ProbeLiveness), string(types.LivenessProbeHealthy)),
		kv(types.KeyObservedInstanceCreatedAt("db-0"), "2026-11-01T10:00:00Z"),
		kv(types.KeyObservedInstanceStartedAt("db-0"), "2026-11-01T10:00:05Z"),
		kv(types.KeyObservedInstanceLastExit("db-0"), "exited with code 1"),
	)
	groupChanges := creationChangesOf(reconcileInstanceTestFacts(t, inputFacts), "db-0")
	if len(groupChanges) == 0 {
		t.Fatal("no creation group for db-0")
	}
	for _, staleKey := range []string{
		types.KeyObservedInstanceProbeState("db-0", types.ProbeReadiness),
		types.KeyObservedInstanceProbeState("db-0", types.ProbeLiveness),
		types.KeyObservedInstanceCreatedAt("db-0"),
		types.KeyObservedInstanceStartedAt("db-0"),
		types.KeyObservedInstanceLastExit("db-0"),
	} {
		if staleChange, hasChange := groupChanges[staleKey]; !hasChange || staleChange.Type != store.OpDelete {
			t.Errorf("creation group of db-0 does not delete %s", staleKey)
		}
	}
}

// TestFailureControllerReplacementRecordsImage: review B1. The failure
// controller's replacement records the service's desired image in the same
// group, as the instance controller's creations do (M241 D1).
func TestFailureControllerReplacementRecordsImage(t *testing.T) {
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()
	if !slices.Contains(failureController.Watch(), types.ScanDesiredServices) {
		t.Error("failure controller does not watch desired/service/ for the image it records")
	}
	inputFacts := buildFacts(
		kv(types.KeyDesiredServiceImage("web"), "app:b"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), string(types.InstanceFailed)),
	)
	proposedChanges, reconcileError := failureController.Reconcile(context.Background(), inputFacts)
	if reconcileError != nil {
		t.Fatal(reconcileError)
	}
	var replacementID string
	for _, change := range proposedChanges {
		if change.Group == "replace/aaa" && change.Key != types.KeyDerivedInstanceControllerStopped("aaa") && replacementID == "" {
			replacementID = instanceIDFromObservedKey(change.Key)
		}
	}
	if replacementID == "" {
		t.Fatal("no replacement created for aaa")
	}
	for _, change := range proposedChanges {
		if change.Key == types.KeyDerivedInstanceImage(replacementID) {
			if change.Group != "replace/aaa" || string(change.Value) != "app:b" {
				t.Errorf("replacement image change = %+v, want app:b in group replace/aaa", change)
			}
			return
		}
	}
	t.Errorf("replacement %s has no recorded image", replacementID)
}

// instanceIDFromObservedKey returns the instance ID of an observed/instance/{id}... key.
func instanceIDFromObservedKey(factKey string) string {
	relativePath := factKey[len(types.ScanObservedInstances):]
	for index, character := range relativePath {
		if character == '/' {
			return relativePath[:index]
		}
	}
	return relativePath
}
