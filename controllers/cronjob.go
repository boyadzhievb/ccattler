// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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

// cronJobSpec holds the desired configuration for a single cronjob, extracted
// from store facts.
type cronJobSpec struct {
	name         string // unique cronjob identifier
	schedule     string // 5-field cron expression
	image        string // container image reference
	command      string // container command to execute
	completions  int    // target completions per run (default 1)
	parallelism  int    // max concurrent instances per run (default 1)
	backoffLimit int    // max failures per run (default 3)
}

// CronController creates job instances on a cron schedule. It checks whether
// the current minute matches each cronjob's schedule and, if so, creates a
// one-shot job for the JobController to execute.
type CronController struct {
	// Now returns the current time. Defaults to time.Now but can be
	// overridden in tests for deterministic scheduling.
	Now func() time.Time
}

// NewCronController returns a CronController wired to the real clock.
func NewCronController() *CronController {
	return &CronController{
		Now: time.Now,
	}
}

// Name returns "cron", identifying this controller in logs.
func (cronController *CronController) Name() string { return "cron" }

// Watch returns the fact prefixes this controller monitors.
func (cronController *CronController) Watch() []string {
	return []string{
		types.ScanDesiredCronJobs,
		types.ScanDerivedCronJobs,
		types.ScanDesiredJobs,
		types.ScanDerivedJobs,
	}
}

// Reconcile checks each desired cronjob against the current time and creates
// job facts when the schedule matches.
func (cronController *CronController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredCronJobs := extractDesiredCronJobs(facts)
	if len(desiredCronJobs) == 0 {
		return nil, nil
	}

	derivedCronJobFacts := extractDerivedCronJobFacts(facts)
	derivedJobFacts := extractDerivedJobFacts(facts)
	currentTime := cronController.Now()

	var changes []Change
	cronJobNames := sortedMapKeys(desiredCronJobs)
	for _, cronJobName := range cronJobNames {
		cronJobSpecification := desiredCronJobs[cronJobName]
		cronJobChanges := cronController.reconcileSingleCronJob(cronJobSpecification, derivedCronJobFacts, derivedJobFacts, facts, currentTime)
		changes = append(changes, cronJobChanges...)
	}
	return changes, nil
}

// reconcileSingleCronJob checks one cronjob's schedule, creates a job if it
// matches, and mirrors the latest job status.
func (cronController *CronController) reconcileSingleCronJob(
	spec cronJobSpec,
	derivedCronJobs map[string]map[string]string,
	derivedJobs map[string]map[string]string,
	facts []store.Fact,
	currentTime time.Time,
) []Change {
	var changes []Change

	cronJobDerived := derivedCronJobs[spec.name]
	latestJobName := findLatestJobForCronJob(spec.name, facts)

	if latestJobName != "" {
		latestJobDerived := derivedJobs[latestJobName]
		if latestJobDerived != nil {
			latestStatus := latestJobDerived["status"]
			currentMirroredStatus := ""
			if cronJobDerived != nil {
				currentMirroredStatus = cronJobDerived["last_status"]
			}
			if latestStatus != "" && latestStatus != currentMirroredStatus {
				changes = append(changes, Change{
					Type:  store.OpPut,
					Key:   types.KeyDerivedCronJobLastStatus(spec.name),
					Value: []byte(latestStatus),
				})
			}
		}
	}

	if !matchesCronSchedule(spec.schedule, currentTime) {
		return changes
	}

	if cronJobDerived != nil {
		lastScheduledTimestamp := cronJobDerived["last_scheduled"]
		if lastScheduledTimestamp != "" {
			lastScheduledTime, parseErr := time.Parse(time.RFC3339, lastScheduledTimestamp)
			if parseErr == nil && withinSameMinute(lastScheduledTime, currentTime) {
				return changes
			}
		}
	}

	if latestJobName != "" && isJobActive(latestJobName, derivedJobs) {
		return changes
	}

	jobName := fmt.Sprintf("%s-%s", spec.name, currentTime.UTC().Format("20060102-1504"))
	jobChanges := buildCronJobInstanceChanges(spec, jobName)
	changes = append(changes, jobChanges...)

	changes = append(changes, Change{
		Type:  store.OpPut,
		Key:   types.KeyDerivedCronJobLastScheduled(spec.name),
		Value: []byte(currentTime.UTC().Format(time.RFC3339)),
	})

	return changes
}

// buildCronJobInstanceChanges creates the store changes to define a new job
// from a cronjob specification.
func buildCronJobInstanceChanges(spec cronJobSpec, jobName string) []Change {
	changes := []Change{
		{Type: store.OpPut, Key: types.KeyDesiredJob(jobName), Value: []byte("")},
		{Type: store.OpPut, Key: types.KeyDesiredJobImage(jobName), Value: []byte(spec.image)},
		{Type: store.OpPut, Key: types.KeyDesiredJobCompletions(jobName), Value: []byte(strconv.Itoa(spec.completions))},
		{Type: store.OpPut, Key: types.KeyDesiredJobParallelism(jobName), Value: []byte(strconv.Itoa(spec.parallelism))},
		{Type: store.OpPut, Key: types.KeyDesiredJobBackoffLimit(jobName), Value: []byte(strconv.Itoa(spec.backoffLimit))},
	}
	if spec.command != "" {
		changes = append(changes, Change{
			Type: store.OpPut, Key: types.KeyDesiredJobCommand(jobName), Value: []byte(spec.command),
		})
	}
	return changes
}

