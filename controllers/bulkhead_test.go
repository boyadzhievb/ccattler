package controllers

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBulkheadAcquireAndRelease(t *testing.T) {
	bulkhead := NewBulkhead(2)

	release, acquireError := bulkhead.Acquire(context.Background(), "instance")
	if acquireError != nil {
		t.Fatal(acquireError)
	}

	if bulkhead.ActiveWorkers("instance") != 1 {
		t.Errorf("expected 1 active worker, got %d", bulkhead.ActiveWorkers("instance"))
	}

	release()

	if bulkhead.ActiveWorkers("instance") != 0 {
		t.Errorf("expected 0 active workers after release, got %d", bulkhead.ActiveWorkers("instance"))
	}
}

func TestBulkheadBlocksAtLimit(t *testing.T) {
	bulkhead := NewBulkhead(1)

	release1, _ := bulkhead.Acquire(context.Background(), "instance")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, acquireError := bulkhead.Acquire(ctx, "instance")
	if acquireError == nil {
		t.Error("expected acquire to fail when bulkhead is full")
	}

	release1()

	release2, acquireError := bulkhead.Acquire(context.Background(), "instance")
	if acquireError != nil {
		t.Fatalf("expected acquire to succeed after release: %v", acquireError)
	}
	release2()
}

func TestBulkheadTryAcquire(t *testing.T) {
	bulkhead := NewBulkhead(1)

	release, acquired := bulkhead.TryAcquire("instance")
	if !acquired {
		t.Fatal("expected first TryAcquire to succeed")
	}

	_, acquired = bulkhead.TryAcquire("instance")
	if acquired {
		t.Error("expected second TryAcquire to fail when full")
	}

	release()

	release2, acquired := bulkhead.TryAcquire("instance")
	if !acquired {
		t.Error("expected TryAcquire to succeed after release")
	}
	release2()
}

func TestBulkheadIndependentControllers(t *testing.T) {
	bulkhead := NewBulkhead(1)

	release1, _ := bulkhead.Acquire(context.Background(), "instance")

	release2, acquireError := bulkhead.Acquire(context.Background(), "endpoint")
	if acquireError != nil {
		t.Fatal("expected independent controller to acquire successfully")
	}

	if bulkhead.ActiveWorkers("instance") != 1 {
		t.Errorf("expected 1 active worker for instance, got %d", bulkhead.ActiveWorkers("instance"))
	}
	if bulkhead.ActiveWorkers("endpoint") != 1 {
		t.Errorf("expected 1 active worker for endpoint, got %d", bulkhead.ActiveWorkers("endpoint"))
	}

	release1()
	release2()
}

func TestBulkheadConcurrentAccess(t *testing.T) {
	bulkhead := NewBulkhead(5)
	waitGroup := sync.WaitGroup{}

	for goroutineIndex := 0; goroutineIndex < 20; goroutineIndex++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			release, acquireError := bulkhead.Acquire(context.Background(), "instance")
			if acquireError != nil {
				return
			}
			time.Sleep(time.Millisecond)
			release()
		}()
	}

	waitGroup.Wait()

	if bulkhead.ActiveWorkers("instance") != 0 {
		t.Errorf("expected 0 active workers, got %d", bulkhead.ActiveWorkers("instance"))
	}
}

func TestBulkheadDoubleReleaseIsSafe(t *testing.T) {
	bulkhead := NewBulkhead(1)

	release, _ := bulkhead.Acquire(context.Background(), "instance")
	release()
	release()

	if bulkhead.ActiveWorkers("instance") != 0 {
		t.Errorf("expected 0 after double release, got %d", bulkhead.ActiveWorkers("instance"))
	}
}

func TestBulkheadMinimumWorkers(t *testing.T) {
	bulkhead := NewBulkhead(0)

	release, acquireError := bulkhead.Acquire(context.Background(), "instance")
	if acquireError != nil {
		t.Fatal(acquireError)
	}
	release()
}

func TestBulkheadUnknownControllerReturnsZero(t *testing.T) {
	bulkhead := NewBulkhead(5)

	if bulkhead.ActiveWorkers("nonexistent") != 0 {
		t.Errorf("expected 0 for unknown controller, got %d", bulkhead.ActiveWorkers("nonexistent"))
	}
}
