// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "time"

// Shared default constants used across multiple packages. Constants that are
// only used within a single package belong in that package instead.

const (
	// DefaultSimulatedNodeCPU is the default CPU capacity in millicores for
	// simulated nodes (apply, run, chaos, agent simulations).
	DefaultSimulatedNodeCPU = 4000

	// DefaultSimulatedNodeMemory is the default memory capacity in MiB for
	// simulated nodes.
	DefaultSimulatedNodeMemory = 8192

	// DefaultEventLogMaxEvents is the maximum number of events retained by
	// the in-memory event log used in server/run/demo commands.
	DefaultEventLogMaxEvents = 1000

	// DefaultEtcdDialTimeout is the timeout for establishing a connection to
	// an etcd server. Used by the store layer and CLI commands that create
	// etcd clients.
	DefaultEtcdDialTimeout = 5 * time.Second

	// DefaultReadHeaderTimeout is the timeout for reading HTTP request headers.
	// Applied to all HTTP servers (API, status, userspace proxy) to prevent
	// slowloris attacks.
	DefaultReadHeaderTimeout = 10 * time.Second

	// DefaultStatusPrintInterval is the interval for printing status updates
	// in watch mode for run/run-container/demo commands.
	DefaultStatusPrintInterval = 2 * time.Second

	// DefaultPostStartupSettleTime is the delay after startup before printing
	// initial status, allowing the reconciliation loop to converge.
	DefaultPostStartupSettleTime = 2 * time.Second
)
