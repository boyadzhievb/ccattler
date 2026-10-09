// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/metrics"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tracing"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// defaultControllerDebounceInterval is the minimum quiet period between
	// consecutive reconciliation cycles for a single controller.
	defaultControllerDebounceInterval = 50 * time.Millisecond

	// defaultControllerResyncInterval is the period between forced full
	// reconciliation cycles independent of watch events.
	defaultControllerResyncInterval = 30 * time.Second

	// reconcileRetryBaseDelayMs is the base delay in milliseconds for the
	// exponential backoff between optimistic concurrency retries.
	reconcileRetryBaseDelayMs = 10

	// reconcileRetryMaxDelayMs is the maximum delay in milliseconds for the
	// exponential backoff between optimistic concurrency retries.
	reconcileRetryMaxDelayMs = 500

	// etcdTransactionOperationLimit is the maximum number of operations
	// (compares + success ops + failure ops) in a single etcd transaction.
	etcdTransactionOperationLimit = 128

	// maxTransactionChanges is the maximum number of fact changes the runner
	// will commit in a single transaction. Each change produces roughly two
	// transaction items (one compare + one operation), so 60 changes yields
	// ~120 items — safely under the 128-op etcd limit. Oversized change sets
	// are truncated (capped) to this limit and committed; remaining changes
	// converge on subsequent cycles via watch re-trigger.
	//
	// CONTRACT: Every built-in controller MUST produce ≤ maxTransactionChanges
	// per Reconcile call. Controllers with variable output must cap internally
	// (e.g. FailureController.MaxReplacementsPerCycle, NodeFailureController
	// .MaxTotalChangesPerCycle). The runner logs a warning for any violation.
	maxTransactionChanges = 60
)

// controllerRestartBackoffDelays is the sequence of delays used when a
// controller fails during setup and needs to be restarted. Each successive
// failure uses the next delay until the final value, which repeats.
var controllerRestartBackoffDelays = []time.Duration{
	100 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	30 * time.Second,
}

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
	writeDomainViolations = metrics.DefaultRegistry.RegisterCounter(
		"ccattler_write_domain_violations_total",
		"Changes rejected because the key fell outside the controller's declared write domain",
		"controller",
	)
	transactionRejections = metrics.DefaultRegistry.RegisterCounter(
		"ccattler_transaction_rejections_total",
		"Number of times a change set was rejected for exceeding the transaction budget",
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
	// maxReconciliationAttempts is the maximum number of optimistic concurrency
	// retries per reconciliation cycle. Higher values improve convergence under
	// heavy write contention (e.g. 50+ concurrent agents).
	maxReconciliationAttempts int
	// maxInputKeyGuards limits how many input-key (non-change-set) guards are
	// added to the transaction. Under high write contention from many agents,
	// guarding all scanned facts causes persistent conflicts. A lower value
	// reduces the conflict surface at the cost of weaker stale-read detection.
	// Zero disables input-key guards entirely; negative means unlimited (fill
	// up to the 128-op etcd cap). Change-set guards are always included.
	maxInputKeyGuards int
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
		store:                     stateStore,
		controllers:               controllers,
		debounce:                  defaultControllerDebounceInterval,
		resyncInterval:            defaultControllerResyncInterval,
		maxReconciliationAttempts: defaultMaxReconciliationAttempts,
		maxInputKeyGuards:         -1,
	}
}

// SetDebounce overrides the default debounce interval. A shorter interval
// makes the system more responsive but increases reconciliation frequency;
// a longer one batches more events together.
func (controllerRunner *Runner) SetDebounce(debounceInterval time.Duration) {
	controllerRunner.debounce = debounceInterval
}

// SetMaxReconciliationAttempts overrides the default number of optimistic
// concurrency retries per reconciliation cycle. Values below 1 are clamped
// to 1 to prevent silent no-op reconciliation.
func (controllerRunner *Runner) SetMaxReconciliationAttempts(maxAttempts int) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	controllerRunner.maxReconciliationAttempts = maxAttempts
}

