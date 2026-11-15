// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"sync"
	"time"
)

// responseCacheEntry holds a cached response with its expiration time.
type responseCacheEntry struct {
	data      []byte
	expiresAt time.Time
}

// ResponseCache provides short-TTL caching for expensive API read paths.
// It uses a simple key→entry map with expiration. Thread-safe. A generation
// counter stops a read that started before an invalidation from caching its
// older answer afterwards.
type ResponseCache struct {
	entries    map[string]responseCacheEntry // entries maps a cache key to its response and expiry.
	mutex      sync.RWMutex                  // mutex guards entries and generation.
	ttl        time.Duration                 // ttl is how long an entry stays valid.
	timeNow    func() time.Time              // timeNow is the clock (replaced in tests).
	generation uint64                        // generation increases on every InvalidateAll.
}

// NewResponseCache creates a cache with the given TTL for entries.
func NewResponseCache(ttl time.Duration) *ResponseCache {
	return &ResponseCache{
		entries: make(map[string]responseCacheEntry),
		ttl:     ttl,
		timeNow: time.Now,
	}
}

// Get retrieves a cached response by key. Returns nil if not found or expired.
func (cache *ResponseCache) Get(cacheKey string) []byte {
	cache.mutex.RLock()
	defer cache.mutex.RUnlock()

	entry, exists := cache.entries[cacheKey]
	if !exists {
		return nil
	}
	if cache.timeNow().After(entry.expiresAt) {
		return nil
	}
	return entry.data
}

// Generation returns the current generation. A reader takes it before
// building a response and passes it to Set.
func (cache *ResponseCache) Generation() uint64 {
	cache.mutex.RLock()
	defer cache.mutex.RUnlock()
	return cache.generation
}

// Set stores a response with the configured TTL, unless the cache was
// invalidated after builtAtGeneration (the response may then be stale).
func (cache *ResponseCache) Set(cacheKey string, data []byte, builtAtGeneration uint64) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	if builtAtGeneration != cache.generation {
		return
	}
	cache.entries[cacheKey] = responseCacheEntry{
		data:      data,
		expiresAt: cache.timeNow().Add(cache.ttl),
	}
}

// InvalidateAll drops every entry and starts a new generation. Called after
// every API write so the writer reads its own change.
func (cache *ResponseCache) InvalidateAll() {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	cache.entries = make(map[string]responseCacheEntry)
	cache.generation++
}

// CleanupExpired removes all expired entries. Call periodically.
func (cache *ResponseCache) CleanupExpired() {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	currentTime := cache.timeNow()
	for cacheKey, entry := range cache.entries {
		if currentTime.After(entry.expiresAt) {
			delete(cache.entries, cacheKey)
		}
	}
}
