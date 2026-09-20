package controllers

import (
	"context"
	"fmt"
	"sync"
)

// Bulkhead limits the number of concurrent reconciliations that can execute
// simultaneously for a given controller. This prevents a slow or stuck
// controller from consuming all available goroutines and starving others.
type Bulkhead struct {
	semaphores map[string]chan struct{}
	mutex      sync.Mutex
	maxWorkers int
}

// NewBulkhead creates a bulkhead with the specified concurrency limit per controller.
func NewBulkhead(maxWorkers int) *Bulkhead {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	return &Bulkhead{
		semaphores: make(map[string]chan struct{}),
		maxWorkers: maxWorkers,
	}
}

// Acquire attempts to acquire a slot for the named controller. If the bulkhead
// is full, it blocks until a slot becomes available or the context is cancelled.
// Returns a release function that must be called when the work completes.
func (bulkhead *Bulkhead) Acquire(ctx context.Context, controllerName string) (func(), error) {
	semaphore := bulkhead.getOrCreateSemaphore(controllerName)

	select {
	case semaphore <- struct{}{}:
		releaseOnce := sync.Once{}
		return func() {
			releaseOnce.Do(func() { <-semaphore })
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("bulkhead acquire cancelled for %s: %w", controllerName, ctx.Err())
	}
}

// TryAcquire attempts to acquire a slot without blocking. Returns the release
// function and true if a slot was available, or nil and false if the bulkhead
// is full.
func (bulkhead *Bulkhead) TryAcquire(controllerName string) (func(), bool) {
	semaphore := bulkhead.getOrCreateSemaphore(controllerName)

	select {
	case semaphore <- struct{}{}:
		releaseOnce := sync.Once{}
		return func() {
			releaseOnce.Do(func() { <-semaphore })
		}, true
	default:
		return nil, false
	}
}

// ActiveWorkers returns how many concurrent reconciliations are running
// for the named controller.
func (bulkhead *Bulkhead) ActiveWorkers(controllerName string) int {
	bulkhead.mutex.Lock()
	semaphore, exists := bulkhead.semaphores[controllerName]
	bulkhead.mutex.Unlock()

	if !exists {
		return 0
	}
	return len(semaphore)
}

func (bulkhead *Bulkhead) getOrCreateSemaphore(controllerName string) chan struct{} {
	bulkhead.mutex.Lock()
	defer bulkhead.mutex.Unlock()

	if semaphore, exists := bulkhead.semaphores[controllerName]; exists {
		return semaphore
	}
	semaphore := make(chan struct{}, bulkhead.maxWorkers)
	bulkhead.semaphores[controllerName] = semaphore
	return semaphore
}
