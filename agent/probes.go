package agent

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

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

// executeProbesForInstance runs all configured probes for the given instance,
// respecting their individual intervals and the startup gate. Probe results
// are written to the store as observed facts.
func (nodeAgent *Agent) executeProbesForInstance(ctx context.Context, instanceInfo placedInstanceInfo) {
	startupConfig := nodeAgent.loadProbeConfig(ctx, instanceInfo.service, "startup")
	livenessConfig := nodeAgent.loadProbeConfig(ctx, instanceInfo.service, "liveness")
	readinessConfig := nodeAgent.loadProbeConfig(ctx, instanceInfo.service, "readiness")

	if startupConfig == nil && livenessConfig == nil && readinessConfig == nil {
		return
	}

	probeState := nodeAgent.getOrCreateProbeState(instanceInfo.id)
	now := time.Now()

	instanceIP := "127.0.0.1"
	if ipFact, ipErr := nodeAgent.store.Get(ctx, types.KeyObservedInstanceIP(instanceInfo.id)); ipErr == nil {
		instanceIP = string(ipFact.Value)
	}

	startupComplete := true
	if startupConfig != nil {
		if probeState.startup == nil {
			probeState.startup = &probeTracker{startTime: now}
		}
		startupComplete = nodeAgent.executeStartupProbe(ctx, instanceInfo, instanceIP, startupConfig, probeState.startup, now)
	}

	if startupComplete && livenessConfig != nil {
		if probeState.liveness == nil {
			probeState.liveness = &probeTracker{startTime: now}
		}
		nodeAgent.executeLivenessProbe(ctx, instanceInfo, instanceIP, livenessConfig, probeState.liveness, now)
	}

	if startupComplete && readinessConfig != nil {
		if probeState.readiness == nil {
			probeState.readiness = &probeTracker{startTime: now}
		}
		nodeAgent.executeReadinessProbe(ctx, instanceInfo, instanceIP, readinessConfig, probeState.readiness, now)
	}
}

// executeStartupProbe runs a startup probe check if the interval has elapsed.
// Returns true if the startup probe has succeeded (or was never configured).
func (nodeAgent *Agent) executeStartupProbe(ctx context.Context, instanceInfo placedInstanceInfo, instanceIP string, config *probeConfig, tracker *probeTracker, now time.Time) bool {
	currentState := nodeAgent.readProbeState(ctx, instanceInfo.id, "startup")
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

	passed := nodeAgent.runProbeCheck(ctx, config, instanceIP)
	tracker.lastCheckTime = now

	if passed {
		tracker.consecutiveSuccesses++
		tracker.consecutiveFailures = 0
		if tracker.consecutiveSuccesses >= config.successThreshold {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "startup"), []byte(string(types.StartupProbeSucceeded)))
			log.Printf("agent %s: startup probe succeeded for %s", nodeAgent.nodeID, instanceInfo.id)
			return true
		}
	} else {
		tracker.consecutiveFailures++
		tracker.consecutiveSuccesses = 0
		if tracker.consecutiveFailures >= config.failureThreshold {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "startup"), []byte(string(types.StartupProbeFailed)))
			log.Printf("agent %s: startup probe failed for %s (threshold %d reached)", nodeAgent.nodeID, instanceInfo.id, config.failureThreshold)
			return false
		}
	}

	if currentState != string(types.StartupProbePending) {
		nodeAgent.store.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "startup"), []byte(string(types.StartupProbePending)))
	}
	return false
}

// executeLivenessProbe runs a liveness probe check if the interval has elapsed.
func (nodeAgent *Agent) executeLivenessProbe(ctx context.Context, instanceInfo placedInstanceInfo, instanceIP string, config *probeConfig, tracker *probeTracker, now time.Time) {
	if !tracker.started {
		if now.Sub(tracker.startTime) < config.initialDelay {
			return
		}
		tracker.started = true
	}

	if !tracker.lastCheckTime.IsZero() && now.Sub(tracker.lastCheckTime) < config.interval {
		return
	}

	passed := nodeAgent.runProbeCheck(ctx, config, instanceIP)
	tracker.lastCheckTime = now

	if passed {
		tracker.consecutiveSuccesses++
		tracker.consecutiveFailures = 0
		if tracker.consecutiveSuccesses >= config.successThreshold {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "liveness"), []byte(string(types.LivenessProbeHealthy)))
		}
	} else {
		tracker.consecutiveFailures++
		tracker.consecutiveSuccesses = 0
		if tracker.consecutiveFailures >= config.failureThreshold {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "liveness"), []byte(string(types.LivenessProbeUnhealthy)))
			log.Printf("agent %s: liveness probe unhealthy for %s (threshold %d reached)", nodeAgent.nodeID, instanceInfo.id, config.failureThreshold)
		}
	}
}

