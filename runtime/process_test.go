package runtime

import (
	"testing"
	"time"
)

func TestProcessStartAndStop(t *testing.T) {
	r := NewProcessRuntime()
	r.SetGracePeriod(2 * time.Second)

	err := r.Start(ctx, Spec{ID: "test-1", Image: "sleep 60"})
	if err != nil {
		t.Fatal(err)
	}

	s, err := r.Status(ctx, "test-1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Running {
		t.Fatal("expected running")
	}
	if s.PID <= 0 {
		t.Fatalf("expected positive PID, got %d", s.PID)
	}

	err = r.Stop(ctx, "test-1")
	if err != nil {
		t.Fatal(err)
	}

	s, _ = r.Status(ctx, "test-1")
	if s.Running {
		t.Fatal("expected stopped after Stop()")
	}
}

func TestProcessStartIdempotent(t *testing.T) {
	r := NewProcessRuntime()
	r.SetGracePeriod(2 * time.Second)
	defer r.StopAll(ctx)

	r.Start(ctx, Spec{ID: "test-2", Image: "sleep 60"})
	s1, _ := r.Status(ctx, "test-2")

	r.Start(ctx, Spec{ID: "test-2", Image: "sleep 60"})
	s2, _ := r.Status(ctx, "test-2")

	if s1.PID != s2.PID {
		t.Errorf("idempotent start should keep same PID: %d vs %d", s1.PID, s2.PID)
	}
}

func TestProcessStopIdempotent(t *testing.T) {
	r := NewProcessRuntime()
	err := r.Stop(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("stop nonexistent should not error: %v", err)
	}
}

func TestProcessExitDetected(t *testing.T) {
	r := NewProcessRuntime()

	// "true" exits immediately with code 0.
	r.Start(ctx, Spec{ID: "quick", Image: "true"})
	time.Sleep(100 * time.Millisecond)

	s, err := r.Status(ctx, "quick")
	if err != nil {
		t.Fatal(err)
	}
	if s.Running {
		t.Error("expected not running after exit")
	}
	if s.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", s.ExitCode)
	}
}

func TestProcessList(t *testing.T) {
	r := NewProcessRuntime()
	defer r.StopAll(ctx)

	r.Start(ctx, Spec{ID: "a", Image: "sleep 60"})
	r.Start(ctx, Spec{ID: "b", Image: "sleep 60"})

	list, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestProcessStartError(t *testing.T) {
	r := NewProcessRuntime()
	err := r.Start(ctx, Spec{ID: "bad", Image: "/nonexistent/binary"})
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

func TestProcessInterfaceCompliance(t *testing.T) {
	var _ Runtime = NewProcessRuntime()
}
