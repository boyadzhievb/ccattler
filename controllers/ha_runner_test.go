// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestHARunnerStartsControllersOnLeadership(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Seed a fact for the controller to reconcile.
	memoryStore.Put(ctx, "effective/service/web/instances", []byte("3"))

	metrics := NewMetricsCollector()

	instanceController := NewInstanceController()
	haRunner := NewHARunner(memoryStore, "cp-1", metrics, noOpLeadershipPrecondition(), instanceController)

	go haRunner.Run(ctx)

	// Wait for leader acquisition and first reconciliation.
	time.Sleep(500 * time.Millisecond)

	if !haRunner.IsLeader() {
		t.Fatal("should be leader")
	}

	// Verify that the controller ran via metrics.
	instanceMetrics := metrics.GetReconciliationMetrics("instance")
	if instanceMetrics == nil {
		t.Fatal("instance controller metrics should exist after reconciliation")
	}
	if instanceMetrics.TotalCycles.Load() < 1 {
		t.Errorf("expected at least 1 reconciliation cycle, got %d", instanceMetrics.TotalCycles.Load())
	}
}

func TestRunnerRecordsMetricsViaCollector(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	metrics := NewMetricsCollector()

	// Create a custom controller that produces one change on first sight.
	testController := NewCustomController("test", []string{"test/"}, func(ctx context.Context, facts *FactMap) ([]Change, error) {
		return []Change{PutChange("test/output/done", "true")}, nil
	})

	testRunner := NewRunner(memoryStore, testController)
	testRunner.SetMetricsCollector(metrics)
	facts := []store.Fact{{Key: "test/item", Value: []byte("value")}}
	changes, err := testRunner.computeReconciliationChanges(ctx, testController, facts, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}

	testMetrics := metrics.GetReconciliationMetrics("test")
	if testMetrics == nil {
		t.Fatal("metrics should be recorded")
	}
	if testMetrics.TotalCycles.Load() != 1 {
		t.Errorf("cycles = %d, want 1", testMetrics.TotalCycles.Load())
	}
	if testMetrics.TotalChanges.Load() != 1 {
		t.Errorf("changes = %d, want 1", testMetrics.TotalChanges.Load())
	}
}

// postCommitProbeController is a minimal controller implementing
// PostCommitController, used to verify the runner still discovers the
// optional interface when the controller is managed by an HARunner.
type postCommitProbeController struct {
	// postCommitCalls counts ExecutePostCommitOperations invocations.
	postCommitCalls atomic.Int64
}

// Name identifies the probe controller in logs and metrics.
func (probeController *postCommitProbeController) Name() string { return "postcommit-probe" }

// Watch declares the single test prefix the probe controller observes.
func (probeController *postCommitProbeController) Watch() []string { return []string{"test/"} }

// Reconcile emits one change until the output fact exists, then goes quiet.
func (probeController *postCommitProbeController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	for _, fact := range facts {
		if fact.Key == "test/output/done" {
			return nil, nil
		}
	}
	return []Change{{Type: store.OpPut, Key: "test/output/done", Value: []byte("true")}}, nil
}

// ExecutePostCommitOperations records that the runner invoked the post-commit
// hook, proving the optional interface survives HARunner management.
func (probeController *postCommitProbeController) ExecutePostCommitOperations(_ context.Context) error {
	probeController.postCommitCalls.Add(1)
	return nil
}

// TestHARunnerPreservesPostCommitController is the regression test for the
// metrics-wrapper bug where HARunner-managed controllers lost their
// PostCommitController identity and post-commit operations never executed.
func TestHARunnerPreservesPostCommitController(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	memoryStore.Put(ctx, "test/input", []byte("value"))

	probeController := &postCommitProbeController{}
	haRunner := NewHARunner(memoryStore, "cp-1", NewMetricsCollector(), noOpLeadershipPrecondition(), probeController)

	go haRunner.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if probeController.postCommitCalls.Load() >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("ExecutePostCommitOperations was never called for an HARunner-managed controller")
}

