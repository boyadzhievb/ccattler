// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package store

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
)

const (
	// defaultWriteRatePerSecond is the sustained write rate allowed to etcd.
	defaultWriteRatePerSecond = 500.0
	// defaultWriteBurstSize is the maximum burst of writes before throttling.
	defaultWriteBurstSize = 1000
	// writeRateLimitLogInterval controls how often a throttle warning is logged
	// to avoid flooding the journal when the limiter is actively dropping.
	writeRateLimitLogInterval = 10 * time.Second
)

// ErrWriteRateLimited is returned when a Put is rejected by the store-level
// write rate limiter. Callers should back off and retry.
var ErrWriteRateLimited = fmt.Errorf("store write rate limited")

// writeRateLimiter implements a global token-bucket rate limiter for store
// writes. It protects etcd from write floods caused by controller bugs,
// runaway reconciliation loops, or compromised agents.
type writeRateLimiter struct {
	// tokensAvailable is the current number of tokens in the bucket.
	tokensAvailable float64
	// lastRefillTime is the time tokens were last refilled.
	lastRefillTime time.Time
	// ratePerSecond is the sustained token refill rate.
	ratePerSecond float64
	// burstSize is the maximum number of tokens the bucket can hold.
	burstSize int
	// droppedSinceLastLog counts rejected writes since the last log message.
	droppedSinceLastLog int64
	// lastLogTime tracks when the last throttle warning was emitted.
	lastLogTime time.Time
	// mutex guards all fields for concurrent access.
	mutex sync.Mutex
	// timeNow is a clock function, overridable in tests.
	timeNow func() time.Time
}

// newWriteRateLimiter creates a rate limiter with the default write budget.
func newWriteRateLimiter() *writeRateLimiter {
	return &writeRateLimiter{
		tokensAvailable: float64(defaultWriteBurstSize),
		lastRefillTime:  time.Now(),
		ratePerSecond:   defaultWriteRatePerSecond,
		burstSize:       defaultWriteBurstSize,
		lastLogTime:     time.Time{},
		timeNow:         time.Now,
	}
}

// allowWrite consumes one token from the bucket, returning true if the write
// should proceed. When the bucket is empty, the write is rejected and a
// rate-limited warning is logged periodically.
func (limiter *writeRateLimiter) allowWrite() bool {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	currentTime := limiter.timeNow()
	elapsed := currentTime.Sub(limiter.lastRefillTime).Seconds()
	limiter.tokensAvailable += elapsed * limiter.ratePerSecond
	if limiter.tokensAvailable > float64(limiter.burstSize) {
		limiter.tokensAvailable = float64(limiter.burstSize)
	}
	limiter.lastRefillTime = currentTime

	if limiter.tokensAvailable < 1 {
		limiter.droppedSinceLastLog++
		if currentTime.Sub(limiter.lastLogTime) >= writeRateLimitLogInterval {
			logging.Default().Warn("store write rate limited",
				"dropped", strconv.FormatInt(limiter.droppedSinceLastLog, 10),
				"rate", strconv.FormatFloat(limiter.ratePerSecond, 'f', 0, 64),
				"burst", strconv.Itoa(limiter.burstSize))
			limiter.droppedSinceLastLog = 0
			limiter.lastLogTime = currentTime
		}
		return false
	}

	limiter.tokensAvailable--
	return true
}
