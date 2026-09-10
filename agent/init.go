package agent

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// initStepDefinition holds the parsed configuration for a single init step
// read from the desired-state section of the fact store.
type initStepDefinition struct {
	index   int           // index is the ordinal position of this step (0-based).
	exec    string        // exec is the command to run.
	timeout time.Duration // timeout is the maximum time for the step to complete (0 = no timeout).
	retry   int           // retry is the number of retry attempts on failure (0 = no retries).
}

// executeInitializationSteps runs all init steps for an instance sequentially.
// Each step is executed via the runtime's Exec method. Step results are reported
// to the store as observed init step state facts. Returns true if all steps
// succeeded, false if any step failed or the instance has no init steps.
func (nodeAgent *Agent) executeInitializationSteps(ctx context.Context, instanceInfo placedInstanceInfo) bool {
	stepDefinitions := nodeAgent.loadInitStepDefinitions(ctx, instanceInfo.service)
	if len(stepDefinitions) == 0 {
		return true
	}

	currentPhase := nodeAgent.readCurrentInitPhase(ctx, instanceInfo.id)
	if currentPhase == string(types.InitPhaseComplete) {
		return true
	}

	nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitPhase(instanceInfo.id), []byte(string(types.InitPhaseRunning)))

	for _, stepDefinition := range stepDefinitions {
		existingState := nodeAgent.readInitStepState(ctx, instanceInfo.id, stepDefinition.index)
		if existingState == string(types.InitStepSucceeded) {
			continue
		}

		succeeded := nodeAgent.executeInitStep(ctx, instanceInfo, stepDefinition)
		if !succeeded {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitPhase(instanceInfo.id), []byte(string(types.InitPhaseFailed)))
			return false
		}
	}

	nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitPhase(instanceInfo.id), []byte(string(types.InitPhaseComplete)))
	return true
}

// executeInitStep runs a single init step with retries and timeout. Reports step
// state transitions (running → succeeded/failed) to the store. Returns true if
// the step eventually succeeded.
func (nodeAgent *Agent) executeInitStep(ctx context.Context, instanceInfo placedInstanceInfo, stepDefinition initStepDefinition) bool {
	maxAttempts := 1 + stepDefinition.retry
	for attemptIndex := 0; attemptIndex < maxAttempts; attemptIndex++ {
		nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepRunning)))

		execError := nodeAgent.runInitCommand(ctx, instanceInfo.id, stepDefinition)

		if execError == nil {
			nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepSucceeded)))
			log.Printf("agent %s: init step %d (%s) succeeded for instance %s",
				nodeAgent.nodeID, stepDefinition.index, stepDefinition.exec, instanceInfo.id)
			return true
		}

		log.Printf("agent %s: init step %d (%s) failed for instance %s (attempt %d/%d): %v",
			nodeAgent.nodeID, stepDefinition.index, stepDefinition.exec, instanceInfo.id,
			attemptIndex+1, maxAttempts, execError)

		if attemptIndex < maxAttempts-1 {
			backoffDuration := time.Duration(1<<uint(attemptIndex)) * time.Second
			select {
			case <-ctx.Done():
				nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepFailed)))
				nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepReason(instanceInfo.id, stepDefinition.index), []byte(ctx.Err().Error()))
				return false
			case <-time.After(backoffDuration):
			}
		}
	}

	nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepFailed)))
	nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepReason(instanceInfo.id, stepDefinition.index), []byte("max retries exceeded"))
	return false
}