// TestHARunnerAwaitsShutdown verifies that stopControllers blocks until the
// runner goroutine actually exits, preventing split-brain where the old
// leader's controllers commit transactions after the new leader starts.
func TestHARunnerAwaitsShutdown(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memoryStore.Put(ctx, "effective/service/web/instances", []byte("2"))

	metrics := NewMetricsCollector()
	instanceController := NewInstanceController()
	haRunner := NewHARunner(memoryStore, "cp-1", metrics, noOpLeadershipPrecondition(), instanceController)

	go haRunner.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	if !haRunner.IsLeader() {
		t.Fatal("should be leader before stop")
	}

	// Directly call stopControllers and verify it completes (doesn't hang).
	done := make(chan struct{})
	go func() {
		haRunner.stopControllers()
		close(done)
	}()

	select {
	case <-done:
		// stopControllers returned — runner goroutine was awaited.
	case <-time.After(stopControllersTimeout + 2*time.Second):
		t.Fatal("stopControllers did not return within timeout — runner was not awaited")
	}

	// After stop, runner should be nil.
	haRunner.mutex.Lock()
	runnerIsNil := haRunner.runner == nil
	haRunner.mutex.Unlock()
	if !runnerIsNil {
		t.Error("runner should be nil after stopControllers")
	}
}

func TestHARunnerFailoverTransfersControllers(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(context.Background(), "effective/service/web/instances", []byte("2"))

	metrics1 := NewMetricsCollector()
	metrics2 := NewMetricsCollector()

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	haRunner1 := NewHARunner(memoryStore, "cp-1", metrics1, noOpLeadershipPrecondition(), NewInstanceController())
	haRunner2 := NewHARunner(memoryStore, "cp-2", metrics2, noOpLeadershipPrecondition(), NewInstanceController())

	// Override lease durations for fast failover.
	haRunner1.election.leaseDuration = 300 * time.Millisecond
	haRunner1.election.renewInterval = 50 * time.Millisecond
	haRunner2.election.leaseDuration = 300 * time.Millisecond
	haRunner2.election.renewInterval = 50 * time.Millisecond

	go haRunner1.Run(ctx1)
	time.Sleep(200 * time.Millisecond)

	go haRunner2.Run(ctx2)
	time.Sleep(200 * time.Millisecond)

	if !haRunner1.IsLeader() {
		t.Fatal("cp-1 should be leader initially")
	}
	if haRunner2.IsLeader() {
		t.Fatal("cp-2 should not be leader initially")
	}

	// Kill cp-1.
	cancel1()
	time.Sleep(800 * time.Millisecond)

	if !haRunner2.IsLeader() {
		t.Fatal("cp-2 should be leader after failover")
	}

	// Verify cp-2's controllers ran.
	instanceMetrics := metrics2.GetReconciliationMetrics("instance")
	if instanceMetrics == nil {
		t.Fatal("cp-2 should have reconciliation metrics after failover")
	}
	if instanceMetrics.TotalCycles.Load() < 1 {
		t.Errorf("cp-2 expected at least 1 cycle, got %d", instanceMetrics.TotalCycles.Load())
	}
}

// noOpLeadershipPrecondition is the explicit "nothing to wait for"
// precondition of HA tests that do not exercise the migration (R5-I1).
func noOpLeadershipPrecondition() ControllerPrecondition {
	return ControllerPrecondition{Name: "no-op test precondition", Check: func(context.Context) error { return nil }}
}

// TestNewHARunnerRefusesNilLeadershipPrecondition: review R5-I1. The
// leadership precondition is required; a missing one is a wiring error and
// panics at construction, so a runner can never skip the migration silently.
func TestNewHARunnerRefusesNilLeadershipPrecondition(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer func() { _ = memoryStore.Close() }()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Errorf("NewHARunner accepted a precondition with a nil Check, want a panic")
		}
	}()
	NewHARunner(memoryStore, "cp-1", NewMetricsCollector(), ControllerPrecondition{Name: "missing"})
}

