package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// InitController watches init step observations for instances and derives the
// overall initialization phase. When all init steps for an instance have
// succeeded, the controller sets the init phase to "complete", allowing the
// main workload to start. If any step fails, the phase is set to "failed".
type InitController struct{}

// NewInitController creates a new InitController.
func NewInitController() *InitController {
	return &InitController{}
}

// Name returns the human-readable controller identifier.
func (initController *InitController) Name() string {
	return "init"
}

// Watch returns the fact prefixes that trigger reconciliation: desired service
// init steps and observed instance init step results.
func (initController *InitController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanObservedInstances,
	}
}

// Reconcile examines desired init steps and observed init step results for each
// instance. It derives the overall init phase per instance: pending if no steps
// have run, running if any are in progress, complete if all succeeded, or failed
// if any step failed.
func (initController *InitController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	serviceInitStepCounts := initController.countDesiredInitSteps(facts)
	instanceServices := initController.mapInstancesToServices(facts)
	observedInitStepStates := initController.collectObservedInitStepStates(facts)
	currentInitPhases := initController.collectCurrentInitPhases(facts)

	var changes []Change

	for instanceID, serviceName := range instanceServices {
		desiredStepCount, hasInitSteps := serviceInitStepCounts[serviceName]
		if !hasInitSteps || desiredStepCount == 0 {
			continue
		}

		derivedPhase := initController.deriveInitPhase(instanceID, desiredStepCount, observedInitStepStates)
		currentPhase := currentInitPhases[instanceID]

		if string(derivedPhase) != currentPhase {
			changes = append(changes, Change{
				Type:  store.OpPut,
				Key:   types.KeyObservedInstanceInitPhase(instanceID),
				Value: []byte(string(derivedPhase)),
			})
		}
	}

	return changes, nil
}

// countDesiredInitSteps builds a map from service name to the number of desired
// init steps by scanning for init step marker keys.
func (initController *InitController) countDesiredInitSteps(facts []store.Fact) map[string]int {
	serviceStepCounts := make(map[string]int)
	initPrefix := "/init/"

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.PrefixDesired+"/service/") {
			continue
		}
		initIndex := strings.Index(fact.Key, initPrefix)
		if initIndex < 0 {
			continue
		}

		afterInit := fact.Key[initIndex+len(initPrefix):]
		if strings.Contains(afterInit, "/") {
			continue
		}

		servicePart := fact.Key[len(types.PrefixDesired+"/service/"):initIndex]
		serviceStepCounts[servicePart]++
	}

	return serviceStepCounts
}

// mapInstancesToServices builds a map from instance ID to service name.
func (initController *InitController) mapInstancesToServices(facts []store.Fact) map[string]string {
	instanceServices := make(map[string]string)
	servicePrefix := types.PrefixObserved + "/instance/"
	serviceSuffix := "/service"

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, servicePrefix) || !strings.HasSuffix(fact.Key, serviceSuffix) {
			continue
		}
		instanceID := fact.Key[len(servicePrefix) : len(fact.Key)-len(serviceSuffix)]
		instanceServices[instanceID] = string(fact.Value)
	}

	return instanceServices
}

// collectObservedInitStepStates builds a map from "instanceID/stepIndex" to the
// observed step state string.
func (initController *InitController) collectObservedInitStepStates(facts []store.Fact) map[string]string {
	stepStates := make(map[string]string)
	stepPrefix := "/init/step/"
	stateSuffix := "/state"

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.PrefixObserved+"/instance/") {
			continue
		}
		stepIndex := strings.Index(fact.Key, stepPrefix)
		if stepIndex < 0 || !strings.HasSuffix(fact.Key, stateSuffix) {
			continue
		}

		instanceID := fact.Key[len(types.PrefixObserved+"/instance/"):stepIndex]
		stepPart := fact.Key[stepIndex+len(stepPrefix) : len(fact.Key)-len(stateSuffix)]
		compositeKey := fmt.Sprintf("%s/%s", instanceID, stepPart)
		stepStates[compositeKey] = string(fact.Value)
	}

	return stepStates
}

// collectCurrentInitPhases builds a map from instance ID to the current init
// phase string as stored in the fact store.
func (initController *InitController) collectCurrentInitPhases(facts []store.Fact) map[string]string {
	phases := make(map[string]string)
	phaseSuffix := "/init/phase"

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.PrefixObserved+"/instance/") || !strings.HasSuffix(fact.Key, phaseSuffix) {
			continue
		}
		instanceID := fact.Key[len(types.PrefixObserved+"/instance/") : len(fact.Key)-len(phaseSuffix)]
		phases[instanceID] = string(fact.Value)
	}

	return phases
}

// deriveInitPhase determines the overall init phase for an instance based on
// its observed init step states and the total number of desired steps.
func (initController *InitController) deriveInitPhase(instanceID string, desiredStepCount int, stepStates map[string]string) types.InitPhase {
	succeededCount := 0
	hasRunning := false

	for stepIndex := 0; stepIndex < desiredStepCount; stepIndex++ {
		compositeKey := fmt.Sprintf("%s/%d", instanceID, stepIndex)
		stepState, exists := stepStates[compositeKey]

		if !exists {
			return types.InitPhasePending
		}

		switch types.InitStepState(stepState) {
		case types.InitStepSucceeded:
			succeededCount++
		case types.InitStepFailed:
			return types.InitPhaseFailed
		case types.InitStepRunning:
			hasRunning = true
		case types.InitStepPending:
			if hasRunning {
				return types.InitPhaseRunning
			}
			return types.InitPhasePending
		}
	}

	if succeededCount == desiredStepCount {
		return types.InitPhaseComplete
	}

	if hasRunning {
		return types.InitPhaseRunning
	}

	return types.InitPhasePending
}

// initStepCountForService counts the number of init step marker keys for a
// service by scanning the store directly. Used by the agent to determine how
// many init steps to execute.
func initStepCountForService(ctx context.Context, stateStore store.StateStore, serviceName string) int {
	scanPrefix := types.ScanDesiredServiceInitSteps(serviceName)
	facts, scanError := stateStore.Scan(ctx, scanPrefix)
	if scanError != nil || len(facts) == 0 {
		return 0
	}

	stepCount := 0
	for _, fact := range facts {
		afterPrefix := strings.TrimPrefix(fact.Key, scanPrefix)
		if _, parseError := strconv.Atoi(afterPrefix); parseError == nil {
			stepCount++
		}
	}

	return stepCount
}
