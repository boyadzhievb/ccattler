package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

func TestHARunnerStartsControllersOnLeadership(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Seed a fact for the controller to reconcile.
	memoryStore.Put(ctx, "/ccattler/effective/service/web/instances", []byte("3"))

	metrics := NewMetricsCollector()

	instanceController := NewInstanceController()
	haRunner := NewHARunner(memoryStore, "cp-1", metrics, instanceController)

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

func TestHARunnerMetricsWrapping(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	metrics := NewMetricsCollector()

	// Create a custom controller that produces changes.
	testController := NewCustomController("test", []string{"/ccattler/test/"}, func(ctx context.Context, facts *FactMap) ([]Change, error) {
		return []Change{PutChange("/ccattler/output/done", "true")}, nil
	})

	// Wrap and reconcile directly.
	wrapped := &metricsWrappedController{inner: testController, metrics: metrics}
	facts := []store.Fact{{Key: "/ccattler/test/item", Value: []byte("value")}}
	changes, err := wrapped.Reconcile(ctx, facts)
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

func TestHARunnerFailoverTransfersControllers(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(context.Background(), "/ccattler/effective/service/web/instances", []byte("2"))

	metrics1 := NewMetricsCollector()
	metrics2 := NewMetricsCollector()

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	haRunner1 := NewHARunner(memoryStore, "cp-1", metrics1, NewInstanceController())
	haRunner2 := NewHARunner(memoryStore, "cp-2", metrics2, NewInstanceController())

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