// SetMaxInputKeyGuards limits the number of input-key guards added to each
// transaction. Under high write contention (many concurrent agents), the
// default behavior of guarding all scanned facts causes persistent conflicts.
// Set to 0 to disable input-key guards entirely (only change-set keys are
// guarded). Set to a positive value to cap the count. Negative means unlimited
// (the default: fill up to the 128-op etcd cap).
func (controllerRunner *Runner) SetMaxInputKeyGuards(maxGuards int) {
	controllerRunner.maxInputKeyGuards = maxGuards
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
	attemptIndex := 0

	for {
		err := controllerRunner.runControllerLoop(ctx, controller)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}

		delay := controllerRestartBackoffDelays[attemptIndex]
		if attemptIndex < len(controllerRestartBackoffDelays)-1 {
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
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	reconcileTrigger := make(chan struct{}, 1)
	watchLost := make(chan struct{}, 1)

	for _, prefix := range controller.Watch() {
		watchEventChannel, err := controllerRunner.store.Watch(watchCtx, prefix, store.WatchOption{Prefix: true})
		if err != nil {
			return err
		}
		go func(watchEventChannel <-chan store.Event, controllerName string) {
			for {
				select {
				case <-watchCtx.Done():
					return
				case watchEvent, ok := <-watchEventChannel:
					if !ok {
						logging.Default().Warn("watch channel closed, signaling restart", "controller", controllerName)
						select {
						case watchLost <- struct{}{}:
						default:
						}
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
		case <-watchLost:
			return fmt.Errorf("watch channel closed for controller %s, restarting", controller.Name())
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

// defaultMaxReconciliationAttempts is the default number of retries when a
// reconciliation cycle detects that the store changed between scan and commit.
// Higher values help under heavy write contention (many concurrent agents).
const defaultMaxReconciliationAttempts = 5

// executeReconciliationCycle performs a single reconciliation pass with
// optimistic concurrency. It retries up to maxReconciliationAttempts times
// if the store revision changes between the scan and the commit.
func (controllerRunner *Runner) executeReconciliationCycle(ctx context.Context, controller Controller) error {
	startTime := metrics.Timer()
	controllerName := controller.Name()

	reconcileTrace := tracing.NewTraceContext()
	ctx = tracing.ContextWithTrace(ctx, reconcileTrace)

	for attemptIndex := 0; attemptIndex < controllerRunner.maxReconciliationAttempts; attemptIndex++ {
		conflictDetected, reconcileError := controllerRunner.attemptSingleReconciliation(ctx, controller)
		if reconcileError != nil {
			reconciliationTotal.Inc(controllerName, "error")
			reconciliationDuration.ObserveSince(startTime, controllerName)
			logging.Default().Error("reconciliation failed",
				"controller", controllerName,
				"trace_id", reconcileTrace.TraceID,
				"error", reconcileError.Error())
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
			"trace_id", reconcileTrace.TraceID,
			"attempt", fmt.Sprintf("%d/%d", attemptIndex+1, controllerRunner.maxReconciliationAttempts))

		baseDelayMs := reconcileRetryBaseDelayMs * (1 << attemptIndex)
		if baseDelayMs > reconcileRetryMaxDelayMs {
			baseDelayMs = reconcileRetryMaxDelayMs
		}
		jitterMs := rand.Intn(baseDelayMs + 1) //nolint:gosec // math/rand for reconciliation jitter
		retryDelay := time.Duration(baseDelayMs+jitterMs) * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay):
		}
	}
	reconciliationTotal.Inc(controllerName, "abandoned")
	reconciliationDuration.ObserveSince(startTime, controllerName)
	logging.Default().Warn("reconciliation abandoned after conflict retries",
		"controller", controllerName,
		"trace_id", reconcileTrace.TraceID,
		"retries", fmt.Sprintf("%d", controllerRunner.maxReconciliationAttempts))
	return nil
}

// attemptSingleReconciliation performs one scan-reconcile-commit cycle using
// revision-aware optimistic concurrency. It snapshots the store at a known
// revision, computes changes, checks for revision drift, and applies changes
// via a transaction with per-key revision guards.
func (controllerRunner *Runner) attemptSingleReconciliation(ctx context.Context, controller Controller) (conflictDetected bool, reconcileError error) {
	allFacts, scanError := controllerRunner.scanFactsForController(ctx, controller)
	if scanError != nil {
		return false, scanError
	}

	store.SortFacts(allFacts)

	changes, reconcileError := controller.Reconcile(ctx, allFacts)
	if reconcileError != nil {
		return false, reconcileError
	}
	if len(changes) == 0 {
		controllerRunner.executePendingPostCommitOperations(ctx, controller)
		return false, nil
	}

	changes, domainError := enforceWriteDomain(controller.Name(), changes)
	if domainError != nil {
		return false, domainError
	}
	if len(changes) == 0 {
		return false, nil
	}

	sortChangesByGroupAndKey(changes)

	if duplicateKey := findDuplicateChangeKey(changes); duplicateKey != "" {
		return false, fmt.Errorf("controller %s produced duplicate key in single transaction: %s",
			controller.Name(), duplicateKey)
	}

	if len(changes) > maxTransactionChanges {
		var deferredChangeCount int
		var groupError error
		changes, deferredChangeCount, groupError = takeWholeGroups(changes, maxTransactionChanges)
		if groupError != nil {
			return false, fmt.Errorf("controller %s: %w", controller.Name(), groupError)
		}
		if deferredChangeCount > 0 {
			logging.Default().Warn("deferred atomic change groups to fit transaction budget",
				"controller", controller.Name(),
				"committed_changes", fmt.Sprintf("%d", len(changes)),
				"deferred_changes", fmt.Sprintf("%d", deferredChangeCount))
			transactionRejections.Inc(controller.Name())
		}
	}

	transactionCompares, transactionOperations := controllerRunner.buildReconciliationTransaction(controller.Name(), changes, allFacts)

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

	controllerRunner.executePendingPostCommitOperations(ctx, controller)

	return false, nil
}

// executePendingPostCommitOperations calls ExecutePostCommitOperations on a
// controller that implements PostCommitController. Called both after a
// successful transaction commit and when Reconcile produces no changes, so
// that pending operations from previous failed provider calls get retried.
func (controllerRunner *Runner) executePendingPostCommitOperations(ctx context.Context, controller Controller) {
	postCommitController, hasPostCommit := controller.(PostCommitController)
	if !hasPostCommit {
		return
	}
	if postCommitError := postCommitController.ExecutePostCommitOperations(ctx); postCommitError != nil {
		logging.Default().Error("post-commit operations failed",
			"controller", controller.Name(),
			"error", postCommitError.Error())
	}
}

// scanFactsForController scans all fact prefixes declared in the controller's
// Watch list and returns the combined facts.
func (controllerRunner *Runner) scanFactsForController(ctx context.Context, controller Controller) ([]store.Fact, error) {
	var allFacts []store.Fact
	for _, prefix := range controller.Watch() {
		scanResult, scanError := controllerRunner.store.ScanWithRevision(ctx, prefix)
		if scanError != nil {
			return nil, scanError
		}
		allFacts = append(allFacts, scanResult.Facts...)
	}
	return allFacts, nil
}

// buildReconciliationTransaction converts proposed changes and the scanned
// input facts into transaction compare guards and operations. Each changed key
// gets a revision guard (or a create-only guard for new keys). Input keys that
// were read but not written are also guarded up to the etcd transaction
// operation limit and the configured maxInputKeyGuards cap.
func (controllerRunner *Runner) buildReconciliationTransaction(controllerName string, changes []Change, allFacts []store.Fact) ([]store.Compare, []store.Op) {
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

	controllerRunner.appendInputKeyGuards(controllerName, &transactionCompares, transactionOperations, changeKeySet, allFacts)

	return transactionCompares, transactionOperations
}

// appendInputKeyGuards adds revision guards for input keys within the
// controller's write domain. Only facts whose prefix matches the controller's
// declared output are guarded — read-only prefixes are excluded to prevent
// CAS contention with other writers (e.g. agents writing observed/instance/).
func (controllerRunner *Runner) appendInputKeyGuards(controllerName string, transactionCompares *[]store.Compare, transactionOperations []store.Op, changeKeySet map[string]bool, allFacts []store.Fact) {
	if controllerRunner.maxInputKeyGuards == 0 {
		return
	}

	writePrefixes := controllerOutputPrefixes()[controllerName]

	etcdCapacity := etcdTransactionOperationLimit - len(transactionOperations)
	inputKeyLimit := etcdCapacity
	if controllerRunner.maxInputKeyGuards > 0 {
		changeSetGuardCount := len(*transactionCompares)
		inputKeyLimit = changeSetGuardCount + controllerRunner.maxInputKeyGuards
		if inputKeyLimit > etcdCapacity {
			inputKeyLimit = etcdCapacity
		}
	}
	for _, scannedFact := range allFacts {
		if len(*transactionCompares) >= inputKeyLimit {
			break
		}
		if changeKeySet[scannedFact.Key] {
			continue
		}
		if !isKeyWithinWriteDomain(scannedFact.Key, writePrefixes) {
			continue
		}
		*transactionCompares = append(*transactionCompares, store.Compare{
			Key:      scannedFact.Key,
			Revision: scannedFact.Revision,
		})
	}
}

// enforceWriteDomain validates that every proposed change falls within the
// controller's declared output prefixes. Changes that violate the write domain
// are dropped and logged. Returns an error only if the output prefixes map has
// no entry for the controller (unknown controller).
func enforceWriteDomain(controllerName string, changes []Change) ([]Change, error) {
	allowedPrefixes, declared := controllerOutputPrefixes()[controllerName]
	if !declared {
		return changes, nil
	}

	validatedChanges := make([]Change, 0, len(changes))
	for _, change := range changes {
		if isKeyWithinWriteDomain(change.Key, allowedPrefixes) {
			validatedChanges = append(validatedChanges, change)
		} else {
			writeDomainViolations.Inc(controllerName)
			logging.Default().Error("write domain violation: change dropped",
				"controller", controllerName,
				"key", change.Key,
				"allowed_prefixes", fmt.Sprintf("%v", allowedPrefixes))
		}
	}
	return validatedChanges, nil
}

// isKeyWithinWriteDomain checks whether a key starts with any of the allowed
// output prefixes for a controller.
func isKeyWithinWriteDomain(key string, allowedPrefixes []string) bool {
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// sortChangesByGroupAndKey sorts changes so that members of the same group
// are contiguous, with groups ordered by their first key. Within a group,
// changes are ordered by key. Ungrouped changes (empty Group) sort by key
// and are each treated as their own single-change group.
func sortChangesByGroupAndKey(changes []Change) {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Group != changes[j].Group {
			return changes[i].Group < changes[j].Group
		}
		if changes[i].Key != changes[j].Key {
			return changes[i].Key < changes[j].Key
		}
		return changes[i].Type < changes[j].Type
	})
}

// takeWholeGroups selects complete atomic groups from a sorted change list
// up to the given limit. Returns an error if any single group exceeds the
// limit, since such a group can never be committed. Returns the selected
// changes and the number of changes deferred to the next cycle.
func takeWholeGroups(changes []Change, limit int) ([]Change, int, error) {
	selected := make([]Change, 0, min(len(changes), limit))
	deferred := 0

	for scanIndex := 0; scanIndex < len(changes); {
		groupID := changes[scanIndex].Group
		groupEnd := scanIndex + 1

		if groupID != "" {
			for groupEnd < len(changes) && changes[groupEnd].Group == groupID {
				groupEnd++
			}
		}

		groupSize := groupEnd - scanIndex

		if groupSize > limit {
			return nil, 0, fmt.Errorf(
				"atomic change group %q has %d changes, exceeds transaction budget %d",
				groupID, groupSize, limit)
		}

		if len(selected)+groupSize > limit {
			deferred += len(changes) - scanIndex
			break
		}

		selected = append(selected, changes[scanIndex:groupEnd]...)
		scanIndex = groupEnd
	}

	return selected, deferred, nil
}

// findDuplicateChangeKey checks for any duplicate keys in the change list.
// etcd requires unique mutation keys per transaction, so duplicates must be
// caught before commit. Returns the first duplicate key found, or empty
// string if none.
func findDuplicateChangeKey(changes []Change) string {
	seenKeys := make(map[string]bool, len(changes))
	for _, change := range changes {
		if seenKeys[change.Key] {
			return change.Key
		}
		seenKeys[change.Key] = true
	}
	return ""
}
