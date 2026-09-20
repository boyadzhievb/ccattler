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
// It uses a simple key→entry map with expiration. Thread-safe.
type ResponseCache struct {
	entries map[string]responseCacheEntry
	mutex   sync.RWMutex
	ttl     time.Duration
	timeNow func() time.Time
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

// Set stores a response in the cache with the configured TTL.
func (cache *ResponseCache) Set(cacheKey string, data []byte) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	cache.entries[cacheKey] = responseCacheEntry{
		data:      data,
		expiresAt: cache.timeNow().Add(cache.ttl),
	}
}

// Invalidate removes a specific entry from the cache.
func (cache *ResponseCache) Invalidate(cacheKey string) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	delete(cache.entries, cacheKey)
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
