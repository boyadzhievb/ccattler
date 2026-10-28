// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package store

import (
	"testing"
	"time"
)

func TestWriteRateLimiterAllowsBurst(test *testing.T) {
	limiter := newWriteRateLimiter()
	for iteration := 0; iteration < defaultWriteBurstSize; iteration++ {
		if !limiter.allowWrite() {
			test.Fatalf("expected allowWrite to succeed at iteration %d within burst", iteration)
		}
	}
}

func TestWriteRateLimiterRejectsAfterBurstExhausted(test *testing.T) {
	limiter := newWriteRateLimiter()
	for iteration := 0; iteration < defaultWriteBurstSize; iteration++ {
		limiter.allowWrite()
	}
	if limiter.allowWrite() {
		test.Fatal("expected allowWrite to reject after burst exhausted")
	}
}

func TestWriteRateLimiterRefillsOverTime(test *testing.T) {
	frozenTime := time.Now()
	limiter := newWriteRateLimiter()
	limiter.timeNow = func() time.Time { return frozenTime }

	for iteration := 0; iteration < defaultWriteBurstSize; iteration++ {
		limiter.allowWrite()
	}
	if limiter.allowWrite() {
		test.Fatal("expected rejection after burst exhausted")
	}

	frozenTime = frozenTime.Add(1 * time.Second)
	allowedCount := 0
	for iteration := 0; iteration < int(defaultWriteRatePerSecond)+100; iteration++ {
		if limiter.allowWrite() {
			allowedCount++
		}
	}
	if allowedCount < int(defaultWriteRatePerSecond)-1 || allowedCount > int(defaultWriteRatePerSecond)+1 {
		test.Fatalf("expected ~%d writes after 1s refill, got %d", int(defaultWriteRatePerSecond), allowedCount)
	}
}

func TestWriteRateLimiterDoesNotExceedBurstCap(test *testing.T) {
	frozenTime := time.Now()
	limiter := newWriteRateLimiter()
	limiter.timeNow = func() time.Time { return frozenTime }

	frozenTime = frozenTime.Add(10 * time.Second)

	allowedCount := 0
	for iteration := 0; iteration < defaultWriteBurstSize+500; iteration++ {
		if limiter.allowWrite() {
			allowedCount++
		}
	}
	if allowedCount != defaultWriteBurstSize {
		test.Fatalf("expected burst cap of %d, got %d", defaultWriteBurstSize, allowedCount)
	}
}
