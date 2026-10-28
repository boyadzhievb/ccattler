// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// containerdTestImage is the OCI image used for containerd conformance tests.
// busybox is small and available on all registries.
const containerdTestImage = "docker.io/library/busybox:latest"

// skipIfNoContainerd skips the test when the containerd socket is not accessible
// (not running, or not root). Tests are inherently integration tests requiring a
// running containerd daemon.
func skipIfNoContainerd(t *testing.T) {
	t.Helper()

	currentUser, userError := user.Current()
	if userError == nil && currentUser.Uid != "0" {
		t.Skip("containerd conformance tests require root")
	}

	socketPath := defaultContainerdSocketPath
	if envSocket := os.Getenv("CONTAINERD_ADDRESS"); envSocket != "" {
		socketPath = envSocket
	}

	connection, dialError := net.DialTimeout("unix", socketPath, 2*time.Second) // #nosec G704 — test-only, socket path from env or constant
	if dialError != nil {
		t.Skipf("containerd socket not available at %s: %v", socketPath, dialError)
	}
	_ = connection.Close()
}

// ensureTestImagePulled pulls the test image if it's not already cached. Called
// once per test to ensure the image is available for container creation.
func ensureTestImagePulled(t *testing.T, containerdRuntime *ContainerdRuntime) {
	t.Helper()
	ctx := context.Background()

	if containerdRuntime.IsImageCached(ctx, containerdTestImage) {
		return
	}

	pullError := containerdRuntime.PullImage(ctx, containerdTestImage, "")
	if pullError != nil {
		t.Fatalf("failed to pull test image %s: %v", containerdTestImage, pullError)
	}
}

// containerdTestSpec creates a Spec for containerd conformance tests using a
// real OCI image. The command runs "sleep 3600" inside busybox.
func containerdTestSpec(instanceID string) Spec {
	return Spec{
		ID:          instanceID,
		ServiceName: "test-service",
		Image:       containerdTestImage,
		Command:     []string{"sleep", "3600"},
		CPUm:        500,
		MemoryB:     64 * 1024 * 1024,
	}
}

// TestContainerdRuntimeConformance runs the behavioral conformance suite against
// the ContainerdRuntime. Skipped when containerd is not available.
func TestContainerdRuntimeConformance(t *testing.T) {
	skipIfNoContainerd(t)

	baseRuntime, connectError := NewContainerdRuntime("")
	if connectError != nil {
		t.Fatalf("failed to connect to containerd: %v", connectError)
	}
	defer baseRuntime.Close()

	ensureTestImagePulled(t, baseRuntime)
	_ = baseRuntime.Close()

	factory := func() Runtime {
		containerdRuntime, connectionError := NewContainerdRuntime("")
		if connectionError != nil {
			t.Fatalf("failed to create containerd runtime: %v", connectionError)
		}
		return containerdRuntime
	}

	// Run conformance tests that don't depend on the testSpec helper.
	// These tests use testSpec() which has Image: "sleep 3600" — that doesn't
	// work for containerd (needs a real OCI image). We run the subset that
	// only exercises status/stop on unknown IDs, then run containerd-specific
	// lifecycle tests below.
	noImageTests := []conformanceTestCase{
		{"StopIsIdempotentForUnknown", testStopIsIdempotentForUnknown},
		{"StatusReturnsErrNotFoundForUnknown", testStatusReturnsErrNotFoundForUnknown},
		{"ListEmptyOnFreshRuntime", testListEmptyOnFreshRuntime},
		{"StatsReturnsErrNotFoundForUnknown", testStatsReturnsErrNotFoundForUnknown},
		{"LogsReturnsErrNotFoundForUnknown", testLogsReturnsErrNotFoundForUnknown},
	}
	for _, testCase := range noImageTests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.test(t, factory)
		})
	}

	// Containerd-specific lifecycle tests using real images.
	t.Run("StartCreatesRunningContainer", testContainerdStartCreatesRunning)
	t.Run("StartIsIdempotent", testContainerdStartIsIdempotent)
	t.Run("StopTerminatesContainer", testContainerdStopTerminates)
	t.Run("StatusReflectsStartStop", testContainerdStatusReflectsStartStop)
	t.Run("ListReturnsAllContainers", testContainerdListReturnsAll)
	t.Run("StatsReturnsValues", testContainerdStatsReturnsValues)
	t.Run("MultipleContainersIndependent", testContainerdMultipleIndependent)
	t.Run("StartAfterStopRestarts", testContainerdStartAfterStopRestarts)
	t.Run("ExecRunsCommand", testContainerdExecRunsCommand)
	t.Run("ExecCaptureReturnsOutput", testContainerdExecCaptureOutput)
	t.Run("PullAndIsImageCached", testContainerdPullAndCache)
	t.Run("StopCleansResources", testContainerdStopCleansResources)
}