// firstReconcileValueRecorder returns a controller watching the autoscaler
// intents that sends, on its first reconcile only, the value of key it sees.
func firstReconcileValueRecorder(factKey string, firstValue chan<- string) Controller {
	var recordOnce sync.Once
	return NewCustomController("first-value-recorder", []string{types.ScanIntentAutoscalerServices},
		func(_ context.Context, facts *FactMap) ([]Change, error) {
			recordOnce.Do(func() {
				seenValue := "<absent>"
				for _, fact := range facts.Raw() {
					if fact.Key == factKey {
						seenValue = string(fact.Value)
					}
				}
				firstValue <- seenValue
			})
			return nil, nil
		})
}

// TestHARunnerMigratesOnLeadershipAcquisition: spec resource-requests,
// "Leader change in a rolling upgrade" (D9, review finding 3). A bare value an
// old leader wrote after this server started is migrated when this server
// becomes leader, before its first controller reconcile.
func TestHARunnerMigratesOnLeadershipAcquisition(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer func() { _ = memoryStore.Close() }()
	intentKey := types.KeyIntentAutoscalerServiceResourcesCPU("web")
	putFacts(t, memoryStore, map[string]string{intentKey: "750"}) // written by the old leader

	firstValue := make(chan string, 1)
	haRunner := NewHARunner(memoryStore, "cp-new", NewMetricsCollector(), ResourceMigrationPrecondition(memoryStore), // as cmd/cca wires it
		firstReconcileValueRecorder(intentKey, firstValue))
	haRunner.startControllers() // the OnAcquired callback
	defer haRunner.stopControllers()

	select {
	case seenValue := <-firstValue:
		if seenValue != "750m" {
			t.Errorf("first reconcile saw %s = %q, want 750m (migrated before the controllers run)", intentKey, seenValue)
		}
	case <-time.After(migrationTestTimeout):
		t.Fatal("controllers never reconciled after acquiring leadership")
	}
}

// TestHARunnerLeadershipLossCancelsMigrationRetry: design D9. While the
// migration keeps failing the controllers do not start, and losing
// leadership ends the retry promptly instead of blocking the election.
func TestHARunnerLeadershipLossCancelsMigrationRetry(t *testing.T) {
	limitedStore := &limitedTransactionStore{StateStore: store.NewMemoryStore(), failingTransactions: 1 << 30}
	defer func() { _ = limitedStore.Close() }()
	putLegacyServices(t, limitedStore, 1)

	firstValue := make(chan string, 1)
	haRunner := NewHARunner(limitedStore, "cp-new", NewMetricsCollector(), ResourceMigrationPrecondition(limitedStore), // as cmd/cca wires it
		firstReconcileValueRecorder(types.KeyIntentAutoscalerServiceResourcesCPU("svc00"), firstValue))
	haRunner.preconditionBackoff = preconditionRetryBackoff{initialDelay: time.Millisecond, maximumDelay: 2 * time.Millisecond}

	startReturned := make(chan struct{})
	go func() {
		haRunner.startControllers()
		close(startReturned)
	}()
	select {
	case <-startReturned:
	case <-time.After(migrationTestTimeout):
		t.Fatal("startControllers blocked on the failing migration; it must not block the election loop")
	}

	stopReturned := make(chan struct{})
	go func() {
		haRunner.stopControllers()
		close(stopReturned)
	}()
	select {
	case <-stopReturned:
	case <-time.After(stopControllersTimeout / 2):
		t.Fatal("stopControllers did not cancel the migration retry promptly")
	}
	select {
	case seenValue := <-firstValue:
		t.Errorf("controllers ran (saw %q) although the migration never succeeded", seenValue)
	default:
	}
}
