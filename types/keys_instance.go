// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyObservedInstance returns the store path for an instance's root marker key.
// Path: observed/instance/{instanceID}
func KeyObservedInstance(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s", PrefixObserved, instanceID)
}

// KeyObservedInstanceService returns the store path for an instance's parent service name.
// Path: observed/instance/{instanceID}/service
func KeyObservedInstanceService(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/service", PrefixObserved, instanceID)
}

// KeyObservedInstanceNode returns the store path for the node an instance is running on.
// Path: observed/instance/{instanceID}/node
func KeyObservedInstanceNode(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/node", PrefixObserved, instanceID)
}

// KeyObservedInstanceState returns the store path for an instance's lifecycle state.
// Path: observed/instance/{instanceID}/state
func KeyObservedInstanceState(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/state", PrefixObserved, instanceID)
}

// KeyObservedInstanceCreatedAt returns the store path for when an instance was
// first observed. Value is an RFC3339 timestamp written once at first state publish.
// Path: observed/instance/{instanceID}/created-at
func KeyObservedInstanceCreatedAt(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/created-at", PrefixObserved, instanceID)
}

// KeyObservedInstanceStartedAt returns the store path for when the agent last
// started the instance's container (RFC 3339), rewritten at every start, in-place
// restarts included. The rollout controller's min_healthy_time runs from it (M241 D10).
// Path: observed/instance/{instanceID}/started_at
func KeyObservedInstanceStartedAt(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/started_at", PrefixObserved, instanceID)
}

// KeyObservedInstanceImage returns the store path for the container image an instance is running.
// Path: observed/instance/{instanceID}/image
func KeyObservedInstanceImage(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/image", PrefixObserved, instanceID)
}

// KeyObservedInstancePullStatus returns the store path for image pull progress.
// Value is JSON: {"error":"...","attempts":N,"retryAfter":"RFC3339","image":"ref"}.
// Path: observed/instance/{instanceID}/pull-status
func KeyObservedInstancePullStatus(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/pull-status", PrefixObserved, instanceID)
}

// KeyObservedInstanceConfigHash returns the store path for the SHA256 hash of
// all config inputs (env vars, config files, secrets) applied at start time.
// Path: observed/instance/{instanceID}/config-hash
func KeyObservedInstanceConfigHash(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/config-hash", PrefixObserved, instanceID)
}

// KeyObservedInstanceExitReason returns the store path for an instance's exit
// reason. Value is "OOMKilled" or "exit(N)" for non-zero exit codes, or the
// start error when the workload's process could not start (M238).
// Path: observed/instance/{instanceID}/exit-reason
func KeyObservedInstanceExitReason(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/exit-reason", PrefixObserved, instanceID)
}

// KeyObservedInstanceLastExit returns the store path for how the instance's
// container last exited ("exited with code 1", or the process start error).
// Unlike exit-reason it is kept after the restart, for the rollback reason (M241).
// Path: observed/instance/{instanceID}/last_exit
func KeyObservedInstanceLastExit(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/last_exit", PrefixObserved, instanceID)
}

// KeyObservedInstanceIP returns the store path for an instance's assigned IP address.
// Path: observed/instance/{instanceID}/ip
func KeyObservedInstanceIP(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/ip", PrefixObserved, instanceID)
}

// KeyObservedInstanceHostPort returns the store path for the host port
// mapped to a container's first exposed port (legacy single-port format).
// Path: observed/instance/{instanceID}/hostport
func KeyObservedInstanceHostPort(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/hostport", PrefixObserved, instanceID)
}

// KeyObservedInstanceHostPortForPort returns the per-container-port store
// path for a host port mapping. Multi-port services publish one fact per
// container port so the endpoint controller can resolve the correct host
// port for each exposed port.
// Path: observed/instance/{instanceID}/hostport/{containerPort}
func KeyObservedInstanceHostPortForPort(instanceID string, containerPort int) string {
	return fmt.Sprintf("%s/instance/%s/hostport/%d", PrefixObserved, instanceID, containerPort)
}

// KeyObservedInstanceHealth returns the store path for an instance's latest health check result.
// Path: observed/instance/{instanceID}/health
func KeyObservedInstanceHealth(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/health", PrefixObserved, instanceID)
}

// KeyObservedInstanceOrdinal returns the store path for a stateful instance's
// ordinal index within its service (e.g. "0", "1", "2").
// Path: observed/instance/{instanceID}/ordinal
func KeyObservedInstanceOrdinal(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/ordinal", PrefixObserved, instanceID)
}

// Workload utilization keys. The agent reports per-instance resource usage
// under the telemetry prefix so controllers watching observed/instance/ are
// not triggered by high-frequency telemetry updates.

// KeyObservedInstanceCPU returns the store path for an instance's current CPU
// usage in millicores.
// Path: observed/telemetry/instance/{instanceID}/cpu
func KeyObservedInstanceCPU(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/cpu", PrefixObservedTelemetry, instanceID)
}

