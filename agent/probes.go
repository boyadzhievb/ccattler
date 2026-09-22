package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// ProbeScheduler manages health checks and probe execution for all instances
// on a node. It owns the probe state tracking and runs independently of the
// main reconciliation loop so that slow reconciliation does not delay
// liveness/readiness checks.
type ProbeScheduler struct {
	nodeID          string                        // nodeID is the unique identifier for the node this scheduler runs on.
	factStore       store.StateStore              // factStore is the fact store used to read probe config and write probe results.
	runtimeAdapter  runtime.Runtime               // runtimeAdapter is the runtime used for exec-type probes.
	probeStates     map[string]*instanceProbeState // probeStates tracks probe execution state per instance ID.
	lastHealthCheck map[string]time.Time          // lastHealthCheck tracks when each instance was last health-checked.
	interval        time.Duration                 // interval is the period between probe scheduling passes.
}

// NewProbeScheduler creates a new ProbeScheduler wired to the given store and
// runtime. The interval controls how frequently the scheduler ticks through
// all instances to execute their configured probes.
func NewProbeScheduler(nodeID string, factStore store.StateStore, runtimeAdapter runtime.Runtime, interval time.Duration) *ProbeScheduler {
	return &ProbeScheduler{
		nodeID:          nodeID,
		factStore:       factStore,
		runtimeAdapter:  runtimeAdapter,
		probeStates:     make(map[string]*instanceProbeState),
		lastHealthCheck: make(map[string]time.Time),
		interval:        interval,
	}
}

// probeConfig holds the parsed configuration for a single probe type loaded
// from the desired-state section of the fact store.
type probeConfig struct {
	method           string        // "http", "tcp", or "exec"
	path             string        // URL path for HTTP probes or command for exec probes
	port             int           // TCP port to connect to
	interval         time.Duration // time between probe executions
	timeout          time.Duration // max time per probe execution
	failureThreshold int           // consecutive failures before state change
	successThreshold int           // consecutive successes before state change
	initialDelay     time.Duration // delay before first probe
}

// probeTracker holds the runtime state for a single probe on a single instance.
type probeTracker struct {
	lastCheckTime       time.Time // when the probe was last executed
	consecutiveFailures int       // current streak of consecutive failures
	consecutiveSuccesses int      // current streak of consecutive successes
	started             bool      // true after the initial delay has passed
	startTime           time.Time // when the instance was first observed for this probe
}

// instanceProbeState holds the probe tracking state for all three probe types
// on a single instance.
type instanceProbeState struct {
	startup   *probeTracker
	liveness  *probeTracker
	readiness *probeTracker
}

// Run starts the probe scheduling loop. It creates a ticker at the configured
// interval and on each tick calls findInstances to discover placed instances,
// then runs health checks and probes for each instance.
func (probeScheduler *ProbeScheduler) Run(ctx context.Context, findInstances func(context.Context) ([]placedInstanceInfo, error)) {
	probeTicker := time.NewTicker(probeScheduler.interval)
	defer probeTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-probeTicker.C:
			probeScheduler.ExecuteProbePass(ctx, findInstances)
		}
	}
}

// ExecuteProbePass runs health checks and probes for all instances returned by
// findInstances. This is the core scheduling pass invoked on every tick.
func (probeScheduler *ProbeScheduler) ExecuteProbePass(ctx context.Context, findInstances func(context.Context) ([]placedInstanceInfo, error)) {
	instances, err := findInstances(ctx)
	if err != nil {
		return
	}

	for _, instanceInfo := range instances {
		probeScheduler.performHealthCheckAndReportResult(ctx, instanceInfo)
		probeScheduler.executeProbesForInstance(ctx, instanceInfo)
	}
}

