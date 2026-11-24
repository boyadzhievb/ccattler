// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// ScanDesiredJobs is the prefix for scanning all desired job definitions.
const ScanDesiredJobs = PrefixDesired + "/job/"

// ScanDerivedJobs is the prefix for scanning all derived job-level facts.
const ScanDerivedJobs = PrefixDerived + "/job/"

// KeyDesiredJob returns the store path for a job's root marker key.
// Path: desired/job/{name}
func KeyDesiredJob(jobName string) string {
	return fmt.Sprintf("%s/job/%s", PrefixDesired, jobName)
}

// KeyDesiredJobImage returns the store path for a job's container image.
// Path: desired/job/{name}/image
func KeyDesiredJobImage(jobName string) string {
	return fmt.Sprintf("%s/job/%s/image", PrefixDesired, jobName)
}

// KeyDesiredJobCommand returns the store path for a job's container command.
// Path: desired/job/{name}/command
func KeyDesiredJobCommand(jobName string) string {
	return fmt.Sprintf("%s/job/%s/command", PrefixDesired, jobName)
}

// KeyDesiredJobImagePullSecret returns the store path for a job's registry credential secret name.
// Path: desired/job/{name}/image_pull_secret
func KeyDesiredJobImagePullSecret(jobName string) string {
	return fmt.Sprintf("%s/job/%s/image_pull_secret", PrefixDesired, jobName)
}

// KeyDesiredJobCompletions returns the store path for a job's target
// number of successful completions.
// Path: desired/job/{name}/completions
func KeyDesiredJobCompletions(jobName string) string {
	return fmt.Sprintf("%s/job/%s/completions", PrefixDesired, jobName)
}

// KeyDesiredJobParallelism returns the store path for a job's maximum
// number of concurrently running instances.
// Path: desired/job/{name}/parallelism
func KeyDesiredJobParallelism(jobName string) string {
	return fmt.Sprintf("%s/job/%s/parallelism", PrefixDesired, jobName)
}

// KeyDesiredJobBackoffLimit returns the store path for a job's maximum
// number of failures before the job is marked failed.
// Path: desired/job/{name}/backoff_limit
func KeyDesiredJobBackoffLimit(jobName string) string {
	return fmt.Sprintf("%s/job/%s/backoff_limit", PrefixDesired, jobName)
}

// KeyDerivedJobStatus returns the store path for a job's computed status.
// Values: "running", "complete", "failed".
// Path: derived/job/{name}/status
func KeyDerivedJobStatus(jobName string) string {
	return fmt.Sprintf("%s/job/%s/status", PrefixDerived, jobName)
}

// KeyDerivedJobSucceeded returns the store path for a job's count of
// successfully completed instances.
// Path: derived/job/{name}/succeeded
func KeyDerivedJobSucceeded(jobName string) string {
	return fmt.Sprintf("%s/job/%s/succeeded", PrefixDerived, jobName)
}

// KeyDerivedJobFailed returns the store path for a job's count of
// failed instances.
// Path: derived/job/{name}/failed
func KeyDerivedJobFailed(jobName string) string {
	return fmt.Sprintf("%s/job/%s/failed", PrefixDerived, jobName)
}

// KeyDerivedJobInstanceOwner returns the store path marking an instance
// as owned by a job. This prevents the FailureController from replacing it.
// Path: derived/instance/{instanceID}/job_owner
func KeyDerivedJobInstanceOwner(instanceID string) string {
	return fmt.Sprintf("%s/instance/%s/job_owner", PrefixDerived, instanceID)
}

// ScanDesiredCronJobs is the prefix for scanning all desired cronjob facts.
const ScanDesiredCronJobs = "desired/cronjob/"

// ScanDerivedCronJobs is the prefix for scanning all derived cronjob facts.
const ScanDerivedCronJobs = "derived/cronjob/"

// KeyDesiredCronJob returns the store path for a cronjob's existence marker.
// Path: desired/cronjob/{name}
func KeyDesiredCronJob(name string) string {
	return fmt.Sprintf("%s/cronjob/%s", PrefixDesired, name)
}

// KeyDesiredCronJobSchedule returns the store path for a cronjob's cron expression.
// Path: desired/cronjob/{name}/schedule
func KeyDesiredCronJobSchedule(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/schedule", PrefixDesired, name)
}

// KeyDesiredCronJobImage returns the store path for a cronjob's container image.
// Path: desired/cronjob/{name}/image
func KeyDesiredCronJobImage(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/image", PrefixDesired, name)
}

// KeyDesiredCronJobCommand returns the store path for a cronjob's container command.
// Path: desired/cronjob/{name}/command
func KeyDesiredCronJobCommand(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/command", PrefixDesired, name)
}

// KeyDesiredCronJobCompletions returns the store path for a cronjob's completions-per-run target.
// Path: desired/cronjob/{name}/completions
func KeyDesiredCronJobCompletions(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/completions", PrefixDesired, name)
}

// KeyDesiredCronJobParallelism returns the store path for a cronjob's parallelism-per-run limit.
// Path: desired/cronjob/{name}/parallelism
func KeyDesiredCronJobParallelism(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/parallelism", PrefixDesired, name)
}

// KeyDesiredCronJobBackoffLimit returns the store path for a cronjob's failure tolerance per run.
// Path: desired/cronjob/{name}/backoff_limit
func KeyDesiredCronJobBackoffLimit(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/backoff_limit", PrefixDesired, name)
}

// KeyDerivedCronJobLastScheduled returns the store path for the last time a cronjob was triggered.
// Path: derived/cronjob/{name}/last_scheduled
func KeyDerivedCronJobLastScheduled(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/last_scheduled", PrefixDerived, name)
}

// KeyDerivedCronJobLastStatus returns the store path for the latest job status of a cronjob.
// Path: derived/cronjob/{name}/last_status
func KeyDerivedCronJobLastStatus(name string) string {
	return fmt.Sprintf("%s/cronjob/%s/last_status", PrefixDerived, name)
}
