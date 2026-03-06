package core

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// CacheEntry stores a cached search response with its expiration time
type CacheEntry struct {
	Data      []byte
	ExpiresAt time.Time
}

// ResponseCache is a thread-safe in-memory cache with TTL (Time To Live).
// It prevents hammering search engines with identical queries and significantly
// reduces the chance of getting rate-limited or captcha-blocked.
type ResponseCache struct {
	mu      sync.RWMutex
	entries map[string]CacheEntry
	ttl     time.Duration
	maxSize int
}

// NewResponseCache creates a new cache with the specified TTL and max entries.
// It starts a background goroutine that evicts expired entries every minute.
func NewResponseCache(ttl time.Duration, maxSize int) *ResponseCache {
	cache := &ResponseCache{
		entries: make(map[string]CacheEntry),
		ttl:     ttl,
		maxSize: maxSize,
	}

	// Background cleanup of expired entries
	go cache.cleanupLoop()

	return cache
}

// BuildKey creates a unique cache key from the engine name and query parameters
func BuildCacheKey(engine string, q Query) string {
	raw := engine + "|" + q.Text + "|" + q.LangCode + "|" + q.DateInterval + "|" +
		q.Filetype + "|" + q.Site + "|" + string(rune(q.Limit))
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

// Get retrieves a cached entry if it exists and hasn't expired
func (c *ResponseCache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.entries[key]
	if !exists {
		return nil, false
	}

	if time.Now().After(entry.ExpiresAt) {
		return nil, false
	}

	logrus.Debugf("Cache hit for key: %s", key[:12])
	return entry.Data, true
}

// Set stores data in the cache with the configured TTL
func (c *ResponseCache) Set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple eviction: if at max capacity, clear oldest entries
	if len(c.entries) >= c.maxSize {
		c.evictExpired()
		// If still at capacity after evicting expired, remove ~25% of entries
		if len(c.entries) >= c.maxSize {
			count := 0
			target := c.maxSize / 4
			for k := range c.entries {
				delete(c.entries, k)
				count++
				if count >= target {
					break
				}
			}
		}
	}

	c.entries[key] = CacheEntry{
		Data:      data,
		ExpiresAt: time.Now().Add(c.ttl),
	}

	logrus.Debugf("Cache set for key: %s (ttl: %s)", key[:12], c.ttl)
}

// Stats returns the current number of entries in the cache
func (c *ResponseCache) Stats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	active := 0
	now := time.Now()
	for _, entry := range c.entries {
		if now.Before(entry.ExpiresAt) {
			active++
		}
	}

	return map[string]interface{}{
		"total_entries":  len(c.entries),
		"active_entries": active,
		"max_size":       c.maxSize,
		"ttl_seconds":    c.ttl.Seconds(),
	}
}

// evictExpired removes all expired entries (must be called with lock held)
func (c *ResponseCache) evictExpired() {
	now := time.Now()
	for key, entry := range c.entries {
		if now.After(entry.ExpiresAt) {
			delete(c.entries, key)
		}
	}
}

// cleanupLoop runs periodically to remove expired cache entries
func (c *ResponseCache) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		before := len(c.entries)
		c.evictExpired()
		after := len(c.entries)
		c.mu.Unlock()

		if before != after {
			logrus.Debugf("Cache cleanup: removed %d expired entries (%d remaining)", before-after, after)
		}
	}
}
