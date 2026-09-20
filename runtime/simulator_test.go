package runtime

import (
	"context"
	"testing"
)

var ctx = context.Background()

func TestSimStart(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	err := simulatorRuntime.Start(ctx, Spec{ID: "aaa", Image: "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	workloadStatus, err := simulatorRuntime.Status(ctx, "aaa")
	if err != nil {
		t.Fatal(err)
	}
	if !workloadStatus.Running {
		t.Error("expected running")
	}
}

func TestSimStartIdempotent(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	simulatorRuntime.Start(ctx, Spec{ID: "aaa", Image: "v1"})
	simulatorRuntime.Start(ctx, Spec{ID: "aaa", Image: "v2"})
	workloadStatus, _ := simulatorRuntime.Status(ctx, "aaa")
	if !workloadStatus.Running {
		t.Error("expected running")
	}
}

func TestSimStop(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	simulatorRuntime.Start(ctx, Spec{ID: "aaa", Image: "nginx"})
	simulatorRuntime.Stop(ctx, "aaa")
	workloadStatus, _ := simulatorRuntime.Status(ctx, "aaa")
	if workloadStatus.Running {
		t.Error("expected stopped")
	}
}

func TestSimStopIdempotent(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	err := simulatorRuntime.Stop(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("stop nonexistent should not error: %v", err)
	}
}

func TestSimStatusNotFound(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	_, err := simulatorRuntime.Status(ctx, "missing")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSimList(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	simulatorRuntime.Start(ctx, Spec{ID: "aaa", Image: "nginx"})
	simulatorRuntime.Start(ctx, Spec{ID: "bbb", Image: "redis"})
	simulatorRuntime.Stop(ctx, "bbb")

	list, err := simulatorRuntime.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 workloads, got %d", len(list))
	}

	byID := make(map[string]Status)
	for _, workloadStatus := range list {
		byID[workloadStatus.ID] = workloadStatus
	}
	if !byID["aaa"].Running {
		t.Error("aaa should be running")
	}
	if byID["bbb"].Running {
		t.Error("bbb should be stopped")
	}
}

func TestSimInterfaceCompliance(t *testing.T) {
	var _ Runtime = NewSimulatorRuntime()
}

// TestSimStatsRunningWorkload verifies that Stats returns the spec's CPU and
// memory values for a running workload.
func TestSimStatsRunningWorkload(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	simulatorRuntime.Start(ctx, Spec{
		ID:      "stats-aaa",
		Image:   "app:1.0",
		CPUm:    500,
		MemoryB: 536870912,
	})

	resourceStats, statsError := simulatorRuntime.Stats(ctx, "stats-aaa")
	if statsError != nil {
		t.Fatal(statsError)
	}
	if resourceStats.CPUMillicores != 500 {
		t.Errorf("cpu: got %d, want 500", resourceStats.CPUMillicores)
	}
	if resourceStats.MemoryBytes != 536870912 {
		t.Errorf("memory: got %d, want 536870912", resourceStats.MemoryBytes)
	}
}

// TestSimStatsStoppedWorkload verifies that Stats returns zero values for a
// stopped workload.
func TestSimStatsStoppedWorkload(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	simulatorRuntime.Start(ctx, Spec{ID: "stats-bbb", Image: "app:1.0", CPUm: 1000, MemoryB: 1024})
	simulatorRuntime.Stop(ctx, "stats-bbb")

	resourceStats, statsError := simulatorRuntime.Stats(ctx, "stats-bbb")
	if statsError != nil {
		t.Fatal(statsError)
	}
	if resourceStats.CPUMillicores != 0 {
		t.Errorf("stopped cpu: got %d, want 0", resourceStats.CPUMillicores)
	}
	if resourceStats.MemoryBytes != 0 {
		t.Errorf("stopped memory: got %d, want 0", resourceStats.MemoryBytes)
	}
}

// TestSimStatsUnknownWorkload verifies that Stats returns ErrNotFound for a
// workload that was never started.
func TestSimStatsUnknownWorkload(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()

	_, statsError := simulatorRuntime.Stats(ctx, "nonexistent")
	if statsError != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", statsError)
	}
}

// TestSimStatsZeroResources verifies that Stats returns zero for a running
// workload that has no resource requests in its spec.
func TestSimStatsZeroResources(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	simulatorRuntime.Start(ctx, Spec{ID: "stats-ccc", Image: "app:1.0"})

	resourceStats, statsError := simulatorRuntime.Stats(ctx, "stats-ccc")
	if statsError != nil {
		t.Fatal(statsError)
	}
	if resourceStats.CPUMillicores != 0 {
		t.Errorf("cpu: got %d, want 0", resourceStats.CPUMillicores)
	}
	if resourceStats.MemoryBytes != 0 {
		t.Errorf("memory: got %d, want 0", resourceStats.MemoryBytes)
	}
}
