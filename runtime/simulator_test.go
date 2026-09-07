package runtime

import (
	"context"
	"testing"
)

var ctx = context.Background()

func TestSimStart(t *testing.T) {
	r := NewSimulatorRuntime()
	err := r.Start(ctx, Spec{ID: "aaa", Image: "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Status(ctx, "aaa")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Running {
		t.Error("expected running")
	}
}

func TestSimStartIdempotent(t *testing.T) {
	r := NewSimulatorRuntime()
	r.Start(ctx, Spec{ID: "aaa", Image: "v1"})
	r.Start(ctx, Spec{ID: "aaa", Image: "v2"})
	s, _ := r.Status(ctx, "aaa")
	if !s.Running {
		t.Error("expected running")
	}
}

func TestSimStop(t *testing.T) {
	r := NewSimulatorRuntime()
	r.Start(ctx, Spec{ID: "aaa", Image: "nginx"})
	r.Stop(ctx, "aaa")
	s, _ := r.Status(ctx, "aaa")
	if s.Running {
		t.Error("expected stopped")
	}
}

func TestSimStopIdempotent(t *testing.T) {
	r := NewSimulatorRuntime()
	err := r.Stop(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("stop nonexistent should not error: %v", err)
	}
}

func TestSimStatusNotFound(t *testing.T) {
	r := NewSimulatorRuntime()
	_, err := r.Status(ctx, "missing")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSimList(t *testing.T) {
	r := NewSimulatorRuntime()
	r.Start(ctx, Spec{ID: "aaa", Image: "nginx"})
	r.Start(ctx, Spec{ID: "bbb", Image: "redis"})
	r.Stop(ctx, "bbb")

	list, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 workloads, got %d", len(list))
	}

	byID := make(map[string]Status)
	for _, s := range list {
		byID[s.ID] = s
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
