// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// TestCronScheduleMatchingEveryMinute verifies that "* * * * *" matches any
// given time.
func TestCronScheduleMatchingEveryMinute(testContext *testing.T) {
	referenceTime := time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)
	if !matchesCronSchedule("* * * * *", referenceTime) {
		testContext.Error("expected every-minute schedule to match")
	}
}

// TestCronScheduleMatchingSpecificMinute verifies exact minute matching.
func TestCronScheduleMatchingSpecificMinute(testContext *testing.T) {
	matchingTime := time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)
	nonMatchingTime := time.Date(2026, 10, 24, 14, 31, 0, 0, time.UTC)

	if !matchesCronSchedule("30 * * * *", matchingTime) {
		testContext.Error("expected minute 30 to match")
	}
	if matchesCronSchedule("30 * * * *", nonMatchingTime) {
		testContext.Error("expected minute 31 not to match")
	}
}

// TestCronScheduleMatchingCommaList verifies comma-separated value lists.
func TestCronScheduleMatchingCommaList(testContext *testing.T) {
	referenceTime := time.Date(2026, 10, 24, 14, 15, 0, 0, time.UTC)
	if !matchesCronSchedule("0,15,30,45 * * * *", referenceTime) {
		testContext.Error("expected minute 15 to match comma list")
	}

	nonMatchingTime := time.Date(2026, 10, 24, 14, 20, 0, 0, time.UTC)
	if matchesCronSchedule("0,15,30,45 * * * *", nonMatchingTime) {
		testContext.Error("expected minute 20 not to match comma list")
	}
}

// TestCronScheduleMatchingFullExpression verifies all five fields together.
func TestCronScheduleMatchingFullExpression(testContext *testing.T) {
	// Saturday 24 October 2026 at 14:30 UTC — weekday 6 (Saturday)
	referenceTime := time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)

	if !matchesCronSchedule("30 14 24 10 6", referenceTime) {
		testContext.Error("expected full match for 30 14 24 10 6")
	}
	if matchesCronSchedule("30 14 24 10 1", referenceTime) {
		testContext.Error("expected weekday mismatch to fail")
	}
}

// TestCronScheduleInvalidExpression verifies that malformed expressions never
// match.
func TestCronScheduleInvalidExpression(testContext *testing.T) {
	referenceTime := time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)

	if matchesCronSchedule("* *", referenceTime) {
		testContext.Error("expected too-few fields to fail")
	}
	if matchesCronSchedule("abc * * * *", referenceTime) {
		testContext.Error("expected non-numeric field to fail")
	}
	if matchesCronSchedule("99 * * * *", referenceTime) {
		testContext.Error("expected out-of-range field to fail")
	}
}

// TestCronFieldParsingStar verifies wildcard expands to full range.
func TestCronFieldParsingStar(testContext *testing.T) {
	values, parseError := parseCronField("*", 0, 59)
	if parseError != nil {
		testContext.Fatal(parseError)
	}
	if len(values) != 60 {
		testContext.Fatalf("expected 60 values, got %d", len(values))
	}
}

// TestCronFieldParsingSingle verifies single integer parsing.
func TestCronFieldParsingSingle(testContext *testing.T) {
	values, parseError := parseCronField("15", 0, 59)
	if parseError != nil {
		testContext.Fatal(parseError)
	}
	if len(values) != 1 || values[0] != 15 {
		testContext.Fatalf("expected [15], got %v", values)
	}
}

// TestCronFieldParsingCommaList verifies comma-separated parsing.
func TestCronFieldParsingCommaList(testContext *testing.T) {
	values, parseError := parseCronField("0,15,30,45", 0, 59)
	if parseError != nil {
		testContext.Fatal(parseError)
	}
	if len(values) != 4 {
		testContext.Fatalf("expected 4 values, got %d", len(values))
	}
}

// TestCronFieldParsingOutOfRange verifies range validation.
func TestCronFieldParsingOutOfRange(testContext *testing.T) {
	_, parseError := parseCronField("60", 0, 59)
	if parseError == nil {
		testContext.Error("expected error for out-of-range value 60")
	}
}

// TestCronControllerCreatesJobOnSchedule verifies that a cronjob whose schedule
// matches the current time produces job creation changes.
func TestCronControllerCreatesJobOnSchedule(testContext *testing.T) {
	fixedTime := time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)
	cronController := &CronController{Now: func() time.Time { return fixedTime }}

	facts := buildFacts(
		kv(types.KeyDesiredCronJob("nightly-backup"), ""),
		kv(types.KeyDesiredCronJobSchedule("nightly-backup"), "30 14 * * *"),
		kv(types.KeyDesiredCronJobImage("nightly-backup"), "backup:latest"),
		kv(types.KeyDesiredCronJobCommand("nightly-backup"), "pg_dump"),
		kv(types.KeyDesiredCronJobCompletions("nightly-backup"), "1"),
		kv(types.KeyDesiredCronJobParallelism("nightly-backup"), "1"),
		kv(types.KeyDesiredCronJobBackoffLimit("nightly-backup"), "3"),
	)

	changes, reconcileError := cronController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	expectedJobName := "nightly-backup-20261024-1430"
	foundJobMarker := false
	foundJobImage := false
	foundLastScheduled := false
	for _, change := range changes {
		if change.Key == types.KeyDesiredJob(expectedJobName) {
			foundJobMarker = true
		}
		if change.Key == types.KeyDesiredJobImage(expectedJobName) && string(change.Value) == "backup:latest" {
			foundJobImage = true
		}
		if change.Key == types.KeyDerivedCronJobLastScheduled("nightly-backup") {
			foundLastScheduled = true
		}
	}
	if !foundJobMarker {
		testContext.Error("expected job marker for " + expectedJobName)
	}
	if !foundJobImage {
		testContext.Error("expected job image for " + expectedJobName)
	}
	if !foundLastScheduled {
		testContext.Error("expected last_scheduled update")
	}
}

