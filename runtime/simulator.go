package runtime

import (
	"context"
	"errors"
	"sync"
)

// ErrNotFound is returned when a workload lookup fails because the requested
// workload ID has never been registered with the runtime.
var ErrNotFound = errors.New("workload not found")

// SimulatorRuntime is a fake runtime for testing and semantic validation.
// It tracks workload state in memory without launching any real processes or
// containers, allowing the full reconciliation loop to be exercised cheaply.
type SimulatorRuntime struct {
	mu        sync.Mutex                         // mu guards concurrent access to the workloads map.
	workloads map[string]*simulatedWorkload      // workloads maps workload IDs to their simulated state.
}

// simulatedWorkload holds the in-memory state of a single workload managed by
// the SimulatorRuntime. No real process backs this — only bookkeeping.
type simulatedWorkload struct {
	workloadSpec Spec // workloadSpec is the most recently applied specification for this workload.
	isRunning    bool // isRunning tracks whether the workload is considered alive.
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
// not exist, a new entry is created.
func (simulator *SimulatorRuntime) Start(_ context.Context, spec Spec) error {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()

	if workload, ok := simulator.workloads[spec.ID]; ok {
		workload.isRunning = true
		workload.workloadSpec = spec
		return nil
	}
	simulator.workloads[spec.ID] = &simulatedWorkload{workloadSpec: spec, isRunning: true}
	return nil
}

// Stop marks the workload identified by id as not running. If the workload does
// not exist, this is a no-op (idempotent).
func (simulator *SimulatorRuntime) Stop(_ context.Context, id string) error {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()

	if workload, ok := simulator.workloads[id]; ok {
		workload.isRunning = false
		return nil
	}
	return nil
}

// Status returns the current simulated state of the workload identified by id.
// Returns ErrNotFound if the workload has never been started.
func (simulator *SimulatorRuntime) Status(_ context.Context, id string) (Status, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()

	workload, ok := simulator.workloads[id]
	if !ok {
		return Status{}, ErrNotFound
	}
	return Status{
		ID:      id,
		Running: workload.isRunning,
	}, nil
}

// List returns the status of every workload the simulator has ever seen,
// including those that have been stopped.
func (simulator *SimulatorRuntime) List(_ context.Context) ([]Status, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()

	var result []Status
	for id, workload := range simulator.workloads {
		result = append(result, Status{
			ID:      id,
			Running: workload.isRunning,
		})
	}
	return result, nil
}
