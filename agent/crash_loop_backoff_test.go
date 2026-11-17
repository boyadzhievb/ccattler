// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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

const (
	// crashLoopTestBaseDelay is the short base delay the crash-loop tests give the agent (D6).
	crashLoopTestBaseDelay = 150 * time.Millisecond

	// crashLoopTestMaxDelay is the short maximum delay the crash-loop tests give the agent.
	crashLoopTestMaxDelay = 2 * time.Hour

	// crashLoopTestResetPeriod is the short stable-run period the crash-loop tests give the agent.
	crashLoopTestResetPeriod = 600 * time.Millisecond

	// crashLoopTestInterval is the reconciliation interval of the crash-loop tests.
	crashLoopTestInterval = 20 * time.Millisecond

	// crashLoopTestTimeout bounds each wait in the crash-loop tests.
	crashLoopTestTimeout = 5 * time.Second

	// crashLoopLongBaseDelay is a base delay no test waits out: only a config change can end the wait.
	crashLoopLongBaseDelay = time.Hour
)

// crashingRuntime is a simulator runtime whose workloads exit with code 1 one
// reconciliation cycle after every start, as a container that fails at once
// would. It records the time of every start.
type crashingRuntime struct {
	*runtime.SimulatorRuntime                        // SimulatorRuntime runs the workloads.
	mutex                     sync.Mutex             // mutex guards the fields below.
	startTimes                map[string][]time.Time // startTimes lists the start times per instance ID.
	crashPending              map[string]bool        // crashPending marks instances to kill on the next List.
	stableFromStartNumber     int                    // stableFromStartNumber is the start from which workloads no longer crash; 0 means never.
	exitBeforeFirstLook       bool                   // exitBeforeFirstLook makes a crashing workload exit inside Start, before the agent's first Status.
	failingStartError         *runtime.StartError    // failingStartError, when set, is returned by every start (recorded, workload not started).
	exitAfterListing          map[string]bool        // exitAfterListing marks workloads that exit right after the next List reported them running.
}

// newCrashingRuntime creates a crashingRuntime whose workloads crash on every start.
func newCrashingRuntime() *crashingRuntime {
	return &crashingRuntime{
		SimulatorRuntime: runtime.NewSimulatorRuntime(),
		startTimes:       make(map[string][]time.Time),
		crashPending:     make(map[string]bool),
		exitAfterListing: make(map[string]bool),
	}
}

// Start records the start, starts the workload and schedules its crash, or
// crashes it at once when exitBeforeFirstLook is set. With failingStartError
// set, the start is recorded and fails with that error.
func (crashRuntime *crashingRuntime) Start(ctx context.Context, spec runtime.Spec) error {
	crashRuntime.mutex.Lock()
	if crashRuntime.failingStartError != nil {
		crashRuntime.startTimes[spec.ID] = append(crashRuntime.startTimes[spec.ID], time.Now())
		failingStartError := *crashRuntime.failingStartError
		crashRuntime.mutex.Unlock()
		failingStartError.ID = spec.ID
		return &failingStartError
	}
	crashRuntime.mutex.Unlock()
	if startError := crashRuntime.SimulatorRuntime.Start(ctx, spec); startError != nil {
		return startError
	}
	crashRuntime.mutex.Lock()
	defer crashRuntime.mutex.Unlock()
	crashRuntime.startTimes[spec.ID] = append(crashRuntime.startTimes[spec.ID], time.Now())
	stableNow := crashRuntime.stableFromStartNumber > 0 && len(crashRuntime.startTimes[spec.ID]) >= crashRuntime.stableFromStartNumber
	if crashRuntime.exitBeforeFirstLook && !stableNow {
		crashRuntime.KillWorkload(spec.ID, 1, "")
		return nil
	}
	crashRuntime.crashPending[spec.ID] = !stableNow
	return nil
}

// List kills every workload with a pending crash, then lists as usual, so a
// start survives until the next reconciliation cycle. Workloads marked in
// exitAfterListing exit with code 1 right after being listed.
func (crashRuntime *crashingRuntime) List(ctx context.Context) ([]runtime.Status, error) {
	crashRuntime.mutex.Lock()
	defer crashRuntime.mutex.Unlock()
	for instanceID, pending := range crashRuntime.crashPending {
		if pending {
			crashRuntime.KillWorkload(instanceID, 1, "")
			crashRuntime.crashPending[instanceID] = false
		}
	}
	listedStatuses, listError := crashRuntime.SimulatorRuntime.List(ctx)
	for instanceID := range crashRuntime.exitAfterListing {
		crashRuntime.KillWorkload(instanceID, 1, "")
		delete(crashRuntime.exitAfterListing, instanceID)
	}
	return listedStatuses, listError
}

