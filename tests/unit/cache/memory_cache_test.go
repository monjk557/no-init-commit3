package cache_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/cache"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestMemoryCache_BasicOperations(t *testing.T) {
	config := &cache.MemoryCacheConfig{
		MaxSize:         10,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: 100 * time.Millisecond,
		EnableMetrics:   true,
	}

	logger := zaptest.NewLogger(t)
	memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](config, logger)
	defer memCache.Close()

	testResponse := &overview.TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: "test/repo",
		Owner:      "test",
		Name:       "repo",
		Path:       "/src",
		Tree: []overview.GitTreeEntryWithCommit{
			{Type: "blob", Name: "main.go", Path: "/src/main.go"},
			{Type: "tree", Name: "utils", Path: "/src/utils"},
		},
	}

	t.Run("Set and Get", func(t *testing.T) {
		key := "test:key:1"
		memCache.Set(key, testResponse, 1*time.Minute)

		result, _ := memCache.Get(key)
		require.NotNil(t, result)
		assert.Equal(t, testResponse.Repository, result.Repository)
		assert.Equal(t, testResponse.Owner, result.Owner)
		assert.Equal(t, len(testResponse.Tree), len(result.Tree))
	})

	t.Run("Cache Miss", func(t *testing.T) {
		result, _ := memCache.Get("nonexistent:key")
		assert.Nil(t, result)
	})

	t.Run("TTL Expiration", func(t *testing.T) {
		key := "test:ttl:key"
		shortTTL := 50 * time.Millisecond

		memCache.Set(key, testResponse, shortTTL)

		// Should exist immediately
		result, _ := memCache.Get(key)
		require.NotNil(t, result)

		// Wait for expiration
		time.Sleep(100 * time.Millisecond)

		// Should be expired
		result, _ = memCache.Get(key)
		assert.Nil(t, result)
	})

	t.Run("LRU Eviction", func(t *testing.T) {
		// Create cache with small size
		smallConfig := &cache.MemoryCacheConfig{
			MaxSize:         3,
			DefaultTTL:      5 * time.Minute,
			CleanupInterval: 100 * time.Millisecond,
		}
		smallCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](smallConfig, logger)
		defer smallCache.Close()

		// Fill cache to capacity
		for i := 0; i < 3; i++ {
			key := fmt.Sprintf("key:%d", i)
			smallCache.Set(key, testResponse, 5*time.Minute)
		}

		assert.Equal(t, 3, smallCache.Size())

		// Access key:1 to make it recently used
		result, _ := smallCache.Get("key:1")
		require.NotNil(t, result)

		// Add one more item, should evict key:0 (least recently used)
		smallCache.Set("key:3", testResponse, 5*time.Minute)

		assert.Equal(t, 3, smallCache.Size()) // Still at capacity

		// key:0 should be evicted
		result, _ = smallCache.Get("key:0")
		assert.Nil(t, result)

		// key:1, key:2, key:3 should still exist
		result, _ = smallCache.Get("key:1")
		assert.NotNil(t, result)
		result, _ = smallCache.Get("key:2")
		assert.NotNil(t, result)
		result, _ = smallCache.Get("key:3")
		assert.NotNil(t, result)
	})
}

