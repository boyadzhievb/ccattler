// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"testing"
	"time"
)

func TestResponseCacheSetAndGet(t *testing.T) {
	cache := NewResponseCache(5 * time.Second)

	cache.Set("status", []byte(`{"services":[]}`), cache.Generation())
	result := cache.Get("status")

	if result == nil {
		t.Fatal("expected cached value")
	}
	if string(result) != `{"services":[]}` {
		t.Errorf("unexpected cached value: %s", string(result))
	}
}

func TestResponseCacheReturnsNilForMissing(t *testing.T) {
	cache := NewResponseCache(5 * time.Second)

	if cache.Get("missing") != nil {
		t.Error("expected nil for missing key")
	}
}

func TestResponseCacheExpires(t *testing.T) {
	currentTime := time.Now()
	cache := NewResponseCache(2 * time.Second)
	cache.timeNow = func() time.Time { return currentTime }

	cache.Set("status", []byte(`{"services":[]}`), cache.Generation())

	currentTime = currentTime.Add(3 * time.Second)

	if cache.Get("status") != nil {
		t.Error("expected nil for expired entry")
	}
}

func TestResponseCacheInvalidate(t *testing.T) {
	cache := NewResponseCache(5 * time.Second)

	cache.Set("status", []byte(`data`), cache.Generation())
	cache.InvalidateAll()

	if cache.Get("status") != nil {
		t.Error("expected nil after invalidation")
	}
}

// TestResponseCacheRefusesStaleGeneration: a response built before an
// invalidation must not be cached after it.
func TestResponseCacheRefusesStaleGeneration(t *testing.T) {
	cache := NewResponseCache(5 * time.Second)

	generationBeforeWrite := cache.Generation()
	cache.InvalidateAll()
	cache.Set("status", []byte(`stale`), generationBeforeWrite)

	if cache.Get("status") != nil {
		t.Error("stale response was cached after invalidation")
	}
}

func TestResponseCacheCleanupExpired(t *testing.T) {
	currentTime := time.Now()
	cache := NewResponseCache(1 * time.Second)
	cache.timeNow = func() time.Time { return currentTime }

	cache.Set("old", []byte(`old-data`), cache.Generation())

	currentTime = currentTime.Add(2 * time.Second)

	cache.Set("fresh", []byte(`fresh-data`), cache.Generation())

	cache.CleanupExpired()

	if cache.Get("old") != nil {
		t.Error("expected old entry to be cleaned up")
	}
	if cache.Get("fresh") == nil {
		t.Error("expected fresh entry to survive cleanup")
	}
}

func TestResponseCacheOverwrite(t *testing.T) {
	cache := NewResponseCache(5 * time.Second)

	cache.Set("status", []byte(`v1`), cache.Generation())
	cache.Set("status", []byte(`v2`), cache.Generation())

	result := cache.Get("status")
	if string(result) != `v2` {
		t.Errorf("expected v2, got %s", string(result))
	}
}
