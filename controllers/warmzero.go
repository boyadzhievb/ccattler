package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// WarmZeroController manages the activation lifecycle for services that support
// scale-to-zero (min=0 with idle_timeout configured). It transitions services
// between inactive/activating/active states based on traffic patterns and
// instance availability.
type WarmZeroController struct {
	timeNow func() time.Time
}

// NewWarmZeroController creates a WarmZeroController with the real clock.
func NewWarmZeroController() *WarmZeroController {
	return &WarmZeroController{
		timeNow: time.Now,
	}
}

func (warmZeroController *WarmZeroController) Name() string { return "warm-zero" }

func (warmZeroController *WarmZeroController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanObservedServices,
		types.ScanDerivedServices,
		types.ScanObservedInstances,
		types.ScanEndpoints,
	}
}

// Reconcile evaluates each warm-zero-enabled service and manages activation
// state transitions:
//   - idle too long with running instances → inactive (autoscaler scales to 0)
//   - activating with endpoints available → active
//   - active with no running instances → inactive
func (warmZeroController *WarmZeroController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	warmZeroConfigs := extractWarmZeroConfigs(facts)
	activationStates := extractActivationStates(facts)
	instanceCounts := countRunningInstancesPerService(facts)
	endpointCounts := countEndpointsPerService(facts)
	lastRequestTimes := extractLastRequestTimes(facts)

	var changes []Change
	currentTime := warmZeroController.timeNow()

	for serviceName, warmZeroConfig := range warmZeroConfigs {
		currentState := activationStates[serviceName]
		runningCount := instanceCounts[serviceName]
		endpointCount := endpointCounts[serviceName]
		lastRequestTime := lastRequestTimes[serviceName]

		newState := warmZeroController.computeDesiredState(
			currentState, runningCount, endpointCount,
			lastRequestTime, warmZeroConfig.idleTimeoutSeconds, currentTime,
		)

		if newState != "" && newState != currentState {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyDerivedServiceActivationState(serviceName),
				Value: []byte(newState),
			})
		}
	}

	return changes, nil
}

func (warmZeroController *WarmZeroController) computeDesiredState(
	currentState string, runningCount int, endpointCount int,
	lastRequestTime time.Time, idleTimeoutSeconds int, currentTime time.Time,
) string {
	switch currentState {
	case "activating":
		if endpointCount > 0 {
			return "active"
		}
		return ""
	case "active":
		if runningCount == 0 {
			return "inactive"
		}
		if !lastRequestTime.IsZero() && idleTimeoutSeconds > 0 {
			idleDuration := currentTime.Sub(lastRequestTime)
			if idleDuration > time.Duration(idleTimeoutSeconds)*time.Second {
				return "inactive"
			}
		}
		return ""
	case "inactive", "":
		if runningCount > 0 && endpointCount > 0 {
			return "active"
		}
		if currentState == "" && runningCount == 0 {
			return "inactive"
		}
		return ""
	default:
		return ""
	}
}

// warmZeroConfig holds the parsed warm-zero settings for a service.
type warmZeroConfig struct {
	idleTimeoutSeconds       int
	activationTimeoutSeconds int
}

func extractWarmZeroConfigs(facts []store.Fact) map[string]warmZeroConfig {
	configs := make(map[string]warmZeroConfig)
	minValues := make(map[string]int)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) < 2 {
			continue
		}
		serviceName := parts[0]
		suffix := parts[1]

		switch suffix {
		case "scale/horizontal/min":
			parsedValue, parseError := strconv.Atoi(string(fact.Value))
			if parseError == nil {
				minValues[serviceName] = parsedValue
			}
		case "scale/horizontal/idle_timeout":
			config := configs[serviceName]
			config.idleTimeoutSeconds = parseDurationSeconds(string(fact.Value))
			configs[serviceName] = config
		case "scale/horizontal/activation_timeout":
			config := configs[serviceName]
			config.activationTimeoutSeconds = parseDurationSeconds(string(fact.Value))
			configs[serviceName] = config
		}
	}

	for serviceName := range configs {
		if minValues[serviceName] != 0 {
			delete(configs, serviceName)
		}
	}

	return configs
}

func extractActivationStates(facts []store.Fact) map[string]string {
	states := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "activation/state" {
			states[parts[0]] = string(fact.Value)
		}
	}
	return states
}

func countRunningInstancesPerService(facts []store.Fact) map[string]int {
	serviceForInstance := make(map[string]string)
	runningInstances := make(map[string]bool)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) < 2 {
			continue
		}
		instanceID := parts[0]
		suffix := parts[1]
		switch suffix {
		case "service":
			serviceForInstance[instanceID] = string(fact.Value)
		case "state":
			if string(fact.Value) == string(types.InstanceRunning) {
				runningInstances[instanceID] = true
			}
		}
	}

	counts := make(map[string]int)
	for instanceID, serviceName := range serviceForInstance {
		if runningInstances[instanceID] {
			counts[serviceName]++
		}
	}
	return counts
}

func countEndpointsPerService(facts []store.Fact) map[string]int {
	counts := make(map[string]int)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanEndpoints) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEndpoints)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) >= 1 {
			counts[parts[0]]++
		}
	}
	return counts
}

func extractLastRequestTimes(facts []store.Fact) map[string]time.Time {
	times := make(map[string]time.Time)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedServices)
		parts := strings.SplitN(relativePath, "/", 2)
		if len(parts) == 2 && parts[1] == "last_request_time" {
			millis, parseError := strconv.ParseInt(string(fact.Value), 10, 64)
			if parseError == nil {
				times[parts[0]] = time.UnixMilli(millis)
			}
		}
	}
	return times
}

// formatActivationTimeout returns the activation timeout for a service as a
// Go duration. Returns the default 30s if not configured.
func formatActivationTimeout(facts []store.Fact, serviceName string) time.Duration {
	for _, fact := range facts {
		expectedKey := fmt.Sprintf("%s/service/%s/scale/horizontal/activation_timeout", types.PrefixDesired, serviceName)
		if fact.Key == expectedKey {
			seconds := parseDurationSeconds(string(fact.Value))
			if seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return 30 * time.Second
}
