package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestStatefulScaleUpFromZeroCreatesOrdinalZero verifies that a stateful
// service with desired=3 and no existing instances creates only ordinal 0
// (ordered startup: one at a time, lowest first).
func TestStatefulScaleUpFromZeroCreatesOrdinalZero(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "3"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	// Only one instance created (ordinal 0): marker + service + state + ordinal = 4 keys
	if len(changes) != 4 {
		t.Fatalf("expected 4 changes (1 ordinal instance), got %d", len(changes))
	}
	assertChangeKey(t, changes[0], types.KeyObservedInstance("postgres-0"))
	assertChangeKey(t, changes[1], types.KeyObservedInstanceService("postgres-0"))
	assertChangeValue(t, changes[1], "postgres")
	assertChangeKey(t, changes[2], types.KeyObservedInstanceState("postgres-0"))
	assertChangeValue(t, changes[2], "pending")
	assertChangeKey(t, changes[3], types.KeyObservedInstanceOrdinal("postgres-0"))
	assertChangeValue(t, changes[3], "0")
}

// TestStatefulOrderedStartupWaitsForRunning verifies that ordinal 1 is not
// created until ordinal 0 is running.
func TestStatefulOrderedStartupWaitsForRunning(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "3"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "pending"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes (ordinal 0 still pending), got %d", len(changes))
	}
}

// TestStatefulOrderedStartupCreatesNextOrdinal verifies that once ordinal 0
// is running, ordinal 1 is created.
func TestStatefulOrderedStartupCreatesNextOrdinal(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "3"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 4 {
		t.Fatalf("expected 4 changes (create ordinal 1), got %d", len(changes))
	}
	assertChangeKey(t, changes[0], types.KeyObservedInstance("postgres-1"))
	assertChangeKey(t, changes[1], types.KeyObservedInstanceService("postgres-1"))
	assertChangeValue(t, changes[1], "postgres")
	assertChangeKey(t, changes[2], types.KeyObservedInstanceState("postgres-1"))
	assertChangeValue(t, changes[2], "pending")
	assertChangeKey(t, changes[3], types.KeyObservedInstanceOrdinal("postgres-1"))
	assertChangeValue(t, changes[3], "1")
}

// TestStatefulAllRunningNoChanges verifies that when all desired ordinals are
// running, no changes are emitted.
func TestStatefulAllRunningNoChanges(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "3"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
		kv(types.KeyObservedInstanceService("postgres-1"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-1"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-1"), "1"),
		kv(types.KeyObservedInstanceService("postgres-2"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-2"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-2"), "2"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes when all ordinals satisfied, got %d", len(changes))
	}
}

// TestStatefulScaleDownRemovesHighestOrdinal verifies that scale-down from 3
// to 1 stops ordinals 2 and 1 (highest first).
func TestStatefulScaleDownRemovesHighestOrdinal(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "1"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
		kv(types.KeyObservedInstanceService("postgres-1"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-1"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-1"), "1"),
		kv(types.KeyObservedInstanceService("postgres-2"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-2"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-2"), "2"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (stop ordinals 2 and 1), got %d", len(changes))
	}
	// Highest ordinal stopped first
	assertChangeKey(t, changes[0], types.KeyObservedInstanceState("postgres-2"))
	assertChangeValue(t, changes[0], "stopped")
	assertChangeKey(t, changes[1], types.KeyObservedInstanceState("postgres-1"))
	assertChangeValue(t, changes[1], "stopped")
}

// TestStatefulScaleToZero verifies that scaling a stateful service to 0
// stops all ordinals.
func TestStatefulScaleToZero(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "0"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
		kv(types.KeyObservedInstanceService("postgres-1"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-1"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-1"), "1"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (stop all ordinals), got %d", len(changes))
	}
	for _, change := range changes {
		assertChangeValue(t, change, "stopped")
	}
}

// TestStatefulStoppedOrdinalsNotCounted verifies that stopped stateful
// instances are not counted toward the active instance count.
func TestStatefulStoppedOrdinalsNotCounted(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "2"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyObservedInstanceService("postgres-0"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-0"), "running"),
		kv(types.KeyObservedInstanceOrdinal("postgres-0"), "0"),
		kv(types.KeyObservedInstanceService("postgres-1"), "postgres"),
		kv(types.KeyObservedInstanceState("postgres-1"), "stopped"),
		kv(types.KeyObservedInstanceOrdinal("postgres-1"), "1"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	// postgres-1 is stopped, so only postgres-0 is active → need to create postgres-1
	if len(changes) != 4 {
		t.Fatalf("expected 4 changes (create ordinal 1), got %d", len(changes))
	}
	assertChangeKey(t, changes[0], types.KeyObservedInstance("postgres-1"))
}

// TestMixedStatefulAndStatelessServices verifies that stateful and stateless
// services can coexist in the same reconciliation cycle.
func TestMixedStatefulAndStatelessServices(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("postgres"), "2"),
		kv(types.KeyEffectiveServiceStateful("postgres"), "true"),
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
	)

	changes, reconcileErr := instanceController.Reconcile(context.Background(), facts)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}

	// postgres: ordinal 0 only (4 keys), web: 2 instances × 3 keys = 6
	// Total = 10
	if len(changes) != 10 {
		t.Fatalf("expected 10 changes, got %d", len(changes))
	}

	// Verify postgres gets ordinal ID
	foundPostgresOrdinal := false
	foundWebRandom := false
	for _, change := range changes {
		if change.Key == types.KeyObservedInstance("postgres-0") {
			foundPostgresOrdinal = true
		}
		if change.Key == types.KeyObservedInstance("inst-001") {
			foundWebRandom = true
		}
	}
	if !foundPostgresOrdinal {
		t.Error("expected ordinal instance postgres-0 for stateful service")
	}
	if !foundWebRandom {
		t.Error("expected random ID instance for stateless service")
	}
}

// assertChangeKey is a test helper that verifies a change has the expected key.
func assertChangeKey(t *testing.T, change Change, expectedKey string) {
	t.Helper()
	if change.Key != expectedKey {
		t.Errorf("change key: got %q, want %q", change.Key, expectedKey)
	}
}

// assertChangeValue is a test helper that verifies a change has the expected
// string value.
func assertChangeValue(t *testing.T, change Change, expectedValue string) {
	t.Helper()
	if string(change.Value) != expectedValue {
		t.Errorf("change value: got %q, want %q", string(change.Value), expectedValue)
	}
}
