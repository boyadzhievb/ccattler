// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	goruntime "github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/types"
)

// initStepBackoffBaseDelay is the base delay for exponential backoff between
// init step retry attempts. Each subsequent attempt doubles this value.
const initStepBackoffBaseDelay = 1 * time.Second

// connectionProbeDefaultRetry is the default number of retries for connection
// init steps when no explicit retry is configured. Connection probes often need
// retries because the target service may still be starting.
const connectionProbeDefaultRetry = 30

// initStepMaxBackoffDelay is the maximum backoff delay between init step retry
// attempts. Connection probes retry faster than crash loops (30s vs 5m) because
// the target service may become reachable at any moment.
const initStepMaxBackoffDelay = 30 * time.Second

// initStepDefinition holds the parsed configuration for a single init step
// read from the desired-state section of the fact store. Exactly one of exec
// or connection is set — exec runs a command, connection probes a named
// connection for reachability and authentication.
type initStepDefinition struct {
	index      int           // index is the ordinal position of this step (0-based).
	exec       string        // exec is the command to run (mutually exclusive with connection).
	connection string        // connection is the named connection to probe (mutually exclusive with exec).
	timeout    time.Duration // timeout is the maximum time for the step to complete (0 = no timeout).
	retry      int           // retry is the number of retry attempts on failure (0 = no retries).
}

// initStepBackoffEntry tracks retry state for a single init step across
// reconcile cycles. Instead of blocking the reconcile loop with a retry
// loop, each cycle attempts once and records when to retry next.
type initStepBackoffEntry struct {
	attemptCount int       // attemptCount is the number of attempts made so far.
	nextRetryAt  time.Time // nextRetryAt is the earliest time the next attempt should be made.
}

// executeInitializationSteps runs all init steps for an instance sequentially.
// Each step is executed via the runtime's ExecInit method against the service
// image. Returns true if all steps succeeded, false if any step failed.
func (nodeAgent *Agent) executeInitializationSteps(ctx context.Context, instanceInfo placedInstanceInfo, image string) bool {
	stepDefinitions := nodeAgent.loadInitStepDefinitions(ctx, instanceInfo.service)
	if len(stepDefinitions) == 0 {
		return true
	}

	currentPhase := nodeAgent.readCurrentInitPhase(ctx, instanceInfo.id)
	if currentPhase == string(types.InitPhaseComplete) {
		return true
	}

	for _, stepDefinition := range stepDefinitions {
		existingState := nodeAgent.readInitStepState(ctx, instanceInfo.id, stepDefinition.index)
		if existingState == string(types.InitStepSucceeded) {
			continue
		}
		if existingState == string(types.InitStepRunning) {
			if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepFailed))); putError != nil {
				logging.Default().Error("failed to write init step state", "instance", instanceInfo.id, "step", fmt.Sprintf("%d", stepDefinition.index), "error", putError.Error())
			}
			if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepReason(instanceInfo.id, stepDefinition.index), []byte("agent restarted during execution")); putError != nil {
				logging.Default().Error("failed to write init step reason", "instance", instanceInfo.id, "step", fmt.Sprintf("%d", stepDefinition.index), "error", putError.Error())
			}
		}

		succeeded := nodeAgent.executeInitStep(ctx, instanceInfo, stepDefinition, image, len(stepDefinitions))
		if !succeeded {
			return false
		}
	}

	nodeAgent.clearInitProgress(ctx, instanceInfo.id)
	return true
}

