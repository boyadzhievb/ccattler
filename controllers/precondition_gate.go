// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
)

// A precondition gate runs a check until it succeeds, with bounded exponential
// back-off, and only then starts the controllers. The server uses it for the
// resource unit migration (M243 D9, C2), at start and on becoming leader.

// ControllerPrecondition is a named check that must succeed before
// controllers start; it is retried with back-off until it does. The name
// appears in its failure log (R5-N7).
type ControllerPrecondition struct {
	Name  string                          // Name identifies the check in logs, e.g. "resource unit migration"
	Check func(ctx context.Context) error // Check returns nil once controllers may start
}

const (
	// preconditionInitialRetryDelay is the wait after the first failed attempt.
	preconditionInitialRetryDelay = time.Second

	// preconditionMaximumRetryDelay caps the doubling retry delay.
	preconditionMaximumRetryDelay = time.Minute

	// preconditionRetryGrowthFactor multiplies the delay after each failure.
	preconditionRetryGrowthFactor = 2
)

// preconditionRetryBackoff is the bounded exponential back-off between
// precondition attempts.
type preconditionRetryBackoff struct {
	initialDelay time.Duration // initialDelay is the wait after the first failure
	maximumDelay time.Duration // maximumDelay caps the wait
}

// defaultPreconditionRetryBackoff returns the production back-off: 1 s,
// doubling up to 1 min.
func defaultPreconditionRetryBackoff() preconditionRetryBackoff {
	return preconditionRetryBackoff{initialDelay: preconditionInitialRetryDelay, maximumDelay: preconditionMaximumRetryDelay}
}

// delayAfterAttempt returns the wait after failed attempt number attempt
// (1-based): the initial delay doubled per earlier failure, at most the maximum.
func (backoff preconditionRetryBackoff) delayAfterAttempt(attempt int) time.Duration {
	retryDelay := backoff.initialDelay
	for failedAttempt := 1; failedAttempt < attempt && retryDelay < backoff.maximumDelay; failedAttempt++ {
		retryDelay *= preconditionRetryGrowthFactor
	}
	return min(retryDelay, backoff.maximumDelay)
}

// runAfterPreconditionSucceeds runs precondition.Check until it returns nil,
// then calls startControllers and returns true. Each failure is logged with
// the precondition's name, attempt and next delay, unless ctx was cancelled
// (review round 2 N-b). Returns false, never starting, if ctx ends.
func runAfterPreconditionSucceeds(
	ctx context.Context,
	precondition ControllerPrecondition,
	backoff preconditionRetryBackoff,
	startControllers func(),
) bool {
	for attempt := 1; ; attempt++ {
		preconditionError := precondition.Check(ctx)
		if preconditionError == nil {
			startControllers()
			return true
		}
		if ctx.Err() != nil {
			return false // cancelled: not a precondition failure
		}
		retryDelay := backoff.delayAfterAttempt(attempt)
		logging.Default().Error(precondition.Name+" failed; controllers wait for it",
			"attempt", strconv.Itoa(attempt), "next_retry_in", retryDelay.String(), "error", preconditionError.Error())
		retryTimer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			retryTimer.Stop()
			return false
		case <-retryTimer.C:
		}
	}
}
