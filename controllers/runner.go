package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/metrics"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

var (
	reconciliationDuration = metrics.DefaultRegistry.RegisterHistogram(
		"ccattler_reconciliation_duration_seconds",
		"Time spent in a single reconciliation cycle",
		metrics.DurationBuckets(),
		"controller",
	)
	reconciliationTotal = metrics.DefaultRegistry.RegisterCounter(
		"ccattler_reconciliation_total",
		"Total number of reconciliation cycles",
		"controller", "result",
	)
	reconciliationConflicts = metrics.DefaultRegistry.RegisterCounter(
		"ccattler_reconciliation_conflicts_total",
		"Total number of optimistic concurrency conflicts",
		"controller",
	)
	reconciliationChanges = metrics.DefaultRegistry.RegisterCounter(
		"ccattler_reconciliation_changes_total",
		"Total number of fact changes committed by reconciliation",
		"controller",
	)
)

// Runner manages the lifecycle of a set of controllers. It starts each
// controller in its own goroutine, sets up watches on the fact store, and
// drives the reconciliation loop with debouncing to avoid thrashing when
// many facts change in quick succession.
type Runner struct {
	// store is the backing fact store used for reads, writes, and watches.
	store store.StateStore
	// controllers is the ordered list of controllers managed by this runner.
	controllers []Controller
	// debounce is the minimum quiet period between consecutive
	// reconciliation cycles for a single controller. This prevents
	// rapid-fire reconciliations when a burst of store events arrives.
	debounce time.Duration
	// resyncInterval is the period between forced full reconciliation cycles,
	// independent of watch events. This catches missed events, watch gaps,
	// and stale controller state. Zero disables periodic resync.
	resyncInterval time.Duration
	// eventLog is an optional event log for recording reconciliation events.
	// When set, the runner emits events for key state changes (instance
	// creation, failure, placement, etc.) after each reconciliation cycle.
	eventLog *types.EventLog
}

// SetEventLog attaches an event log to the runner. When set, the runner
// emits events for state changes produced by reconciliation cycles.
func (controllerRunner *Runner) SetEventLog(eventLog *types.EventLog) {
	controllerRunner.eventLog = eventLog
}

// NewRunner creates a Runner that will manage the given controllers, all
// sharing the same state store. The default debounce interval is 50ms.
func NewRunner(stateStore store.StateStore, controllers ...Controller) *Runner {
	return &Runner{
		store:          stateStore,
		controllers:    controllers,
		debounce:       50 * time.Millisecond,
		resyncInterval: 30 * time.Second,
	}
}

// SetDebounce overrides the default debounce interval. A shorter interval
// makes the system more responsive but increases reconciliation frequency;
// a longer one batches more events together.
func (controllerRunner *Runner) SetDebounce(debounceInterval time.Duration) {
	controllerRunner.debounce = debounceInterval
}

// SetResyncInterval overrides the default periodic resync interval (30s).
// The resync timer forces a full reconciliation even when no watch events
// arrive, catching dropped events and stale controller state.
// Zero disables periodic resync.
func (controllerRunner *Runner) SetResyncInterval(resyncInterval time.Duration) {
	controllerRunner.resyncInterval = resyncInterval
}

// Run starts all controllers and blocks until ctx is cancelled or any
// controller returns a fatal error. Each controller runs in its own
// goroutine; the first error from any controller cancels all others.
// Run waits for all controller goroutines to finish before returning.
func (controllerRunner *Runner) Run(ctx context.Context) error {
	childCtx, cancelChildren := context.WithCancel(ctx)
	defer cancelChildren()

	var waitGroup sync.WaitGroup
	controllerErrors := make(chan error, len(controllerRunner.controllers)+1)

	if controllerRunner.eventLog != nil {
		eventProjector := NewEventProjector(controllerRunner.store, controllerRunner.eventLog)
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			controllerErrors <- eventProjector.Run(childCtx)
		}()
	}

	for _, controller := range controllerRunner.controllers {
		waitGroup.Add(1)
		go func(controller Controller) {
			defer waitGroup.Done()
			controllerErrors <- controllerRunner.runSingleController(childCtx, controller)
		}(controller)
	}

	var firstError error
	select {
	case <-ctx.Done():
		firstError = ctx.Err()
	case err := <-controllerErrors:
		firstError = err
	}

	cancelChildren()
	waitGroup.Wait()
	return firstError
}

