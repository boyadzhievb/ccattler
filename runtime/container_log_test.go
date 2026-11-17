package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRunLog writes content as the instance's current run log under logBaseDirectory.
func writeRunLog(t *testing.T, logBaseDirectory string, instanceID string, content string) {
	t.Helper()
	currentLogPath := instanceRunLogPath(logBaseDirectory, instanceID)
	if mkdirError := os.MkdirAll(filepath.Dir(currentLogPath), 0o750); mkdirError != nil {
		t.Fatalf("mkdir: %v", mkdirError)
	}
	if writeError := os.WriteFile(currentLogPath, []byte(content), 0o600); writeError != nil {
		t.Fatalf("write: %v", writeError)
	}
}

// readLogFile returns a log file's content, or "" when it does not exist.
func readLogFile(t *testing.T, logPath string) string {
	t.Helper()
	content, readError := os.ReadFile(logPath) // #nosec G304 — test temp dir
	if readError != nil {
		if os.IsNotExist(readError) {
			return ""
		}
		t.Fatalf("read %s: %v", logPath, readError)
	}
	return string(content)
}

// TestContainerdStartKeepsPreviousRunLog checks that each new run moves the
// run before it to the previous log, and that only one previous log is kept
// (spec instance-logs, M234).
func TestContainerdStartKeepsPreviousRunLog(t *testing.T) {
	logBaseDirectory := t.TempDir()
	const instanceID = "inst-1"

	if rotateError := rotateInstanceRunLog(logBaseDirectory, instanceID); rotateError != nil {
		t.Fatalf("first start, no log yet: %v", rotateError)
	}
	for _, runOutput := range []string{"first\n", "second\n", "third\n"} {
		if rotateError := rotateInstanceRunLog(logBaseDirectory, instanceID); rotateError != nil {
			t.Fatalf("rotate before %q: %v", runOutput, rotateError)
		}
		writeRunLog(t, logBaseDirectory, instanceID, runOutput)
	}

	previousLog := readLogFile(t, instanceRunLogPath(logBaseDirectory, instanceID)+previousRunLogSuffix)
	currentLog := readLogFile(t, instanceRunLogPath(logBaseDirectory, instanceID))
	if previousLog != "second\n" {
		t.Errorf("previous log = %q, want the second run only", previousLog)
	}
	if currentLog != "third\n" {
		t.Errorf("current log = %q, want the third run only", currentLog)
	}
}

// TestContainerdLogsPreviousWithoutPreviousRun checks the previous log of an
// instance started once is an error naming the instance, and that following
// a previous log is refused (spec instance-logs, M234).
func TestContainerdLogsPreviousWithoutPreviousRun(t *testing.T) {
	logBaseDirectory := t.TempDir()
	const instanceID = "inst-2"
	writeRunLog(t, logBaseDirectory, instanceID, "only run\n")

	_, openError := openPreviousRunLog(logBaseDirectory, instanceID, LogsOptions{Previous: true})
	if openError == nil || !strings.Contains(openError.Error(), instanceID) || !strings.Contains(openError.Error(), "no previous run") {
		t.Fatalf("error = %v, want one naming %s with no previous run", openError, instanceID)
	}

	if rotateError := rotateInstanceRunLog(logBaseDirectory, instanceID); rotateError != nil {
		t.Fatalf("rotate: %v", rotateError)
	}
	previousReader, openError := openPreviousRunLog(logBaseDirectory, instanceID, LogsOptions{Previous: true})
	if openError != nil {
		t.Fatalf("previous log after a restart: %v", openError)
	}
	previousContent, _ := io.ReadAll(previousReader)
	_ = previousReader.Close()
	if !strings.Contains(string(previousContent), "only run") {
		t.Errorf("previous log = %q, want the first run", previousContent)
	}

	if _, followError := openPreviousRunLog(logBaseDirectory, instanceID, LogsOptions{Previous: true, Follow: true}); followError == nil {
		t.Error("following a previous log must be refused: a finished run does not grow")
	}
}

// TestInstanceIDPatternAcceptsInstanceIDsOnly checks the one anchored pattern
// every log path is guarded with: generated hex IDs, stateful and job IDs from
// DSL names (letters incl. Unicode, digits, "_", "-", ".", ":"), test IDs and
// init step names pass; anything that could leave the log base does not (M238 D7, B4).
func TestInstanceIDPatternAcceptsInstanceIDsOnly(t *testing.T) {
	acceptedInstanceIDs := []string{
		"a8f31bc904", "inst-1", "cca-init-123", "web_1", "Inst1", "0",
		"db.migrate-0", "web.api-1", "ns:svc-0", "_internal-2", "café-0", "сервис.api-3",
	}
	for _, acceptedInstanceID := range acceptedInstanceIDs {
		if !InstanceIDPattern.MatchString(acceptedInstanceID) {
			t.Errorf("instance ID %q was refused", acceptedInstanceID)
		}
	}
	refusedInstanceIDs := []string{
		"", ".", "..", "../x", "a/b", "-x", ".x", ":x", "_x/", " x", "a b", "x\n", "a\x00b", "a\tb", `a\b`,
	}
	for _, refusedInstanceID := range refusedInstanceIDs {
		if InstanceIDPattern.MatchString(refusedInstanceID) {
			t.Errorf("instance ID %q was accepted", refusedInstanceID)
		}
	}
}

