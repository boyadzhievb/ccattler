package runtime

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProcessRuntime runs workloads as OS processes on the local machine.
// The Spec.Image field is interpreted as a shell command string
// (e.g. "python3 -m http.server 8080"), which is split on whitespace
// and executed directly (no shell involved).
type ProcessRuntime struct {
	mu          sync.Mutex                       // mu guards concurrent access to the processes map.
	processes   map[string]*managedProcess       // processes maps workload IDs to their managed process state.
	gracePeriod time.Duration                    // gracePeriod is the time to wait between SIGTERM and SIGKILL during shutdown.
}

// managedProcess tracks a single OS process launched by the ProcessRuntime,
// including its specification, exec handle, and completion state.
type managedProcess struct {
	workloadSpec    Spec         // workloadSpec is the specification that was used to start this process.
	command         *exec.Cmd   // command is the exec handle for the running OS process.
	completionSignal chan struct{} // completionSignal is closed when the process exits, signaling waiters.
	exitError       error        // exitError holds the error returned by cmd.Wait, or nil if the process exited cleanly.
}

// NewProcessRuntime creates a ProcessRuntime with an empty process registry
// and a default 10-second grace period for graceful shutdown.
func NewProcessRuntime() *ProcessRuntime {
	return &ProcessRuntime{
		processes:   make(map[string]*managedProcess),
		gracePeriod: 10 * time.Second,
	}
}

// SetGracePeriod configures the duration the runtime waits after sending
// SIGTERM before escalating to SIGKILL when stopping a process.
func (processRuntime *ProcessRuntime) SetGracePeriod(d time.Duration) {
	processRuntime.gracePeriod = d
}

// Start launches the workload described by spec as an OS process. If the
// workload is already running, this is a no-op (idempotent). The Spec.Image
// field is split on whitespace to form the command and arguments. Returns a
// StartError if the command is empty or fails to launch.
func (processRuntime *ProcessRuntime) Start(_ context.Context, spec Spec) error {
	processRuntime.mu.Lock()
	defer processRuntime.mu.Unlock()

	if process, ok := processRuntime.processes[spec.ID]; ok {
		if process.isRunning() {
			return nil
		}
	}

	args := strings.Fields(spec.Image)
	if len(args) == 0 {
		return &StartError{ID: spec.ID, Reason: "empty command"}
	}

	command := exec.Command(args[0], args[1:]...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	for k, v := range spec.Env {
		command.Env = append(command.Env, k+"="+v)
	}
	if len(spec.Env) > 0 {
		command.Env = append(os.Environ(), command.Env...)
	}

	if err := command.Start(); err != nil {
		return &StartError{ID: spec.ID, Reason: err.Error()}
	}

	managedProc := &managedProcess{
		workloadSpec:     spec,
		command:          command,
		completionSignal: make(chan struct{}),
	}
	go func() {
		managedProc.exitError = command.Wait()
		close(managedProc.completionSignal)
	}()

	processRuntime.processes[spec.ID] = managedProc
	return nil
}

// Stop gracefully terminates the process identified by id. It sends SIGTERM
// first, waits up to the configured grace period, and then sends SIGKILL if the
// process has not exited. Idempotent — returns nil if the process is already
// stopped or was never started.
func (processRuntime *ProcessRuntime) Stop(_ context.Context, id string) error {
	processRuntime.mu.Lock()
	process, ok := processRuntime.processes[id]
	processRuntime.mu.Unlock()

	if !ok || !process.isRunning() {
		return nil
	}

	// SIGTERM → grace period → SIGKILL
	process.command.Process.Signal(syscall.SIGTERM)

	select {
	case <-process.completionSignal:
		return nil
	case <-time.After(processRuntime.gracePeriod):
		process.command.Process.Signal(syscall.SIGKILL)
		<-process.completionSignal
		return nil
	}
}

// Status returns the current observed state of the process identified by id,
// including its PID and exit code. Returns ErrNotFound if the workload has
// never been started.
func (processRuntime *ProcessRuntime) Status(_ context.Context, id string) (Status, error) {
	processRuntime.mu.Lock()
	defer processRuntime.mu.Unlock()

	process, ok := processRuntime.processes[id]
	if !ok {
		return Status{}, ErrNotFound
	}

	processStatus := Status{
		ID:  id,
		PID: process.command.Process.Pid,
	}

	if process.isRunning() {
		processStatus.Running = true
	} else {
		if process.command.ProcessState != nil {
			processStatus.ExitCode = process.command.ProcessState.ExitCode()
		}
		if process.exitError != nil {
			processStatus.Error = process.exitError.Error()
		}
	}

	return processStatus, nil
}

// List returns the current state of every process the runtime has launched,
// including those that have already exited.
func (processRuntime *ProcessRuntime) List(_ context.Context) ([]Status, error) {
	processRuntime.mu.Lock()
	defer processRuntime.mu.Unlock()

	var result []Status
	for id, process := range processRuntime.processes {
		processStatus := Status{ID: id, PID: process.command.Process.Pid}
		if process.isRunning() {
			processStatus.Running = true
		} else {
			if process.command.ProcessState != nil {
				processStatus.ExitCode = process.command.ProcessState.ExitCode()
			}
		}
		result = append(result, processStatus)
	}
	return result, nil
}

// StopAll gracefully stops every process the runtime is tracking. It is
// typically called during application shutdown to clean up child processes.
func (processRuntime *ProcessRuntime) StopAll(ctx context.Context) {
	processRuntime.mu.Lock()
	ids := make([]string, 0, len(processRuntime.processes))
	for id := range processRuntime.processes {
		ids = append(ids, id)
	}
	processRuntime.mu.Unlock()

	for _, id := range ids {
		processRuntime.Stop(ctx, id)
	}
}

// isRunning reports whether the managed process is still executing. It checks
// the completionSignal channel without blocking — if it is closed, the process
// has exited.
func (managedProc *managedProcess) isRunning() bool {
	select {
	case <-managedProc.completionSignal:
		return false
	default:
		return true
	}
}

// StartError represents a failure to launch a workload. It captures the
// workload ID and a human-readable reason for the failure.
type StartError struct {
	ID     string // ID is the workload identifier that failed to start.
	Reason string // Reason is a human-readable description of why the start failed.
}

// Error returns a formatted error string including the workload ID and failure reason.
func (e *StartError) Error() string {
	return "failed to start " + e.ID + ": " + e.Reason
}
