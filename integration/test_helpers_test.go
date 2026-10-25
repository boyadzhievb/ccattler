// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import "github.com/boyadzhievb/ccattler/scheduler"

// newTestScheduler creates a scheduler with zero grace period for integration tests.
func newTestScheduler() *scheduler.Scheduler {
	placementScheduler := scheduler.NewScheduler()
	placementScheduler.SetPlacementGracePeriod(0)
	return placementScheduler
}