// exitAfterNextListing makes a workload exit with code 1 right after the next
// List has reported it running: the race between List and Status (M238 D4).
func (crashRuntime *crashingRuntime) exitAfterNextListing(instanceID string) {
	crashRuntime.mutex.Lock()
	defer crashRuntime.mutex.Unlock()
	crashRuntime.exitAfterListing[instanceID] = true
}

// failStartsWith makes every following start fail with startError; nil lets
// starts succeed again.
func (crashRuntime *crashingRuntime) failStartsWith(startError *runtime.StartError) {
	crashRuntime.mutex.Lock()
	defer crashRuntime.mutex.Unlock()
	crashRuntime.failingStartError = startError
}

// startsOf returns a copy of the start times of one instance.
func (crashRuntime *crashingRuntime) startsOf(instanceID string) []time.Time {
	crashRuntime.mutex.Lock()
	defer crashRuntime.mutex.Unlock()
	return append([]time.Time(nil), crashRuntime.startTimes[instanceID]...)
}

// startCrashLoopTestAgent places one "web" instance on node-1 and runs an
// agent with the short crash-loop delays of D6 until the test ends. It returns
// the agent for RunInAgentLoop.
func startCrashLoopTestAgent(t *testing.T, instanceID string, baseDelay time.Duration, crashRuntime *crashingRuntime) (*Agent, store.StateStore, context.Context) {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { factStore.Close() })
	return startCrashLoopTestAgentOnStore(t, factStore, instanceID, baseDelay, crashRuntime)
}

// startCrashLoopTestAgentOnStore is startCrashLoopTestAgent on a store the
// caller created, so a test can observe the agent's writes from the first one.
func startCrashLoopTestAgentOnStore(t *testing.T, factStore store.StateStore, instanceID string, baseDelay time.Duration, crashRuntime *crashingRuntime) (*Agent, store.StateStore, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	nodeAgent := New("node-1", factStore, crashRuntime)
	nodeAgent.SetInterval(crashLoopTestInterval)
	nodeAgent.crashLoopBaseDelay = baseDelay
	nodeAgent.crashLoopMaxDelay = crashLoopTestMaxDelay
	nodeAgent.crashLoopResetPeriod = crashLoopTestResetPeriod

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.old.local"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})
	nodeAgent.EnableTestHooks()
	go nodeAgent.Run(ctx)
	return nodeAgent, factStore, ctx
}

// readFactValue returns the value of a store key, or "" when it is missing.
func readFactValue(ctx context.Context, factStore store.StateStore, key string) string {
	fact, getError := factStore.Get(ctx, key)
	if getError != nil {
		return ""
	}
	return string(fact.Value)
}

// TestAgentBacksOffCrashLoopingInstance checks that a container that exits
// after every start is restarted after the base delay, then twice the base
// delay, and never earlier (spec instance-restart).
func TestAgentBacksOffCrashLoopingInstance(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	_, factStore, ctx := startCrashLoopTestAgent(t, "loop1", crashLoopTestBaseDelay, crashRuntime)

	waitFor(t, crashLoopTestTimeout, "instance started three times", func() bool {
		return len(crashRuntime.startsOf("loop1")) >= 3
	})
	startTimes := crashRuntime.startsOf("loop1")
	if gap := startTimes[1].Sub(startTimes[0]); gap < crashLoopTestBaseDelay {
		t.Errorf("second start came %v after the first, want at least the base delay %v", gap, crashLoopTestBaseDelay)
	}
	if gap := startTimes[2].Sub(startTimes[1]); gap < 2*crashLoopTestBaseDelay {
		t.Errorf("third start came %v after the second, want at least twice the base delay %v", gap, 2*crashLoopTestBaseDelay)
	}
	waitFor(t, crashLoopTestTimeout, "instance state crash-loop-backoff in store", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("loop1")) == string(types.InstanceCrashLoopBackoff)
	})
}

// TestAgentResetsCrashLoopBackoffAfterStableRun checks that an instance which
// ran longer than the reset period waits the base delay again after its next
// exit, not four times it (spec instance-restart).
func TestAgentResetsCrashLoopBackoffAfterStableRun(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	crashRuntime.stableFromStartNumber = 3 // two crashes, then the third start runs on
	_, factStore, ctx := startCrashLoopTestAgent(t, "stable1", crashLoopTestBaseDelay, crashRuntime)

	waitFor(t, crashLoopTestTimeout, "third start ran longer than the reset period", func() bool {
		startTimes := crashRuntime.startsOf("stable1")
		return len(startTimes) >= 3 && time.Since(startTimes[2]) > 2*crashLoopTestResetPeriod
	})
	crashRuntime.KillWorkload("stable1", 1, "")

	wantReason := "exited with code 1; restarting in " + crashLoopTestBaseDelay.String()
	waitFor(t, crashLoopTestTimeout, "status reason with the base delay after the stable run", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceStatusReason("stable1")) == wantReason
	})
}

