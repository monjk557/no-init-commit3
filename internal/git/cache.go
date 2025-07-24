package git

import (
	"container/list"
	"sync"
	"time"
)

// RepositoryCache manages cached Repository instances with LRU eviction
type RepositoryCache struct {
	mu      sync.RWMutex
	items   map[string]*cacheItem
	lruList *list.List
	maxSize int
	maxAge  time.Duration
	onEvict func(path string, repo Repository)
}

// cacheItem represents a cached repository with metadata
type cacheItem struct {
	path      string
	repo      Repository
	createdAt time.Time
	lastUsed  time.Time
	element   *list.Element
}

// CacheConfig holds configuration for the repository cache
type CacheConfig struct {
	// MaxSize is the maximum number of repositories to cache (0 for unlimited)
	MaxSize int

	// MaxAge is the maximum age of cached repositories (0 for no expiration)
	MaxAge time.Duration

	// OnEvict is called when a repository is evicted from the cache
	OnEvict func(path string, repo Repository)
}

// DefaultCacheConfig returns a default cache configuration
func DefaultCacheConfig() *CacheConfig {
	return &CacheConfig{
		MaxSize: 100,              // Cache up to 100 repositories
		MaxAge:  30 * time.Minute, // Expire after 30 minutes
		OnEvict: func(path string, repo Repository) {
			// Default eviction handler closes the repository
			if repo != nil {
				_ = repo.Close()
			}
		},
	}
}

// NewRepositoryCache creates a new repository cache with the given configuration
func NewRepositoryCache(config *CacheConfig) *RepositoryCache {
	if config == nil {
		config = DefaultCacheConfig()
	}

	return &RepositoryCache{
		items:   make(map[string]*cacheItem),
		lruList: list.New(),
		maxSize: config.MaxSize,
		maxAge:  config.MaxAge,
		onEvict: config.OnEvict,
	}
}

// Get retrieves a repository from the cache
func (c *RepositoryCache) Get(path string) (Repository, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, exists := c.items[path]
	if !exists {
		return nil, false
	}

	// Check if item has expired
	if c.maxAge > 0 && time.Since(item.createdAt) > c.maxAge {
		c.removeItem(item)
		return nil, false
	}

	// Update last used time and move to front of LRU list
	item.lastUsed = time.Now()
	c.lruList.MoveToFront(item.element)

	return item.repo, true
}

// Put stores a repository in the cache
func (c *RepositoryCache) Put(path string, repo Repository) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If item already exists, update it
	if existing, exists := c.items[path]; exists {
		existing.repo = repo
		existing.lastUsed = time.Now()
		existing.createdAt = time.Now()
		c.lruList.MoveToFront(existing.element)
		return
	}

	// Create new cache item
	now := time.Now()
	item := &cacheItem{
		path:      path,
		repo:      repo,
		createdAt: now,
		lastUsed:  now,
	}

	// Add to front of LRU list
	item.element = c.lruList.PushFront(item)
	c.items[path] = item

	// Evict oldest items if cache is full
	c.evictIfNeeded()
}

// Remove removes a repository from the cache
func (c *RepositoryCache) Remove(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, exists := c.items[path]
	if !exists {
		return false
	}

	c.removeItem(item)
	return true
}

// Clear removes all repositories from the cache
func (c *RepositoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Call eviction callback for all items
	for _, item := range c.items {
		if c.onEvict != nil {
			c.onEvict(item.path, item.repo)
		}
	}

	// Clear all data structures
	c.items = make(map[string]*cacheItem)
	c.lruList.Init()
}

// Size returns the current number of cached repositories
func (c *RepositoryCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Stats returns cache statistics
func (c *RepositoryCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := CacheStats{
		Size:    len(c.items),
		MaxSize: c.maxSize,
		MaxAge:  c.maxAge,
	}

	// Calculate oldest and newest items
	if len(c.items) > 0 {
		now := time.Now()
		oldest := now
		newest := time.Time{}

		for _, item := range c.items {
			if item.createdAt.Before(oldest) {
				oldest = item.createdAt
			}
			if item.createdAt.After(newest) {
				newest = item.createdAt
			}
		}

		stats.OldestAge = now.Sub(oldest)
		stats.NewestAge = now.Sub(newest)
	}

	return stats
}

// CleanupExpired removes all expired items from the cache
func (c *RepositoryCache) CleanupExpired() int {
	if c.maxAge <= 0 {
		return 0 // No expiration configured
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var expiredItems []*cacheItem
	cutoff := time.Now().Add(-c.maxAge)

	// Find expired items
	for _, item := range c.items {
		if item.createdAt.Before(cutoff) {
			expiredItems = append(expiredItems, item)
		}
	}

	// Remove expired items
	for _, item := range expiredItems {
		c.removeItem(item)
	}

	return len(expiredItems)
}

// removeItem removes an item from the cache (must be called with lock held)
func (c *RepositoryCache) removeItem(item *cacheItem) {
	// Remove from map
	delete(c.items, item.path)

	// Remove from LRU list
	c.lruList.Remove(item.element)

	// Call eviction callback
	if c.onEvict != nil {
		c.onEvict(item.path, item.repo)
	}
}

// evictIfNeeded evicts oldest items if cache size exceeds maximum (must be called with lock held)
func (c *RepositoryCache) evictIfNeeded() {
	if c.maxSize <= 0 {
		return // No size limit configured
	}

	// Evict oldest items until we're under the size limit
	for len(c.items) > c.maxSize {
		// Get oldest item from back of LRU list
		oldest := c.lruList.Back()
		if oldest == nil {
			break
		}

		item := oldest.Value.(*cacheItem)
		c.removeItem(item)
	}
}

// CacheStats provides statistics about the cache
type CacheStats struct {
	Size      int           `json:"size"`
	MaxSize   int           `json:"max_size"`
	MaxAge    time.Duration `json:"max_age"`
	OldestAge time.Duration `json:"oldest_age"`
	NewestAge time.Duration `json:"newest_age"`
}

// Global cache instance (can be overridden for testing)
var globalCache = NewRepositoryCache(DefaultCacheConfig())

// SetGlobalCache sets the global repository cache instance
func SetGlobalCache(cache *RepositoryCache) {
	globalCache = cache
}

// GetGlobalCache returns the global repository cache instance
func GetGlobalCache() *RepositoryCache {
	return globalCache
}

// ClearGlobalCache clears the global repository cache
func ClearGlobalCache() {
	globalCache.Clear()
}

// GetGlobalCacheStats returns statistics for the global cache
func GetGlobalCacheStats() CacheStats {
	return globalCache.Stats()
}