// executeInitStep attempts a single init step once per reconcile cycle. Backoff
// between attempts is tracked across cycles in nodeAgent.initStepBackoffs so
// the reconcile loop never blocks on retries — it returns immediately and the
// next reconcile cycle re-enters for the next attempt.
func (nodeAgent *Agent) executeInitStep(ctx context.Context, instanceInfo placedInstanceInfo, stepDefinition initStepDefinition, image string, totalSteps int) bool {
	maxAttempts := 1 + stepDefinition.retry
	stepLabel := stepDefinition.exec
	if stepDefinition.connection != "" {
		stepLabel = "connection:" + stepDefinition.connection
	}

	backoffKey := instanceInfo.id + "/" + strconv.Itoa(stepDefinition.index)
	backoffEntry, hasBackoffEntry := nodeAgent.initStepBackoffs[backoffKey]
	if !hasBackoffEntry {
		backoffEntry = &initStepBackoffEntry{}
		nodeAgent.initStepBackoffs[backoffKey] = backoffEntry
	}

	if backoffEntry.attemptCount >= maxAttempts {
		if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepFailed))); putError != nil {
			logging.Default().Error("failed to write init step state", "instance", instanceInfo.id, "step", fmt.Sprintf("%d", stepDefinition.index), "error", putError.Error())
		}
		if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepReason(instanceInfo.id, stepDefinition.index), []byte("max retries exceeded")); putError != nil {
			logging.Default().Error("failed to write init step reason", "instance", instanceInfo.id, "step", fmt.Sprintf("%d", stepDefinition.index), "error", putError.Error())
		}
		return false
	}

	if !backoffEntry.nextRetryAt.IsZero() && time.Now().Before(backoffEntry.nextRetryAt) {
		return false
	}

	nodeAgent.publishInitProgress(ctx, instanceInfo.id, stepDefinition.index, totalSteps, backoffEntry.attemptCount, maxAttempts, stepLabel, "")

	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepRunning))); putError != nil {
		logging.Default().Error("failed to write init step state", "instance", instanceInfo.id, "step", fmt.Sprintf("%d", stepDefinition.index), "error", putError.Error())
	}

	var stepError error
	if stepDefinition.connection != "" {
		stepError = nodeAgent.runConnectionProbe(ctx, stepDefinition, instanceInfo.service)
	} else {
		stepError = nodeAgent.runInitCommand(ctx, image, instanceInfo.service, stepDefinition)
	}

	if stepError == nil {
		if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitStepState(instanceInfo.id, stepDefinition.index), []byte(string(types.InitStepSucceeded))); putError != nil {
			logging.Default().Error("failed to write init step state", "instance", instanceInfo.id, "step", fmt.Sprintf("%d", stepDefinition.index), "error", putError.Error())
		}
		logging.Default().Info("init step succeeded",
			"agent", nodeAgent.nodeID,
			"step", fmt.Sprintf("%d", stepDefinition.index),
			"exec", stepLabel,
			"instance", instanceInfo.id)
		delete(nodeAgent.initStepBackoffs, backoffKey)
		return true
	}

	backoffEntry.attemptCount++
	backoffDuration := initStepBackoffBaseDelay * time.Duration(1<<uint(backoffEntry.attemptCount-1))
	if backoffDuration > initStepMaxBackoffDelay {
		backoffDuration = initStepMaxBackoffDelay
	}
	backoffEntry.nextRetryAt = time.Now().Add(backoffDuration)

	nodeAgent.publishInitProgress(ctx, instanceInfo.id, stepDefinition.index, totalSteps, backoffEntry.attemptCount, maxAttempts, stepLabel, stepError.Error())

	logging.Default().Warn("init step failed",
		"agent", nodeAgent.nodeID,
		"step", fmt.Sprintf("%d", stepDefinition.index),
		"exec", stepLabel,
		"instance", instanceInfo.id,
		"attempt", fmt.Sprintf("%d/%d", backoffEntry.attemptCount, maxAttempts),
		"error", stepError.Error())

	return false
}

// publishInitProgress writes the current init step progress to the store as a
// JSON fact so the CLI and API can display convergence status.
func (nodeAgent *Agent) publishInitProgress(ctx context.Context, instanceID string, stepIndex int, totalSteps int, attempt int, maxAttempts int, stepLabel string, lastError string) {
	progress := types.InitProgress{
		Step:       stepIndex,
		TotalSteps: totalSteps,
		Attempt:    attempt,
		MaxRetries: maxAttempts,
		StepLabel:  stepLabel,
		LastError:  lastError,
	}
	progressJSON, marshalError := json.Marshal(progress)
	if marshalError != nil {
		return
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceInitProgress(instanceID), progressJSON); putError != nil {
		logging.Default().Error("failed to write init progress", "instance", instanceID, "error", putError.Error())
	}
	var statusReason string
	if lastError != "" {
		statusReason = fmt.Sprintf("init:%d %s attempt %d/%d: %s", stepIndex, stepLabel, attempt, maxAttempts, lastError)
	} else {
		statusReason = fmt.Sprintf("init:%d/%d %s", stepIndex, totalSteps, stepLabel)
	}
	if _, putError := nodeAgent.store.Put(ctx, types.KeyObservedInstanceStatusReason(instanceID), []byte(statusReason)); putError != nil {
		logging.Default().Error("failed to write status reason", "instance", instanceID, "error", putError.Error())
	}
}

