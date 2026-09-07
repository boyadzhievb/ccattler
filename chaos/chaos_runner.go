package chaos

import (
	"context"
	"math/rand"
	"time"
)

// FailureScenario identifies the type of failure that can be injected.
type FailureScenario string

const (
	// ScenarioNodeKill kills a random alive node's agent.
	ScenarioNodeKill FailureScenario = "node-kill"
	// ScenarioNodePartition partitions a random alive node from the store.
	ScenarioNodePartition FailureScenario = "node-partition"
	// ScenarioControllerRestart cancels and restarts the controller runner.
	ScenarioControllerRestart FailureScenario = "controller-restart"
	// ScenarioScaleChange randomly scales a service up or down.
	ScenarioScaleChange FailureScenario = "scale-change"
)

// ChaosEvent records a single failure injection with its timestamp and details.
type ChaosEvent struct {
	// Timestamp is when the injection occurred, relative to chaos start.
	Elapsed time.Duration
	// Scenario is the type of failure injected.
	Scenario FailureScenario
	// Target identifies what was affected (e.g. "node-1", "web: 6 -> 8").
	Target string
	// Converged indicates whether the system converged after this injection.
	Converged bool
	// ConvergenceTime is how long it took to converge after injection.
	ConvergenceTime time.Duration
}

// ChaosConfig controls the chaos runner's behavior.
type ChaosConfig struct {
	// Duration is how long the chaos test runs.
	Duration time.Duration
	// InjectionInterval is the minimum time between failure injections.
	InjectionInterval time.Duration
	// ConvergenceTimeout is how long to wait for convergence after each injection.
	ConvergenceTimeout time.Duration
	// EnabledScenarios lists which failure types can be injected.
	EnabledScenarios []FailureScenario
	// RandSource provides deterministic randomness for reproducible tests.
	RandSource *rand.Rand
}

// ChaosCluster provides the ChaosRunner with control over the simulated cluster.
type ChaosCluster interface {
	// NodeIDs returns all node identifiers in the cluster.
	NodeIDs() []string
	// IsNodeAlive reports whether a node's agent is currently running.
	IsNodeAlive(nodeID string) bool
	// KillNode stops a node's agent by cancelling its context.
	KillNode(nodeID string)
	// RestartNode starts a new agent for the given node.
	RestartNode(ctx context.Context, nodeID string)
	// PartitionNode activates the network partition for a node's store.
	PartitionNode(nodeID string)
	// HealNode deactivates the network partition for a node's store.
	HealNode(nodeID string)
	// IsNodePartitioned reports whether a node is currently partitioned.
	IsNodePartitioned(nodeID string) bool
	// RestartControllers cancels and restarts the controller runner.
	RestartControllers(ctx context.Context)
	// SetServiceScale changes the effective instance count for a service.
	SetServiceScale(ctx context.Context, serviceName string, instanceCount int)
	// ServiceNames returns all deployed service names.
	ServiceNames() []string
	// CheckConvergence verifies all services have desired == running instance
	// counts on alive nodes. Returns whether converged and a status description.
	CheckConvergence(ctx context.Context) (bool, string)
}

// ChaosRunner orchestrates random failure injection and convergence verification.
type ChaosRunner struct {
	// config controls the runner's behavior.
	config ChaosConfig
	// cluster provides control over the simulated cluster.
	cluster ChaosCluster
	// events records all injections for reporting.
	events []ChaosEvent
	// eventCallback is called after each injection for live reporting.
	eventCallback func(ChaosEvent)
}

// NewChaosRunner creates a ChaosRunner with the given config and cluster.
func NewChaosRunner(config ChaosConfig, cluster ChaosCluster) *ChaosRunner {
	if config.RandSource == nil {
		config.RandSource = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &ChaosRunner{
		config:  config,
		cluster: cluster,
	}
}

// SetEventCallback registers a function called after each chaos injection.
func (chaosRunner *ChaosRunner) SetEventCallback(callback func(ChaosEvent)) {
	chaosRunner.eventCallback = callback
}

// Run executes the chaos test loop for the configured duration, injecting
// random failures and verifying convergence after each. Returns the list of
// all chaos events.
func (chaosRunner *ChaosRunner) Run(ctx context.Context) []ChaosEvent {
	chaosStartTime := time.Now()
	deadline := chaosStartTime.Add(chaosRunner.config.Duration)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return chaosRunner.events
		default:
		}

		jitter := time.Duration(chaosRunner.config.RandSource.Int63n(int64(chaosRunner.config.InjectionInterval / 2)))
		sleepDuration := chaosRunner.config.InjectionInterval + jitter

		sleepDeadline := time.Now().Add(sleepDuration)
		if sleepDeadline.After(deadline) {
			break
		}
		time.Sleep(sleepDuration)

		event := chaosRunner.injectRandomFailure(ctx, time.Since(chaosStartTime))
		chaosRunner.events = append(chaosRunner.events, event)
		if chaosRunner.eventCallback != nil {
			chaosRunner.eventCallback(event)
		}
	}

	chaosRunner.healAllNodes(ctx)

	finalConverged, _ := chaosRunner.waitForConvergence(ctx)
	if finalConverged && len(chaosRunner.events) > 0 {
		lastEvent := &chaosRunner.events[len(chaosRunner.events)-1]
		_ = lastEvent
	}

	return chaosRunner.events
}

