package agent

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
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

func TestAgentStartsPlacedInstance(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Set up: service config + instance placed on our node.
	s.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, s, types.Instance{ID: "aaa", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, s, types.Placement{InstanceID: "aaa", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for agent to start the instance.
	waitFor(t, 2*time.Second, "instance running in runtime", func() bool {
		st, err := rt.Status(ctx, "aaa")
		return err == nil && st.Running
	})

	// Verify state reported back to store.
	waitFor(t, 2*time.Second, "instance state=running in store", func() bool {
		f, err := s.Get(ctx, types.KeyObservedInstanceState("aaa"))
		return err == nil && string(f.Value) == "running"
	})

	// Verify node assignment reported.
	f, _ := s.Get(ctx, types.KeyObservedInstanceNode("aaa"))
	if string(f.Value) != "node-1" {
		t.Errorf("node: got %s, want node-1", f.Value)
	}
}

func TestAgentIgnoresOtherNode(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Instance placed on node-2, not our node.
	s.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, s, types.Instance{ID: "bbb", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, s, types.Placement{InstanceID: "bbb", NodeID: "node-2"})

	go nodeAgent.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	_, err := rt.Status(ctx, "bbb")
	if err != runtime.ErrNotFound {
		t.Error("agent should not start instances placed on other nodes")
	}
}

func TestAgentRegistersNode(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "node registered", func() bool {
		f, err := s.Get(ctx, types.KeyObservedNodeState("node-1"))
		return err == nil && string(f.Value) == "alive"
	})
}

func TestAgentStopsRemovedInstance(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Start with a placed instance.
	s.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, s, types.Instance{ID: "ccc", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, s, types.Placement{InstanceID: "ccc", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance running", func() bool {
		st, err := rt.Status(ctx, "ccc")
		return err == nil && st.Running
	})

	// Remove the placement.
	s.Delete(ctx, types.KeyPlacementInstance("ccc"))

	// Agent should stop the process.
	waitFor(t, 2*time.Second, "instance stopped in runtime", func() bool {
		st, err := rt.Status(ctx, "ccc")
		return err == nil && !st.Running
	})
}

func TestAgentHandlesNewPlacement(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	s.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("myapp:latest"))

	go nodeAgent.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Dynamically place an instance.
	types.WriteInstance(ctx, s, types.Instance{ID: "ddd", Service: "api", State: types.InstancePending})
	types.WritePlacement(ctx, s, types.Placement{InstanceID: "ddd", NodeID: "node-1"})

	waitFor(t, 2*time.Second, "new instance running", func() bool {
		st, err := rt.Status(ctx, "ddd")
		return err == nil && st.Running
	})
}

func TestAgentReportsHealthy(t *testing.T) {
	// Start an HTTP health endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go http.Serve(ln, mux)
	defer ln.Close()

	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// Service with health check config.
	s.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	s.Put(ctx, types.KeyDesiredServiceHealthMethod("web"), []byte("http"))
	s.Put(ctx, types.KeyDesiredServiceHealthPath("web"), []byte("/health"))
	s.Put(ctx, types.KeyDesiredServiceExpose("web", port), []byte(""))

	// Place instance and set its IP.
	types.WriteInstance(ctx, s, types.Instance{ID: "eee", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, s, types.Placement{InstanceID: "eee", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance health=healthy in store", func() bool {
		f, err := s.Get(ctx, types.KeyObservedInstanceHealth("eee"))
		return err == nil && string(f.Value) == string(types.HealthHealthy)
	})
}

func TestAgentReportsUnhealthy(t *testing.T) {
	// Use a port with nothing listening.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // close immediately so the port is refused

	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	s.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	s.Put(ctx, types.KeyDesiredServiceHealthMethod("web"), []byte("http"))
	s.Put(ctx, types.KeyDesiredServiceHealthPath("web"), []byte("/health"))
	s.Put(ctx, types.KeyDesiredServiceExpose("web", port), []byte(""))

	types.WriteInstance(ctx, s, types.Instance{ID: "fff", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, s, types.Placement{InstanceID: "fff", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "instance health=unhealthy in store", func() bool {
		f, err := s.Get(ctx, types.KeyObservedInstanceHealth("fff"))
		return err == nil && string(f.Value) == string(types.HealthUnhealthy)
	})
}

func TestAgentNoHealthWithoutConfig(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	// No health config for this service.
	s.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, s, types.Instance{ID: "ggg", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, s, types.Placement{InstanceID: "ggg", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	// Wait for instance to be running.
	waitFor(t, 2*time.Second, "instance running", func() bool {
		f, err := s.Get(ctx, types.KeyObservedInstanceState("ggg"))
		return err == nil && string(f.Value) == "running"
	})

	// Health key should not exist.
	_, err := s.Get(ctx, types.KeyObservedInstanceHealth("ggg"))
	if err == nil {
		t.Error("health should not be reported when no health config exists")
	}
}

func TestAgentWritesHeartbeat(t *testing.T) {
	s := store.NewMemoryStore()
	defer s.Close()
	rt := runtime.NewSimulatorRuntime()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeAgent := New("node-1", s, rt)
	nodeAgent.SetInterval(50 * time.Millisecond)

	go nodeAgent.Run(ctx)

	waitFor(t, 2*time.Second, "heartbeat lease written", func() bool {
		f, err := s.Get(ctx, types.KeyLeaseNode("node-1"))
		return err == nil && len(f.Value) > 0
	})
}

