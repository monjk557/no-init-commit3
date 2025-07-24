package git

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRepositoryCache_Basic(t *testing.T) {
	cache := NewRepositoryCache(&CacheConfig{
		MaxSize: 5,
		MaxAge:  time.Minute,
	})

	// Test empty cache
	if cache.Size() != 0 {
		t.Errorf("Expected empty cache, got size %d", cache.Size())
	}

	// Create mock repositories
	mock1 := NewMockRepository("/repo1")
	mock2 := NewMockRepository("/repo2")

	// Test Put and Get
	cache.Put("/repo1", mock1)
	cache.Put("/repo2", mock2)

	if cache.Size() != 2 {
		t.Errorf("Expected cache size 2, got %d", cache.Size())
	}

	// Test successful Get
	retrieved, found := cache.Get("/repo1")
	if !found {
		t.Error("Expected to find /repo1 in cache")
	}
	if retrieved != mock1 {
		t.Error("Retrieved repository doesn't match stored repository")
	}

	// Test Get for non-existent item
	_, found = cache.Get("/nonexistent")
	if found {
		t.Error("Expected not to find /nonexistent in cache")
	}
}

func TestRepositoryCache_LRUEviction(t *testing.T) {
	evictedPaths := make([]string, 0)
	cache := NewRepositoryCache(&CacheConfig{
		MaxSize: 2,
		OnEvict: func(path string, repo Repository) {
			evictedPaths = append(evictedPaths, path)
			if repo != nil {
				_ = repo.Close()
			}
		},
	})

	// Add items to fill cache
	cache.Put("/repo1", NewMockRepository("/repo1"))
	cache.Put("/repo2", NewMockRepository("/repo2"))

	if cache.Size() != 2 {
		t.Errorf("Expected cache size 2, got %d", cache.Size())
	}

	// Access repo1 to make it most recently used
	cache.Get("/repo1")

	// Add repo3, which should evict repo2 (least recently used)
	cache.Put("/repo3", NewMockRepository("/repo3"))

	if cache.Size() != 2 {
		t.Errorf("Expected cache size 2, got %d", cache.Size())
	}

	// Check that repo2 was evicted
	if len(evictedPaths) != 1 || evictedPaths[0] != "/repo2" {
		t.Errorf("Expected /repo2 to be evicted, got %v", evictedPaths)
	}

	// Verify repo1 and repo3 are still in cache
	_, found1 := cache.Get("/repo1")
	_, found3 := cache.Get("/repo3")
	_, found2 := cache.Get("/repo2")

	if !found1 {
		t.Error("Expected /repo1 to still be in cache")
	}
	if !found3 {
		t.Error("Expected /repo3 to be in cache")
	}
	if found2 {
		t.Error("Expected /repo2 to be evicted from cache")
	}
}

func TestRepositoryCache_Expiration(t *testing.T) {
	evictedPaths := make([]string, 0)
	cache := NewRepositoryCache(&CacheConfig{
		MaxAge: 50 * time.Millisecond,
		OnEvict: func(path string, repo Repository) {
			evictedPaths = append(evictedPaths, path)
		},
	})

	// Add item to cache
	cache.Put("/repo1", NewMockRepository("/repo1"))

	// Verify item is in cache
	_, found := cache.Get("/repo1")
	if !found {
		t.Error("Expected /repo1 to be in cache")
	}

	// Wait for expiration
	time.Sleep(60 * time.Millisecond)

	// Try to get expired item
	_, found = cache.Get("/repo1")
	if found {
		t.Error("Expected /repo1 to be expired and removed")
	}

	// Check that item was evicted due to expiration
	if len(evictedPaths) != 1 || evictedPaths[0] != "/repo1" {
		t.Errorf("Expected /repo1 to be evicted due to expiration, got %v", evictedPaths)
	}
}

