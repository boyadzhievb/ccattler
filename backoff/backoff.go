// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Package backoff holds the exponential back-off formula shared by the image
// pull retries, the failure controller's crash-loop delay and the agent's
// in-place restarts.
package backoff

import "time"

// delayGrowthFactor is the factor by which the delay grows on each further attempt.
const delayGrowthFactor = 2

// Delay returns how long to wait before attempt number attempt: baseDelay for
// the first attempt, doubling on each further one, never more than maximumDelay.
// An attempt below 1 is treated as the first.
func Delay(baseDelay time.Duration, maximumDelay time.Duration, attempt int) time.Duration {
	currentDelay := baseDelay
	for step := 1; step < attempt && currentDelay < maximumDelay; step++ {
		currentDelay *= delayGrowthFactor
	}
	if currentDelay > maximumDelay {
		return maximumDelay
	}
	return currentDelay
}