// executeProbesForInstance runs all configured probes for the given instance,
// respecting their individual intervals and the startup gate. Probe results
// are written to the store as observed facts.
func (probeScheduler *ProbeScheduler) executeProbesForInstance(ctx context.Context, instanceInfo placedInstanceInfo) {
	startupConfig := probeScheduler.loadProbeConfig(ctx, instanceInfo.service, "startup")
	livenessConfig := probeScheduler.loadProbeConfig(ctx, instanceInfo.service, "liveness")
	readinessConfig := probeScheduler.loadProbeConfig(ctx, instanceInfo.service, "readiness")

	if startupConfig == nil && livenessConfig == nil && readinessConfig == nil {
		return
	}

	probeState := probeScheduler.getOrCreateProbeState(instanceInfo.id)
	now := time.Now()

	ipFact, ipErr := probeScheduler.factStore.Get(ctx, types.KeyObservedInstanceIP(instanceInfo.id))
	if ipErr != nil || len(ipFact.Value) == 0 {
		return
	}
	instanceIP := string(ipFact.Value)

	startupComplete := true
	if startupConfig != nil {
		if probeState.startup == nil {
			probeState.startup = &probeTracker{startTime: now}
		}
		startupComplete = probeScheduler.executeStartupProbe(ctx, instanceInfo, instanceIP, startupConfig, probeState.startup, now)
	}

	if startupComplete && livenessConfig != nil {
		if probeState.liveness == nil {
			probeState.liveness = &probeTracker{startTime: now}
		}
		probeScheduler.executeLivenessProbe(ctx, instanceInfo, instanceIP, livenessConfig, probeState.liveness, now)
	}

	if startupComplete && readinessConfig != nil {
		if probeState.readiness == nil {
			probeState.readiness = &probeTracker{startTime: now}
		}
		probeScheduler.executeReadinessProbe(ctx, instanceInfo, instanceIP, readinessConfig, probeState.readiness, now)
	}
}

// executeStartupProbe runs a startup probe check if the interval has elapsed.
// Returns true if the startup probe has succeeded (or was never configured).
func (probeScheduler *ProbeScheduler) executeStartupProbe(ctx context.Context, instanceInfo placedInstanceInfo, instanceIP string, config *probeConfig, tracker *probeTracker, now time.Time) bool {
	currentState := probeScheduler.readProbeState(ctx, instanceInfo.id, "startup")
	if currentState == string(types.StartupProbeSucceeded) {
		return true
	}
	if currentState == string(types.StartupProbeFailed) {
		return false
	}

	if !tracker.started {
		if now.Sub(tracker.startTime) < config.initialDelay {
			return false
		}
		tracker.started = true
	}

	if !tracker.lastCheckTime.IsZero() && now.Sub(tracker.lastCheckTime) < config.interval {
		return false
	}

	passed := probeScheduler.runProbeCheck(ctx, config, instanceInfo.id, instanceIP)
	tracker.lastCheckTime = now

	if passed {
		tracker.consecutiveSuccesses++
		tracker.consecutiveFailures = 0
		if tracker.consecutiveSuccesses >= config.successThreshold {
			probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "startup"), []byte(string(types.StartupProbeSucceeded)))
			logging.Default().Info("startup probe succeeded", "agent", probeScheduler.nodeID, "instance", instanceInfo.id)
			return true
		}
	} else {
		tracker.consecutiveFailures++
		tracker.consecutiveSuccesses = 0
		if tracker.consecutiveFailures >= config.failureThreshold {
			probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "startup"), []byte(string(types.StartupProbeFailed)))
			logging.Default().Warn("startup probe failed", "agent", probeScheduler.nodeID, "instance", instanceInfo.id, "threshold", fmt.Sprintf("%d", config.failureThreshold))
			return false
		}
	}

	if currentState != string(types.StartupProbePending) {
		probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "startup"), []byte(string(types.StartupProbePending)))
	}
	return false
}