// TestContainerdLogsRefusesUnsafeInstanceID checks that ContainerdRuntime.Logs
// refuses an instance ID that is not an instance ID before it builds a path
// or asks containerd (no client is set: a call would panic) (M238 D7).
func TestContainerdLogsRefusesUnsafeInstanceID(t *testing.T) {
	containerdRuntime := &ContainerdRuntime{}
	for _, unsafeInstanceID := range []string{"../x", "a/b", "..", ""} {
		for _, previous := range []bool{false, true} {
			if _, logsError := containerdRuntime.Logs(context.Background(), unsafeInstanceID, LogsOptions{Previous: previous}); logsError == nil ||
				!strings.Contains(logsError.Error(), "invalid instance ID") {
				t.Errorf("instance ID %q (previous %v): error %v, want invalid instance ID", unsafeInstanceID, previous, logsError)
			}
		}
	}
}

// TestOpenPreviousRunLogRefusesPathOutsideBase checks that an instance ID from
// the debug API cannot read a file outside the log directory (M234 review).
func TestOpenPreviousRunLogRefusesPathOutsideBase(t *testing.T) {
	logBaseDirectory := t.TempDir()
	for _, unsafeInstanceID := range []string{"../x", "a/b", "..", ""} {
		if _, openError := openPreviousRunLog(logBaseDirectory, unsafeInstanceID, LogsOptions{Previous: true}); openError == nil ||
			strings.Contains(openError.Error(), "no previous run") {
			t.Errorf("instance ID %q: error %v, want it refused as not a plain name", unsafeInstanceID, openError)
		}
	}
}

// TestRotateKeepsPreviousLogWhenCurrentIsEmpty checks that a start that left
// an empty log (it failed after the task was created) does not replace the
// real previous run (M234 review).
func TestRotateKeepsPreviousLogWhenCurrentIsEmpty(t *testing.T) {
	logBaseDirectory := t.TempDir()
	const instanceID = "inst-4"
	writeRunLog(t, logBaseDirectory, instanceID, "the run that exited\n")
	if rotateError := rotateInstanceRunLog(logBaseDirectory, instanceID); rotateError != nil {
		t.Fatalf("rotate: %v", rotateError)
	}
	writeRunLog(t, logBaseDirectory, instanceID, "") // failed start: empty log
	if rotateError := rotateInstanceRunLog(logBaseDirectory, instanceID); rotateError != nil {
		t.Fatalf("rotate: %v", rotateError)
	}
	if previousLog := readLogFile(t, instanceRunLogPath(logBaseDirectory, instanceID)+previousRunLogSuffix); previousLog != "the run that exited\n" {
		t.Errorf("previous log = %q, want the run that exited", previousLog)
	}
}

// TestOtherRuntimesRefusePreviousLog checks the runtimes without previous
// logs say so (spec instance-logs).
func TestOtherRuntimesRefusePreviousLog(t *testing.T) {
	for runtimeName, runtimeAdapter := range map[string]Runtime{
		"simulator": NewSimulatorRuntime(),
		"process":   &ProcessRuntime{},
		"nerdctl":   &ContainerRuntime{},
	} {
		if _, logsError := runtimeAdapter.Logs(context.Background(), "inst-5", LogsOptions{Previous: true}); !errors.Is(logsError, ErrPreviousLogNotAvailable) {
			t.Errorf("%s: error %v, want ErrPreviousLogNotAvailable", runtimeName, logsError)
		}
	}
}

// TestListInstanceDataNamesSkipsInitDirectories checks the sweep only sees
// instance directories, never a running init step's directory.
func TestListInstanceDataNamesSkipsInitDirectories(t *testing.T) {
	logBaseDirectory := t.TempDir()
	writeRunLog(t, logBaseDirectory, "inst-6", "run\n")
	writeRunLog(t, logBaseDirectory, "db.migrate-0", "run\n") // stateful/job ID from a dotted DSL name (B4)
	if mkdirError := os.MkdirAll(filepath.Join(logBaseDirectory, initContainerNamePrefix+"123"), 0o750); mkdirError != nil {
		t.Fatalf("mkdir: %v", mkdirError)
	}
	instanceIDs, listError := listInstanceLogDirectories(logBaseDirectory)
	if listError != nil {
		t.Fatalf("list: %v", listError)
	}
	if strings.Join(instanceIDs, ",") != "db.migrate-0,inst-6" {
		t.Errorf("listed %v, want db.migrate-0 and inst-6", instanceIDs)
	}
}

// TestRemoveInstanceLogDirectoryStaysUnderBase checks the removal deletes the
// instance's directory and refuses an instance ID that is not a plain name.
func TestRemoveInstanceLogDirectoryStaysUnderBase(t *testing.T) {
	logBaseDirectory := t.TempDir()
	writeRunLog(t, logBaseDirectory, "inst-3", "run\n")
	if removeError := removeInstanceLogDirectory(logBaseDirectory, "inst-3"); removeError != nil {
		t.Fatalf("remove: %v", removeError)
	}
	if _, statError := os.Stat(filepath.Join(logBaseDirectory, "inst-3")); !os.IsNotExist(statError) {
		t.Errorf("log directory still exists (stat error %v)", statError)
	}
	writeRunLog(t, logBaseDirectory, "web.api-1", "run\n") // stateful ID from a dotted DSL name (B4)
	if removeError := removeInstanceLogDirectory(logBaseDirectory, "web.api-1"); removeError != nil {
		t.Fatalf("remove web.api-1: %v", removeError)
	}
	if _, statError := os.Stat(filepath.Join(logBaseDirectory, "web.api-1")); !os.IsNotExist(statError) {
		t.Errorf("log directory of web.api-1 still exists (stat error %v)", statError)
	}
	for _, unsafeInstanceID := range []string{"", ".", "..", "../other", "a/b"} {
		if removeError := removeInstanceLogDirectory(logBaseDirectory, unsafeInstanceID); removeError == nil {
			t.Errorf("instance ID %q was accepted", unsafeInstanceID)
		}
	}
}
