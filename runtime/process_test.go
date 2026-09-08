package runtime

import (
	"testing"
	"time"
)

func TestProcessStartAndStop(t *testing.T) {
	processRuntime := NewProcessRuntime()
	processRuntime.SetGracePeriod(2 * time.Second)

	err := processRuntime.Start(ctx, Spec{ID: "test-1", Image: "sleep 60"})
	if err != nil {
		t.Fatal(err)
	}

	workloadStatus, err := processRuntime.Status(ctx, "test-1")
	if err != nil {
		t.Fatal(err)
	}
	if !workloadStatus.Running {
		t.Fatal("expected running")
	}
	if workloadStatus.PID <= 0 {
		t.Fatalf("expected positive PID, got %d", workloadStatus.PID)
	}

	err = processRuntime.Stop(ctx, "test-1")
	if err != nil {
		t.Fatal(err)
	}

	workloadStatus, _ = processRuntime.Status(ctx, "test-1")
	if workloadStatus.Running {
		t.Fatal("expected stopped after Stop()")
	}
}

func TestProcessStartIdempotent(t *testing.T) {
	processRuntime := NewProcessRuntime()
	processRuntime.SetGracePeriod(2 * time.Second)
	defer processRuntime.StopAll(ctx)

	processRuntime.Start(ctx, Spec{ID: "test-2", Image: "sleep 60"})
	firstStatus, _ := processRuntime.Status(ctx, "test-2")

	processRuntime.Start(ctx, Spec{ID: "test-2", Image: "sleep 60"})
	secondStatus, _ := processRuntime.Status(ctx, "test-2")

	if firstStatus.PID != secondStatus.PID {
		t.Errorf("idempotent start should keep same PID: %d vs %d", firstStatus.PID, secondStatus.PID)
	}
}

func TestProcessStopIdempotent(t *testing.T) {
	processRuntime := NewProcessRuntime()
	err := processRuntime.Stop(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("stop nonexistent should not error: %v", err)
	}
}

func TestProcessExitDetected(t *testing.T) {
	processRuntime := NewProcessRuntime()

	// "true" exits immediately with code 0.
	processRuntime.Start(ctx, Spec{ID: "quick", Image: "true"})
	time.Sleep(100 * time.Millisecond)

	workloadStatus, err := processRuntime.Status(ctx, "quick")
	if err != nil {
		t.Fatal(err)
	}
	if workloadStatus.Running {
		t.Error("expected not running after exit")
	}
	if workloadStatus.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", workloadStatus.ExitCode)
	}
}

func TestProcessList(t *testing.T) {
	processRuntime := NewProcessRuntime()
	defer processRuntime.StopAll(ctx)

	processRuntime.Start(ctx, Spec{ID: "a", Image: "sleep 60"})
	processRuntime.Start(ctx, Spec{ID: "b", Image: "sleep 60"})

	list, err := processRuntime.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestProcessStartError(t *testing.T) {
	processRuntime := NewProcessRuntime()
	err := processRuntime.Start(ctx, Spec{ID: "bad", Image: "/nonexistent/binary"})
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

func TestProcessInterfaceCompliance(t *testing.T) {
	var _ Runtime = NewProcessRuntime()
}