// clearInitProgress removes the init progress fact after all steps complete.
func (nodeAgent *Agent) clearInitProgress(ctx context.Context, instanceID string) {
	_ = nodeAgent.store.Delete(ctx, types.KeyObservedInstanceInitProgress(instanceID))
}

// runInitCommand executes the init step's command via the runtime's ExecInit
// method. For container runtimes, this runs the command in a temporary container
// from the service image. The service's config env vars are passed so init steps
// can access the same configuration as the main workload (e.g. DB connection
// strings). Applies a timeout if configured.
func (nodeAgent *Agent) runInitCommand(ctx context.Context, image string, serviceName string, stepDefinition initStepDefinition) error {
	execCtx := ctx
	var cancelFunc context.CancelFunc
	if stepDefinition.timeout > 0 {
		execCtx, cancelFunc = context.WithTimeout(ctx, stepDefinition.timeout)
		defer cancelFunc()
	}

	if strings.TrimSpace(stepDefinition.exec) == "" {
		return fmt.Errorf("empty init exec command for step %d", stepDefinition.index)
	}

	serviceEnvVars := nodeAgent.resolveServiceConfigEnvVars(execCtx, serviceName)
	initSpec := goruntime.ExecSpec{
		Command: stepDefinition.exec,
		Env:     serviceEnvVars,
	}
	nodeAgent.materializeInitConfigFiles(execCtx, serviceName, &initSpec)
	return nodeAgent.runtime.ExecInit(execCtx, image, initSpec)
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

		connectionFact, connectionError := nodeAgent.store.Get(ctx, types.KeyDesiredServiceInitStepConnection(serviceName, stepIndex))
		if connectionError == nil {
			stepDefinitions[stepIndex].connection = string(connectionFact.Value)
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

		if stepDefinitions[stepIndex].connection != "" && stepDefinitions[stepIndex].retry == 0 {
			stepDefinitions[stepIndex].retry = connectionProbeDefaultRetry
		}
	}

	return stepDefinitions
}