func newTestContainerdRuntime(t *testing.T) *ContainerdRuntime {
	t.Helper()
	containerdRuntime, connectionError := NewContainerdRuntime("")
	if connectionError != nil {
		t.Fatalf("failed to connect to containerd: %v", connectionError)
	}
	return containerdRuntime
}

func testContainerdStartCreatesRunning(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	startError := containerdRuntime.Start(testCtx, containerdTestSpec("ctest-start-1"))
	if startError != nil {
		t.Fatalf("Start failed: %v", startError)
	}
	defer containerdRuntime.Stop(testCtx, "ctest-start-1")

	workloadStatus, statusError := containerdRuntime.Status(testCtx, "ctest-start-1")
	if statusError != nil {
		t.Fatalf("Status failed: %v", statusError)
	}
	if !workloadStatus.Running {
		t.Error("container should be running after Start")
	}
}

func testContainerdStartIsIdempotent(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-idem-1"))
	defer containerdRuntime.Stop(testCtx, "ctest-idem-1")

	secondStartError := containerdRuntime.Start(testCtx, containerdTestSpec("ctest-idem-1"))
	if secondStartError != nil {
		t.Fatalf("second Start should be idempotent, got: %v", secondStartError)
	}
}

func testContainerdStopTerminates(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-stop-1"))
	stopError := containerdRuntime.Stop(testCtx, "ctest-stop-1")
	if stopError != nil {
		t.Fatalf("Stop failed: %v", stopError)
	}

	workloadStatus, _ := containerdRuntime.Status(testCtx, "ctest-stop-1")
	if workloadStatus.Running {
		t.Error("container should not be running after Stop")
	}
}

func testContainerdStatusReflectsStartStop(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-status-1"))

	workloadStatus, _ := containerdRuntime.Status(testCtx, "ctest-status-1")
	if !workloadStatus.Running {
		t.Error("should be running after Start")
	}

	containerdRuntime.Stop(testCtx, "ctest-status-1")

	workloadStatus, _ = containerdRuntime.Status(testCtx, "ctest-status-1")
	if workloadStatus.Running {
		t.Error("should not be running after Stop")
	}
}

func testContainerdListReturnsAll(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-list-1"))
	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-list-2"))
	defer containerdRuntime.Stop(testCtx, "ctest-list-1")
	defer containerdRuntime.Stop(testCtx, "ctest-list-2")

	workloads, listError := containerdRuntime.List(testCtx)
	if listError != nil {
		t.Fatalf("List failed: %v", listError)
	}

	runningCount := 0
	for _, workloadStatus := range workloads {
		if workloadStatus.Running {
			runningCount++
		}
	}
	if runningCount < 2 {
		t.Errorf("expected at least 2 running containers, got %d", runningCount)
	}
}

func testContainerdStatsReturnsValues(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-stats-1"))
	defer containerdRuntime.Stop(testCtx, "ctest-stats-1")

	time.Sleep(500 * time.Millisecond)

	resourceStats, statsError := containerdRuntime.Stats(testCtx, "ctest-stats-1")
	if statsError != nil {
		t.Fatalf("Stats failed: %v", statsError)
	}
	if resourceStats.MemoryBytes <= 0 {
		t.Errorf("MemoryBytes should be > 0 for a running container, got %d", resourceStats.MemoryBytes)
	}
	if resourceStats.CPUMillicores < 0 {
		t.Errorf("CPUMillicores should be >= 0, got %d", resourceStats.CPUMillicores)
	}
}

func testContainerdMultipleIndependent(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-multi-1"))
	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-multi-2"))
	defer containerdRuntime.Stop(testCtx, "ctest-multi-2")

	containerdRuntime.Stop(testCtx, "ctest-multi-1")

	status1, _ := containerdRuntime.Status(testCtx, "ctest-multi-1")
	status2, _ := containerdRuntime.Status(testCtx, "ctest-multi-2")

	if status1.Running {
		t.Error("ctest-multi-1 should be stopped")
	}
	if !status2.Running {
		t.Error("ctest-multi-2 should still be running")
	}
}