// executeReadinessProbe runs a readiness probe check if the interval has elapsed.
func (nodeAgent *Agent) executeReadinessProbe(ctx context.Context, instanceInfo placedInstanceInfo, instanceIP string, config *probeConfig, tracker *probeTracker, now time.Time) {
	if !tracker.started {
		if now.Sub(tracker.startTime) < config.initialDelay {
			return
		}
		tracker.started = true
	}

	if !tracker.lastCheckTime.IsZero() && now.Sub(tracker.lastCheckTime) < config.interval {
		return
	}

	passed := nodeAgent.runProbeCheck(ctx, config, instanceIP)
	tracker.lastCheckTime = now

	if passed {
		tracker.consecutiveSuccesses++
		tracker.consecutiveFailures = 0
		if tracker.consecutiveSuccesses >= config.successThreshold {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "readiness"), []byte(string(types.ReadinessProbeReady)))
		}
	} else {
		tracker.consecutiveFailures++
		tracker.consecutiveSuccesses = 0
		if tracker.consecutiveFailures >= config.failureThreshold {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceProbeState(instanceInfo.id, "readiness"), []byte(string(types.ReadinessProbeNotReady)))
		}
	}
}

// runProbeCheck executes a single probe against the target and returns true
// if the check passed. Reuses the existing CheckHealth infrastructure for
// HTTP and TCP probes.
func (nodeAgent *Agent) runProbeCheck(ctx context.Context, config *probeConfig, instanceIP string) bool {
	probe := HealthProbe{
		Timeout: config.timeout,
	}
	switch config.method {
	case "http":
		probe.Type = ProbeHTTP
		probe.Path = config.path
		probe.Port = config.port
	case "tcp":
		probe.Type = ProbeTCP
		probe.Port = config.port
	default:
		return false
	}
	return CheckHealth(ctx, probe, instanceIP)
}

// loadProbeConfig reads the probe configuration for a service and probe type
// from the store. Returns nil if no probe is configured.
func (nodeAgent *Agent) loadProbeConfig(ctx context.Context, serviceName string, probeType string) *probeConfig {
	methodFact, methodErr := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbeMethod(serviceName, probeType))
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

	if pathFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbePath(serviceName, probeType)); err == nil {
		config.path = string(pathFact.Value)
	}
	if portFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbePort(serviceName, probeType)); err == nil {
		if parsedPort, parseErr := strconv.Atoi(string(portFact.Value)); parseErr == nil {
			config.port = parsedPort
		}
	}
	if intervalFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbeInterval(serviceName, probeType)); err == nil {
		if parsedInterval, parseErr := time.ParseDuration(string(intervalFact.Value)); parseErr == nil {
			config.interval = parsedInterval
		}
	}
	if timeoutFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbeTimeout(serviceName, probeType)); err == nil {
		if parsedTimeout, parseErr := time.ParseDuration(string(timeoutFact.Value)); parseErr == nil {
			config.timeout = parsedTimeout
		}
	}
	if failureFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbeFailureThreshold(serviceName, probeType)); err == nil {
		if parsedThreshold, parseErr := strconv.Atoi(string(failureFact.Value)); parseErr == nil {
			config.failureThreshold = parsedThreshold
		}
	}
	if successFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbeSuccessThreshold(serviceName, probeType)); err == nil {
		if parsedThreshold, parseErr := strconv.Atoi(string(successFact.Value)); parseErr == nil {
			config.successThreshold = parsedThreshold
		}
	}
	if delayFact, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbeInitialDelay(serviceName, probeType)); err == nil {
		if parsedDelay, parseErr := time.ParseDuration(string(delayFact.Value)); parseErr == nil {
			config.initialDelay = parsedDelay
		}
	}

	if config.port == 0 {
		config.port = nodeAgent.deriveProbePortFromService(ctx, serviceName)
	}

	return config
}

// deriveProbePortFromService reads the first exposed port from the service's
// desired state as a fallback when no explicit probe port is configured.
func (nodeAgent *Agent) deriveProbePortFromService(ctx context.Context, serviceName string) int {
	ports := nodeAgent.lookupServiceExposedPortsFromStore(ctx, serviceName)
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}

// readProbeState reads the current observed probe state for an instance from
// the store. Returns empty string if no state has been recorded yet.
func (nodeAgent *Agent) readProbeState(ctx context.Context, instanceID string, probeType string) string {
	stateFact, stateErr := nodeAgent.store.Get(ctx, types.KeyObservedInstanceProbeState(instanceID, probeType))
	if stateErr != nil {
		return ""
	}
	return string(stateFact.Value)
}

// getOrCreateProbeState returns the probe tracking state for the given instance,
// creating a new empty state if none exists.
func (nodeAgent *Agent) getOrCreateProbeState(instanceID string) *instanceProbeState {
	if nodeAgent.probeStates == nil {
		nodeAgent.probeStates = make(map[string]*instanceProbeState)
	}
	state, exists := nodeAgent.probeStates[instanceID]
	if !exists {
		state = &instanceProbeState{}
		nodeAgent.probeStates[instanceID] = state
	}
	return state
}

// cleanupProbeState removes the probe tracking state for an instance that is
// no longer running on this node.
func (nodeAgent *Agent) cleanupProbeState(instanceID string) {
	delete(nodeAgent.probeStates, instanceID)
}

// hasConfiguredProbes checks whether a service has any probe types configured
// in the store.
func (nodeAgent *Agent) hasConfiguredProbes(ctx context.Context, serviceName string) bool {
	for _, probeType := range []string{"startup", "liveness", "readiness"} {
		if _, err := nodeAgent.store.Get(ctx, types.KeyDesiredServiceProbeMethod(serviceName, probeType)); err == nil {
			return true
		}
	}
	return false
}

