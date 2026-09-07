package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func waitForInstances(t *testing.T, s store.StateStore, service string, count int, timeout time.Duration) []types.Instance {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		instances, _ := types.ListInstances(context.Background(), s)
		var matching []types.Instance
		for _, inst := range instances {
			if inst.Service == service && inst.State != types.InstanceStopped {
				matching = append(matching, inst)
			}
		}
		if len(matching) == count {
			return matching
		}
		time.Sleep(10 * time.Millisecond)
	}
	instances, _ := types.ListInstances(context.Background(), s)
	t.Fatalf("timed out waiting for %d %s instances, have %d total instances", count, service, len(instances))
	return nil
}

func TestRunnerCreatesInstances(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	ic := NewInstanceController()
	ic.NewID = seqIDGen()

	runner := NewRunner(s, ic)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	// Write desired state: web service wants 3 instances.
	s.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	instances := waitForInstances(t, s, "web", 3, 2*time.Second)
	for _, inst := range instances {
		if inst.State != types.InstancePending {
			t.Errorf("instance %s: expected pending, got %s", inst.ID, inst.State)
		}
	}
}

func TestRunnerScalesUp(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	ic := NewInstanceController()
	ic.NewID = seqIDGen()

	runner := NewRunner(s, ic)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed with 2 running instances.
	s.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))
	for _, id := range []string{"existing-1", "existing-2"} {
		types.WriteInstance(ctx, s, types.Instance{
			ID: id, Service: "web", State: types.InstanceRunning,
		})
	}

	go runner.Run(ctx)

	// Already satisfied — should stay at 2.
	time.Sleep(100 * time.Millisecond)
	instances := waitForInstances(t, s, "web", 2, 500*time.Millisecond)
	if len(instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(instances))
	}

	// Scale up to 5.
	s.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("5"))

	waitForInstances(t, s, "web", 5, 2*time.Second)
}

func TestRunnerScalesDown(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	ic := NewInstanceController()

	runner := NewRunner(s, ic)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed with 4 running instances, desired is 4.
	s.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("4"))
	for i := 1; i <= 4; i++ {
		types.WriteInstance(ctx, s, types.Instance{
			ID: fmt.Sprintf("api-%d", i), Service: "api", State: types.InstanceRunning,
		})
	}

	go runner.Run(ctx)

	// Scale down to 2.
	s.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("2"))

	waitForInstances(t, s, "api", 2, 2*time.Second)
}

func TestRunnerMultipleControllers(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	ic := NewInstanceController()
	ic.NewID = seqIDGen()

	// A trivial second controller that just counts reconcile calls.
	noop := &noopController{}

	runner := NewRunner(s, ic, noop)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	s.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("1"))

	waitForInstances(t, s, "web", 1, 2*time.Second)
}

type noopController struct{}

func (n *noopController) Name() string       { return "noop" }
func (n *noopController) Watch() []string     { return []string{types.ScanObservedInstances} }
func (n *noopController) Reconcile(_ context.Context, _ []store.Fact) ([]Change, error) {
	return nil, nil
}
