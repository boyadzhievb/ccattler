// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ErrNotFound is returned when a workload lookup fails because the requested
// workload ID has never been registered with the runtime.
var ErrNotFound = errors.New("workload not found")

// ErrResizeUnsupported is returned by Resize when the runtime does not support
// live resource updates. The caller should stop and restart the workload.
var ErrResizeUnsupported = errors.New("live resize not supported")

// SimulatorRuntime is a fake runtime for testing and semantic validation.
// It tracks workload state in memory without launching any real processes or
// containers, allowing the full reconciliation loop to be exercised cheaply.
type SimulatorRuntime struct {
	mutex         sync.Mutex                    // mutex guards concurrent access to the workloads map.
	workloads     map[string]*simulatedWorkload // workloads maps workload IDs to their simulated state.
	ExecFailures  map[string]bool               // ExecFailures is a set of workload IDs whose Exec calls should return an error.
	StartFailures map[string]string             // StartFailures maps workload IDs to error reasons returned by Start.
	ExecInitCalls []ExecInitCall                // ExecInitCalls records all ExecInit calls for test verification.
	PulledImages  []string                      // PulledImages records image references passed to PullImage for test verification.
}

// simulatedWorkload holds the in-memory state of a single workload managed by
// the SimulatorRuntime. No real process backs this — only bookkeeping.
type simulatedWorkload struct {
	workloadSpec Spec   // workloadSpec is the most recently applied specification for this workload.
	isRunning    bool   // isRunning tracks whether the workload is considered alive.
	exitCode     int    // exitCode is set when the workload is killed (e.g. 137 for OOM).
	errorMessage string // errorMessage describes why the workload died.
}

// NewSimulatorRuntime creates and returns a SimulatorRuntime with an empty
// workload registry, ready for use in tests or simulation scenarios.
func NewSimulatorRuntime() *SimulatorRuntime {
	return &SimulatorRuntime{
		workloads: make(map[string]*simulatedWorkload),
	}
}

// Start marks the workload described by spec as running. If the workload already
// exists, its spec is updated and it is marked running (idempotent). If it does
// not exist, a new entry is created. When a StartFailures entry exists for the
// workload ID, Start returns a StartError instead (simulating image pull failure
// or other start-time errors).
func (simulator *SimulatorRuntime) Start(_ context.Context, spec Spec) error {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	if failureReason, shouldFail := simulator.StartFailures[spec.ID]; shouldFail {
		return &StartError{ID: spec.ID, Reason: failureReason}
	}

	if workload, ok := simulator.workloads[spec.ID]; ok {
		workload.isRunning = true
		workload.exitCode = 0
		workload.errorMessage = ""
		workload.workloadSpec = spec
		return nil
	}
	simulator.workloads[spec.ID] = &simulatedWorkload{workloadSpec: spec, isRunning: true}
	return nil
}

// Stop marks the workload identified by id as not running with a clean exit
// (exit code 0, no error). If the workload does not exist, this is a no-op.
func (simulator *SimulatorRuntime) Stop(_ context.Context, id string) error {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	if workload, ok := simulator.workloads[id]; ok {
		workload.isRunning = false
		workload.exitCode = 0
		workload.errorMessage = ""
		return nil
	}
	return nil
}

// Status returns the current simulated state of the workload identified by id.
// Returns ErrNotFound if the workload has never been started. When a workload
// has been killed via KillWorkload, the returned Status includes the exit code
// and error message.
func (simulator *SimulatorRuntime) Status(_ context.Context, id string) (Status, error) {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	workload, ok := simulator.workloads[id]
	if !ok {
		return Status{}, ErrNotFound
	}
	return Status{
		ID:       id,
		Running:  workload.isRunning,
		ExitCode: workload.exitCode,
		Error:    workload.errorMessage,
	}, nil
}

// KillWorkload simulates a workload being killed (e.g. OOM kill with exit code
// 137). The workload is marked as not running with the given exit code and error
// message, which the agent's observeInstanceState will detect and report as
// InstanceFailed.
func (simulator *SimulatorRuntime) KillWorkload(id string, exitCode int, errorMessage string) {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	workload, ok := simulator.workloads[id]
	if !ok {
		return
	}
	workload.isRunning = false
	workload.exitCode = exitCode
	workload.errorMessage = errorMessage
}

