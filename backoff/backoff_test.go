// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package backoff

import (
	"math"
	"testing"
	"time"
)

// TestDelay checks the doubling, the cap and the out-of-range attempts.
func TestDelay(t *testing.T) {
	testCases := []struct {
		name         string
		baseDelay    time.Duration
		maximumDelay time.Duration
		attempt      int
		wantDelay    time.Duration
	}{
		{"first attempt is the base delay", 10 * time.Second, 5 * time.Minute, 1, 10 * time.Second},
		{"second attempt doubles", 10 * time.Second, 5 * time.Minute, 2, 20 * time.Second},
		{"third attempt doubles again", 10 * time.Second, 5 * time.Minute, 3, 40 * time.Second},
		{"sixth attempt reaches the cap", 10 * time.Second, 5 * time.Minute, 6, 5 * time.Minute},
		{"far attempts stay at the cap", 10 * time.Second, 5 * time.Minute, 1000, 5 * time.Minute},
		{"attempt zero is the base delay", 10 * time.Second, 5 * time.Minute, 0, 10 * time.Second},
		{"negative attempt is the base delay", 10 * time.Second, 5 * time.Minute, -3, 10 * time.Second},
		{"base above the cap is capped", 10 * time.Minute, 5 * time.Minute, 1, 5 * time.Minute},
		{"sub-second delays work", 10 * time.Millisecond, time.Second, 3, 40 * time.Millisecond},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			gotDelay := Delay(testCase.baseDelay, testCase.maximumDelay, testCase.attempt)
			if gotDelay != testCase.wantDelay {
				t.Fatalf("Delay(%v, %v, %d) = %v, want %v", testCase.baseDelay, testCase.maximumDelay, testCase.attempt, gotDelay, testCase.wantDelay)
			}
		})
	}
}

// TestDelayMatchesFormerFormulas pins attempts 1-7 for the 10 s base and 5 min
// cap shared by the image pull back-off and the failure controller's
// crash-loop back-off to the float formula both used before M233.
func TestDelayMatchesFormerFormulas(t *testing.T) {
	const formerBaseSeconds = 10.0
	const formerMaximumSeconds = 300.0
	for attempt := 1; attempt <= 7; attempt++ {
		formerSeconds := formerBaseSeconds * math.Pow(2, float64(attempt-1))
		if formerSeconds > formerMaximumSeconds {
			formerSeconds = formerMaximumSeconds
		}
		wantDelay := time.Duration(formerSeconds) * time.Second
		gotDelay := Delay(10*time.Second, 5*time.Minute, attempt)
		if gotDelay != wantDelay {
			t.Errorf("attempt %d: Delay = %v, former formula = %v", attempt, gotDelay, wantDelay)
		}
	}
}
