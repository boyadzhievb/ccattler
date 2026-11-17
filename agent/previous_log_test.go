package agent

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// logRecordingRuntime is a simulator runtime that records the options of
// every Logs call and every instance whose data is removed (M234).
type logRecordingRuntime struct {
	*runtime.SimulatorRuntime                     // SimulatorRuntime runs the workloads.
	mutex                     sync.Mutex          // mutex guards the recorded values.
	lastLogsOptions           runtime.LogsOptions // lastLogsOptions are the options of the latest Logs call.
	logsCallCount             int                 // logsCallCount is the number of Logs calls.
	removedInstanceIDs        []string            // removedInstanceIDs lists every RemoveInstanceData call.
}

// Logs records the call and its options and answers with a fixed line.
func (recordingRuntime *logRecordingRuntime) Logs(_ context.Context, _ string, options runtime.LogsOptions) (io.ReadCloser, error) {
	recordingRuntime.mutex.Lock()
	recordingRuntime.lastLogsOptions = options
	recordingRuntime.logsCallCount++
	recordingRuntime.mutex.Unlock()
	return io.NopCloser(strings.NewReader("previous run output\n")), nil
}

// TestDebugLogsRefusesUnsafeInstanceID checks that the debug logs endpoint
// answers 400 for an instance parameter that is not an instance ID and never
// asks the runtime, while real IDs still reach it (M238 D7, CodeQL path-injection).
func TestDebugLogsRefusesUnsafeInstanceID(t *testing.T) {
	recordingRuntime := &logRecordingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime()}
	baseURL, cancel := startTestDebugServer(t, recordingRuntime)
	defer cancel()

	requestLogsStatus := func(instanceParameter string) int {
		response, requestError := http.Get(baseURL + "/debug/logs?previous=true&instance=" + url.QueryEscape(instanceParameter))
		if requestError != nil {
			t.Fatalf("GET /debug/logs: %v", requestError)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}
	for _, unsafeInstanceID := range []string{"../x", "a/b", "..", ".", "-x", " x", "x\n"} {
		if statusCode := requestLogsStatus(unsafeInstanceID); statusCode != http.StatusBadRequest {
			t.Errorf("instance %q: status %d, want 400", unsafeInstanceID, statusCode)
		}
	}
	recordingRuntime.mutex.Lock()
	refusedCallCount := recordingRuntime.logsCallCount
	recordingRuntime.mutex.Unlock()
	if refusedCallCount != 0 {
		t.Errorf("runtime Logs called %d times for refused instance IDs, want 0", refusedCallCount)
	}
	for _, acceptedInstanceID := range []string{"a8f31bc904", "inst-1", "cca-init-123", "db.migrate-0", "web.api-1", "ns:svc-0"} {
		if statusCode := requestLogsStatus(acceptedInstanceID); statusCode != http.StatusOK {
			t.Errorf("instance %q: status %d, want 200", acceptedInstanceID, statusCode)
		}
	}
}

// RemoveInstanceData records the instance whose data the agent removes.
func (recordingRuntime *logRecordingRuntime) RemoveInstanceData(_ context.Context, instanceID string) error {
	recordingRuntime.mutex.Lock()
	defer recordingRuntime.mutex.Unlock()
	recordingRuntime.removedInstanceIDs = append(recordingRuntime.removedInstanceIDs, instanceID)
	return nil
}

// ListInstanceDataIDs reports the instances the runtime keeps data for: the
// test's leftover names plus every workload the simulator runs.
func (recordingRuntime *logRecordingRuntime) ListInstanceDataIDs(ctx context.Context) ([]string, error) {
	instanceIDs := []string{"orphan-left-while-agent-was-down"}
	runningWorkloads, listError := recordingRuntime.List(ctx)
	if listError != nil {
		return nil, listError
	}
	for _, workloadStatus := range runningWorkloads {
		instanceIDs = append(instanceIDs, workloadStatus.ID)
	}
	return instanceIDs, nil
}

// wasRemoved reports whether RemoveInstanceData was called for instanceID.
func (recordingRuntime *logRecordingRuntime) wasRemoved(instanceID string) bool {
	recordingRuntime.mutex.Lock()
	defer recordingRuntime.mutex.Unlock()
	for _, removedInstanceID := range recordingRuntime.removedInstanceIDs {
		if removedInstanceID == instanceID {
			return true
		}
	}
	return false
}

// TestDebugLogsPassesPreviousOption checks that previous=true on the debug
// logs endpoint reaches the runtime (spec instance-logs, M234).
func TestDebugLogsPassesPreviousOption(t *testing.T) {
	recordingRuntime := &logRecordingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime()}
	baseURL, cancel := startTestDebugServer(t, recordingRuntime)
	defer cancel()

	response, requestError := http.Get(baseURL + "/debug/logs?instance=inst-1&previous=true")
	if requestError != nil {
		t.Fatalf("GET /debug/logs: %v", requestError)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	recordingRuntime.mutex.Lock()
	defer recordingRuntime.mutex.Unlock()
	if !recordingRuntime.lastLogsOptions.Previous {
		t.Error("runtime was not asked for the previous log")
	}
}

// TestAgentSweepsDataOfInstancesItNoLongerKnows checks that log directories
// left behind while the agent was down (graceful shutdown, removal meanwhile)
// are removed, and that a desired instance's data is kept (M234 review).
func TestAgentSweepsDataOfInstancesItNoLongerKnows(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recordingRuntime := &logRecordingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime()}
	nodeAgent := New("node-1", factStore, recordingRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "kept1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "kept1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 3*time.Second, "orphaned instance data swept", func() bool {
		return recordingRuntime.wasRemoved("orphan-left-while-agent-was-down")
	})
	waitFor(t, 3*time.Second, "kept1 running", func() bool {
		observedStatus, statusError := recordingRuntime.Status(ctx, "kept1")
		return statusError == nil && observedStatus.Running
	})
	if recordingRuntime.wasRemoved("kept1") {
		t.Error("the data of a desired, running instance was removed")
	}
}

// TestAgentRemovesLogsOfInstanceLeavingNode checks that an instance no
// longer placed on the node gets its runtime data (logs) removed (spec
// instance-logs, M234).
func TestAgentRemovesLogsOfInstanceLeavingNode(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recordingRuntime := &logRecordingRuntime{SimulatorRuntime: runtime.NewSimulatorRuntime()}
	nodeAgent := New("node-1", factStore, recordingRuntime)
	nodeAgent.SetInterval(50 * time.Millisecond)

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: "leave1", Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "leave1", NodeID: "node-1"})

	go nodeAgent.Run(ctx)

	waitFor(t, 3*time.Second, "instance running", func() bool {
		observedStatus, statusError := recordingRuntime.Status(ctx, "leave1")
		return statusError == nil && observedStatus.Running
	})
	types.DeletePlacement(ctx, factStore, "node-1", "leave1")
	waitFor(t, 3*time.Second, "runtime data removed for the instance that left the node", func() bool {
		return recordingRuntime.wasRemoved("leave1")
	})
}
