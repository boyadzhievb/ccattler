package controllers

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
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
	// eventLog is an optional event log for recording reconciliation events.
	// When set, the runner emits events for key state changes (instance
	// creation, failure, placement, etc.) after each reconciliation cycle.
	eventLog *EventLog
}

// SetEventLog attaches an event log to the runner. When set, the runner
// emits events for state changes produced by reconciliation cycles.
func (controllerRunner *Runner) SetEventLog(eventLog *EventLog) {
	controllerRunner.eventLog = eventLog
}

// NewRunner creates a Runner that will manage the given controllers, all
// sharing the same state store. The default debounce interval is 50ms.
func NewRunner(stateStore store.StateStore, controllers ...Controller) *Runner {
	return &Runner{
		store:       stateStore,
		controllers: controllers,
		debounce:    50 * time.Millisecond,
	}
}

// SetDebounce overrides the default debounce interval. A shorter interval
// makes the system more responsive but increases reconciliation frequency;
// a longer one batches more events together.
func (controllerRunner *Runner) SetDebounce(debounceInterval time.Duration) {
	controllerRunner.debounce = debounceInterval
}

// Run starts all controllers and blocks until ctx is cancelled or any
// controller returns a fatal error. Each controller runs in its own
// goroutine; the first error from any controller causes Run to return.
func (controllerRunner *Runner) Run(ctx context.Context) error {
	controllerErrors := make(chan error, len(controllerRunner.controllers))
	for _, controller := range controllerRunner.controllers {
		go func(controller Controller) {
			controllerErrors <- controllerRunner.runSingleController(ctx, controller)
		}(controller)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-controllerErrors:
		return err
	}
}

// runSingleController manages one controller's watch-reconcile loop. It sets
// up watches on all prefixes returned by the controller's Watch method,
// performs an initial reconciliation, and then re-reconciles each time a
// watched fact changes (with debouncing).
func (controllerRunner *Runner) runSingleController(ctx context.Context, controller Controller) error {
	reconcileTrigger := make(chan struct{}, 1)

	// Set up watches on all prefixes.
	for _, prefix := range controller.Watch() {
		watchEventChannel, err := controllerRunner.store.Watch(ctx, prefix, store.WatchOption{Prefix: true})
		if err != nil {
			return err
		}
		go func(watchEventChannel <-chan store.Event) {
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-watchEventChannel:
					if !ok {
						return
					}
					select {
					case reconcileTrigger <- struct{}{}:
					default:
					}
				}
			}
		}(watchEventChannel)
	}

	// Initial reconciliation.
	if err := controllerRunner.executeReconciliationCycle(ctx, controller); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reconcileTrigger:
			// Debounce: drain events that arrive in quick succession.
			if controllerRunner.debounce > 0 {
				timer := time.NewTimer(controllerRunner.debounce)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				// Drain any pending triggers accumulated during debounce.
				select {
				case <-reconcileTrigger:
				default:
				}
			}
			if err := controllerRunner.executeReconciliationCycle(ctx, controller); err != nil {
				log.Printf("controller %s reconcile error: %v", controller.Name(), err)
			}
		}
	}
}

// executeReconciliationCycle performs a single reconciliation pass for the
// given controller. It scans all watched prefixes to collect current facts,
// calls the controller's Reconcile method to compute desired changes, and
// applies each change to the store.
func (controllerRunner *Runner) executeReconciliationCycle(ctx context.Context, controller Controller) error {
	var facts []store.Fact
	for _, prefix := range controller.Watch() {
		scanned, err := controllerRunner.store.Scan(ctx, prefix)
		if err != nil {
			return err
		}
		facts = append(facts, scanned...)
	}

	changes, err := controller.Reconcile(ctx, facts)
	if err != nil {
		return err
	}

	for _, change := range changes {
		switch change.Type {
		case store.OpPut:
			if _, err := controllerRunner.store.Put(ctx, change.Key, change.Value); err != nil {
				return err
			}
		case store.OpDelete:
			if err := controllerRunner.store.Delete(ctx, change.Key); err != nil {
				return err
			}
		}
	}

	if controllerRunner.eventLog != nil {
		controllerRunner.emitEventsForChanges(ctx, controller, changes)
	}

	return nil
}

// emitEventsForChanges inspects the change keys produced by a reconciliation
// cycle and emits human-readable events for important state transitions like
// instance creation, failure, placement, and node state changes.
func (controllerRunner *Runner) emitEventsForChanges(ctx context.Context, controller Controller, changes []Change) {
	controllerName := controller.Name()
	for _, change := range changes {
		eventKind, eventTarget, eventDetail := classifyChangeAsEvent(change)
		if eventKind == "" {
			continue
		}
		controllerRunner.eventLog.Emit(ctx, eventKind, eventTarget, eventDetail, controllerName)
	}
}

// classifyChangeAsEvent examines a single store change and returns the event
// kind, target, and detail if the change represents a noteworthy state
// transition. Returns empty strings for changes that do not warrant an event.
func classifyChangeAsEvent(change Change) (string, string, string) {
	changeKey := change.Key
	changeValue := string(change.Value)

	// Instance state changes: /ccattler/observed/instance/{id}/state
	observedInstancePrefix := types.PrefixObserved + "/instance/"
	if strings.HasPrefix(changeKey, observedInstancePrefix) && strings.HasSuffix(changeKey, "/state") {
		instanceID := strings.TrimPrefix(changeKey, observedInstancePrefix)
		instanceID = strings.TrimSuffix(instanceID, "/state")
		switch changeValue {
		case string(types.InstanceRunning):
			return "instance.running", "instance/" + instanceID, fmt.Sprintf("instance %s is now running", instanceID)
		case string(types.InstanceFailed):
			return "instance.failed", "instance/" + instanceID, fmt.Sprintf("instance %s has failed", instanceID)
		case string(types.InstancePending):
			return "instance.created", "instance/" + instanceID, fmt.Sprintf("instance %s created (pending)", instanceID)
		case string(types.InstanceStopped):
			return "instance.stopped", "instance/" + instanceID, fmt.Sprintf("instance %s stopped", instanceID)
		}
	}

	// Placement decisions: /ccattler/placement/instance/{id}
	placementPrefix := types.PrefixPlacement + "/instance/"
	if strings.HasPrefix(changeKey, placementPrefix) && change.Type == store.OpPut {
		instanceID := strings.TrimPrefix(changeKey, placementPrefix)
		return "instance.placed", "instance/" + instanceID, fmt.Sprintf("instance %s placed on node %s", instanceID, changeValue)
	}

	// Node state changes: /ccattler/observed/node/{id}/state
	observedNodePrefix := types.PrefixObserved + "/node/"
	if strings.HasPrefix(changeKey, observedNodePrefix) && strings.HasSuffix(changeKey, "/state") {
		nodeID := strings.TrimPrefix(changeKey, observedNodePrefix)
		nodeID = strings.TrimSuffix(nodeID, "/state")
		return "node." + changeValue, "node/" + nodeID, fmt.Sprintf("node %s is now %s", nodeID, changeValue)
	}

	// Effective instance count changes: /ccattler/effective/service/{name}/instances
	effectivePrefix := types.PrefixEffective + "/service/"
	if strings.HasPrefix(changeKey, effectivePrefix) && strings.HasSuffix(changeKey, "/instances") {
		serviceName := strings.TrimPrefix(changeKey, effectivePrefix)
		serviceName = strings.TrimSuffix(serviceName, "/instances")
		return "service.scaled", "service/" + serviceName, fmt.Sprintf("service %s scaled to %s instances", serviceName, changeValue)
	}

	return "", "", ""
}