// runSingleController manages one controller's watch-reconcile loop with
// automatic restart and exponential backoff. If setup fails (watch or initial
// reconciliation), the controller is retried with increasing delays. Once
// running, individual reconciliation errors are logged but do not restart
// the controller.
func (controllerRunner *Runner) runSingleController(ctx context.Context, controller Controller) error {
	backoffDelays := []time.Duration{
		100 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		5 * time.Second,
		30 * time.Second,
	}
	attemptIndex := 0

	for {
		err := controllerRunner.runControllerLoop(ctx, controller)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}

		delay := backoffDelays[attemptIndex]
		if attemptIndex < len(backoffDelays)-1 {
			attemptIndex++
		}
		logging.Default().Warn("controller failed, retrying",
			"controller", controller.Name(),
			"attempt", fmt.Sprintf("%d", attemptIndex),
			"delay", delay.String(),
			"error", err.Error())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// runControllerLoop sets up watches on all prefixes returned by the
// controller's Watch method, performs an initial reconciliation, and then
// re-reconciles each time a watched fact changes (with debouncing) or the
// periodic resync timer fires.
func (controllerRunner *Runner) runControllerLoop(ctx context.Context, controller Controller) error {
	reconcileTrigger := make(chan struct{}, 1)

	for _, prefix := range controller.Watch() {
		watchEventChannel, err := controllerRunner.store.Watch(ctx, prefix, store.WatchOption{Prefix: true})
		if err != nil {
			return err
		}
		go func(watchEventChannel <-chan store.Event, controllerName string) {
			for {
				select {
				case <-ctx.Done():
					return
				case watchEvent, ok := <-watchEventChannel:
					if !ok {
						return
					}
					if watchEvent.Type == store.EventOverflow {
						logging.Default().Warn("watch events dropped, triggering resync", "controller", controllerName)
					}
					if watchEvent.Type == store.EventCompacted {
						logging.Default().Warn("watch revision compacted, triggering resync", "controller", controllerName)
					}
					select {
					case reconcileTrigger <- struct{}{}:
					default:
					}
				}
			}
		}(watchEventChannel, controller.Name())
	}

	if err := controllerRunner.executeReconciliationCycle(ctx, controller); err != nil {
		return err
	}

	var resyncTicker *time.Ticker
	var resyncChannel <-chan time.Time
	if controllerRunner.resyncInterval > 0 {
		resyncTicker = time.NewTicker(controllerRunner.resyncInterval)
		resyncChannel = resyncTicker.C
		defer resyncTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resyncChannel:
			if err := controllerRunner.executeReconciliationCycle(ctx, controller); err != nil {
				logging.Default().Error("resync error", "controller", controller.Name(), "error", err.Error())
			}
		case <-reconcileTrigger:
			if controllerRunner.debounce > 0 {
				timer := time.NewTimer(controllerRunner.debounce)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				select {
				case <-reconcileTrigger:
				default:
				}
			}
			if err := controllerRunner.executeReconciliationCycle(ctx, controller); err != nil {
				logging.Default().Error("reconcile error", "controller", controller.Name(), "error", err.Error())
			}
		}
	}
}

// maxReconciliationAttempts is the maximum number of retries when a
// reconciliation cycle detects that the store changed between scan and commit.
const maxReconciliationAttempts = 3