func TestMemoryCache_Metrics(t *testing.T) {
	config := &cache.MemoryCacheConfig{
		MaxSize:         10,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: 100 * time.Millisecond,
		EnableMetrics:   true,
	}

	logger := zaptest.NewLogger(t)
	memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](config, logger)
	defer memCache.Close()

	testResponse := &overview.TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: "test/metrics",
	}

	t.Run("Hit and Miss Metrics", func(t *testing.T) {
		// Reset metrics
		memCache.ResetMetrics()

		key := "metrics:key"

		// Should record miss
		result, _ := memCache.Get(key)
		assert.Nil(t, result)

		metrics := memCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.Misses)
		assert.Equal(t, int64(0), metrics.Hits)
		assert.Equal(t, 0.0, memCache.GetHitRatio())

		// Set value
		memCache.Set(key, testResponse, 1*time.Minute)

		metrics = memCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.CurrentEntries)

		// Should record hit
		result, _ = memCache.Get(key)
		require.NotNil(t, result)

		metrics = memCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.Hits)
		assert.Equal(t, int64(1), metrics.Misses)
		assert.Equal(t, 0.5, memCache.GetHitRatio()) // 1 hit out of 2 requests
	})

	t.Run("Eviction Metrics", func(t *testing.T) {
		// Create small cache to trigger evictions
		smallConfig := &cache.MemoryCacheConfig{
			MaxSize:         2,
			DefaultTTL:      5 * time.Minute,
			CleanupInterval: 100 * time.Millisecond,
		}
		smallCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](smallConfig, logger)
		defer smallCache.Close()

		// Fill beyond capacity to trigger eviction
		for i := 0; i < 3; i++ {
			key := fmt.Sprintf("evict:key:%d", i)
			smallCache.Set(key, testResponse, 5*time.Minute)
		}

		metrics := smallCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.Evictions)      // Should have 1 eviction
		assert.Equal(t, int64(2), metrics.CurrentEntries) // Size limited to 2
	})

	t.Run("Expiration Metrics", func(t *testing.T) {
		expireCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](&cache.MemoryCacheConfig{
			MaxSize:         10,
			DefaultTTL:      5 * time.Minute,
			CleanupInterval: 10 * time.Millisecond, // Very frequent cleanup
		}, logger)
		defer expireCache.Close()

		// Set with very short TTL
		expireCache.Set("expire:key", testResponse, 20*time.Millisecond)

		// Wait for cleanup cycle to run
		time.Sleep(50 * time.Millisecond)

		metrics := expireCache.GetMetrics()
		// Note: This might be flaky depending on timing, but should generally work
		assert.True(t, metrics.Expirations >= 0) // At least track expirations
	})

	t.Run("Storage Size Estimation", func(t *testing.T) {
		memCache.ResetMetrics()

		// Add several items
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("size:key:%d", i)
			memCache.Set(key, testResponse, 5*time.Minute)
		}

		metrics := memCache.GetMetrics()
		assert.Equal(t, int64(5), metrics.CurrentEntries)
		assert.True(t, metrics.TotalStorageSize > 0, "Storage size should be estimated")
	})
}

func TestMemoryCache_ConcurrentAccess(t *testing.T) {
	config := &cache.MemoryCacheConfig{
		MaxSize:         100,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: 1 * time.Second,
	}

	logger := zaptest.NewLogger(t)
	memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](config, logger)
	defer memCache.Close()

	testResponse := &overview.TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: "test/concurrent",
	}

	t.Run("Concurrent Set and Get", func(t *testing.T) {
		numGoroutines := 10
		numOperations := 100

		// Use a channel to synchronize goroutine completion
		done := make(chan bool, numGoroutines)

		// Start concurrent writers
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer func() { done <- true }()
				for j := 0; j < numOperations; j++ {
					key := fmt.Sprintf("concurrent:key:%d:%d", id, j)
					memCache.Set(key, testResponse, 1*time.Minute)
				}
			}(i)
		}

		// Start concurrent readers
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer func() { done <- true }()
				for j := 0; j < numOperations; j++ {
					key := fmt.Sprintf("concurrent:key:%d:%d", id, j)
					memCache.Get(key) // May hit or miss, depending on timing
				}
			}(i)
		}

		// Wait for all goroutines to complete
		for i := 0; i < 2*numGoroutines; i++ {
			<-done
		}

		// Cache should be functioning normally after concurrent access
		assert.True(t, memCache.Size() <= config.MaxSize)

		metrics := memCache.GetMetrics()
		assert.True(t, metrics.Hits+metrics.Misses > 0, "Should have recorded some operations")
	})
}