// TestAgentCountsRestartsAndPublishesCrashLoopState checks the restart count
// and the waiting state with its reason while the agent backs off.
func TestAgentCountsRestartsAndPublishesCrashLoopState(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	_, factStore, ctx := startCrashLoopTestAgent(t, "count1", crashLoopTestBaseDelay, crashRuntime)

	if got := readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("count1")); got != "" {
		t.Fatalf("restarts = %q before the first start, want none", got)
	}
	wantReason := "exited with code 1; restarting in " + (2 * crashLoopTestBaseDelay).String()
	waitFor(t, crashLoopTestTimeout, "second exit waiting with state, reason and one restart counted", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceStatusReason("count1")) == wantReason &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceState("count1")) == string(types.InstanceCrashLoopBackoff) &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("count1")) == "1"
	})
	waitFor(t, crashLoopTestTimeout, "restart count follows the number of restarts", func() bool {
		startCount := len(crashRuntime.startsOf("count1"))
		return startCount >= 3 && readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("count1")) == "2"
	})
}

// TestAgentRestartsAtOnceWhenConfigChangesDuringBackoff checks that a config
// change ends the wait: the user who fixed the env does not wait minutes (D5).
func TestAgentRestartsAtOnceWhenConfigChangesDuringBackoff(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	_, factStore, ctx := startCrashLoopTestAgent(t, "cfg1", crashLoopLongBaseDelay, crashRuntime)
	waitFor(t, crashLoopTestTimeout, "instance waiting in crash-loop-backoff", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("cfg1")) == string(types.InstanceCrashLoopBackoff)
	})
	startsBeforeChange := len(crashRuntime.startsOf("cfg1"))
	configChangedAt := time.Now()
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("web", "DB_HOST"), []byte("db.new.local"))

	waitFor(t, crashLoopTestTimeout, "instance started again after the config change", func() bool {
		startTimes := crashRuntime.startsOf("cfg1")
		return len(startTimes) > startsBeforeChange && startTimes[startsBeforeChange].After(configChangedAt)
	})
}

// crashLoopBailOutWindow is how long the bail-out test watches the instance after the failed start.
const crashLoopBailOutWindow = 600 * time.Millisecond

// TestAgentDoesNotCountBailedOutStartAsCrash checks that a restart which
// starts nothing (here: the image pull fails) neither counts as a restart nor
// as a new exit, and does not hide the real state behind crash-loop-backoff.
func TestAgentDoesNotCountBailedOutStartAsCrash(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	crashRuntime.PullFailures = map[string]string{"nginx:broken": "registry down"}
	nodeAgent, factStore, ctx := startCrashLoopTestAgent(t, "bail1", 500*time.Millisecond, crashRuntime)

	waitFor(t, crashLoopTestTimeout, "first start", func() bool {
		return len(crashRuntime.startsOf("bail1")) >= 1
	})
	// The restart after the wait needs an image that cannot be pulled.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:broken"))
	waitFor(t, crashLoopTestTimeout, "restart bails out on the pull failure", func() bool {
		return readFactValue(ctx, factStore, types.KeyObservedInstanceState("bail1")) == string(types.InstanceImagePullBackoff)
	})

	windowStart := time.Now()
	waitFor(t, crashLoopTestTimeout, "state stays image-pull-backoff without a counted restart or exit", func() bool {
		if state := readFactValue(ctx, factStore, types.KeyObservedInstanceState("bail1")); state != string(types.InstanceImagePullBackoff) {
			t.Errorf("state = %q during the pull back-off, want %q", state, types.InstanceImagePullBackoff)
			return true
		}
		if restarts := readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("bail1")); restarts != "" {
			t.Errorf("restarts = %q after a start that bailed out, want none", restarts)
			return true
		}
		return time.Since(windowStart) > crashLoopBailOutWindow
	})
	var recordedAttempts int
	nodeAgent.RunInAgentLoop(func() { recordedAttempts = nodeAgent.crashLoopBackoffs["bail1"].attempts })
	if recordedAttempts != 1 {
		t.Errorf("recorded %d exits, want 1: the bailed-out start is not an exit", recordedAttempts)
	}
	if starts := len(crashRuntime.startsOf("bail1")); starts != 1 {
		t.Errorf("container started %d times, want 1", starts)
	}
}