// executeReconciliationCycle performs a single reconciliation pass with
// optimistic concurrency. It retries up to maxReconciliationAttempts times
// if the store revision changes between the scan and the commit.
func (controllerRunner *Runner) executeReconciliationCycle(ctx context.Context, controller Controller) error {
	startTime := metrics.Timer()
	controllerName := controller.Name()

	for attemptIndex := 0; attemptIndex < maxReconciliationAttempts; attemptIndex++ {
		conflictDetected, reconcileError := controllerRunner.attemptSingleReconciliation(ctx, controller)
		if reconcileError != nil {
			reconciliationTotal.Inc(controllerName, "error")
			reconciliationDuration.ObserveSince(startTime, controllerName)
			return reconcileError
		}
		if !conflictDetected {
			reconciliationTotal.Inc(controllerName, "success")
			reconciliationDuration.ObserveSince(startTime, controllerName)
			return nil
		}
		reconciliationConflicts.Inc(controllerName)
		logging.Default().Warn("reconciliation conflict, retrying",
			"controller", controllerName,
			"attempt", fmt.Sprintf("%d/%d", attemptIndex+1, maxReconciliationAttempts))
	}
	reconciliationTotal.Inc(controllerName, "abandoned")
	reconciliationDuration.ObserveSince(startTime, controllerName)
	logging.Default().Warn("reconciliation abandoned after conflict retries",
		"controller", controllerName,
		"retries", fmt.Sprintf("%d", maxReconciliationAttempts))
	return nil
}

// attemptSingleReconciliation performs one scan-reconcile-commit cycle using
// revision-aware optimistic concurrency. It snapshots the store at a known
// revision, computes changes, checks for revision drift, and applies changes
// via a transaction with per-key revision guards.
func (controllerRunner *Runner) attemptSingleReconciliation(ctx context.Context, controller Controller) (conflictDetected bool, reconcileError error) {
	var allFacts []store.Fact
	var snapshotRevision int64

	for _, prefix := range controller.Watch() {
		scanResult, scanError := controllerRunner.store.ScanWithRevision(ctx, prefix)
		if scanError != nil {
			return false, scanError
		}
		allFacts = append(allFacts, scanResult.Facts...)
		if scanResult.Revision > snapshotRevision {
			snapshotRevision = scanResult.Revision
		}
	}

	store.SortFacts(allFacts)

	changes, reconcileError := controller.Reconcile(ctx, allFacts)
	if reconcileError != nil {
		return false, reconcileError
	}
	if len(changes) == 0 {
		return false, nil
	}

	scannedFactRevisions := make(map[string]int64, len(allFacts))
	for _, scannedFact := range allFacts {
		scannedFactRevisions[scannedFact.Key] = scannedFact.Revision
	}

	var transactionCompares []store.Compare
	var transactionOperations []store.Op
	changeKeySet := make(map[string]bool, len(changes))
	for _, change := range changes {
		changeKeySet[change.Key] = true
		transactionOperations = append(transactionOperations, store.Op{
			Type:  change.Type,
			Key:   change.Key,
			Value: change.Value,
		})
		if factRevision, existedInScan := scannedFactRevisions[change.Key]; existedInScan {
			transactionCompares = append(transactionCompares, store.Compare{
				Key:      change.Key,
				Revision: factRevision,
			})
		} else if change.Type == store.OpPut {
			transactionCompares = append(transactionCompares, store.Compare{
				Key:      change.Key,
				Revision: 0,
			})
		}
	}

	// Guard input keys: if any fact the controller read has changed since the scan,
	// the plan was derived from stale state and must not be committed.
	for _, scannedFact := range allFacts {
		if !changeKeySet[scannedFact.Key] {
			transactionCompares = append(transactionCompares, store.Compare{
				Key:      scannedFact.Key,
				Revision: scannedFact.Revision,
			})
		}
	}

	// etcd enforces a maximum of 128 total operations per transaction (compares +
	// success ops + failure ops). Cap compares to fit within this limit. Change-set
	// compares (added first) have priority over input-set compares.
	maxCompareCount := 128 - len(transactionOperations)
	if len(transactionCompares) > maxCompareCount {
		transactionCompares = transactionCompares[:maxCompareCount]
	}

	transactionSucceeded, transactionError := controllerRunner.store.Transaction(
		ctx, transactionCompares, transactionOperations, nil,
	)
	if transactionError != nil {
		return false, transactionError
	}
	if !transactionSucceeded {
		return true, nil
	}

	reconciliationChanges.Add(int64(len(changes)), controller.Name())

	return false, nil
}

