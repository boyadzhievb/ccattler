package controllers

import (
	"context"
	"log"
	"time"

	"github.com/boyadzhievb/ccattler/store"
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
	return nil
}
