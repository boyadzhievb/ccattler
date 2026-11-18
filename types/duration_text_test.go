// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import (
	"testing"
	"time"
)

// TestFormatCompactDuration checks that zero trailing units are dropped.
func TestFormatCompactDuration(t *testing.T) {
	testCases := map[time.Duration]string{
		10 * time.Minute:                     "10m",
		90 * time.Minute:                     "1h30m",
		2 * time.Hour:                        "2h",
		45 * time.Second:                     "45s",
		5*time.Minute + 400*time.Millisecond: "5m",
		61 * time.Second:                     "1m1s",
	}
	for duration, wantText := range testCases {
		if gotText := FormatCompactDuration(duration); gotText != wantText {
			t.Errorf("FormatCompactDuration(%v) = %q, want %q", duration, gotText, wantText)
		}
	}
}
