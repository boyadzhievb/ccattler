package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestInitControllerName verifies the controller returns its expected name.
func TestInitControllerName(t *testing.T) {
	initController := NewInitController()
	if initController.Name() != "init" {
		t.Fatalf("expected name 'init', got %q", initController.Name())
	}
}

// TestInitControllerNoInitSteps verifies that the controller produces no changes
// when a service has no init steps defined.
func TestInitControllerNoInitSteps(t *testing.T) {
	testContext := context.Background()
	initController := NewInitController()

	facts := []store.Fact{
		{Key: types.KeyDesiredService("web"), Value: []byte("")},
		{Key: types.KeyObservedInstanceService("abc"), Value: []byte("web")},
		{Key: types.KeyObservedInstanceState("abc"), Value: []byte("running")},
	}

	changes, reconcileError := initController.Reconcile(testContext, facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes for service without init steps, got %d", len(changes))
	}
}

// TestInitControllerAllStepsSucceeded verifies the controller sets the init
// phase to "complete" when all init steps have succeeded.
func TestInitControllerAllStepsSucceeded(t *testing.T) {
	testContext := context.Background()
	initController := NewInitController()

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceInitStep("api", 0), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 0), Value: []byte("migrate-db")},
		{Key: types.KeyDesiredServiceInitStep("api", 1), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 1), Value: []byte("generate-config")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("api")},
		{Key: types.KeyObservedInstanceInitStepState("inst-1", 0), Value: []byte("succeeded")},
		{Key: types.KeyObservedInstanceInitStepState("inst-1", 1), Value: []byte("succeeded")},
	}

	changes, reconcileError := initController.Reconcile(testContext, facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "complete" {
		t.Fatalf("expected init phase 'complete', got %q", string(changes[0].Value))
	}
	if changes[0].Key != types.KeyObservedInstanceInitPhase("inst-1") {
		t.Fatalf("unexpected key: %s", changes[0].Key)
	}
}

// TestInitControllerStepFailed verifies the controller sets the init phase
// to "failed" when any step fails.
func TestInitControllerStepFailed(t *testing.T) {
	testContext := context.Background()
	initController := NewInitController()

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceInitStep("api", 0), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 0), Value: []byte("migrate-db")},
		{Key: types.KeyDesiredServiceInitStep("api", 1), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 1), Value: []byte("generate-config")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("api")},
		{Key: types.KeyObservedInstanceInitStepState("inst-1", 0), Value: []byte("succeeded")},
		{Key: types.KeyObservedInstanceInitStepState("inst-1", 1), Value: []byte("failed")},
	}

	changes, reconcileError := initController.Reconcile(testContext, facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "failed" {
		t.Fatalf("expected init phase 'failed', got %q", string(changes[0].Value))
	}
}

// TestInitControllerStepRunning verifies the controller sets the init phase
// to "running" when a step is in progress.
func TestInitControllerStepRunning(t *testing.T) {
	testContext := context.Background()
	initController := NewInitController()

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceInitStep("api", 0), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 0), Value: []byte("migrate-db")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("api")},
		{Key: types.KeyObservedInstanceInitStepState("inst-1", 0), Value: []byte("running")},
	}

	changes, reconcileError := initController.Reconcile(testContext, facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "running" {
		t.Fatalf("expected init phase 'running', got %q", string(changes[0].Value))
	}
}

// TestInitControllerNoChangeWhenPhaseAlreadyCurrent verifies the controller
// produces no changes when the stored phase matches the derived phase.
func TestInitControllerNoChangeWhenPhaseAlreadyCurrent(t *testing.T) {
	testContext := context.Background()
	initController := NewInitController()

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceInitStep("api", 0), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 0), Value: []byte("migrate-db")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("api")},
		{Key: types.KeyObservedInstanceInitStepState("inst-1", 0), Value: []byte("succeeded")},
		{Key: types.KeyObservedInstanceInitPhase("inst-1"), Value: []byte("complete")},
	}

	changes, reconcileError := initController.Reconcile(testContext, facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes when phase is already current, got %d", len(changes))
	}
}

// TestInitControllerPendingWhenNoObservations verifies the controller derives
// "pending" when init steps exist but no observations have been recorded.
func TestInitControllerPendingWhenNoObservations(t *testing.T) {
	testContext := context.Background()
	initController := NewInitController()

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceInitStep("api", 0), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 0), Value: []byte("migrate-db")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("api")},
	}

	changes, reconcileError := initController.Reconcile(testContext, facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if string(changes[0].Value) != "pending" {
		t.Fatalf("expected init phase 'pending', got %q", string(changes[0].Value))
	}
}

// TestInitControllerMultipleInstances verifies the controller handles multiple
// instances of the same service independently.
func TestInitControllerMultipleInstances(t *testing.T) {
	testContext := context.Background()
	initController := NewInitController()

	facts := []store.Fact{
		{Key: types.KeyDesiredServiceInitStep("api", 0), Value: []byte("")},
		{Key: types.KeyDesiredServiceInitStepExec("api", 0), Value: []byte("migrate-db")},
		{Key: types.KeyObservedInstanceService("inst-1"), Value: []byte("api")},
		{Key: types.KeyObservedInstanceService("inst-2"), Value: []byte("api")},
		{Key: types.KeyObservedInstanceInitStepState("inst-1", 0), Value: []byte("succeeded")},
		{Key: types.KeyObservedInstanceInitStepState("inst-2", 0), Value: []byte("failed")},
	}

	changes, reconcileError := initController.Reconcile(testContext, facts)
	if reconcileError != nil {
		t.Fatalf("unexpected error: %v", reconcileError)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (one per instance), got %d", len(changes))
	}

	phaseByInstance := make(map[string]string)
	for _, change := range changes {
		for _, instanceID := range []string{"inst-1", "inst-2"} {
			if change.Key == types.KeyObservedInstanceInitPhase(instanceID) {
				phaseByInstance[instanceID] = string(change.Value)
			}
		}
	}

	if phaseByInstance["inst-1"] != "complete" {
		t.Fatalf("expected inst-1 phase 'complete', got %q", phaseByInstance["inst-1"])
	}
	if phaseByInstance["inst-2"] != "failed" {
		t.Fatalf("expected inst-2 phase 'failed', got %q", phaseByInstance["inst-2"])
	}
}
