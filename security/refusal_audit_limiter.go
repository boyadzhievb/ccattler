// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"sync"
	"time"
)

// refusalAuditInterval is the minimum time between two refusal audit records
// for the same rate-limit key, so a waiting agent that retries does not flood
// the audit log.
const refusalAuditInterval = time.Minute

// refusalAuditMaxEntries caps the rate-limit map; new keys beyond it are not
// audited until expired entries are pruned, so a node flooding random request
// targets cannot grow memory without bound.
const refusalAuditMaxEntries = 10000

// refusalAuditLimiter decides whether a refused node delivery (secrets or gate
// keys) is audited, at most once per refusalAuditInterval per key.
type refusalAuditLimiter struct {
	mutex          sync.Mutex           // mutex guards lastAuditTimes and lastPruneTime.
	lastAuditTimes map[string]time.Time // lastAuditTimes maps rate-limit key to the last audit time.
	lastPruneTime  time.Time            // lastPruneTime is when expired entries were last dropped.
}

// newRefusalAuditLimiter creates an empty refusal audit limiter.
func newRefusalAuditLimiter() *refusalAuditLimiter {
	return &refusalAuditLimiter{lastAuditTimes: make(map[string]time.Time)}
}

// shouldAudit reports whether a refusal with this key is audited now, and
// records the audit time when it is. Prunes expired entries once per interval.
func (auditLimiter *refusalAuditLimiter) shouldAudit(rateLimitKey string, now time.Time) bool {
	auditLimiter.mutex.Lock()
	defer auditLimiter.mutex.Unlock()
	if now.Sub(auditLimiter.lastPruneTime) >= refusalAuditInterval {
		for existingKey, lastAuditTime := range auditLimiter.lastAuditTimes {
			if now.Sub(lastAuditTime) >= refusalAuditInterval {
				delete(auditLimiter.lastAuditTimes, existingKey)
			}
		}
		auditLimiter.lastPruneTime = now
	}
	lastAuditTime, audited := auditLimiter.lastAuditTimes[rateLimitKey]
	if (audited && now.Sub(lastAuditTime) < refusalAuditInterval) ||
		(!audited && len(auditLimiter.lastAuditTimes) >= refusalAuditMaxEntries) {
		return false
	}
	auditLimiter.lastAuditTimes[rateLimitKey] = now
	return true
}
