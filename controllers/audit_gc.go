// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultAuditRetentionPeriod is the minimum age of an audit entry before it
// becomes eligible for garbage collection. Entries younger than this are kept.
const defaultAuditRetentionPeriod = 7 * 24 * time.Hour

// defaultMaxAuditGCDeletionsPerCycle caps how many audit entries the GC
// controller removes in a single reconciliation cycle to limit transaction size.
const defaultMaxAuditGCDeletionsPerCycle = 50

// AuditLogGCController garbage-collects audit log entries that are older than
// the configured retention period. Without this controller, audit entries
// accumulate in etcd indefinitely.
type AuditLogGCController struct {
	// RetentionPeriod is the minimum age of an audit entry before it becomes
	// eligible for garbage collection.
	RetentionPeriod time.Duration

	// MaxDeletionsPerCycle limits how many audit entries are deleted per
	// reconciliation cycle to stay within the transaction budget.
	MaxDeletionsPerCycle int

	// NowFunc returns the current time. Replaced in tests for determinism.
	NowFunc func() time.Time
}

// NewAuditLogGCController returns a controller with default retention (7 days)
// and deletion limits.
func NewAuditLogGCController() *AuditLogGCController {
	return &AuditLogGCController{
		RetentionPeriod:      defaultAuditRetentionPeriod,
		MaxDeletionsPerCycle: defaultMaxAuditGCDeletionsPerCycle,
		NowFunc:              time.Now,
	}
}

// Name returns "audit-log-gc", identifying this controller in logs and runner
// bookkeeping.
func (auditGCController *AuditLogGCController) Name() string { return "audit-log-gc" }

// Watch returns the fact prefix for audit log entries.
func (auditGCController *AuditLogGCController) Watch() []string {
	return []string{types.PrefixAudit}
}

// Reconcile scans audit entries, parses their timestamps, and emits Delete
// changes for entries older than the retention period. Candidates are sorted
// by key (oldest sequence first) and capped at MaxDeletionsPerCycle.
func (auditGCController *AuditLogGCController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	now := auditGCController.NowFunc()
	auditPrefix := types.PrefixAudit + "/"

	var expiredKeys []string
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, auditPrefix) {
			continue
		}
		var entry security.AuditEntry
		if unmarshalError := json.Unmarshal(fact.Value, &entry); unmarshalError != nil {
			logging.Default().Warn("audit-log-gc: failed to parse audit entry",
				"key", fact.Key, "error", unmarshalError.Error())
			continue
		}
		if entry.Timestamp.IsZero() {
			continue
		}
		if now.Sub(entry.Timestamp) > auditGCController.RetentionPeriod {
			expiredKeys = append(expiredKeys, fact.Key)
		}
	}

	sort.Strings(expiredKeys)

	if len(expiredKeys) > auditGCController.MaxDeletionsPerCycle {
		expiredKeys = expiredKeys[:auditGCController.MaxDeletionsPerCycle]
	}

	var changes []Change
	for _, expiredKey := range expiredKeys {
		changes = append(changes, Change{Type: store.OpDelete, Key: expiredKey})
	}

	if len(expiredKeys) > 0 {
		logging.Default().Info("audit-log-gc collecting expired entries",
			"count", strconv.Itoa(len(expiredKeys)))
	}

	return changes, nil
}