// Exec simulates running a command in the context of a workload. In the
// simulator, this always succeeds if the workload exists and is running,
// and returns ErrNotFound otherwise.
func (simulator *SimulatorRuntime) Exec(_ context.Context, id string, execSpec ExecSpec) error {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	workload, ok := simulator.workloads[id]
	if !ok {
		return ErrNotFound
	}
	if !workload.isRunning {
		return &StartError{ID: id, Reason: "workload not running"}
	}
	if simulator.ExecFailures[id] {
		return &StartError{ID: id, Reason: "exec probe failed (injected)"}
	}
	return nil
}

// ExecCapture simulates running a command and capturing its output. Returns
// a synthetic response indicating success, or ErrNotFound if not running.
func (simulator *SimulatorRuntime) ExecCapture(_ context.Context, instanceID string, execSpec ExecSpec) ([]byte, error) {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	workload, exists := simulator.workloads[instanceID]
	if !exists {
		return nil, ErrNotFound
	}
	if !workload.isRunning {
		return nil, &StartError{ID: instanceID, Reason: "workload not running"}
	}
	if simulator.ExecFailures[instanceID] {
		return nil, &StartError{ID: instanceID, Reason: "exec probe failed (injected)"}
	}
	return []byte(fmt.Sprintf("[simulated] exec %q in %s\n", execSpec.Command, instanceID)), nil
}

// PullImage simulates pulling a container image. Records the pull and succeeds.
func (simulator *SimulatorRuntime) PullImage(_ context.Context, imageReference string) error {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()
	simulator.PulledImages = append(simulator.PulledImages, imageReference)
	return nil
}

// ExecInit runs an initialization command against an image in the simulator.
// The simulator records the call and succeeds.
func (simulator *SimulatorRuntime) ExecInit(_ context.Context, image string, execSpec ExecSpec) error {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()
	simulator.ExecInitCalls = append(simulator.ExecInitCalls, ExecInitCall{Image: image, Command: execSpec.Command})
	return nil
}

// ExecInitCall records a call to ExecInit for test verification.
type ExecInitCall struct {
	Image   string
	Command string
}

// Stats returns simulated resource usage for the workload. For running
// workloads, it returns the spec's requested CPU and memory as usage (i.e.
// simulated 100% utilization of requested resources). For stopped workloads,
// zero values are returned.
func (simulator *SimulatorRuntime) Stats(_ context.Context, id string) (ResourceStats, error) {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	workload, exists := simulator.workloads[id]
	if !exists {
		return ResourceStats{}, ErrNotFound
	}
	if !workload.isRunning {
		return ResourceStats{}, nil
	}
	return ResourceStats{
		CPUMillicores: workload.workloadSpec.CPUm,
		MemoryBytes:   workload.workloadSpec.MemoryB,
	}, nil
}

// Logs returns an empty reader for simulated workloads.
func (simulator *SimulatorRuntime) Logs(_ context.Context, id string, follow bool) (io.ReadCloser, error) {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	if _, ok := simulator.workloads[id]; !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// Resize updates the resource allocation of a simulated workload in place.
// The simulator supports live resize by simply updating the workload spec.
func (simulator *SimulatorRuntime) Resize(_ context.Context, id string, cpuMillicores int64, memoryBytes int64) error {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	workload, exists := simulator.workloads[id]
	if !exists {
		return ErrNotFound
	}
	workload.workloadSpec.CPUm = cpuMillicores
	workload.workloadSpec.MemoryB = memoryBytes
	return nil
}

// List returns the status of every workload the simulator has ever seen,
// including those that have been stopped.
func (simulator *SimulatorRuntime) List(_ context.Context) ([]Status, error) {
	simulator.mutex.Lock()
	defer simulator.mutex.Unlock()

	var result []Status
	for id, workload := range simulator.workloads {
		result = append(result, Status{
			ID:       id,
			Running:  workload.isRunning,
			ExitCode: workload.exitCode,
			Error:    workload.errorMessage,
		})
	}
	return result, nil
}
