package permissions

import (
	"sync"
	"time"
)

// PermissionCache provides thread-safe caching of permission check results.
// It uses sync.Map for concurrent access and includes TTL-based expiration.
type PermissionCache struct {
	cache sync.Map
	ttl   time.Duration
	// We could add a cleanup goroutine here in the future for automatic expiration
}

// NewPermissionCache creates a new permission cache with the specified TTL.
func NewPermissionCache(ttl time.Duration) *PermissionCache {
	return &PermissionCache{
		cache: sync.Map{},
		ttl:   ttl,
	}
}

// Get retrieves a permission result from the cache.
// Returns nil if the key doesn't exist.
func (pc *PermissionCache) Get(key string) *PermissionResult {
	value, exists := pc.cache.Load(key)
	if !exists {
		return nil
	}

	result, ok := value.(*PermissionResult)
	if !ok {
		// This should never happen, but handle it gracefully
		pc.cache.Delete(key)
		return nil
	}

	return result
}

// Set stores a permission result in the cache.
func (pc *PermissionCache) Set(key string, result *PermissionResult) {
	pc.cache.Store(key, result)
}

// Delete removes a permission result from the cache.
func (pc *PermissionCache) Delete(key string) {
	pc.cache.Delete(key)
}

// Clear removes all entries from the cache.
func (pc *PermissionCache) Clear() {
	pc.cache.Range(func(key, value interface{}) bool {
		pc.cache.Delete(key)
		return true
	})
}

// Close gracefully shuts down the cache.
// Currently, this doesn't do anything since sync.Map doesn't require cleanup,
// but it's here for future extensibility (e.g., stopping cleanup goroutines).
func (pc *PermissionCache) Close() {
	// Future: Stop any background cleanup goroutines
}

// CleanupExpired removes expired entries from the cache.
// This is a manual cleanup method that can be called periodically.
func (pc *PermissionCache) CleanupExpired() {
	now := time.Now()
	pc.cache.Range(func(key, value interface{}) bool {
		result, ok := value.(*PermissionResult)
		if !ok {
			// Invalid entry, remove it
			pc.cache.Delete(key)
			return true
		}

		if now.After(result.ExpiresAt) {
			pc.cache.Delete(key)
		}
		return true
	})
}

// Size returns the approximate number of entries in the cache.
// Note: This iterates through all entries, so it's O(n) and should be used sparingly.
func (pc *PermissionCache) Size() int {
	count := 0
	pc.cache.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}
