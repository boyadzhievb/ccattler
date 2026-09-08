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
