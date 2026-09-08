package controllers

import (
	"fmt"
	"testing"
	"time"
)

func TestMetricsRecordReconciliation(t *testing.T) {
	collector := NewMetricsCollector()

	collector.RecordReconciliation("instance", 100*time.Microsecond, 3, nil)
	collector.RecordReconciliation("instance", 200*time.Microsecond, 5, nil)

	metrics := collector.GetReconciliationMetrics("instance")
	if metrics == nil {
		t.Fatal("metrics should exist for recorded controller")
	}
	if metrics.TotalCycles.Load() != 2 {
		t.Errorf("cycles = %d, want 2", metrics.TotalCycles.Load())
	}
	if metrics.TotalChanges.Load() != 8 {
		t.Errorf("changes = %d, want 8", metrics.TotalChanges.Load())
	}
	if metrics.TotalErrors.Load() != 0 {
		t.Errorf("errors = %d, want 0", metrics.TotalErrors.Load())
	}
	if metrics.MaxDuration.Load() != 200 {
		t.Errorf("max duration = %d, want 200", metrics.MaxDuration.Load())
	}
}

func TestMetricsRecordReconciliationWithError(t *testing.T) {
	collector := NewMetricsCollector()

	collector.RecordReconciliation("endpoint", 50*time.Microsecond, 0, fmt.Errorf("store unavailable"))

	metrics := collector.GetReconciliationMetrics("endpoint")
	if metrics.TotalErrors.Load() != 1 {
		t.Errorf("errors = %d, want 1", metrics.TotalErrors.Load())
	}
}

func TestMetricsSchedulingDecisions(t *testing.T) {
	collector := NewMetricsCollector()

	collector.RecordSchedulingDecision()
	collector.RecordSchedulingDecision()
	collector.RecordSchedulingDecision()

	if collector.GetSchedulingDecisions() != 3 {
		t.Errorf("decisions = %d, want 3", collector.GetSchedulingDecisions())
	}
}

func TestMetricsInstanceTransitions(t *testing.T) {
	collector := NewMetricsCollector()

	collector.RecordInstanceTransition("pending→running")
	collector.RecordInstanceTransition("pending→running")
	collector.RecordInstanceTransition("running→stopped")
	collector.RecordInstanceTransition("running→failed")

	if collector.GetInstanceTransitions("pending→running") != 2 {
		t.Errorf("pending→running = %d, want 2", collector.GetInstanceTransitions("pending→running"))
	}
	if collector.GetInstanceTransitions("running→stopped") != 1 {
		t.Errorf("running→stopped = %d, want 1", collector.GetInstanceTransitions("running→stopped"))
	}
	if collector.GetInstanceTransitions("nonexistent") != 0 {
		t.Error("unknown transition should return 0")
	}
}

func TestMetricsGetAllTransitions(t *testing.T) {
	collector := NewMetricsCollector()

	collector.RecordInstanceTransition("pending→running")
	collector.RecordInstanceTransition("running→failed")

	all := collector.GetAllTransitions()
	if len(all) != 2 {
		t.Fatalf("expected 2 transition types, got %d", len(all))
	}
}

func TestMetricsGetAllReconciliationStats(t *testing.T) {
	collector := NewMetricsCollector()

	collector.RecordReconciliation("instance", 100*time.Microsecond, 2, nil)
	collector.RecordReconciliation("instance", 300*time.Microsecond, 4, nil)
	collector.RecordReconciliation("endpoint", 50*time.Microsecond, 1, nil)

	stats := collector.GetAllReconciliationStats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 controllers, got %d", len(stats))
	}

	instanceStats := stats["instance"]
	if instanceStats.TotalCycles != 2 {
		t.Errorf("instance cycles = %d, want 2", instanceStats.TotalCycles)
	}
	if instanceStats.AvgDurationMicro != 200 {
		t.Errorf("instance avg = %d, want 200", instanceStats.AvgDurationMicro)
	}
	if instanceStats.MaxDurationMicro != 300 {
		t.Errorf("instance max = %d, want 300", instanceStats.MaxDurationMicro)
	}
}

func TestMetricsUnrecordedController(t *testing.T) {
	collector := NewMetricsCollector()

	if collector.GetReconciliationMetrics("nonexistent") != nil {
		t.Error("unrecorded controller should return nil")
	}
}