// executeLivenessProbe runs a liveness probe check if the interval has elapsed.
func (probeScheduler *ProbeScheduler) executeLivenessProbe(ctx context.Context, instanceInfo placedInstanceInfo, instanceIP string, config *probeConfig, tracker *probeTracker, now time.Time) {
	if !tracker.started {
		if now.Sub(tracker.startTime) < config.initialDelay {
			return
		}
		tracker.started = true
	}

	if !tracker.lastCheckTime.IsZero() && now.Sub(tracker.lastCheckTime) < config.interval {
		return
	}

	passed := probeScheduler.runProbeCheck(ctx, config, instanceInfo.id, instanceIP)
	tracker.lastCheckTime = now

	if passed {
		tracker.consecutiveSuccesses++
		tracker.consecutiveFailures = 0
		if tracker.consecutiveSuccesses >= config.successThreshold {
			probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "liveness"), []byte(string(types.LivenessProbeHealthy)))
		}
	} else {
		tracker.consecutiveFailures++
		tracker.consecutiveSuccesses = 0
		if tracker.consecutiveFailures >= config.failureThreshold {
			probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "liveness"), []byte(string(types.LivenessProbeUnhealthy)))
			logging.Default().Warn("liveness probe unhealthy", "agent", probeScheduler.nodeID, "instance", instanceInfo.id, "threshold", fmt.Sprintf("%d", config.failureThreshold))
		}
	}
}

// executeReadinessProbe runs a readiness probe check if the interval has elapsed.
func (probeScheduler *ProbeScheduler) executeReadinessProbe(ctx context.Context, instanceInfo placedInstanceInfo, instanceIP string, config *probeConfig, tracker *probeTracker, now time.Time) {
	if !tracker.started {
		if now.Sub(tracker.startTime) < config.initialDelay {
			return
		}
		tracker.started = true
	}

	if !tracker.lastCheckTime.IsZero() && now.Sub(tracker.lastCheckTime) < config.interval {
		return
	}

	passed := probeScheduler.runProbeCheck(ctx, config, instanceInfo.id, instanceIP)
	tracker.lastCheckTime = now

	if passed {
		tracker.consecutiveSuccesses++
		tracker.consecutiveFailures = 0
		if tracker.consecutiveSuccesses >= config.successThreshold {
			probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "readiness"), []byte(string(types.ReadinessProbeReady)))
		}
	} else {
		tracker.consecutiveFailures++
		tracker.consecutiveSuccesses = 0
		if tracker.consecutiveFailures >= config.failureThreshold {
			probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "readiness"), []byte(string(types.ReadinessProbeNotReady)))
		}
	}
}

// runProbeCheck executes a single probe against the target and returns true
// if the check passed. Reuses the existing CheckHealth infrastructure for
// HTTP and TCP probes, and runtime.Exec for exec probes.
func (probeScheduler *ProbeScheduler) runProbeCheck(ctx context.Context, config *probeConfig, instanceID string, instanceIP string) bool {
	switch config.method {
	case "http":
		return CheckHealth(ctx, HealthProbe{
			Type:    ProbeHTTP,
			Path:    config.path,
			Port:    config.port,
			Timeout: config.timeout,
		}, instanceIP)
	case "tcp":
		return CheckHealth(ctx, HealthProbe{
			Type:    ProbeTCP,
			Port:    config.port,
			Timeout: config.timeout,
		}, instanceIP)
	case "exec":
		execCtx, cancelExec := context.WithTimeout(ctx, config.timeout)
		defer cancelExec()
		execErr := probeScheduler.runtimeAdapter.Exec(execCtx, instanceID, runtime.ExecSpec{
			Command: config.path,
		})
		return execErr == nil
	default:
		return false
	}
}

