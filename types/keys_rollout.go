// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// KeyDesiredServiceUpdateMaxUnavailable returns the store path for the maximum number
// of instances that can be unavailable during a rolling update.
func KeyDesiredServiceUpdateMaxUnavailable(name string) string {
	return fmt.Sprintf("%s/service/%s/update/max_unavailable", PrefixDesired, name)
}

// KeyDesiredServiceUpdateMaxExtra returns the store path for the maximum number
// of extra instances allowed during a rolling update surge.
func KeyDesiredServiceUpdateMaxExtra(name string) string {
	return fmt.Sprintf("%s/service/%s/update/max_extra", PrefixDesired, name)
}

// KeyDesiredServiceUpdateProgressDeadline returns the store path for how long a
// rollout may go without progress before it is rolled back (Go duration text,
// DSL update { progress_deadline }, M241 D7).
// Path: desired/service/{name}/update/progress_deadline
func KeyDesiredServiceUpdateProgressDeadline(name string) string {
	return fmt.Sprintf("%s/service/%s/update/progress_deadline", PrefixDesired, name)
}

// KeyDesiredServiceUpdateMinHealthyTime returns the store path for how long a
// new instance must stay running (and ready, when probed) before a rollout
// counts it (Go duration text, DSL update { min_healthy_time }, M241 D10).
// Path: desired/service/{name}/update/min_healthy_time
func KeyDesiredServiceUpdateMinHealthyTime(name string) string {
	return fmt.Sprintf("%s/service/%s/update/min_healthy_time", PrefixDesired, name)
}

// KeyDesiredServiceDisruptionMinAvailable returns the store path for the
// minimum number of instances that must remain running during drains and
// rollouts.
func KeyDesiredServiceDisruptionMinAvailable(name string) string {
	return fmt.Sprintf("%s/service/%s/disruption/min_available", PrefixDesired, name)
}

// KeyDesiredServiceDisruptionMaxUnavailable returns the store path for the
// maximum number of instances that may be simultaneously unavailable during
// drains and rollouts.
func KeyDesiredServiceDisruptionMaxUnavailable(name string) string {
	return fmt.Sprintf("%s/service/%s/disruption/max_unavailable", PrefixDesired, name)
}

// Derived key functions (controller-derived state, not agent observations)

// KeyDerivedServiceRolloutState returns the store path for a service's rollout state.
// Path: derived/service/{name}/rollout/state
func KeyDerivedServiceRolloutState(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/state", PrefixDerived, name)
}

// KeyDerivedServiceRolloutImage returns the store path tracking the previous image
// during a rolling update for rollback purposes.
// Path: derived/service/{name}/rollout/previous_image
func KeyDerivedServiceRolloutImage(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/previous_image", PrefixDerived, name)
}

// KeyDerivedServiceRolloutFailures returns the count of failed new-image instances
// during a rollout, used for rollback decisions.
// Path: derived/service/{name}/rollout/failures
func KeyDerivedServiceRolloutFailures(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/failures", PrefixDerived, name)
}

// KeyDerivedServiceRolloutSurgeAllowance returns the store path for the surge
// allowance during a rolling update. The instance controller reads this to
// temporarily increase the effective desired count by max_extra.
// Path: derived/service/{name}/rollout/surge_allowance
func KeyDerivedServiceRolloutSurgeAllowance(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/surge_allowance", PrefixDerived, name)
}

// KeyDerivedServiceRolloutRevision returns the store path for a service's current
// rollout revision counter (monotonically increasing integer).
// Path: derived/service/{name}/rollout/revision
func KeyDerivedServiceRolloutRevision(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/revision", PrefixDerived, name)
}

// KeyDerivedServiceRolloutHistory returns the store path for a historical image
// at a specific revision number. Used for multi-step rollback.
// Path: derived/service/{name}/rollout/history/{revision}
func KeyDerivedServiceRolloutHistory(name string, revision int) string {
	return fmt.Sprintf("%s/service/%s/rollout/history/%d", PrefixDerived, name, revision)
}

// ScanDerivedServiceRolloutHistory returns the prefix for scanning all history entries.
// Path: derived/service/{name}/rollout/history/
func ScanDerivedServiceRolloutHistory(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/history/", PrefixDerived, name)
}

// KeyDerivedServiceRolloutTargetImage returns the store path for the image the
// current rollout (or rollback) moves the service to (M241 D5).
// Path: derived/service/{name}/rollout/target_image
func KeyDerivedServiceRolloutTargetImage(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/target_image", PrefixDerived, name)
}

// KeyDerivedServiceRolloutReason returns the store path for the plain-text
// reason a rollout in progress is waiting (M241 D7).
// Path: derived/service/{name}/rollout/reason
func KeyDerivedServiceRolloutReason(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/reason", PrefixDerived, name)
}

// KeyDerivedServiceRolloutRollbackReason returns the store path for why the last
// rollback happened; kept until the next rollout starts (M241 D7).
// Path: derived/service/{name}/rollout/rollback_reason
func KeyDerivedServiceRolloutRollbackReason(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/rollback_reason", PrefixDerived, name)
}

// KeyDerivedServiceRolloutProgressAt returns the store path for the RFC 3339 time
// of the rollout's last progress, the start of its progress deadline (M241 D7).
// Path: derived/service/{name}/rollout/progress_at
func KeyDerivedServiceRolloutProgressAt(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/progress_at", PrefixDerived, name)
}

// KeyDerivedServiceRolloutStartedAt returns the store path for the RFC 3339 time
// the rollout (or its rollback) started: only new-image instances created from
// then on count as its failures (M241 D13).
// Path: derived/service/{name}/rollout/started_at
func KeyDerivedServiceRolloutStartedAt(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/started_at", PrefixDerived, name)
}

// KeyDerivedServiceRolloutRunningNew returns the store path for the number of
// running target-image instances last seen, used to detect progress (M241 D7).
// Path: derived/service/{name}/rollout/running_new
func KeyDerivedServiceRolloutRunningNew(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/running_new", PrefixDerived, name)
}

// KeyDerivedServiceRolloutRolledBackImage returns the store path for the image
// the last rollback moved away from; apply warns when it is applied again (M241 D9).
// Path: derived/service/{name}/rollout/rolled_back_image
func KeyDerivedServiceRolloutRolledBackImage(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/rolled_back_image", PrefixDerived, name)
}

// KeyDerivedServiceRolloutRolledBackAt returns the store path for the RFC 3339
// time of the last rollback (M241 D9).
// Path: derived/service/{name}/rollout/rolled_back_at
func KeyDerivedServiceRolloutRolledBackAt(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/rolled_back_at", PrefixDerived, name)
}

// KeyDesiredServiceRolloutPaused returns the store path for pausing a rolling update.
// Path: desired/service/{name}/rollout/paused
func KeyDesiredServiceRolloutPaused(name string) string {
	return fmt.Sprintf("%s/service/%s/rollout/paused", PrefixDesired, name)
}