// readCurrentInitPhase reads the current init phase for an instance from the store.
func (nodeAgent *Agent) readCurrentInitPhase(ctx context.Context, instanceID string) string {
	phaseFact, phaseError := nodeAgent.store.Get(ctx, types.KeyDerivedInstanceInitPhase(instanceID))
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

// runConnectionProbe validates that a named connection is reachable by doing a
// TCP dial to host:port. The connection definition is read from the store and
// the URL is assembled (including secret resolution) to verify the full chain.
// Applies the step timeout if configured.
func (nodeAgent *Agent) runConnectionProbe(ctx context.Context, stepDefinition initStepDefinition, serviceName string) error {
	probeCtx := ctx
	var cancelFunc context.CancelFunc
	if stepDefinition.timeout > 0 {
		probeCtx, cancelFunc = context.WithTimeout(ctx, stepDefinition.timeout)
		defer cancelFunc()
	}

	connectionURL := nodeAgent.assembleConnectionURL(probeCtx, stepDefinition.connection, serviceName)
	if connectionURL == "" {
		return fmt.Errorf("connection %q: failed to assemble URL", stepDefinition.connection)
	}

	connectionFact, getError := nodeAgent.store.Get(probeCtx, types.KeyDesiredConnection(stepDefinition.connection))
	if getError != nil {
		return fmt.Errorf("connection %q: definition not found", stepDefinition.connection)
	}
	connectionDef := parseConnectionJSON(string(connectionFact.Value))

	var dialAddress string
	if connectionDef.port > 0 {
		dialAddress = net.JoinHostPort(connectionDef.host, strconv.Itoa(connectionDef.port))
	} else {
		dialAddress = net.JoinHostPort(connectionDef.host, "0")
	}

	dialer := &net.Dialer{
		Resolver: nodeAgent.buildCCattlerResolver(),
	}
	tcpConnection, dialError := dialer.DialContext(probeCtx, "tcp", dialAddress)
	if dialError != nil {
		return fmt.Errorf("connection %q: TCP dial to %s failed: %w", stepDefinition.connection, dialAddress, dialError)
	}
	_ = tcpConnection.Close()

	logging.Default().Info("connection probe passed",
		"agent", nodeAgent.nodeID,
		"connection", stepDefinition.connection,
		"address", dialAddress)
	return nil
}

// buildCCattlerResolver returns a DNS resolver that queries CCattler's embedded
// DNS server on the advertise address. Falls back to system resolver if no
// advertise address is configured.
func (nodeAgent *Agent) buildCCattlerResolver() *net.Resolver {
	if nodeAgent.advertiseAddress == "" || nodeAgent.dnsForwarder == nil {
		return net.DefaultResolver
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network string, address string) (net.Conn, error) {
			resolverDialer := net.Dialer{}
			return resolverDialer.DialContext(ctx, "udp", net.JoinHostPort(nodeAgent.advertiseAddress, "53"))
		},
	}
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

// materializeInitConfigFiles resolves secrets and connection files for an init
// container so it has the same file mounts as the main workload container.
func (nodeAgent *Agent) materializeInitConfigFiles(ctx context.Context, serviceName string, initSpec *goruntime.ExecSpec) {
	if initSpec.ConfigFiles == nil {
		initSpec.ConfigFiles = make(map[string]string)
	}

	if nodeAgent.secretProvider != nil {
		grantPrefix := types.ScanDesiredServiceSecrets(serviceName)
		grantFacts, scanError := nodeAgent.store.Scan(ctx, grantPrefix)
		if scanError == nil {
			for _, grantFact := range grantFacts {
				secretName := strings.TrimPrefix(grantFact.Key, grantPrefix)
				if secretName == "" {
					continue
				}
				explicitMountPath := string(grantFact.Value)
				plaintext, _, decryptError := nodeAgent.secretProvider.GetSecretForService(ctx, serviceName, secretName)
				if decryptError != nil {
					logging.Default().Error("init: failed to decrypt secret",
						"agent", nodeAgent.nodeID, "service", serviceName,
						"secret", secretName, "error", decryptError.Error())
					continue
				}
				secretValue := string(plaintext)
				initSpec.ConfigFiles[ccaSecretMountPrefix+secretName] = secretValue
				if explicitMountPath != "" && strings.HasPrefix(explicitMountPath, "/") {
					initSpec.ConfigFiles[explicitMountPath] = secretValue
				}
			}
		}
	}

	connectionRefs, scanError := nodeAgent.store.Scan(ctx, types.ScanDesiredServiceConnections(serviceName))
	if scanError == nil {
		for _, connectionRef := range connectionRefs {
			connectionName := strings.TrimPrefix(connectionRef.Key, types.ScanDesiredServiceConnections(serviceName))
			if connectionName == "" {
				continue
			}
			mountPath := string(connectionRef.Value)
			connectionURL := nodeAgent.assembleConnectionURL(ctx, connectionName, serviceName)
			if connectionURL == "" {
				continue
			}
			initSpec.ConfigFiles[mountPath] = connectionURL
		}
	}

	hasSecrets := false
	for path := range initSpec.ConfigFiles {
		if strings.HasPrefix(path, ccaSecretMountPrefix) {
			hasSecrets = true
			break
		}
	}
	if hasSecrets {
		initSpec.ConfigFiles[ccaSecretHelperPath] = ccaSecretHelperScript
	}

	if len(initSpec.ConfigFiles) == 0 {
		initSpec.ConfigFiles = nil
	}
}