// findLatestJobForCronJob scans desired jobs for one whose name starts with
// the cronjob name prefix. Returns the lexicographically last match (newest
// timestamp suffix).
func findLatestJobForCronJob(cronJobName string, facts []store.Fact) string {
	prefix := cronJobName + "-"
	var latestJobName string
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredJobs) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredJobs)
		jobName := strings.SplitN(relativePath, "/", 2)[0]
		if strings.HasPrefix(jobName, prefix) && jobName > latestJobName {
			latestJobName = jobName
		}
	}
	return latestJobName
}

// isJobActive returns true if the job's derived status is neither complete
// nor failed — meaning it's still running or hasn't started yet.
func isJobActive(jobName string, derivedJobs map[string]map[string]string) bool {
	jobDerived := derivedJobs[jobName]
	if jobDerived == nil {
		return true
	}
	status := jobDerived["status"]
	return status != jobStatusComplete && status != jobStatusFailed
}

// withinSameMinute returns true if both times fall within the same UTC minute.
func withinSameMinute(timeA time.Time, timeB time.Time) bool {
	utcA := timeA.UTC()
	utcB := timeB.UTC()
	return utcA.Year() == utcB.Year() &&
		utcA.Month() == utcB.Month() &&
		utcA.Day() == utcB.Day() &&
		utcA.Hour() == utcB.Hour() &&
		utcA.Minute() == utcB.Minute()
}

// extractDesiredCronJobs scans facts for desired/cronjob/{name}/... keys
// and groups them into cronJobSpec values.
func extractDesiredCronJobs(facts []store.Fact) map[string]cronJobSpec {
	cronJobs := make(map[string]cronJobSpec)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDesiredCronJobs) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredCronJobs)
		pathParts := strings.SplitN(relativePath, "/", 2)
		cronJobName := pathParts[0]

		existing := cronJobs[cronJobName]
		existing.name = cronJobName

		if len(pathParts) == 2 {
			switch pathParts[1] {
			case "schedule":
				existing.schedule = string(fact.Value)
			case "image":
				existing.image = string(fact.Value)
			case "command":
				existing.command = string(fact.Value)
			case "completions":
				existing.completions, _ = strconv.Atoi(string(fact.Value))
			case "parallelism":
				existing.parallelism, _ = strconv.Atoi(string(fact.Value))
			case "backoff_limit":
				existing.backoffLimit, _ = strconv.Atoi(string(fact.Value))
			}
		}
		cronJobs[cronJobName] = existing
	}

	for cronJobName, spec := range cronJobs {
		if spec.completions == 0 {
			spec.completions = defaultJobCompletions
		}
		if spec.parallelism == 0 {
			spec.parallelism = defaultJobParallelism
		}
		if spec.backoffLimit == 0 {
			spec.backoffLimit = defaultJobBackoffLimitController
		}
		cronJobs[cronJobName] = spec
	}
	return cronJobs
}

// extractDerivedCronJobFacts reads derived/cronjob/{name}/{field} facts.
func extractDerivedCronJobFacts(facts []store.Fact) map[string]map[string]string {
	derivedCronJobs := make(map[string]map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedCronJobs) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDerivedCronJobs)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		cronJobName := pathParts[0]
		fieldName := pathParts[1]
		if derivedCronJobs[cronJobName] == nil {
			derivedCronJobs[cronJobName] = make(map[string]string)
		}
		derivedCronJobs[cronJobName][fieldName] = string(fact.Value)
	}
	return derivedCronJobs
}

// matchesCronSchedule checks whether the given 5-field cron expression matches
// the provided time. Fields are: minute hour day-of-month month day-of-week.
// Supports * (any), fixed values, and comma-separated lists.
func matchesCronSchedule(expression string, currentTime time.Time) bool {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return false
	}

	utcTime := currentTime.UTC()
	timeValues := []int{
		utcTime.Minute(),
		utcTime.Hour(),
		utcTime.Day(),
		int(utcTime.Month()),
		int(utcTime.Weekday()),
	}
	fieldRanges := []struct{ min, max int }{
		{0, 59}, // minute
		{0, 23}, // hour
		{1, 31}, // day of month
		{1, 12}, // month
		{0, 6},  // day of week (0=Sunday)
	}

	for fieldIndex := 0; fieldIndex < 5; fieldIndex++ {
		allowedValues, parseError := parseCronField(fields[fieldIndex], fieldRanges[fieldIndex].min, fieldRanges[fieldIndex].max)
		if parseError != nil {
			return false
		}
		if !containsInt(allowedValues, timeValues[fieldIndex]) {
			return false
		}
	}
	return true
}

// parseCronField parses a single cron field and returns the set of allowed
// integer values. Supports "*" (all values in range), single integers, and
// comma-separated lists.
func parseCronField(field string, minValue int, maxValue int) ([]int, error) {
	if field == "*" {
		allValues := make([]int, 0, maxValue-minValue+1)
		for value := minValue; value <= maxValue; value++ {
			allValues = append(allValues, value)
		}
		return allValues, nil
	}

	parts := strings.Split(field, ",")
	var allowedValues []int
	for _, part := range parts {
		parsedValue, parseError := strconv.Atoi(strings.TrimSpace(part))
		if parseError != nil {
			return nil, fmt.Errorf("invalid cron field value %q: %w", part, parseError)
		}
		if parsedValue < minValue || parsedValue > maxValue {
			return nil, fmt.Errorf("cron field value %d out of range [%d, %d]", parsedValue, minValue, maxValue)
		}
		allowedValues = append(allowedValues, parsedValue)
	}
	return allowedValues, nil
}

// containsInt returns true if the slice contains the target value.
func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
