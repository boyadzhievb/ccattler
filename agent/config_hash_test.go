package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// configHashTestTimeout bounds each wait in the config hash tests.
const configHashTestTimeout = 3 * time.Second

// lateFactRuntime is a simulator runtime that writes one fact into the store
// during the first Start of an instance, as an apply still writing facts
// while the agent starts the container would (M235).
type lateFactRuntime struct {
	*runtime.SimulatorRuntime                  // SimulatorRuntime runs the workloads.
	factStore                 store.StateStore // factStore receives the late fact.
	lateFactKey               string           // lateFactKey is written during the first Start.
	lateFactValue             string           // lateFactValue is the late fact's value.
	mutex                     sync.Mutex       // mutex guards startCounts.
	startCounts               map[string]int   // startCounts counts Start calls per instance ID.
}

// Start counts the call, writes the late fact on the first start, then starts the workload.
func (lateRuntime *lateFactRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	lateRuntime.mutex.Lock()
	lateRuntime.startCounts[spec.ID]++
	firstStart := lateRuntime.startCounts[spec.ID] == 1
	lateRuntime.mutex.Unlock()
	if firstStart {
		if _, putError := lateRuntime.factStore.Put(ctx, lateRuntime.lateFactKey, []byte(lateRuntime.lateFactValue)); putError != nil {
			return putError
		}
	}
	return lateRuntime.SimulatorRuntime.Start(ctx, spec)
}

// startCount returns how often instanceID was started.
func (lateRuntime *lateFactRuntime) startCount(instanceID string) int {
	lateRuntime.mutex.Lock()
	defer lateRuntime.mutex.Unlock()
	return lateRuntime.startCounts[instanceID]
}

// TestAgentRestartsWhenConfigArrivesDuringStart checks that an env var written
// while the container starts reaches the instance by a restart (spec
// instance-config-reload, M235).
func TestAgentRestartsWhenConfigArrivesDuringStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lateRuntime := &lateFactRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		factStore:        factStore,
		lateFactKey:      types.KeyDesiredServiceConfigEnv("web", "DB_HOST"),
		lateFactValue:    "db.new.local",
		startCounts:      make(map[string]int),
	}
	nodeAgent := New("node-1", factStore, lateRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.old.local"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "late1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "late1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "instance restarted after the env var written during its start", func() bool {
		return lateRuntime.startCount("late1") >= 2
	})
}

// TestAgentRestartsWhenFirstConfigArrivesAfterStart checks that the first env
// var of a service started with no config inputs restarts its instance (spec
// instance-config-reload, M235).
func TestAgentRestartsWhenFirstConfigArrivesAfterStart(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	countingRuntime := &lateFactRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		factStore:        factStore,
		lateFactKey:      types.KeyDesiredServiceImage("web"), // rewrites the image with the same value: no config input
		lateFactValue:    "nginx:1.28",
		startCounts:      make(map[string]int),
	}
	nodeAgent := New("node-1", factStore, countingRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "first1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "first1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "instance running", func() bool {
		observedStatus, statusError := countingRuntime.Status(ctx, "first1")
		return statusError == nil && observedStatus.Running
	})
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.local"))

	waitFor(t, configHashTestTimeout, "instance restarted after the first env var", func() bool {
		return countingRuntime.startCount("first1") >= 2
	})
}