func TestRepositoryCache_Remove(t *testing.T) {
	evictedPaths := make([]string, 0)
	cache := NewRepositoryCache(&CacheConfig{
		OnEvict: func(path string, repo Repository) {
			evictedPaths = append(evictedPaths, path)
		},
	})

	cache.Put("/repo1", NewMockRepository("/repo1"))
	cache.Put("/repo2", NewMockRepository("/repo2"))

	// Test successful removal
	removed := cache.Remove("/repo1")
	if !removed {
		t.Error("Expected Remove to return true")
	}

	if cache.Size() != 1 {
		t.Errorf("Expected cache size 1 after removal, got %d", cache.Size())
	}

	// Check eviction callback was called
	if len(evictedPaths) != 1 || evictedPaths[0] != "/repo1" {
		t.Errorf("Expected /repo1 to be evicted, got %v", evictedPaths)
	}

	// Test removal of non-existent item
	removed = cache.Remove("/nonexistent")
	if removed {
		t.Error("Expected Remove to return false for non-existent item")
	}
}

func TestRepositoryCache_Clear(t *testing.T) {
	evictedPaths := make([]string, 0)
	cache := NewRepositoryCache(&CacheConfig{
		OnEvict: func(path string, repo Repository) {
			evictedPaths = append(evictedPaths, path)
		},
	})

	cache.Put("/repo1", NewMockRepository("/repo1"))
	cache.Put("/repo2", NewMockRepository("/repo2"))
	cache.Put("/repo3", NewMockRepository("/repo3"))

	if cache.Size() != 3 {
		t.Errorf("Expected cache size 3, got %d", cache.Size())
	}

	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("Expected cache size 0 after clear, got %d", cache.Size())
	}

	// Check that all items were evicted
	if len(evictedPaths) != 3 {
		t.Errorf("Expected 3 items to be evicted, got %d", len(evictedPaths))
	}
}

func TestRepositoryCache_CleanupExpired(t *testing.T) {
	cache := NewRepositoryCache(&CacheConfig{
		MaxAge: 50 * time.Millisecond,
	})

	// Add items at different times
	cache.Put("/repo1", NewMockRepository("/repo1"))
	time.Sleep(25 * time.Millisecond)
	cache.Put("/repo2", NewMockRepository("/repo2"))
	time.Sleep(25 * time.Millisecond)
	cache.Put("/repo3", NewMockRepository("/repo3"))

	if cache.Size() != 3 {
		t.Errorf("Expected cache size 3, got %d", cache.Size())
	}

	// Wait for some items to expire
	time.Sleep(10 * time.Millisecond)

	// Clean up expired items
	cleaned := cache.CleanupExpired()

	// Should have cleaned up repo1 (oldest)
	if cleaned < 1 {
		t.Errorf("Expected at least 1 item to be cleaned up, got %d", cleaned)
	}

	// Cache size should be reduced
	if cache.Size() >= 3 {
		t.Errorf("Expected cache size to be reduced after cleanup, got %d", cache.Size())
	}
}

func TestRepositoryCache_Stats(t *testing.T) {
	cache := NewRepositoryCache(&CacheConfig{
		MaxSize: 10,
		MaxAge:  time.Minute,
	})

	// Test stats for empty cache
	stats := cache.Stats()
	if stats.Size != 0 {
		t.Errorf("Expected size 0, got %d", stats.Size)
	}
	if stats.MaxSize != 10 {
		t.Errorf("Expected max size 10, got %d", stats.MaxSize)
	}
	if stats.MaxAge != time.Minute {
		t.Errorf("Expected max age 1 minute, got %v", stats.MaxAge)
	}

	// Add items and check stats
	cache.Put("/repo1", NewMockRepository("/repo1"))
	cache.Put("/repo2", NewMockRepository("/repo2"))

	stats = cache.Stats()
	if stats.Size != 2 {
		t.Errorf("Expected size 2, got %d", stats.Size)
	}
	if stats.OldestAge <= 0 {
		t.Error("Expected oldest age to be > 0")
	}
	if stats.NewestAge < 0 {
		t.Error("Expected newest age to be >= 0")
	}
}