// runInitCommand executes the init step's command as a standalone subprocess.
// Init steps run before the main workload starts, so they execute directly on
// the host rather than inside a container. Applies a timeout if configured.
func (nodeAgent *Agent) runInitCommand(ctx context.Context, instanceID string, stepDefinition initStepDefinition) error {
	execCtx := ctx
	var cancelFunc context.CancelFunc
	if stepDefinition.timeout > 0 {
		execCtx, cancelFunc = context.WithTimeout(ctx, stepDefinition.timeout)
		defer cancelFunc()
	}

	commandParts := strings.Fields(stepDefinition.exec)
	if len(commandParts) == 0 {
		return fmt.Errorf("empty init exec command for instance %s step %d", instanceID, stepDefinition.index)
	}

	command := exec.CommandContext(execCtx, commandParts[0], commandParts[1:]...)
	return command.Run()
}

// loadInitStepDefinitions reads the desired init step configuration for a service
// from the store and returns them in order.
func (nodeAgent *Agent) loadInitStepDefinitions(ctx context.Context, serviceName string) []initStepDefinition {
	scanPrefix := types.ScanDesiredServiceInitSteps(serviceName)
	initFacts, scanError := nodeAgent.store.Scan(ctx, scanPrefix)
	if scanError != nil || len(initFacts) == 0 {
		return nil
	}

	stepCount := 0
	for _, fact := range initFacts {
		afterPrefix := strings.TrimPrefix(fact.Key, scanPrefix)
		if _, parseError := strconv.Atoi(afterPrefix); parseError == nil {
			stepCount++
		}
	}
	if stepCount == 0 {
		return nil
	}

	stepDefinitions := make([]initStepDefinition, stepCount)
	for stepIndex := 0; stepIndex < stepCount; stepIndex++ {
		stepDefinitions[stepIndex] = initStepDefinition{index: stepIndex}

		execFact, execError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceInitStepExec(serviceName, stepIndex))
		if execError == nil {
			stepDefinitions[stepIndex].exec = string(execFact.Value)
		}

		timeoutFact, timeoutError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceInitStepTimeout(serviceName, stepIndex))
		if timeoutError == nil {
			parsedTimeout, parseError := time.ParseDuration(string(timeoutFact.Value))
			if parseError == nil {
				stepDefinitions[stepIndex].timeout = parsedTimeout
			}
		}

		retryFact, retryError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceInitStepRetry(serviceName, stepIndex))
		if retryError == nil {
			parsedRetry, parseError := strconv.Atoi(string(retryFact.Value))
			if parseError == nil {
				stepDefinitions[stepIndex].retry = parsedRetry
			}
		}
	}

	return stepDefinitions
}

// readCurrentInitPhase reads the current init phase for an instance from the store.
func (nodeAgent *Agent) readCurrentInitPhase(ctx context.Context, instanceID string) string {
	phaseFact, phaseError := nodeAgent.store.Get(ctx, types.KeyObservedInstanceInitPhase(instanceID))
	if phaseError != nil {
		return ""
	}
	return string(phaseFact.Value)
}

// readInitStepState reads the current state of a specific init step for an instance.
func (nodeAgent *Agent) readInitStepState(ctx context.Context, instanceID string, stepIndex int) string {
	stateFact, stateError := nodeAgent.store.Get(ctx, types.KeyObservedInstanceInitStepState(instanceID, stepIndex))
	if stateError != nil {
		return ""
	}
	return string(stateFact.Value)
}

// hasInitSteps returns true if the named service has any desired init steps.
func (nodeAgent *Agent) hasInitSteps(ctx context.Context, serviceName string) bool {
	scanPrefix := types.ScanDesiredServiceInitSteps(serviceName)
	facts, scanError := nodeAgent.store.Scan(ctx, scanPrefix)
	if scanError != nil {
		return false
	}
	for _, fact := range facts {
		afterPrefix := strings.TrimPrefix(fact.Key, scanPrefix)
		if _, parseError := strconv.Atoi(afterPrefix); parseError == nil {
			return true
		}
	}
	return false
}

// initPhaseDescription returns a human-readable description of an init step
// failure for logging purposes.
func initPhaseDescription(stepIndex int, exec string, reason string) string {
	return fmt.Sprintf("init step %d (%s): %s", stepIndex, exec, reason)
}
