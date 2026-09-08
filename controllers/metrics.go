package controllers

import (
	"sync"
	"sync/atomic"
	"time"
)

// MetricsCollector tracks operational metrics for the control plane:
// reconciliation latency per controller, scheduling decision counts,
// and instance state transitions. All methods are safe for concurrent use.
type MetricsCollector struct {
	// reconciliationMetrics tracks per-controller reconciliation stats.
	reconciliationMetrics map[string]*ReconciliationMetrics
	// schedulingDecisions counts total scheduling placements made.
	schedulingDecisions atomic.Int64
	// instanceTransitions tracks state change counts by transition type.
	instanceTransitions map[string]*atomic.Int64
	mutex               sync.RWMutex
}

// ReconciliationMetrics tracks timing and count statistics for a single
// controller's reconciliation cycles.
type ReconciliationMetrics struct {
	// TotalCycles is the number of reconciliation cycles completed.
	TotalCycles atomic.Int64
	// TotalErrors is the number of cycles that returned an error.
	TotalErrors atomic.Int64
	// TotalChanges is the cumulative number of changes produced.
	TotalChanges atomic.Int64
	// LastDuration is the wall-clock time of the most recent cycle.
	LastDuration atomic.Int64
	// MaxDuration is the longest reconciliation cycle observed.
	MaxDuration atomic.Int64
	// TotalDuration is the sum of all cycle durations (for computing averages).
	TotalDuration atomic.Int64
}

// NewMetricsCollector creates an empty metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		reconciliationMetrics: make(map[string]*ReconciliationMetrics),
		instanceTransitions:  make(map[string]*atomic.Int64),
	}
}

// RecordReconciliation records the result of a single reconciliation cycle
// for the named controller.
func (collector *MetricsCollector) RecordReconciliation(controllerName string, duration time.Duration, changeCount int, err error) {
	metrics := collector.getOrCreateReconciliationMetrics(controllerName)

	durationMicros := duration.Microseconds()
	metrics.TotalCycles.Add(1)
	metrics.TotalChanges.Add(int64(changeCount))
	metrics.LastDuration.Store(durationMicros)
	metrics.TotalDuration.Add(durationMicros)

	for {
		currentMax := metrics.MaxDuration.Load()
		if durationMicros <= currentMax {
			break
		}
		if metrics.MaxDuration.CompareAndSwap(currentMax, durationMicros) {
			break
		}
	}

	if err != nil {
		metrics.TotalErrors.Add(1)
	}
}

// RecordSchedulingDecision increments the scheduling decision counter.
func (collector *MetricsCollector) RecordSchedulingDecision() {
	collector.schedulingDecisions.Add(1)
}

// RecordInstanceTransition records a state change for an instance (e.g.
// "pending→running", "running→stopped", "running→failed").
func (collector *MetricsCollector) RecordInstanceTransition(transition string) {
	counter := collector.getOrCreateTransitionCounter(transition)
	counter.Add(1)
}

// GetReconciliationMetrics returns the metrics for the named controller,
// or nil if no cycles have been recorded.
func (collector *MetricsCollector) GetReconciliationMetrics(controllerName string) *ReconciliationMetrics {
	collector.mutex.RLock()
	defer collector.mutex.RUnlock()
	return collector.reconciliationMetrics[controllerName]
}

// GetSchedulingDecisions returns the total number of scheduling decisions.
func (collector *MetricsCollector) GetSchedulingDecisions() int64 {
	return collector.schedulingDecisions.Load()
}

// GetInstanceTransitions returns the count for a specific transition type.
func (collector *MetricsCollector) GetInstanceTransitions(transition string) int64 {
	collector.mutex.RLock()
	counter, exists := collector.instanceTransitions[transition]
	collector.mutex.RUnlock()
	if !exists {
		return 0
	}
	return counter.Load()
}

// GetAllTransitions returns a snapshot of all instance transition counts.
func (collector *MetricsCollector) GetAllTransitions() map[string]int64 {
	collector.mutex.RLock()
	defer collector.mutex.RUnlock()

	snapshot := make(map[string]int64, len(collector.instanceTransitions))
	for name, counter := range collector.instanceTransitions {
		snapshot[name] = counter.Load()
	}
	return snapshot
}

// GetAllReconciliationStats returns a snapshot of all controller metrics.
func (collector *MetricsCollector) GetAllReconciliationStats() map[string]ReconciliationSnapshot {
	collector.mutex.RLock()
	defer collector.mutex.RUnlock()

	snapshot := make(map[string]ReconciliationSnapshot, len(collector.reconciliationMetrics))
	for name, metrics := range collector.reconciliationMetrics {
		totalCycles := metrics.TotalCycles.Load()
		totalDuration := metrics.TotalDuration.Load()
		avgDuration := int64(0)
		if totalCycles > 0 {
			avgDuration = totalDuration / totalCycles
		}
		snapshot[name] = ReconciliationSnapshot{
			TotalCycles:       totalCycles,
			TotalErrors:       metrics.TotalErrors.Load(),
			TotalChanges:      metrics.TotalChanges.Load(),
			LastDurationMicro: metrics.LastDuration.Load(),
			MaxDurationMicro:  metrics.MaxDuration.Load(),
			AvgDurationMicro:  avgDuration,
		}
	}
	return snapshot
}

// ReconciliationSnapshot is a point-in-time view of a controller's metrics.
type ReconciliationSnapshot struct {
	TotalCycles       int64
	TotalErrors       int64
	TotalChanges      int64
	LastDurationMicro int64
	MaxDurationMicro  int64
	AvgDurationMicro  int64
}

// getOrCreateReconciliationMetrics returns existing metrics or creates new ones.
func (collector *MetricsCollector) getOrCreateReconciliationMetrics(controllerName string) *ReconciliationMetrics {
	collector.mutex.RLock()
	metrics, exists := collector.reconciliationMetrics[controllerName]
	collector.mutex.RUnlock()
	if exists {
		return metrics
	}

	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	if metrics, exists = collector.reconciliationMetrics[controllerName]; exists {
		return metrics
	}
	metrics = &ReconciliationMetrics{}
	collector.reconciliationMetrics[controllerName] = metrics
	return metrics
}

// getOrCreateTransitionCounter returns the counter for a transition type.
func (collector *MetricsCollector) getOrCreateTransitionCounter(transition string) *atomic.Int64 {
	collector.mutex.RLock()
	counter, exists := collector.instanceTransitions[transition]
	collector.mutex.RUnlock()
	if exists {
		return counter
	}

	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	if counter, exists = collector.instanceTransitions[transition]; exists {
		return counter
	}
	counter = &atomic.Int64{}
	collector.instanceTransitions[transition] = counter
	return counter
}