func TestRepositoryCache_ConcurrentAccess(t *testing.T) {
	cache := NewRepositoryCache(&CacheConfig{
		MaxSize: 100,
	})

	// Test concurrent reads and writes
	var wg sync.WaitGroup
	numGoroutines := 50
	numOperations := 100

	// Writers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				path := fmt.Sprintf("/repo-%d-%d", id, j)
				cache.Put(path, NewMockRepository(path))
			}
		}(i)
	}

	// Readers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				path := fmt.Sprintf("/repo-%d-%d", id, j%10) // Read some existing paths
				cache.Get(path)
			}
		}(i)
	}

	wg.Wait()

	// Cache should have some items (exact number depends on timing and eviction)
	if cache.Size() == 0 {
		t.Error("Expected cache to have some items after concurrent operations")
	}
}

func TestRepositoryCache_UpdateExisting(t *testing.T) {
	cache := NewRepositoryCache(nil)

	mock1 := NewMockRepository("/repo1")
	mock2 := NewMockRepository("/repo1") // Same path, different instance

	// Put first repository
	cache.Put("/repo1", mock1)
	retrieved, found := cache.Get("/repo1")
	if !found || retrieved != mock1 {
		t.Error("Expected to get first repository")
	}

	// Update with second repository
	cache.Put("/repo1", mock2)
	retrieved, found = cache.Get("/repo1")
	if !found || retrieved != mock2 {
		t.Error("Expected to get updated repository")
	}

	// Cache size should still be 1
	if cache.Size() != 1 {
		t.Errorf("Expected cache size 1, got %d", cache.Size())
	}
}

func TestRepositoryCache_DefaultConfig(t *testing.T) {
	config := DefaultCacheConfig()

	if config.MaxSize != 100 {
		t.Errorf("Expected default max size 100, got %d", config.MaxSize)
	}
	if config.MaxAge != 30*time.Minute {
		t.Errorf("Expected default max age 30 minutes, got %v", config.MaxAge)
	}
	if config.OnEvict == nil {
		t.Error("Expected default OnEvict function to be set")
	}
}

func TestRepositoryCache_NoLimits(t *testing.T) {
	cache := NewRepositoryCache(&CacheConfig{
		MaxSize: 0, // No size limit
		MaxAge:  0, // No age limit
	})

	// Add many items
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/repo%d", i)
		cache.Put(path, NewMockRepository(path))
	}

	// All items should still be in cache
	if cache.Size() != 1000 {
		t.Errorf("Expected cache size 1000, got %d", cache.Size())
	}

	// No cleanup should happen
	cleaned := cache.CleanupExpired()
	if cleaned != 0 {
		t.Errorf("Expected no cleanup with no age limit, got %d", cleaned)
	}
}

func TestGlobalCache_Functions(t *testing.T) {
	// Save original global cache
	originalCache := GetGlobalCache()
	defer SetGlobalCache(originalCache)

	// Create test cache
	testCache := NewRepositoryCache(&CacheConfig{
		MaxSize: 2,
	})

	// Test setting and getting global cache
	SetGlobalCache(testCache)
	if GetGlobalCache() != testCache {
		t.Error("Expected GetGlobalCache to return the set cache")
	}

	// Test global cache operations
	testCache.Put("/repo1", NewMockRepository("/repo1"))
	stats := GetGlobalCacheStats()
	if stats.Size != 1 {
		t.Errorf("Expected global cache size 1, got %d", stats.Size)
	}

	// Test clear global cache
	ClearGlobalCache()
	stats = GetGlobalCacheStats()
	if stats.Size != 0 {
		t.Errorf("Expected global cache size 0 after clear, got %d", stats.Size)
	}
}
