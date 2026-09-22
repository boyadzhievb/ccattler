package runtime

import (
	"context"
	"io"
	"testing"
	"time"
)

// runtimeFactory creates a fresh Runtime instance for each test case. Each
// implementation provides its own factory in the test matrix below.
type runtimeFactory func() Runtime

// conformanceTestCase defines a single behavioral contract test that every
// Runtime implementation must satisfy.
type conformanceTestCase struct {
	name string
	test func(t *testing.T, factory runtimeFactory)
}

// conformanceTests is the complete set of behavioral contract tests for the
// Runtime interface. Every implementation must pass all of them.
var conformanceTests = []conformanceTestCase{
	{"StartCreatesRunningWorkload", testStartCreatesRunningWorkload},
	{"StartIsIdempotent", testStartIsIdempotent},
	{"StopTerminatesWorkload", testStopTerminatesWorkload},
	{"StopIsIdempotentForUnknown", testStopIsIdempotentForUnknown},
	{"StopIsIdempotentForStopped", testStopIsIdempotentForStopped},
	{"StatusReturnsErrNotFoundForUnknown", testStatusReturnsErrNotFoundForUnknown},
	{"StatusReflectsStartStop", testStatusReflectsStartStop},
	{"ListReturnsAllWorkloads", testListReturnsAllWorkloads},
	{"ListEmptyOnFreshRuntime", testListEmptyOnFreshRuntime},
	{"StatsReturnsErrNotFoundForUnknown", testStatsReturnsErrNotFoundForUnknown},
	{"StatsReturnsValuesForRunning", testStatsReturnsValuesForRunning},
	{"LogsReturnsErrNotFoundForUnknown", testLogsReturnsErrNotFoundForUnknown},
	{"LogsReturnsReaderForRunning", testLogsReturnsReaderForRunning},
	{"MultipleWorkloadsIndependent", testMultipleWorkloadsIndependent},
	{"StartAfterStopRestartsWorkload", testStartAfterStopRestartsWorkload},
}

// TestSimulatorRuntimeConformance runs the full conformance suite against the
// SimulatorRuntime.
func TestSimulatorRuntimeConformance(t *testing.T) {
	factory := func() Runtime { return NewSimulatorRuntime() }
	for _, testCase := range conformanceTests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.test(t, factory)
		})
	}
}

// TestProcessRuntimeConformance runs the full conformance suite against the
// ProcessRuntime. Uses "sleep" as the workload command since it's available
// on all Unix systems.
func TestProcessRuntimeConformance(t *testing.T) {
	factory := func() Runtime { return NewProcessRuntime() }
	for _, testCase := range conformanceTests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.test(t, factory)
		})
	}
}

func testSpec(id string) Spec {
	return Spec{
		ID:          id,
		ServiceName: "test-service",
		Image:       "sleep 3600",
		CPUm:        500,
		MemoryB:     512 * 1024 * 1024,
	}
}

func testStartCreatesRunningWorkload(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	err := runtimeInstance.Start(testCtx, testSpec("workload-1"))
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	workloadStatus, err := runtimeInstance.Status(testCtx, "workload-1")
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !workloadStatus.Running {
		t.Error("workload should be running after Start")
	}
	if workloadStatus.ID != "workload-1" {
		t.Errorf("Status.ID = %q, want %q", workloadStatus.ID, "workload-1")
	}

	runtimeInstance.Stop(testCtx, "workload-1")
}

func testStartIsIdempotent(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))
	err := runtimeInstance.Start(testCtx, testSpec("workload-1"))
	if err != nil {
		t.Fatalf("second Start should be idempotent, got: %v", err)
	}

	workloadStatus, _ := runtimeInstance.Status(testCtx, "workload-1")
	if !workloadStatus.Running {
		t.Error("workload should still be running after idempotent Start")
	}

	runtimeInstance.Stop(testCtx, "workload-1")
}

func testStopTerminatesWorkload(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))
	err := runtimeInstance.Stop(testCtx, "workload-1")
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	workloadStatus, _ := runtimeInstance.Status(testCtx, "workload-1")
	if workloadStatus.Running {
		t.Error("workload should not be running after Stop")
	}
}

func testStopIsIdempotentForUnknown(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	err := runtimeInstance.Stop(testCtx, "nonexistent-workload")
	if err != nil {
		t.Fatalf("Stop on unknown workload should be idempotent, got: %v", err)
	}
}

func testStopIsIdempotentForStopped(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))
	runtimeInstance.Stop(testCtx, "workload-1")

	err := runtimeInstance.Stop(testCtx, "workload-1")
	if err != nil {
		t.Fatalf("second Stop should be idempotent, got: %v", err)
	}
}

func testStatusReturnsErrNotFoundForUnknown(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	_, err := runtimeInstance.Status(testCtx, "nonexistent-workload")
	if err == nil {
		t.Fatal("Status on unknown workload should return error")
	}
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func testStatusReflectsStartStop(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))

	workloadStatus, _ := runtimeInstance.Status(testCtx, "workload-1")
	if !workloadStatus.Running {
		t.Error("should be running after Start")
	}

	runtimeInstance.Stop(testCtx, "workload-1")

	workloadStatus, _ = runtimeInstance.Status(testCtx, "workload-1")
	if workloadStatus.Running {
		t.Error("should not be running after Stop")
	}
}