// loadProbeConfig reads the probe configuration for a service and probe type
// from the store. Returns nil if no probe is configured.
func (probeScheduler *ProbeScheduler) loadProbeConfig(ctx context.Context, serviceName string, probeType string) *probeConfig {
	methodFact, methodErr := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbeMethod(serviceName, probeType))
	if methodErr != nil {
		return nil
	}

	config := &probeConfig{
		method:           string(methodFact.Value),
		interval:         10 * time.Second,
		timeout:          2 * time.Second,
		failureThreshold: 3,
		successThreshold: 1,
	}

	if pathFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbePath(serviceName, probeType)); err == nil {
		config.path = string(pathFact.Value)
	}
	if portFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbePort(serviceName, probeType)); err == nil {
		if parsedPort, parseErr := strconv.Atoi(string(portFact.Value)); parseErr == nil {
			config.port = parsedPort
		}
	}
	if intervalFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbeInterval(serviceName, probeType)); err == nil {
		if parsedInterval, parseErr := time.ParseDuration(string(intervalFact.Value)); parseErr == nil {
			config.interval = parsedInterval
		}
	}
	if timeoutFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbeTimeout(serviceName, probeType)); err == nil {
		if parsedTimeout, parseErr := time.ParseDuration(string(timeoutFact.Value)); parseErr == nil {
			config.timeout = parsedTimeout
		}
	}
	if failureFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbeFailureThreshold(serviceName, probeType)); err == nil {
		if parsedThreshold, parseErr := strconv.Atoi(string(failureFact.Value)); parseErr == nil {
			config.failureThreshold = parsedThreshold
		}
	}
	if successFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbeSuccessThreshold(serviceName, probeType)); err == nil {
		if parsedThreshold, parseErr := strconv.Atoi(string(successFact.Value)); parseErr == nil {
			config.successThreshold = parsedThreshold
		}
	}
	if delayFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbeInitialDelay(serviceName, probeType)); err == nil {
		if parsedDelay, parseErr := time.ParseDuration(string(delayFact.Value)); parseErr == nil {
			config.initialDelay = parsedDelay
		}
	}

	if config.port == 0 {
		config.port = probeScheduler.deriveProbePortFromService(ctx, serviceName)
	}

	return config
}

// deriveProbePortFromService reads the first exposed port from the service's
// desired state as a fallback when no explicit probe port is configured.
func (probeScheduler *ProbeScheduler) deriveProbePortFromService(ctx context.Context, serviceName string) int {
	ports := lookupExposedPorts(ctx, probeScheduler.factStore, serviceName)
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}

// readProbeState reads the current observed probe state for an instance from
// the store. Returns empty string if no state has been recorded yet.
func (probeScheduler *ProbeScheduler) readProbeState(ctx context.Context, instanceID string, probeType string) string {
	stateFact, stateErr := probeScheduler.factStore.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, probeType))
	if stateErr != nil {
		return ""
	}
	return string(stateFact.Value)
}

// getOrCreateProbeState returns the probe tracking state for the given instance,
// creating a new empty state if none exists.
func (probeScheduler *ProbeScheduler) getOrCreateProbeState(instanceID string) *instanceProbeState {
	if probeScheduler.probeStates == nil {
		probeScheduler.probeStates = make(map[string]*instanceProbeState)
	}
	state, exists := probeScheduler.probeStates[instanceID]
	if !exists {
		state = &instanceProbeState{}
		probeScheduler.probeStates[instanceID] = state
	}
	return state
}

// CleanupInstance removes the probe tracking state for an instance that is
// no longer running on this node.
func (probeScheduler *ProbeScheduler) CleanupInstance(instanceID string) {
	delete(probeScheduler.probeStates, instanceID)
}

// HasConfiguredProbes checks whether a service has any probe types configured
// in the store.
func (probeScheduler *ProbeScheduler) HasConfiguredProbes(ctx context.Context, serviceName string) bool {
	for _, probeType := range []string{"startup", "liveness", "readiness"} {
		if _, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceProbeMethod(serviceName, probeType)); err == nil {
			return true
		}
	}
	return false
}