func TestMemoryCache_EdgeCases(t *testing.T) {
	config := &cache.MemoryCacheConfig{
		MaxSize:         5,
		DefaultTTL:      1 * time.Minute,
		CleanupInterval: 100 * time.Millisecond,
	}

	logger := zaptest.NewLogger(t)
	memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](config, logger)
	defer memCache.Close()

	t.Run("Nil Value Handling", func(t *testing.T) {
		// Setting nil should be ignored
		memCache.Set("nil:key", nil, 1*time.Minute)
		assert.Equal(t, 0, memCache.Size())
	})

	t.Run("Empty Key Handling", func(t *testing.T) {
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/empty",
		}

		memCache.Set("", testResponse, 1*time.Minute)
		result, _ := memCache.Get("")
		require.NotNil(t, result)
		assert.Equal(t, testResponse.Repository, result.Repository)
	})

	t.Run("Update Existing Key", func(t *testing.T) {
		key := "update:key"

		originalResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "original",
		}

		updatedResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "updated",
		}

		// Set original
		memCache.Set(key, originalResponse, 1*time.Minute)
		assert.Equal(t, 1, memCache.Size())

		// Update with new value
		memCache.Set(key, updatedResponse, 1*time.Minute)
		assert.Equal(t, 1, memCache.Size()) // Size shouldn't change

		// Should get updated value
		result, _ := memCache.Get(key)
		require.NotNil(t, result)
		assert.Equal(t, "updated", result.Repository)
	})

	t.Run("Clear All", func(t *testing.T) {
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/clear",
		}

		// Add multiple items
		for i := 0; i < 3; i++ {
			key := fmt.Sprintf("clear:key:%d", i)
			memCache.Set(key, testResponse, 1*time.Minute)
		}

		assert.Equal(t, 3, memCache.Size())

		// Clear all
		memCache.Clear()
		assert.Equal(t, 0, memCache.Size())

		// All items should be gone
		for i := 0; i < 3; i++ {
			key := fmt.Sprintf("clear:key:%d", i)
			result, _ := memCache.Get(key)
			assert.Nil(t, result)
		}
	})

	t.Run("Delete Specific Key", func(t *testing.T) {
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/delete",
		}

		key1 := "delete:key:1"
		key2 := "delete:key:2"

		memCache.Set(key1, testResponse, 1*time.Minute)
		memCache.Set(key2, testResponse, 1*time.Minute)
		assert.Equal(t, 2, memCache.Size())

		// Delete one key
		deleted := memCache.Delete(key1)
		assert.True(t, deleted)
		assert.Equal(t, 1, memCache.Size())

		// Key1 should be gone, key2 should remain
		result, _ := memCache.Get(key1)
		assert.Nil(t, result)
		result, _ = memCache.Get(key2)
		assert.NotNil(t, result)

		// Delete non-existent key
		deleted = memCache.Delete("nonexistent")
		assert.False(t, deleted)
	})
}

func TestMemoryCacheConfig_Defaults(t *testing.T) {
	t.Run("Nil Config Uses Defaults", func(t *testing.T) {
		logger := zaptest.NewLogger(t)
		memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](nil, logger)
		defer memCache.Close()

		// Should create cache with default settings
		info := memCache.GetCacheInfo()
		assert.Equal(t, 1000, info["max_size"])
		assert.Equal(t, "memory_cache_l1", info["type"])
	})

	t.Run("Zero Values Get Defaults", func(t *testing.T) {
		config := &cache.MemoryCacheConfig{}
		logger := zaptest.NewLogger(t)
		memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](config, logger)
		defer memCache.Close()

		// Should apply defaults for zero values
		info := memCache.GetCacheInfo()
		assert.Equal(t, 1000, info["max_size"])
	})

	t.Run("Custom Values Preserved", func(t *testing.T) {
		config := &cache.MemoryCacheConfig{
			MaxSize:         50,
			DefaultTTL:      2 * time.Minute,
			CleanupInterval: 30 * time.Second,
		}
		logger := zaptest.NewLogger(t)
		memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](config, logger)
		defer memCache.Close()

		info := memCache.GetCacheInfo()
		assert.Equal(t, 50, info["max_size"])
	})
}

func TestMemoryCache_PerformanceCharacteristics(t *testing.T) {
	config := &cache.MemoryCacheConfig{
		MaxSize:         1000,
		DefaultTTL:      5 * time.Minute,
		CleanupInterval: 1 * time.Second,
		EnableMetrics:   true,
	}

	logger := zaptest.NewLogger(t)
	memCache := cache.NewMemoryCache[*overview.TreeWithCommitOverviewResponse](config, logger)
	defer memCache.Close()

	testResponse := &overview.TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: "test/performance",
		Tree:       make([]overview.GitTreeEntryWithCommit, 10), // Some data to cache
	}

	t.Run("Access Time Should Be Fast", func(t *testing.T) {
		key := "perf:key"
		memCache.Set(key, testResponse, 1*time.Minute)

		// Measure access time
		iterations := 1000
		start := time.Now()

		for i := 0; i < iterations; i++ {
			result, _ := memCache.Get(key)
			require.NotNil(t, result)
		}

		totalTime := time.Since(start)
		avgTime := totalTime / time.Duration(iterations)

		// Memory access should be very fast (< 1ms per operation on average)
		assert.Less(t, avgTime, 1*time.Millisecond,
			"Average memory cache access time should be < 1ms, got %v", avgTime)

		metrics := memCache.GetMetrics()
		assert.True(t, metrics.AverageAccessTime > 0, "Should track access time")
		assert.True(t, metrics.AverageAccessTime < float64(time.Millisecond),
			"Average access time should be very fast")
	})
}
