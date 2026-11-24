// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyDesiredService returns the store path for a service's root marker key.
// Path: desired/service/{name}
func KeyDesiredService(name string) string {
	return fmt.Sprintf("%s/service/%s", PrefixDesired, name)
}

// KeyDesiredServiceImage returns the store path for a service's container image.
// Path: desired/service/{name}/image
func KeyDesiredServiceImage(name string) string {
	return fmt.Sprintf("%s/service/%s/image", PrefixDesired, name)
}

// KeyDesiredServiceCommand returns the store path for a service's container command override.
// Path: desired/service/{name}/command
func KeyDesiredServiceCommand(name string) string {
	return fmt.Sprintf("%s/service/%s/command", PrefixDesired, name)
}

// KeyDesiredServicePullPolicy returns the store path for a service's image pull policy.
// Path: desired/service/{name}/pull_policy
func KeyDesiredServicePullPolicy(name string) string {
	return fmt.Sprintf("%s/service/%s/pull_policy", PrefixDesired, name)
}

// KeyDesiredServiceImagePullSecret returns the store path for a service's registry credential secret name.
// Path: desired/service/{name}/image_pull_secret
func KeyDesiredServiceImagePullSecret(name string) string {
	return fmt.Sprintf("%s/service/%s/image_pull_secret", PrefixDesired, name)
}

// KeyDesiredServiceLoggingMaxSize returns the store path for the max log file size.
// Path: desired/service/{name}/logging/max_size
func KeyDesiredServiceLoggingMaxSize(name string) string {
	return fmt.Sprintf("%s/service/%s/logging/max_size", PrefixDesired, name)
}

// KeyDesiredServiceLoggingMaxFiles returns the store path for the max rotated log file count.
// Path: desired/service/{name}/logging/max_files
func KeyDesiredServiceLoggingMaxFiles(name string) string {
	return fmt.Sprintf("%s/service/%s/logging/max_files", PrefixDesired, name)
}

// KeyDesiredServiceLifecyclePreStop returns the store path for the pre-stop hook command.
// Path: desired/service/{name}/lifecycle/pre_stop
func KeyDesiredServiceLifecyclePreStop(name string) string {
	return fmt.Sprintf("%s/service/%s/lifecycle/pre_stop", PrefixDesired, name)
}

// KeyDesiredServiceLifecyclePostStart returns the store path for the post-start hook command.
// Path: desired/service/{name}/lifecycle/post_start
func KeyDesiredServiceLifecyclePostStart(name string) string {
	return fmt.Sprintf("%s/service/%s/lifecycle/post_start", PrefixDesired, name)
}

// KeyDesiredServiceTerminationGracePeriod returns the store path for the
// service's SIGTERM-to-SIGKILL grace period override (e.g. "600s").
// Path: desired/service/{name}/termination_grace_period
func KeyDesiredServiceTerminationGracePeriod(name string) string {
	return fmt.Sprintf("%s/service/%s/termination_grace_period", PrefixDesired, name)
}

// KeyDesiredServiceExpose returns the store path for a service's exposed port entry.
// Path: desired/service/{name}/expose/{port}
func KeyDesiredServiceExpose(name string, port int) string {
	return fmt.Sprintf("%s/service/%s/expose/%d", PrefixDesired, name, port)
}

// KeyDesiredServiceResourcesCPU returns the store path for a service's CPU resource requirement.
// Path: desired/service/{name}/resources/cpu
func KeyDesiredServiceResourcesCPU(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/cpu", PrefixDesired, name)
}

// KeyDesiredServiceResourcesMemory returns the store path for a service's memory resource requirement.
// Path: desired/service/{name}/resources/memory
func KeyDesiredServiceResourcesMemory(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/memory", PrefixDesired, name)
}

// KeyDesiredServiceResourcesCPULimit returns the store path for a service's CPU limit.
// Path: desired/service/{name}/resources/cpu_limit
func KeyDesiredServiceResourcesCPULimit(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/cpu_limit", PrefixDesired, name)
}

// KeyDesiredServiceResourcesMemoryLimit returns the store path for a service's memory limit.
// Path: desired/service/{name}/resources/memory_limit
func KeyDesiredServiceResourcesMemoryLimit(name string) string {
	return fmt.Sprintf("%s/service/%s/resources/memory_limit", PrefixDesired, name)
}

// KeyDerivedServiceQoSClass returns the store path for a service's derived QoS class.
// Path: derived/service/{name}/qos_class
func KeyDerivedServiceQoSClass(name string) string {
	return fmt.Sprintf("%s/service/%s/qos_class", PrefixDerived, name)
}

// KeyDesiredServiceHealthMethod returns the store path for a service's health check method (http, tcp, exec).
// Path: desired/service/{name}/health/method
func KeyDesiredServiceHealthMethod(name string) string {
	return fmt.Sprintf("%s/service/%s/health/method", PrefixDesired, name)
}

// KeyDesiredServiceHealthPath returns the store path for a service's health check HTTP path.
// Path: desired/service/{name}/health/path
func KeyDesiredServiceHealthPath(name string) string {
	return fmt.Sprintf("%s/service/%s/health/path", PrefixDesired, name)
}

// KeyDesiredServiceHealthInterval returns the store path for a service's health check interval.
// Path: desired/service/{name}/health/interval
func KeyDesiredServiceHealthInterval(name string) string {
	return fmt.Sprintf("%s/service/%s/health/interval", PrefixDesired, name)
}

// KeyDesiredServiceHealthTimeout returns the store path for a service's health check timeout.
// Path: desired/service/{name}/health/timeout
func KeyDesiredServiceHealthTimeout(name string) string {
	return fmt.Sprintf("%s/service/%s/health/timeout", PrefixDesired, name)
}