// Events returns all recorded chaos events.
func (chaosRunner *ChaosRunner) Events() []ChaosEvent {
	return chaosRunner.events
}

// injectRandomFailure picks a random scenario and target, executes it, then
// waits for convergence.
func (chaosRunner *ChaosRunner) injectRandomFailure(ctx context.Context, elapsed time.Duration) ChaosEvent {
	scenario := chaosRunner.config.EnabledScenarios[chaosRunner.config.RandSource.Intn(len(chaosRunner.config.EnabledScenarios))]

	var target string
	switch scenario {
	case ScenarioNodeKill:
		target = chaosRunner.injectNodeKill()
	case ScenarioNodePartition:
		target = chaosRunner.injectNodePartition()
	case ScenarioControllerRestart:
		target = chaosRunner.injectControllerRestart(ctx)
	case ScenarioScaleChange:
		target = chaosRunner.injectScaleChange(ctx)
	}

	if target == "" {
		target = "(no valid target)"
	}

	converged, convergenceTime := chaosRunner.waitForConvergence(ctx)

	return ChaosEvent{
		Elapsed:         elapsed,
		Scenario:        scenario,
		Target:          target,
		Converged:       converged,
		ConvergenceTime: convergenceTime,
	}
}

// injectNodeKill kills a random alive, non-partitioned node.
func (chaosRunner *ChaosRunner) injectNodeKill() string {
	candidates := chaosRunner.aliveNonPartitionedNodes()
	if len(candidates) <= 1 {
		return ""
	}
	nodeID := candidates[chaosRunner.config.RandSource.Intn(len(candidates))]
	chaosRunner.cluster.KillNode(nodeID)
	return nodeID
}

// injectNodePartition partitions a random alive, non-partitioned node.
func (chaosRunner *ChaosRunner) injectNodePartition() string {
	candidates := chaosRunner.aliveNonPartitionedNodes()
	if len(candidates) <= 1 {
		return ""
	}
	nodeID := candidates[chaosRunner.config.RandSource.Intn(len(candidates))]
	chaosRunner.cluster.PartitionNode(nodeID)
	return nodeID
}

// injectControllerRestart restarts the controller runner.
func (chaosRunner *ChaosRunner) injectControllerRestart(ctx context.Context) string {
	chaosRunner.cluster.RestartControllers(ctx)
	return "controllers"
}

// injectScaleChange picks a random service and randomly scales it up or down.
func (chaosRunner *ChaosRunner) injectScaleChange(ctx context.Context) string {
	serviceNames := chaosRunner.cluster.ServiceNames()
	if len(serviceNames) == 0 {
		return ""
	}
	serviceName := serviceNames[chaosRunner.config.RandSource.Intn(len(serviceNames))]
	newCount := 2 + chaosRunner.config.RandSource.Intn(8)
	chaosRunner.cluster.SetServiceScale(ctx, serviceName, newCount)
	return serviceName
}

// waitForConvergence polls CheckConvergence until success or timeout.
func (chaosRunner *ChaosRunner) waitForConvergence(ctx context.Context) (bool, time.Duration) {
	convergenceStart := time.Now()
	convergenceDeadline := convergenceStart.Add(chaosRunner.config.ConvergenceTimeout)

	for time.Now().Before(convergenceDeadline) {
		select {
		case <-ctx.Done():
			return false, time.Since(convergenceStart)
		default:
		}

		converged, _ := chaosRunner.cluster.CheckConvergence(ctx)
		if converged {
			return true, time.Since(convergenceStart)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false, time.Since(convergenceStart)
}

// aliveNonPartitionedNodes returns nodes that are alive and not partitioned.
func (chaosRunner *ChaosRunner) aliveNonPartitionedNodes() []string {
	var candidates []string
	for _, nodeID := range chaosRunner.cluster.NodeIDs() {
		if chaosRunner.cluster.IsNodeAlive(nodeID) && !chaosRunner.cluster.IsNodePartitioned(nodeID) {
			candidates = append(candidates, nodeID)
		}
	}
	return candidates
}

// healAllNodes restores all partitioned nodes and restarts all killed nodes.
func (chaosRunner *ChaosRunner) healAllNodes(ctx context.Context) {
	for _, nodeID := range chaosRunner.cluster.NodeIDs() {
		if chaosRunner.cluster.IsNodePartitioned(nodeID) {
			chaosRunner.cluster.HealNode(nodeID)
		}
		if !chaosRunner.cluster.IsNodeAlive(nodeID) {
			chaosRunner.cluster.RestartNode(ctx, nodeID)
		}
	}
}
