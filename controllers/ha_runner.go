package controllers

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// HARunner is a high-availability controller runner that integrates leader
// election with the reconciliation loop. Multiple HARunner instances can run
// on different control-plane nodes — only the elected leader actively
// reconciles. When leadership changes, controllers are started or stopped
// automatically.
//
// Controllers remain stateless: all state lives in the shared fact store.
// Failover is automatic — when a leader dies, its lease expires, a standby
// acquires leadership, and controllers resume from current store state.
type HARunner struct {
	// factStore is the shared state store.
	factStore store.StateStore
	// controllers are the reconciliation controllers to run.
	controllers []Controller
	// election manages leader lease acquisition and renewal.
	election *LeaderElection
	// metrics collects reconciliation performance data.
	metrics *MetricsCollector
	// runner is the active controller runner (nil when not leader).
	runner *Runner
	// cancelRunner stops the active controller runner.
	cancelRunner context.CancelFunc
	mutex        sync.Mutex
}

// NewHARunner creates a high-availability runner for the given controllers.
// The nodeID identifies this control-plane replica. Only the elected leader
// actively runs controllers.
func NewHARunner(factStore store.StateStore, nodeID string, metrics *MetricsCollector, controllerList ...Controller) *HARunner {
	haRunner := &HARunner{
		factStore:   factStore,
		controllers: controllerList,
		metrics:     metrics,
	}

	haRunner.election = NewLeaderElection(factStore, LeaderElectionConfig{
		NodeID:     nodeID,
		OnAcquired: haRunner.startControllers,
		OnLost:     haRunner.stopControllers,
	})

	return haRunner
}

// Run starts the leader election loop and blocks until ctx is cancelled.
// When this node becomes leader, it starts all controllers. When it loses
// leadership (or shuts down), controllers are stopped.
func (haRunner *HARunner) Run(ctx context.Context) error {
	haRunner.mutex.Lock()
	haRunner.mutex.Unlock()

	return haRunner.election.Run(ctx)
}

// IsLeader returns whether this node is the active leader.
func (haRunner *HARunner) IsLeader() bool {
	return haRunner.election.IsLeader()
}

// Metrics returns the metrics collector for this runner.
func (haRunner *HARunner) Metrics() *MetricsCollector {
	return haRunner.metrics
}

// startControllers is called when this node acquires leadership. It creates
// a new Runner and starts all controllers in a background goroutine.
func (haRunner *HARunner) startControllers() {
	haRunner.mutex.Lock()
	defer haRunner.mutex.Unlock()

	if haRunner.cancelRunner != nil {
		return
	}

	controllerCtx, cancelFunc := context.WithCancel(context.Background())
	haRunner.cancelRunner = cancelFunc

	var wrappedControllers []Controller
	for _, controller := range haRunner.controllers {
		wrappedControllers = append(wrappedControllers, &metricsWrappedController{
			inner:   controller,
			metrics: haRunner.metrics,
		})
	}

	haRunner.runner = NewRunner(haRunner.factStore, wrappedControllers...)
	go func() {
		if err := haRunner.runner.Run(controllerCtx); err != nil && controllerCtx.Err() == nil {
			log.Printf("ha-runner: controller error: %v", err)
		}
	}()

	log.Printf("ha-runner: controllers started (leader)")
}

// stopControllers is called when this node loses leadership. It cancels
// the active controller runner.
func (haRunner *HARunner) stopControllers() {
	haRunner.mutex.Lock()
	defer haRunner.mutex.Unlock()

	if haRunner.cancelRunner != nil {
		haRunner.cancelRunner()
		haRunner.cancelRunner = nil
		haRunner.runner = nil
		log.Printf("ha-runner: controllers stopped (lost leadership)")
	}
}

// metricsWrappedController wraps a Controller to record reconciliation metrics.
type metricsWrappedController struct {
	inner   Controller
	metrics *MetricsCollector
}

func (wrapped *metricsWrappedController) Name() string      { return wrapped.inner.Name() }
func (wrapped *metricsWrappedController) Watch() []string    { return wrapped.inner.Watch() }
func (wrapped *metricsWrappedController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	startTime := time.Now()
	changes, err := wrapped.inner.Reconcile(ctx, facts)
	duration := time.Since(startTime)

	if wrapped.metrics != nil {
		wrapped.metrics.RecordReconciliation(wrapped.inner.Name(), duration, len(changes), err)
	}

	return changes, err
}
