// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyDesiredServiceInstances returns the store path for a service's desired instance count.
// Path: desired/service/{name}/instances
func KeyDesiredServiceInstances(name string) string {
	return fmt.Sprintf("%s/service/%s/instances", PrefixDesired, name)
}

// KeyEffectiveServiceInstances returns the store path for a service's effective (resolved)
// instance count, derived from all intent layers.
// Path: effective/service/{name}/instances
func KeyEffectiveServiceInstances(name string) string {
	return fmt.Sprintf("%s/service/%s/instances", PrefixEffective, name)
}

// KeyObservedMetric returns the store path for a simulated metric value used by
// the autoscaler. Metrics are keyed by service name and metric name.
// Path: observed/metric/service/{service}/{metric}
func KeyObservedMetric(service, metric string) string {
	return fmt.Sprintf("%s/metric/service/%s/%s", PrefixObserved, service, metric)
}

// KeyIntentUserServiceInstances returns the store path for the user intent layer's
// desired instance count for a service.
// Path: intent/user/service/{name}/instances
func KeyIntentUserServiceInstances(name string) string {
	return fmt.Sprintf("%s/user/service/%s/instances", PrefixIntent, name)
}

// KeyIntentAutoscalerServiceInstances returns the store path for the autoscaler
// intent layer's recommended instance count for a service.
// Path: intent/autoscaler/service/{name}/instances
func KeyIntentAutoscalerServiceInstances(name string) string {
	return fmt.Sprintf("%s/autoscaler/service/%s/instances", PrefixIntent, name)
}

// KeyDesiredServiceScaleHorizontalMin returns the store path for a service's
// horizontal autoscaling minimum instance count.
// Path: desired/service/{name}/scale/horizontal/min
func KeyDesiredServiceScaleHorizontalMin(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/min", PrefixDesired, name)
}

// KeyDesiredServiceScaleHorizontalMax returns the store path for a service's
// horizontal autoscaling maximum instance count.
// Path: desired/service/{name}/scale/horizontal/max
func KeyDesiredServiceScaleHorizontalMax(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/max", PrefixDesired, name)
}

// KeyDesiredServiceScaleHorizontalTarget returns the store path for a single
// autoscaling target metric and its threshold value.
// Path: desired/service/{name}/scale/horizontal/target/{metric}
func KeyDesiredServiceScaleHorizontalTarget(name string, metric string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/target/%s", PrefixDesired, name, metric)
}

// ScanDesiredServiceScaleTargets returns the scan prefix for all horizontal
// autoscaling target metrics of a specific service.
func ScanDesiredServiceScaleTargets(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/target/", PrefixDesired, name)
}

// KeyDesiredServiceScaleStabilizationUp returns the store path for the scale-up
// stabilization window duration in seconds.
func KeyDesiredServiceScaleStabilizationUp(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/stabilization/up", PrefixDesired, name)
}

// KeyDesiredServiceScaleStabilizationDown returns the store path for the scale-down
// stabilization window duration in seconds.
func KeyDesiredServiceScaleStabilizationDown(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/stabilization/down", PrefixDesired, name)
}

// KeyDesiredServiceScaleIdleTimeout returns the store path for the warm-zero
// idle timeout duration. When traffic is idle longer than this, the service
// scales to zero instances.
// Path: desired/service/{name}/scale/horizontal/idle_timeout
func KeyDesiredServiceScaleIdleTimeout(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/idle_timeout", PrefixDesired, name)
}

// KeyDesiredServiceScaleActivationTimeout returns the store path for the
// warm-zero activation timeout. This is the maximum time the proxy will wait
// for a backend to become ready during cold activation.
// Path: desired/service/{name}/scale/horizontal/activation_timeout
func KeyDesiredServiceScaleActivationTimeout(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/activation_timeout", PrefixDesired, name)
}

// KeyObservedServiceLastRequestTime returns the store path for the last time
// the proxy forwarded a request to this service. Written as Unix milliseconds.
// Path: observed/service/{name}/last_request_time
func KeyObservedServiceLastRequestTime(name string) string {
	return fmt.Sprintf("%s/service/%s/last_request_time", PrefixObserved, name)
}

// KeyDerivedServiceActivationState returns the store path for the warm-zero
// activation state of a service: "inactive", "activating", or "active".
// Path: derived/service/{name}/activation/state
func KeyDerivedServiceActivationState(name string) string {
	return fmt.Sprintf("%s/service/%s/activation/state", PrefixDerived, name)
}

// KeyDesiredServiceScaleVerticalCPUMin returns the store path for the minimum CPU
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalCPUMin(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/cpu/min", PrefixDesired, name)
}

// KeyDesiredServiceScaleVerticalCPUMax returns the store path for the maximum CPU
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalCPUMax(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/cpu/max", PrefixDesired, name)
}

// KeyDesiredServiceScaleVerticalMemoryMin returns the store path for the minimum memory
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalMemoryMin(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/memory/min", PrefixDesired, name)
}

// KeyDesiredServiceScaleVerticalMemoryMax returns the store path for the maximum memory
// for vertical autoscaling.
func KeyDesiredServiceScaleVerticalMemoryMax(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/vertical/memory/max", PrefixDesired, name)
}

// KeyIntentAutoscalerServiceResourcesCPU returns the store path for the autoscaler's
// recommended CPU resource for a service.
func KeyIntentAutoscalerServiceResourcesCPU(name string) string {
	return fmt.Sprintf("%s/autoscaler/service/%s/resources/cpu", PrefixIntent, name)
}

// KeyIntentAutoscalerServiceResourcesMemory returns the store path for the autoscaler's
// recommended memory resource for a service.
func KeyIntentAutoscalerServiceResourcesMemory(name string) string {
	return fmt.Sprintf("%s/autoscaler/service/%s/resources/memory", PrefixIntent, name)
}

// KeyEffectiveServiceResourcesCPU returns the store path for a service's effective CPU.
func KeyEffectiveServiceResourcesCPU(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/cpu", PrefixEffective, name)
}

// KeyEffectiveServiceResourcesMemory returns the store path for a service's effective memory.
func KeyEffectiveServiceResourcesMemory(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/memory", PrefixEffective, name)
}

// KeyDesiredServiceScaleHorizontalEvent returns the store path for an event-driven
// scaling source and its target messages-per-instance value.
func KeyDesiredServiceScaleHorizontalEvent(name, source string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/event/%s", PrefixDesired, name, source)
}

// KeyDesiredServiceScaleScheduleDays returns the store path for schedule scaling days.
func KeyDesiredServiceScaleScheduleDays(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/days", PrefixDesired, name)
}

// KeyDesiredServiceScaleScheduleStart returns the store path for schedule scaling start time.
func KeyDesiredServiceScaleScheduleStart(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/start", PrefixDesired, name)
}

// KeyDesiredServiceScaleScheduleEnd returns the store path for schedule scaling end time.
func KeyDesiredServiceScaleScheduleEnd(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/end", PrefixDesired, name)
}

// KeyDesiredServiceScaleScheduleMinimum returns the store path for schedule scaling minimum instances.
func KeyDesiredServiceScaleScheduleMinimum(name string) string {
	return fmt.Sprintf("%s/service/%s/scale/horizontal/schedule/minimum", PrefixDesired, name)
}