// KeyDesiredServiceTag returns the store path for a user-defined tag on a service.
// Tags are metadata for grouping and filtering — not used for scheduling or identity.
// Path: desired/service/{name}/tag/{tagKey}
func KeyDesiredServiceTag(serviceName string, tagKey string) string {
	return fmt.Sprintf("%s/service/%s/tag/%s", PrefixDesired, serviceName, tagKey)
}

// KeyDesiredServiceTagPrefix returns the scan prefix for all tags of a service.
// Path: desired/service/{name}/tag/
func KeyDesiredServiceTagPrefix(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/tag/", PrefixDesired, serviceName)
}

// KeyDesiredServiceStateful returns the store path for a service's stateful
// flag. When "true", the instance controller uses ordinal IDs, ordered
// startup, and reverse scale-down.
// Path: desired/service/{name}/stateful
func KeyDesiredServiceStateful(name string) string {
	return fmt.Sprintf("%s/service/%s/stateful", PrefixDesired, name)
}

// KeyEffectiveServiceStateful returns the store path for a service's effective
// stateful flag, propagated from desired state.
// Path: effective/service/{name}/stateful
func KeyEffectiveServiceStateful(name string) string {
	return fmt.Sprintf("%s/service/%s/stateful", PrefixEffective, name)
}

// KeyDesiredServiceConfigEnv returns the store path for a service's environment variable.
// Path: desired/service/{name}/config/env/{varName}
func KeyDesiredServiceConfigEnv(serviceName, varName string) string {
	return fmt.Sprintf("%s/service/%s/config/env/%s", PrefixDesired, serviceName, varName)
}

// KeyDesiredServiceConfigFile returns the store path for a service's config file.
// Path: desired/service/{name}/config/file/{path}
func KeyDesiredServiceConfigFile(serviceName, filePath string) string {
	return fmt.Sprintf("%s/service/%s/config/file/%s", PrefixDesired, serviceName, filePath)
}

// KeyDesiredServiceSecret returns the store path for a service's secret grant.
// The value is the mount path.
// Path: desired/service/{name}/secret/{secretName}
func KeyDesiredServiceSecret(serviceName, secretName string) string {
	return fmt.Sprintf("%s/service/%s/secret/%s", PrefixDesired, serviceName, secretName)
}

// ScanDesiredServiceConfig returns the scan prefix for all config facts of a service.
func ScanDesiredServiceConfig(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/config/", PrefixDesired, serviceName)
}

// ScanDesiredServiceSecrets returns the scan prefix for all secret grants of a service.
func ScanDesiredServiceSecrets(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/secret/", PrefixDesired, serviceName)
}

// Connection keys. Top-level connection definitions are stored as JSON under
// desired/connection/{name}. Service-level connection references are stored
// under desired/service/{svc}/connection/{name} with the mount path as value.

// KeyDesiredConnection returns the store path for a top-level connection definition.
// Path: desired/connection/{name}
func KeyDesiredConnection(connectionName string) string {
	return fmt.Sprintf("%s/connection/%s", PrefixDesired, connectionName)
}

// ScanDesiredConnections returns the scan prefix for all connection definitions.
func ScanDesiredConnections() string {
	return fmt.Sprintf("%s/connection/", PrefixDesired)
}

// KeyDesiredServiceConnection returns the store path for a service's connection
// mount reference. The value is the mount path.
// Path: desired/service/{name}/connection/{connectionName}
func KeyDesiredServiceConnection(serviceName, connectionName string) string {
	return fmt.Sprintf("%s/service/%s/connection/%s", PrefixDesired, serviceName, connectionName)
}

// ScanDesiredServiceConnections returns the scan prefix for all connection
// references of a service.
func ScanDesiredServiceConnections(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/connection/", PrefixDesired, serviceName)
}

// Init step key functions. Init steps are ordered by index (0, 1, 2, ...) and
// stored as desired facts per service. Observed init results are per-instance.

// KeyDesiredServiceInitStep returns the store path for an init step's root marker.
// Path: desired/service/{name}/init/{index}
func KeyDesiredServiceInitStep(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d", PrefixDesired, serviceName, stepIndex)
}

// KeyDesiredServiceInitStepExec returns the store path for an init step's exec command.
// Path: desired/service/{name}/init/{index}/exec
func KeyDesiredServiceInitStepExec(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d/exec", PrefixDesired, serviceName, stepIndex)
}

// KeyDesiredServiceInitStepTimeout returns the store path for an init step's timeout.
// Path: desired/service/{name}/init/{index}/timeout
func KeyDesiredServiceInitStepTimeout(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d/timeout", PrefixDesired, serviceName, stepIndex)
}

// KeyDesiredServiceInitStepRetry returns the store path for an init step's retry count.
// Path: desired/service/{name}/init/{index}/retry
func KeyDesiredServiceInitStepRetry(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d/retry", PrefixDesired, serviceName, stepIndex)
}

// KeyDesiredServiceInitStepConnection returns the store path for an init step's
// connection probe name. Mutually exclusive with the exec key for the same step.
// Path: desired/service/{name}/init/{index}/connection
func KeyDesiredServiceInitStepConnection(serviceName string, stepIndex int) string {
	return fmt.Sprintf("%s/service/%s/init/%d/connection", PrefixDesired, serviceName, stepIndex)
}

// ScanDesiredServiceInitSteps returns the scan prefix for all init steps of a service.
func ScanDesiredServiceInitSteps(serviceName string) string {
	return fmt.Sprintf("%s/service/%s/init/", PrefixDesired, serviceName)
}