// KeyObservedInstanceMemory returns the store path for an instance's current
// memory usage in bytes.
// Path: observed/telemetry/instance/{instanceID}/memory
func KeyObservedInstanceMemory(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/memory", PrefixObservedTelemetry, instanceID)
}

// KeyObservedInstanceOOMKills returns the store path for an instance's cumulative
// OOM kill count from cgroup memory events.
// Path: observed/telemetry/instance/{instanceID}/oom-kills
func KeyObservedInstanceOOMKills(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/oom-kills", PrefixObservedTelemetry, instanceID)
}

// KeyObservedInstanceRestarts returns the store path for an instance's restart count.
// Path: observed/instance/{instanceID}/restarts
func KeyObservedInstanceRestarts(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/restarts", PrefixObserved, instanceID)
}

// KeyObservedInstanceStatusReason returns the store path for an instance's
// human-readable status reason explaining why it is in its current state.
// Value examples: "pulling image", "init:0 connection:airflow-db attempt 6/31: TCP dial failed",
// "startup probe 5/30", "running".
// Path: observed/instance/{instanceID}/status-reason
func KeyObservedInstanceStatusReason(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/status-reason", PrefixObserved, instanceID)
}

// KeyObservedInstanceInitProgress returns the store path for an instance's
// init step progress summary. Value is JSON with current step, attempt, total
// steps, and last error. Deleted when init completes.
// Path: observed/instance/{instanceID}/init-progress
func KeyObservedInstanceInitProgress(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/init-progress", PrefixObserved, instanceID)
}

// KeyObservedInstanceInitStepState returns the store path for an instance's init step result.
// Path: observed/instance/{instanceID}/init/step/{index}/state
func KeyObservedInstanceInitStepState(instanceID string, stepIndex int) string {
	return fmt.Sprintf("%s/instance/%s/init/step/%d/state", PrefixObserved, instanceID, stepIndex)
}

// KeyObservedInstanceInitStepReason returns the store path for an init step's failure reason.
// Path: observed/instance/{instanceID}/init/step/{index}/reason
func KeyObservedInstanceInitStepReason(instanceID string, stepIndex int) string {
	return fmt.Sprintf("%s/instance/%s/init/step/%d/reason", PrefixObserved, instanceID, stepIndex)
}

// ScanObservedInstanceInitSteps returns the scan prefix for all init step observations of an instance.
func ScanObservedInstanceInitSteps(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/init/step/", PrefixObserved, instanceID)
}

// KeyDerivedInstanceImage returns the store path for the image an instance was
// created with. The instance controller records it at creation; the agent starts
// and restarts the instance only with it (M241 D1). It shadows the observed image
// in merged instance fields, which is the image the instance runs.
// Path: derived/instance/{instanceID}/image
func KeyDerivedInstanceImage(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/image", PrefixDerived, instanceID)
}

// KeyDerivedInstanceInitPhase returns the store path for a controller-derived init
// phase. The init controller is the sole authority for this key; it derives the
// phase from per-step observed results written by the agent.
// Path: derived/instance/{instanceID}/init/phase
func KeyDerivedInstanceInitPhase(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/init/phase", PrefixDerived, instanceID)
}

// KeyDerivedInstanceDrainSince returns the store path for the Unix-millisecond
// timestamp when the failure controller began draining an instance.
// Path: derived/instance/{instanceID}/drain_since
func KeyDerivedInstanceDrainSince(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/drain_since", PrefixDerived, instanceID)
}

// KeyDerivedInstanceDrainReadiness returns the store path for a controller-derived
// drain readiness override. Written by the FailureController when draining an
// instance, separate from the agent-owned observed probe readiness key.
// Path: derived/instance/{instanceID}/drain_readiness
func KeyDerivedInstanceDrainReadiness(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/drain_readiness", PrefixDerived, instanceID)
}

// KeyDerivedInstanceNodeFailure returns the derived node-failure marker path.
// Path: derived/instance/{instanceID}/node_failure
func KeyDerivedInstanceNodeFailure(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/node_failure", PrefixDerived, instanceID)
}

// KeyDerivedInstanceControllerStopped returns the derived stopped marker path.
// Path: derived/instance/{instanceID}/controller_stopped
func KeyDerivedInstanceControllerStopped(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/controller_stopped", PrefixDerived, instanceID)
}

// KeyDerivedCrashLoopCounter returns the crash-loop backoff counter path for a
// service. Value is JSON: {"count":N,"since":<unix_ms>}.
// Path: derived/crash-loop/{serviceName}
func KeyDerivedCrashLoopCounter(serviceName string) string {
	return fmt.Sprintf("%s/crash-loop/%s", PrefixDerived, serviceName)
}

// ScanDerivedCrashLoops scans all crash-loop backoff counters.
const ScanDerivedCrashLoops = PrefixDerived + "/crash-loop/"
