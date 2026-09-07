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
func NewRunner(s store.StateStore, controllers ...Controller) *Runner {
	return &Runner{
		store:       s,
		controllers: controllers,
		debounce:    50 * time.Millisecond,
	}
}

// SetDebounce overrides the default debounce interval. A shorter interval
// makes the system more responsive but increases reconciliation frequency;
// a longer one batches more events together.
func (r *Runner) SetDebounce(d time.Duration) {
	r.debounce = d
}

// Run starts all controllers and blocks until ctx is cancelled or any
// controller returns a fatal error. Each controller runs in its own
// goroutine; the first error from any controller causes Run to return.
func (r *Runner) Run(ctx context.Context) error {
	controllerErrors := make(chan error, len(r.controllers))
	for _, ctrl := range r.controllers {
		go func(ctrl Controller) {
			controllerErrors <- r.runSingleController(ctx, ctrl)
		}(ctrl)
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
func (r *Runner) runSingleController(ctx context.Context, ctrl Controller) error {
	reconcileTrigger := make(chan struct{}, 1)

	// Set up watches on all prefixes.
	for _, prefix := range ctrl.Watch() {
		watchEventChannel, err := r.store.Watch(ctx, prefix, store.WatchOption{Prefix: true})
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
	if err := r.executeReconciliationCycle(ctx, ctrl); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reconcileTrigger:
			// Debounce: drain events that arrive in quick succession.
			if r.debounce > 0 {
				timer := time.NewTimer(r.debounce)
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
			if err := r.executeReconciliationCycle(ctx, ctrl); err != nil {
				log.Printf("controller %s reconcile error: %v", ctrl.Name(), err)
			}
		}
	}
}

// executeReconciliationCycle performs a single reconciliation pass for the
// given controller. It scans all watched prefixes to collect current facts,
// calls the controller's Reconcile method to compute desired changes, and
// applies each change to the store.
func (r *Runner) executeReconciliationCycle(ctx context.Context, ctrl Controller) error {
	var facts []store.Fact
	for _, prefix := range ctrl.Watch() {
		scanned, err := r.store.Scan(ctx, prefix)
		if err != nil {
			return err
		}
		facts = append(facts, scanned...)
	}

	changes, err := ctrl.Reconcile(ctx, facts)
	if err != nil {
		return err
	}

	for _, change := range changes {
		switch change.Type {
		case store.OpPut:
			if _, err := r.store.Put(ctx, change.Key, change.Value); err != nil {
				return err
			}
		case store.OpDelete:
			if err := r.store.Delete(ctx, change.Key); err != nil {
				return err
			}
		}
	}
	return nil
}
