// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyObservedInstanceProbeProgress returns the store path for an instance's
// probe progress summary. Value is JSON with per-probe-type failure counts
// and thresholds. Updated on every probe execution.
// Path: observed/instance/{instanceID}/probe-progress
func KeyObservedInstanceProbeProgress(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/probe-progress", PrefixObserved, instanceID)
}

// Desired probe config keys (per service, per probe type)
// Pattern: desired/service/{name}/probe/{probeType}/{field}
// Probe types: startup, liveness, readiness

// KeyDesiredServiceProbeMethod returns the store path for a probe's check method.
// Path: desired/service/{name}/probe/{probeType}/method
func KeyDesiredServiceProbeMethod(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/method", PrefixDesired, serviceName, probeType)
}

// KeyDesiredServiceProbePath returns the store path for a probe's check path.
// Path: desired/service/{name}/probe/{probeType}/path
func KeyDesiredServiceProbePath(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/path", PrefixDesired, serviceName, probeType)
}

// KeyDesiredServiceProbePort returns the store path for a probe's check port.
// Path: desired/service/{name}/probe/{probeType}/port
func KeyDesiredServiceProbePort(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/port", PrefixDesired, serviceName, probeType)
}

// KeyDesiredServiceProbeInterval returns the store path for a probe's check interval.
// Path: desired/service/{name}/probe/{probeType}/interval
func KeyDesiredServiceProbeInterval(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/interval", PrefixDesired, serviceName, probeType)
}

// KeyDesiredServiceProbeTimeout returns the store path for a probe's check timeout.
// Path: desired/service/{name}/probe/{probeType}/timeout
func KeyDesiredServiceProbeTimeout(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/timeout", PrefixDesired, serviceName, probeType)
}

// KeyDesiredServiceProbeFailureThreshold returns the store path for a probe's failure threshold.
// Path: desired/service/{name}/probe/{probeType}/failure_threshold
func KeyDesiredServiceProbeFailureThreshold(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/failure_threshold", PrefixDesired, serviceName, probeType)
}

// KeyDesiredServiceProbeSuccessThreshold returns the store path for a probe's success threshold.
// Path: desired/service/{name}/probe/{probeType}/success_threshold
func KeyDesiredServiceProbeSuccessThreshold(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/success_threshold", PrefixDesired, serviceName, probeType)
}

// KeyDesiredServiceProbeInitialDelay returns the store path for a probe's initial delay
// before the first check runs.
// Path: desired/service/{name}/probe/{probeType}/initial_delay
func KeyDesiredServiceProbeInitialDelay(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/initial_delay", PrefixDesired, serviceName, probeType)
}

// ScanDesiredServiceProbe returns the scan prefix for all config fields of a
// specific probe type on a service.
// Path: desired/service/{name}/probe/{probeType}/
func ScanDesiredServiceProbe(serviceName string, probeType ProbeType) string {
	return fmt.Sprintf("%s/service/%s/probe/%s/", PrefixDesired, serviceName, probeType)
}

// Observed probe result keys (per instance, per probe type)
// Pattern: observed/instance/{instanceID}/probe/{probeType}

// KeyObservedInstanceProbeState returns the store path for an observed probe
// result on a specific instance and probe type (startup, liveness, readiness).
// Path: observed/instance/{instanceID}/probe/{probeType}
func KeyObservedInstanceProbeState(instanceID string, probeType ProbeType) string {
	return fmt.Sprintf("%s/instance/%s/probe/%s", PrefixObserved, instanceID, probeType)
}

// ScanObservedInstanceProbes returns the scan prefix for all probe results
// on a specific instance.
// Path: observed/instance/{instanceID}/probe/
func ScanObservedInstanceProbes(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/probe/", PrefixObserved, instanceID)
}