// TestCronControllerSkipsDuplicateWithinMinute verifies that a cronjob with a
// last_scheduled timestamp in the same minute does not create a duplicate job.
func TestCronControllerSkipsDuplicateWithinMinute(testContext *testing.T) {
	fixedTime := time.Date(2026, 10, 24, 14, 30, 45, 0, time.UTC)
	cronController := &CronController{Now: func() time.Time { return fixedTime }}

	facts := buildFacts(
		kv(types.KeyDesiredCronJob("nightly-backup"), ""),
		kv(types.KeyDesiredCronJobSchedule("nightly-backup"), "30 14 * * *"),
		kv(types.KeyDesiredCronJobImage("nightly-backup"), "backup:latest"),
		kv(types.KeyDerivedCronJobLastScheduled("nightly-backup"), "2026-10-24T14:30:10Z"),
	)

	changes, reconcileError := cronController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	for _, change := range changes {
		if change.Key == types.KeyDesiredJob("nightly-backup-20261024-1430") {
			testContext.Error("expected no job creation within same minute")
		}
	}
}

// TestCronControllerSkipsWhenJobActive verifies that a cronjob does not create
// a new job while the previous one is still running.
func TestCronControllerSkipsWhenJobActive(testContext *testing.T) {
	fixedTime := time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)
	cronController := &CronController{Now: func() time.Time { return fixedTime }}

	facts := buildFacts(
		kv(types.KeyDesiredCronJob("nightly-backup"), ""),
		kv(types.KeyDesiredCronJobSchedule("nightly-backup"), "30 14 * * *"),
		kv(types.KeyDesiredCronJobImage("nightly-backup"), "backup:latest"),
		// Previous job exists and is still running
		kv(types.KeyDesiredJob("nightly-backup-20261024-1400"), ""),
		kv(types.KeyDerivedJobStatus("nightly-backup-20261024-1400"), "running"),
	)

	changes, reconcileError := cronController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	for _, change := range changes {
		if change.Key == types.KeyDesiredJob("nightly-backup-20261024-1430") {
			testContext.Error("expected no new job while previous is active")
		}
	}
}

// TestCronControllerCreatesJobAfterPreviousCompletes verifies that a completed
// previous job does not block new job creation.
func TestCronControllerCreatesJobAfterPreviousCompletes(testContext *testing.T) {
	fixedTime := time.Date(2026, 10, 24, 14, 30, 0, 0, time.UTC)
	cronController := &CronController{Now: func() time.Time { return fixedTime }}

	facts := buildFacts(
		kv(types.KeyDesiredCronJob("nightly-backup"), ""),
		kv(types.KeyDesiredCronJobSchedule("nightly-backup"), "30 14 * * *"),
		kv(types.KeyDesiredCronJobImage("nightly-backup"), "backup:latest"),
		// Previous job completed
		kv(types.KeyDesiredJob("nightly-backup-20261024-1400"), ""),
		kv(types.KeyDerivedJobStatus("nightly-backup-20261024-1400"), "complete"),
	)

	changes, reconcileError := cronController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	foundNewJob := false
	for _, change := range changes {
		if change.Key == types.KeyDesiredJob("nightly-backup-20261024-1430") {
			foundNewJob = true
		}
	}
	if !foundNewJob {
		testContext.Error("expected new job creation after previous completed")
	}
}

// TestCronControllerMirrorsJobStatus verifies that derived job status is copied
// to the cronjob's last_status.
func TestCronControllerMirrorsJobStatus(testContext *testing.T) {
	fixedTime := time.Date(2026, 10, 24, 14, 31, 0, 0, time.UTC)
	cronController := &CronController{Now: func() time.Time { return fixedTime }}

	facts := buildFacts(
		kv(types.KeyDesiredCronJob("nightly-backup"), ""),
		kv(types.KeyDesiredCronJobSchedule("nightly-backup"), "30 14 * * *"),
		kv(types.KeyDesiredCronJobImage("nightly-backup"), "backup:latest"),
		kv(types.KeyDesiredJob("nightly-backup-20261024-1430"), ""),
		kv(types.KeyDerivedJobStatus("nightly-backup-20261024-1430"), "complete"),
	)

	changes, reconcileError := cronController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	foundStatusMirror := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedCronJobLastStatus("nightly-backup") && string(change.Value) == "complete" {
			foundStatusMirror = true
		}
	}
	if !foundStatusMirror {
		testContext.Error("expected last_status to mirror job's complete status")
	}
}

// TestCronControllerNoMatchSkipsJobCreation verifies that a cronjob whose
// schedule doesn't match the current time produces no job changes.
func TestCronControllerNoMatchSkipsJobCreation(testContext *testing.T) {
	fixedTime := time.Date(2026, 10, 24, 14, 31, 0, 0, time.UTC)
	cronController := &CronController{Now: func() time.Time { return fixedTime }}

	facts := buildFacts(
		kv(types.KeyDesiredCronJob("nightly-backup"), ""),
		kv(types.KeyDesiredCronJobSchedule("nightly-backup"), "30 14 * * *"),
		kv(types.KeyDesiredCronJobImage("nightly-backup"), "backup:latest"),
	)

	changes, reconcileError := cronController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	for _, change := range changes {
		if change.Key == types.KeyDesiredJob("nightly-backup-20261024-1431") {
			testContext.Error("expected no job creation when schedule doesn't match")
		}
	}
}
