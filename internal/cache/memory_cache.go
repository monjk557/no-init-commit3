package cache

import (
	"container/list"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MemoryCache implements high-speed L1 caching using in-memory LRU with TTL support
type MemoryCache[T any] struct {
	mu        sync.RWMutex
	items     map[string]*memoryCacheItem[T]
	evictList *list.List
	maxSize   int
	logger    *zap.Logger
	metrics   *MemoryCacheMetrics

	// Background cleanup
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
	cleanupDone   sync.WaitGroup
}

// memoryCacheItem represents a cached item with TTL and LRU support
type memoryCacheItem[T any] struct {
	key         string
	value       T
	expiresAt   time.Time
	element     *list.Element
	createdAt   time.Time
	accessCount int64
}

// MemoryCacheConfig contains memory cache configuration
type MemoryCacheConfig struct {
	MaxSize         int           `json:"max_size" yaml:"max_size"`
	DefaultTTL      time.Duration `json:"default_ttl" yaml:"default_ttl"`
	CleanupInterval time.Duration `json:"cleanup_interval" yaml:"cleanup_interval"`
	EnableMetrics   bool          `json:"enable_metrics" yaml:"enable_metrics"`
}

// MemoryCacheMetrics tracks memory cache performance
type MemoryCacheMetrics struct {
	mu                sync.RWMutex
	Hits              int64   `json:"hits"`
	Misses            int64   `json:"misses"`
	Evictions         int64   `json:"evictions"`
	Expirations       int64   `json:"expirations"`
	CurrentEntries    int64   `json:"current_entries"`
	TotalStorageSize  int64   `json:"total_storage_size_bytes"`
	TotalAccessTime   int64   `json:"total_access_time_ns"`
	AverageAccessTime float64 `json:"average_access_time_ns"`
}

// NewMemoryCache creates a new high-performance memory cache
func NewMemoryCache[T any](config *MemoryCacheConfig, logger *zap.Logger) *MemoryCache[T] {
	if config == nil {
		config = &MemoryCacheConfig{
			MaxSize:         1000,
			DefaultTTL:      10 * time.Minute,
			CleanupInterval: 1 * time.Minute,
			EnableMetrics:   true,
		}
	}

	// Apply defaults
	if config.MaxSize <= 0 {
		config.MaxSize = 1000
	}
	if config.DefaultTTL <= 0 {
		config.DefaultTTL = 10 * time.Minute
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 1 * time.Minute
	}

	mc := &MemoryCache[T]{
		items:       make(map[string]*memoryCacheItem[T]),
		evictList:   list.New(),
		maxSize:     config.MaxSize,
		logger:      logger,
		metrics:     &MemoryCacheMetrics{},
		stopCleanup: make(chan struct{}),
	}

	// Start background cleanup goroutine
	mc.startCleanup(config.CleanupInterval)

	logger.Info("Memory cache initialized",
		zap.Int("max_size", config.MaxSize),
		zap.Duration("default_ttl", config.DefaultTTL),
		zap.Duration("cleanup_interval", config.CleanupInterval))

	return mc
}

// Get retrieves an item from memory cache with microsecond-level performance
func (m *MemoryCache[T]) Get(key string) (T, bool) {
	var zero T
	startTime := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.items[key]
	if !exists {
		m.recordMiss()
		return zero, false
	}

	// Check if expired
	if time.Now().After(item.expiresAt) {
		m.removeItemLocked(item)
		m.recordExpiration()
		return zero, false
	}

	// Move to front (most recently used)
	m.evictList.MoveToFront(item.element)
	item.accessCount++

	m.recordHit(time.Since(startTime))
	return item.value, true
}

// Set stores an item in memory cache with specified TTL
func (m *MemoryCache[T]) Set(key string, value T, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	expiresAt := now.Add(ttl)

	// Check if item already exists
	if existingItem, exists := m.items[key]; exists {
		// Update existing item
		existingItem.value = value
		existingItem.expiresAt = expiresAt
		existingItem.accessCount++
		m.evictList.MoveToFront(existingItem.element)
		return
	}

	// Create new item
	item := &memoryCacheItem[T]{
		key:         key,
		value:       value,
		expiresAt:   expiresAt,
		createdAt:   now,
		accessCount: 1,
	}

	// Add to front of eviction list
	element := m.evictList.PushFront(item)
	item.element = element
	m.items[key] = item

	// Update metrics
	m.metrics.mu.Lock()
	m.metrics.CurrentEntries++
	m.metrics.TotalStorageSize += m.estimateItemSize(item)
	m.metrics.mu.Unlock()

	// Evict if necessary
	for len(m.items) > m.maxSize {
		m.evictOldestLocked()
	}
}

// Delete removes an item from memory cache
func (m *MemoryCache[T]) Delete(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.items[key]
	if !exists {
		return false
	}

	m.removeItemLocked(item)
	return true
}

// Clear removes all items from memory cache
func (m *MemoryCache[T]) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear all data structures
	m.items = make(map[string]*memoryCacheItem[T])
	m.evictList.Init()

	// Reset metrics
	m.metrics.mu.Lock()
	m.metrics.CurrentEntries = 0
	m.metrics.TotalStorageSize = 0
	m.metrics.mu.Unlock()
}

// Size returns the current number of items in cache
func (m *MemoryCache[T]) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}