// performHealthCheckAndReportResult looks up the health probe configuration for
// the service that owns the given instance, executes the probe against the
// instance's IP address, and writes the resulting health status (healthy or
// unhealthy) back to the store. If no health probe is configured for the
// service, this method is a no-op.
func (probeScheduler *ProbeScheduler) performHealthCheckAndReportResult(ctx context.Context, instanceInfo placedInstanceInfo) {
	probe, ok := probeScheduler.buildHealthProbeFromServiceConfig(ctx, instanceInfo.service)
	if !ok {
		return
	}

	healthInterval := probeScheduler.lookupHealthIntervalFromStore(ctx, instanceInfo.service)
	if lastCheck, exists := probeScheduler.lastHealthCheck[instanceInfo.id]; exists {
		if time.Since(lastCheck) < healthInterval {
			return
		}
	}

	ipFact, ipError := probeScheduler.factStore.Get(ctx, types.KeyObservedInstanceIP(instanceInfo.id))
	if ipError != nil || len(ipFact.Value) == 0 {
		return
	}
	instanceIP := string(ipFact.Value)

	healthy := CheckHealth(ctx, probe, instanceIP)
	status := types.HealthUnknown
	if healthy {
		status = types.HealthHealthy
	} else {
		status = types.HealthUnhealthy
	}
	probeScheduler.factStore.Put(ctx, types.KeyObservedInstanceHealth(instanceInfo.id), []byte(string(status)))
	probeScheduler.lastHealthCheck[instanceInfo.id] = time.Now()
}

// lookupHealthIntervalFromStore reads the configured health check interval
// for the given service from the store. Returns 10s as default if not configured.
func (probeScheduler *ProbeScheduler) lookupHealthIntervalFromStore(ctx context.Context, serviceName string) time.Duration {
	factEntry, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceHealthInterval(serviceName))
	if err != nil {
		return 10 * time.Second
	}
	parsed, err := time.ParseDuration(string(factEntry.Value))
	if err != nil {
		return 10 * time.Second
	}
	return parsed
}

// buildHealthProbeFromServiceConfig reads the health check configuration for
// the named service from the store and assembles a HealthProbe. It returns
// false if the service has no health method configured, if the method is
// unrecognized, or if no exposed port can be derived.
func (probeScheduler *ProbeScheduler) buildHealthProbeFromServiceConfig(ctx context.Context, service string) (HealthProbe, bool) {
	methodFact, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceHealthMethod(service))
	if err != nil {
		return HealthProbe{}, false
	}
	method := string(methodFact.Value)

	probe := HealthProbe{Timeout: 2 * time.Second}
	switch method {
	case "http":
		probe.Type = ProbeHTTP
		if factEntry, err := probeScheduler.factStore.Get(ctx, types.KeyDesiredServiceHealthPath(service)); err == nil {
			probe.Path = string(factEntry.Value)
		} else {
			probe.Path = "/"
		}
	case "tcp":
		probe.Type = ProbeTCP
	default:
		return HealthProbe{}, false
	}

	// Derive port from the first expose port on the service.
	facts, err := probeScheduler.factStore.Scan(ctx, fmt.Sprintf("%s/service/%s/expose/", types.PrefixDesired, service))
	if err == nil && len(facts) > 0 {
		portStr := facts[0].Key[strings.LastIndex(facts[0].Key, "/")+1:]
		if parsedPort, err := strconv.Atoi(portStr); err == nil {
			probe.Port = parsedPort
		}
	}

	if probe.Port == 0 {
		return HealthProbe{}, false
	}

	return probe, true
}

// lookupExposedPorts reads all exposed port declarations for the given service
// from the fact store and returns them as a slice of integers. This is a
// package-level utility used by ProbeScheduler when no explicit probe port
// is configured.
func lookupExposedPorts(ctx context.Context, factStore store.StateStore, serviceName string) []int {
	exposeFacts, err := factStore.Scan(ctx, fmt.Sprintf("%s/service/%s/expose/", types.PrefixDesired, serviceName))
	if err != nil || len(exposeFacts) == 0 {
		return nil
	}

	ports := make([]int, 0, len(exposeFacts))
	for _, fact := range exposeFacts {
		portStr := fact.Key[strings.LastIndex(fact.Key, "/")+1:]
		if parsedPort, err := strconv.Atoi(portStr); err == nil {
			ports = append(ports, parsedPort)
		}
	}
	return ports
}