func testContainerdStartAfterStopRestarts(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-restart-1"))
	containerdRuntime.Stop(testCtx, "ctest-restart-1")

	restartError := containerdRuntime.Start(testCtx, containerdTestSpec("ctest-restart-1"))
	if restartError != nil {
		t.Fatalf("Start after Stop failed: %v", restartError)
	}
	defer containerdRuntime.Stop(testCtx, "ctest-restart-1")

	workloadStatus, _ := containerdRuntime.Status(testCtx, "ctest-restart-1")
	if !workloadStatus.Running {
		t.Error("container should be running after restart")
	}
}

func testContainerdExecRunsCommand(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-exec-1"))
	defer containerdRuntime.Stop(testCtx, "ctest-exec-1")

	time.Sleep(200 * time.Millisecond)

	execError := containerdRuntime.Exec(testCtx, "ctest-exec-1", ExecSpec{
		Command: "echo hello",
	})
	if execError != nil {
		t.Fatalf("Exec failed: %v", execError)
	}
}

func testContainerdExecCaptureOutput(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	containerdRuntime.Start(testCtx, containerdTestSpec("ctest-execcap-1"))
	defer containerdRuntime.Stop(testCtx, "ctest-execcap-1")

	time.Sleep(200 * time.Millisecond)

	output, captureError := containerdRuntime.ExecCapture(testCtx, "ctest-execcap-1", ExecSpec{
		Command: "echo containerd-test-output",
	})
	if captureError != nil {
		t.Fatalf("ExecCapture failed: %v", captureError)
	}

	outputString := string(output)
	if !contains(outputString, "containerd-test-output") {
		t.Errorf("expected output to contain 'containerd-test-output', got: %q", outputString)
	}
}

func testContainerdPullAndCache(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	if !containerdRuntime.IsImageCached(testCtx, containerdTestImage) {
		pullError := containerdRuntime.PullImage(testCtx, containerdTestImage, "")
		if pullError != nil {
			t.Fatalf("PullImage failed: %v", pullError)
		}
	}

	if !containerdRuntime.IsImageCached(testCtx, containerdTestImage) {
		t.Error("image should be cached after pull")
	}

	images, listError := containerdRuntime.ListImages(testCtx)
	if listError != nil {
		t.Fatalf("ListImages failed: %v", listError)
	}

	found := false
	for _, imageInfo := range images {
		if contains(imageInfo.Repository, "busybox") {
			found = true
			break
		}
	}
	if !found {
		t.Error("busybox should appear in ListImages after pull")
	}
}

// testContainerdStopCleansResources verifies that Stop() fully removes a
// container's resources: the container disappears from List(), the log
// directory is deleted, and Status() returns ErrNotFound.
func testContainerdStopCleansResources(t *testing.T) {
	skipIfNoContainerd(t)
	containerdRuntime := newTestContainerdRuntime(t)
	defer containerdRuntime.Close()
	testCtx := context.Background()

	instanceID := "ctest-cleanup-1"
	containerdRuntime.Start(testCtx, containerdTestSpec(instanceID))

	logDirectory := filepath.Join(containerdLogBaseDir, instanceID)
	if _, statError := os.Stat(logDirectory); os.IsNotExist(statError) {
		t.Fatalf("log directory %s should exist after Start", logDirectory)
	}

	workloadStatus, statusError := containerdRuntime.Status(testCtx, instanceID)
	if statusError != nil {
		t.Fatalf("Status failed after Start: %v", statusError)
	}
	if !workloadStatus.Running {
		t.Fatal("container should be running after Start")
	}

	stopError := containerdRuntime.Stop(testCtx, instanceID)
	if stopError != nil {
		t.Fatalf("Stop failed: %v", stopError)
	}

	if _, statError := os.Stat(logDirectory); !os.IsNotExist(statError) {
		t.Errorf("log directory %s should be removed after Stop", logDirectory)
	}

	statusList, listError := containerdRuntime.List(testCtx)
	if listError != nil {
		t.Fatalf("List failed after Stop: %v", listError)
	}
	for _, entry := range statusList {
		if entry.ID == instanceID {
			t.Errorf("container %s should not appear in List after Stop", instanceID)
		}
	}

	_, statusError = containerdRuntime.Status(testCtx, instanceID)
	if statusError != ErrNotFound {
		t.Errorf("Status after Stop should return ErrNotFound, got %v", statusError)
	}
}

// contains checks if a string contains a substring.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && searchSubstring(haystack, needle)
}

// searchSubstring scans for needle in haystack.
func searchSubstring(haystack, needle string) bool {
	for position := 0; position <= len(haystack)-len(needle); position++ {
		if haystack[position:position+len(needle)] == needle {
			return true
		}
	}
	return false
}