func testListReturnsAllWorkloads(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))
	runtimeInstance.Start(testCtx, testSpec("workload-2"))
	runtimeInstance.Start(testCtx, testSpec("workload-3"))

	workloads, err := runtimeInstance.List(testCtx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	runningCount := 0
	for _, workloadStatus := range workloads {
		if workloadStatus.Running {
			runningCount++
		}
	}
	if runningCount != 3 {
		t.Errorf("expected 3 running workloads, got %d (total listed: %d)", runningCount, len(workloads))
	}

	runtimeInstance.Stop(testCtx, "workload-1")
	runtimeInstance.Stop(testCtx, "workload-2")
	runtimeInstance.Stop(testCtx, "workload-3")
}

func testListEmptyOnFreshRuntime(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	workloads, err := runtimeInstance.List(testCtx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	runningCount := 0
	for _, workloadStatus := range workloads {
		if workloadStatus.Running {
			runningCount++
		}
	}
	if runningCount != 0 {
		t.Errorf("fresh runtime should have 0 running workloads, got %d", runningCount)
	}
}

func testStatsReturnsErrNotFoundForUnknown(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	_, err := runtimeInstance.Stats(testCtx, "nonexistent-workload")
	if err == nil {
		t.Fatal("Stats on unknown workload should return error")
	}
}

func testStatsReturnsValuesForRunning(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))

	// Give process runtime a moment to start.
	time.Sleep(50 * time.Millisecond)

	resourceStats, err := runtimeInstance.Stats(testCtx, "workload-1")
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	// We don't assert specific values since they vary by implementation,
	// but the returned struct should not have negative values.
	if resourceStats.CPUMillicores < 0 {
		t.Errorf("CPUMillicores should be >= 0, got %d", resourceStats.CPUMillicores)
	}
	if resourceStats.MemoryBytes < 0 {
		t.Errorf("MemoryBytes should be >= 0, got %d", resourceStats.MemoryBytes)
	}

	runtimeInstance.Stop(testCtx, "workload-1")
}

func testLogsReturnsErrNotFoundForUnknown(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	_, err := runtimeInstance.Logs(testCtx, "nonexistent-workload", false)
	if err == nil {
		t.Fatal("Logs on unknown workload should return error")
	}
}

func testLogsReturnsReaderForRunning(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))

	reader, err := runtimeInstance.Logs(testCtx, "workload-1", false)
	if err != nil {
		t.Fatalf("Logs failed: %v", err)
	}
	if reader == nil {
		t.Fatal("Logs should return a non-nil reader")
	}
	reader.Close()

	runtimeInstance.Stop(testCtx, "workload-1")
}

func testMultipleWorkloadsIndependent(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))
	runtimeInstance.Start(testCtx, testSpec("workload-2"))

	runtimeInstance.Stop(testCtx, "workload-1")

	status1, _ := runtimeInstance.Status(testCtx, "workload-1")
	status2, _ := runtimeInstance.Status(testCtx, "workload-2")

	if status1.Running {
		t.Error("workload-1 should be stopped")
	}
	if !status2.Running {
		t.Error("workload-2 should still be running after stopping workload-1")
	}

	runtimeInstance.Stop(testCtx, "workload-2")
}

func testStartAfterStopRestartsWorkload(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))
	runtimeInstance.Stop(testCtx, "workload-1")

	err := runtimeInstance.Start(testCtx, testSpec("workload-1"))
	if err != nil {
		t.Fatalf("Start after Stop failed: %v", err)
	}

	workloadStatus, _ := runtimeInstance.Status(testCtx, "workload-1")
	if !workloadStatus.Running {
		t.Error("workload should be running after restart")
	}

	runtimeInstance.Stop(testCtx, "workload-1")
}

// --- Failure Matrix Tests ---

// failureMatrixTests covers edge cases and error conditions that every Runtime
// implementation must handle correctly.
var failureMatrixTests = []conformanceTestCase{
	{"StartWithEmptyID", testStartWithEmptyID},
	{"ExecOnUnknownWorkload", testExecOnUnknownWorkload},
	{"ExecInitWithCommand", testExecInitWithCommand},
	{"StatsAfterStop", testStatsAfterStop},
	{"ConcurrentStartStop", testConcurrentStartStop},
	{"StartWithEnvVars", testStartWithEnvVars},
	{"LogsFollowReturnsReader", testLogsFollowReturnsReader},
}

func TestSimulatorRuntimeFailureMatrix(t *testing.T) {
	factory := func() Runtime { return NewSimulatorRuntime() }
	for _, testCase := range failureMatrixTests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.test(t, factory)
		})
	}
}

func TestProcessRuntimeFailureMatrix(t *testing.T) {
	factory := func() Runtime { return NewProcessRuntime() }
	for _, testCase := range failureMatrixTests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.test(t, factory)
		})
	}
}

