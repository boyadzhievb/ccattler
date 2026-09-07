package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func waitFor(t *testing.T, timeout time.Duration, desc string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

func TestDesiredToPlaced(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	ic := controllers.NewInstanceController()
	sc := scheduler.NewScheduler()

	runner := controllers.NewRunner(s, ic, sc)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register 3 alive nodes.
	for _, id := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, s, types.Node{
			ID: id, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	go runner.Run(ctx)

	// User intent: web service wants 3 instances.
	s.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	// Wait for instances to be created and placed.
	waitFor(t, 3*time.Second, "3 placements", func() bool {
		facts, _ := s.Scan(ctx, types.ScanPlacements)
		return len(facts) >= 3
	})

	// Verify placements are spread across nodes.
	facts, _ := s.Scan(ctx, types.ScanPlacements)
	nodeCount := make(map[string]int)
	for _, f := range facts {
		nodeCount[string(f.Value)]++
	}
	for _, n := range []string{"node-1", "node-2", "node-3"} {
		if nodeCount[n] != 1 {
			t.Errorf("node %s got %d placements, want 1 (spread)", n, nodeCount[n])
		}
	}

	// Verify all instances are pending (no node agent to start them).
	instances, _ := types.ListInstances(ctx, s)
	webCount := 0
	for _, inst := range instances {
		if inst.Service == "web" {
			webCount++
			if inst.State != types.InstancePending {
				t.Errorf("instance %s: expected pending, got %s", inst.ID, inst.State)
			}
		}
	}
	if webCount != 3 {
		t.Fatalf("expected 3 web instances, got %d", webCount)
	}
}

func TestScaleUpPlacesNewInstances(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	ic := controllers.NewInstanceController()
	sc := scheduler.NewScheduler()

	runner := controllers.NewRunner(s, ic, sc)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	types.WriteNode(ctx, s, types.Node{ID: "node-1", State: types.NodeAlive, CapacityCPU: 4000, CapacityMemory: 8192, AvailableCPU: 4000, AvailableMemory: 8192})
	types.WriteNode(ctx, s, types.Node{ID: "node-2", State: types.NodeAlive, CapacityCPU: 4000, CapacityMemory: 8192, AvailableCPU: 4000, AvailableMemory: 8192})

	go runner.Run(ctx)

	// Start with 2 instances.
	s.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("2"))

	waitFor(t, 3*time.Second, "2 placements", func() bool {
		facts, _ := s.Scan(ctx, types.ScanPlacements)
		return len(facts) >= 2
	})

	// Scale to 4.
	s.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("4"))

	waitFor(t, 3*time.Second, "4 placements", func() bool {
		facts, _ := s.Scan(ctx, types.ScanPlacements)
		return len(facts) >= 4
	})

	// Verify spread: 2 per node.
	facts, _ := s.Scan(ctx, types.ScanPlacements)
	nodeCount := make(map[string]int)
	for _, f := range facts {
		nodeCount[string(f.Value)]++
	}
	if nodeCount["node-1"] != 2 || nodeCount["node-2"] != 2 {
		t.Errorf("expected 2 per node, got node-1=%d node-2=%d", nodeCount["node-1"], nodeCount["node-2"])
	}
}

func TestNoPlacementWithoutNodes(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()

	ic := controllers.NewInstanceController()
	sc := scheduler.NewScheduler()

	runner := controllers.NewRunner(s, ic, sc)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	// Desired 3 but no nodes registered.
	s.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	// Instances should be created but not placed.
	waitFor(t, 2*time.Second, "3 instances created", func() bool {
		instances, _ := types.ListInstances(ctx, s)
		count := 0
		for _, inst := range instances {
			if inst.Service == "web" {
				count++
			}
		}
		return count >= 3
	})

	// No placements.
	facts, _ := s.Scan(ctx, types.ScanPlacements)
	if len(facts) != 0 {
		t.Fatalf("expected 0 placements without nodes, got %d", len(facts))
	}

	// Now add a node — instances should get placed.
	types.WriteNode(ctx, s, types.Node{ID: "node-1", State: types.NodeAlive, CapacityCPU: 4000, CapacityMemory: 8192, AvailableCPU: 4000, AvailableMemory: 8192})

	waitFor(t, 3*time.Second, "3 placements after node added", func() bool {
		facts, _ := s.Scan(ctx, types.ScanPlacements)
		return len(facts) >= 3
	})
}