// GetMetrics returns current cache metrics
func (m *MemoryCache[T]) GetMetrics() MemoryCacheMetrics {
	m.metrics.mu.RLock()
	defer m.metrics.mu.RUnlock()

	// Calculate average access time
	avgAccessTime := float64(0)
	if m.metrics.Hits > 0 {
		avgAccessTime = float64(m.metrics.TotalAccessTime) / float64(m.metrics.Hits)
	}

	return MemoryCacheMetrics{
		Hits:              m.metrics.Hits,
		Misses:            m.metrics.Misses,
		Evictions:         m.metrics.Evictions,
		Expirations:       m.metrics.Expirations,
		CurrentEntries:    m.metrics.CurrentEntries,
		TotalStorageSize:  m.metrics.TotalStorageSize,
		AverageAccessTime: avgAccessTime,
	}
}

// GetHitRatio returns the cache hit ratio (0.0 to 1.0)
func (m *MemoryCache[T]) GetHitRatio() float64 {
	m.metrics.mu.RLock()
	defer m.metrics.mu.RUnlock()

	totalAccess := m.metrics.Hits + m.metrics.Misses
	if totalAccess == 0 {
		return 0.0
	}
	return float64(m.metrics.Hits) / float64(totalAccess)
}

// ResetMetrics clears all performance metrics
func (m *MemoryCache[T]) ResetMetrics() {
	m.metrics.mu.Lock()
	defer m.metrics.mu.Unlock()

	m.metrics.Hits = 0
	m.metrics.Misses = 0
	m.metrics.Evictions = 0
	m.metrics.Expirations = 0
	m.metrics.TotalAccessTime = 0
	m.metrics.AverageAccessTime = 0
}

// Close stops the background cleanup and releases resources
func (m *MemoryCache[T]) Close() error {
	close(m.stopCleanup)
	m.cleanupDone.Wait()
	return nil
}

// removeItemLocked removes an item from cache (must be called with lock held)
func (m *MemoryCache[T]) removeItemLocked(item *memoryCacheItem[T]) {
	delete(m.items, item.key)
	m.evictList.Remove(item.element)

	m.metrics.mu.Lock()
	m.metrics.CurrentEntries--
	m.metrics.TotalStorageSize -= m.estimateItemSize(item)
	m.metrics.mu.Unlock()
}

// evictOldestLocked removes the least recently used item
func (m *MemoryCache[T]) evictOldestLocked() {
	element := m.evictList.Back()
	if element != nil {
		item := element.Value.(*memoryCacheItem[T])
		m.removeItemLocked(item)
		m.recordEviction()
	}
}

// startCleanup begins the background cleanup goroutine
func (m *MemoryCache[T]) startCleanup(interval time.Duration) {
	m.cleanupTicker = time.NewTicker(interval)
	m.cleanupDone.Add(1)

	go func() {
		defer m.cleanupDone.Done()
		for {
			select {
			case <-m.cleanupTicker.C:
				m.cleanupExpired()
			case <-m.stopCleanup:
				m.cleanupTicker.Stop()
				return
			}
		}
	}()
}

// cleanupExpired removes all expired items
func (m *MemoryCache[T]) cleanupExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var expiredKeys []string

	// Find expired items
	for key, item := range m.items {
		if now.After(item.expiresAt) {
			expiredKeys = append(expiredKeys, key)
		}
	}

	// Remove expired items
	for _, key := range expiredKeys {
		if item, exists := m.items[key]; exists {
			m.removeItemLocked(item)
			m.recordExpiration()
		}
	}

	// Log cleanup results if significant
	if len(expiredKeys) > 0 {
		m.logger.Debug("Cleaned up expired cache entries",
			zap.Int("expired_count", len(expiredKeys)),
			zap.Int("remaining_count", len(m.items)))
	}
}

// estimateItemSize estimates the memory size of a cache item
func (m *MemoryCache[T]) estimateItemSize(item *memoryCacheItem[T]) int64 {
	// Base overhead: key + timestamps + pointers + struct overhead
	size := int64(len(item.key) + 64 + 24 + 16)

	// Add estimated value size (approximate)
	size += 1024 // Rough estimate for cached response objects

	return size
}

// recordHit records a cache hit with timing
func (m *MemoryCache[T]) recordHit(accessTime time.Duration) {
	m.metrics.mu.Lock()
	m.metrics.Hits++
	m.metrics.TotalAccessTime += accessTime.Nanoseconds()
	m.metrics.mu.Unlock()
}

// recordMiss records a cache miss
func (m *MemoryCache[T]) recordMiss() {
	m.metrics.mu.Lock()
	m.metrics.Misses++
	m.metrics.mu.Unlock()
}

// recordEviction records an item eviction
func (m *MemoryCache[T]) recordEviction() {
	m.metrics.mu.Lock()
	m.metrics.Evictions++
	m.metrics.mu.Unlock()
}

// recordExpiration records an item expiration
func (m *MemoryCache[T]) recordExpiration() {
	m.metrics.mu.Lock()
	m.metrics.Expirations++
	m.metrics.mu.Unlock()
}

// GetCacheInfo returns detailed cache information for monitoring
func (m *MemoryCache[T]) GetCacheInfo() map[string]interface{} {
	m.mu.RLock()
	currentSize := len(m.items)
	m.mu.RUnlock()

	metrics := m.GetMetrics()
	hitRatio := m.GetHitRatio()

	return map[string]interface{}{
		"type":         "memory_cache",
		"current_size": currentSize,
		"max_size":     m.maxSize,
		"hit_ratio":    hitRatio,
		"metrics": map[string]interface{}{
			"hits":                metrics.Hits,
			"misses":              metrics.Misses,
			"evictions":           metrics.Evictions,
			"expirations":         metrics.Expirations,
			"current_entries":     metrics.CurrentEntries,
			"total_storage_size":  metrics.TotalStorageSize,
			"average_access_time": metrics.AverageAccessTime,
		},
		"utilization":     float64(currentSize) / float64(m.maxSize),
		"storage_size_mb": float64(metrics.TotalStorageSize) / (1024 * 1024),
	}
}