func testStartWithEmptyID(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	spec := testSpec("")
	err := runtimeInstance.Start(testCtx, spec)
	// Implementations may or may not error — the contract is that it should
	// not panic and should leave the runtime in a consistent state.
	_ = err

	workloads, listErr := runtimeInstance.List(testCtx)
	if listErr != nil {
		t.Fatalf("List should still work after empty-ID Start: %v", listErr)
	}
	_ = workloads

	runtimeInstance.Stop(testCtx, "")
}

func testExecOnUnknownWorkload(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	err := runtimeInstance.Exec(testCtx, "nonexistent", ExecSpec{Command: "echo hello"})
	if err == nil {
		t.Error("Exec on unknown workload should return error")
	}
}

func testExecInitWithCommand(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	err := runtimeInstance.ExecInit(testCtx, "sleep 3600", ExecSpec{Command: "echo init"})
	// SimulatorRuntime always succeeds; ProcessRuntime runs the command.
	// The contract is no panic and consistent state.
	_ = err
}

func testStatsAfterStop(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))
	runtimeInstance.Stop(testCtx, "workload-1")

	resourceStats, err := runtimeInstance.Stats(testCtx, "workload-1")
	// After stop, Stats may return error or zero values — both are valid.
	if err == nil {
		if resourceStats.CPUMillicores < 0 || resourceStats.MemoryBytes < 0 {
			t.Error("stats should have non-negative values even for stopped workloads")
		}
	}
}

func testConcurrentStartStop(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	done := make(chan struct{})
	go func() {
		for range 20 {
			runtimeInstance.Start(testCtx, testSpec("workload-1"))
		}
		close(done)
	}()
	for range 20 {
		runtimeInstance.Stop(testCtx, "workload-1")
	}
	<-done

	// The workload should be in a consistent state (running or stopped, not corrupted).
	workloadStatus, err := runtimeInstance.Status(testCtx, "workload-1")
	if err != nil && err != ErrNotFound {
		t.Fatalf("Status should return valid state or ErrNotFound, got: %v", err)
	}
	_ = workloadStatus

	runtimeInstance.Stop(testCtx, "workload-1")
}

func testStartWithEnvVars(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	spec := testSpec("workload-1")
	spec.Env = map[string]string{
		"FOO": "bar",
		"BAZ": "qux",
	}
	err := runtimeInstance.Start(testCtx, spec)
	if err != nil {
		t.Fatalf("Start with env vars failed: %v", err)
	}

	workloadStatus, _ := runtimeInstance.Status(testCtx, "workload-1")
	if !workloadStatus.Running {
		t.Error("workload should be running with env vars")
	}

	runtimeInstance.Stop(testCtx, "workload-1")
}

func testLogsFollowReturnsReader(t *testing.T, factory runtimeFactory) {
	runtimeInstance := factory()
	testCtx := context.Background()

	runtimeInstance.Start(testCtx, testSpec("workload-1"))

	reader, err := runtimeInstance.Logs(testCtx, "workload-1", true)
	if err != nil {
		t.Fatalf("Logs(follow=true) failed: %v", err)
	}
	if reader == nil {
		t.Fatal("Logs(follow=true) should return a non-nil reader")
	}

	// Stop first so the pipe drains, then close the reader.
	runtimeInstance.Stop(testCtx, "workload-1")
	reader.Close()
}

// --- Exec Failure Matrix ---

func TestSimulatorExecFailure(t *testing.T) {
	simulatorRuntime := NewSimulatorRuntime()
	simulatorRuntime.ExecFailures = map[string]bool{"workload-1": true}

	simulatorRuntime.Start(context.Background(), testSpec("workload-1"))

	err := simulatorRuntime.Exec(context.Background(), "workload-1", ExecSpec{Command: "will-fail"})
	if err == nil {
		t.Error("Exec should fail when configured to fail")
	}
}

func TestProcessRuntimeStartRejected(t *testing.T) {
	processRuntime := NewProcessRuntime()

	spec := Spec{
		ID:    "bad-workload",
		Image: "nonexistent-binary-that-does-not-exist-xyz",
	}
	err := processRuntime.Start(context.Background(), spec)
	if err == nil {
		t.Error("Start with nonexistent binary should fail")
	}
}

func TestProcessRuntimeStartContainerImageRejected(t *testing.T) {
	processRuntime := NewProcessRuntime()

	spec := Spec{
		ID:    "container-workload",
		Image: "nginx:1.27",
	}
	err := processRuntime.Start(context.Background(), spec)
	if err == nil {
		t.Error("Start with container image should fail for ProcessRuntime")
	}
}

// Verify that Logs reader for ProcessRuntime implements io.ReadCloser properly.
func TestProcessRuntimeLogsClose(t *testing.T) {
	processRuntime := NewProcessRuntime()
	testCtx := context.Background()

	processRuntime.Start(testCtx, testSpec("workload-1"))
	time.Sleep(50 * time.Millisecond)

	reader, err := processRuntime.Logs(testCtx, "workload-1", false)
	if err != nil {
		t.Fatalf("Logs failed: %v", err)
	}

	var _ io.ReadCloser = reader
	reader.Close()

	processRuntime.Stop(testCtx, "workload-1")
}
