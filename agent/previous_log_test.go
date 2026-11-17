package agent

import (
	"context"
	"io"
	"net/http"
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
	removedInstanceIDs        []string            // removedInstanceIDs lists every RemoveInstanceData call.
}

// Logs records the options and answers with a fixed line.
func (recordingRuntime *logRecordingRuntime) Logs(_ context.Context, _ string, options runtime.LogsOptions) (io.ReadCloser, error) {
	recordingRuntime.mutex.Lock()
	recordingRuntime.lastLogsOptions = options
	recordingRuntime.mutex.Unlock()
	return io.NopCloser(strings.NewReader("previous run output\n")), nil
}

// RemoveInstanceData records the instance whose data the agent removes.
func (recordingRuntime *logRecordingRuntime) RemoveInstanceData(_ context.Context, instanceID string) error {
	recordingRuntime.mutex.Lock()
	defer recordingRuntime.mutex.Unlock()
	recordingRuntime.removedInstanceIDs = append(recordingRuntime.removedInstanceIDs, instanceID)
	return nil
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
