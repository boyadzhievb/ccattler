package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

// failingConfigScanStore fails every Scan of a service's config or secret
// facts while failScans is set, as a store blip during a reconcile would.
type failingConfigScanStore struct {
	store.StateStore              // StateStore serves every other call.
	failScans        atomic.Bool  // failScans makes config and secret scans fail.
	failedScanCount  atomic.Int64 // failedScanCount counts the scans that failed.
}

// Scan fails config and secret scans while failScans is set.
func (failingStore *failingConfigScanStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	if failingStore.failScans.Load() && (strings.Contains(prefix, "/config/") || strings.Contains(prefix, "/secret/")) {
		failingStore.failedScanCount.Add(1)
		return nil, errors.New("store unavailable")
	}
	return failingStore.StateStore.Scan(ctx, prefix)
}

// TestAgentKeepsInstanceWhenConfigReadFails checks that a store error while
// comparing the config hash does not stop a running instance (M235 review).
func TestAgentKeepsInstanceWhenConfigReadFails(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	failingStore := &failingConfigScanStore{StateStore: memoryStore}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	countingRuntime := &lateFactRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		factStore:        memoryStore,
		lateFactKey:      types.KeyDesiredServiceImage("web"), // same value: changes nothing
		lateFactValue:    "nginx:1.28",
		startCounts:      make(map[string]int),
	}
	nodeAgent := New("node-1", failingStore, countingRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	memoryStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	memoryStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.local"))
	types.WriteInstance(ctx, memoryStore, types.Instance{ID: "blip1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, memoryStore, types.Placement{InstanceID: "blip1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, configHashTestTimeout, "config hash stored", func() bool {
		_, getError := memoryStore.Get(ctx, types.KeyObservedInstanceConfigHash("blip1"))
		return getError == nil
	})
	failingStore.failScans.Store(true)
	waitFor(t, configHashTestTimeout, "several reconcile cycles with failing config reads", func() bool {
		return failingStore.failedScanCount.Load() >= 10
	})
	observedStatus, statusError := countingRuntime.Status(ctx, "blip1")
	if statusError != nil || !observedStatus.Running {
		t.Fatalf("instance stopped during failing config reads (status %+v, error %v); a read error is not a config change", observedStatus, statusError)
	}
	if starts := countingRuntime.startCount("blip1"); starts != 1 {
		t.Fatalf("instance started %d times; a config read error must not restart it", starts)
	}
}

// TestServiceConfigHashFormatUnchanged pins the bytes the config hash is built
// from: hashes stored by older agents must still match, or an upgrade would
// restart every instance with config (M235 review).
func TestServiceConfigHashFormatUnchanged(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "B_VAR"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "A_VAR"), []byte("1"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigFile("web", "/etc/app.conf"), []byte("port=80"))
	factStore.Put(ctx, types.KeyDesiredServiceSecret("web", "db-password"), []byte("/run/secrets/db"))
	nodeAgent := New("node-1", factStore, runtime.NewSimulatorRuntime())

	actualHash, hashError := nodeAgent.computeServiceConfigHash(ctx, "web")
	if hashError != nil {
		t.Fatalf("hash: %v", hashError)
	}
	legacyInput := "env:A_VAR=1\nenv:B_VAR=2\nfile:/etc/app.conf=port=80\n" +
		"secret:" + types.KeyDesiredServiceSecret("web", "db-password") + "=/run/secrets/db\n"
	legacyDigest := sha256.Sum256([]byte(legacyInput))
	if expectedHash := hex.EncodeToString(legacyDigest[:]); actualHash != expectedHash {
		t.Fatalf("config hash format changed: got %s, want %s", actualHash, expectedHash)
	}
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
