// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import (
	"strings"
	"time"
)

// FormatCompactDuration formats a duration rounded to the second without zero
// trailing units: 10m, 1h30m, 45s, 2h (instead of 10m0s, 1h30m0s, 2h0m0s).
// Used in rollout and apply messages (M241).
func FormatCompactDuration(duration time.Duration) string {
	durationText := duration.Round(time.Second).String()
	if strings.HasSuffix(durationText, "m0s") {
		durationText = strings.TrimSuffix(durationText, "0s")
	}
	if strings.HasSuffix(durationText, "h0m") {
		durationText = strings.TrimSuffix(durationText, "0m")
	}
	return durationText
}
