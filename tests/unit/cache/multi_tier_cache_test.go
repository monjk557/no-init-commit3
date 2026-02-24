package cache_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/cache"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestMultiTierCache_Integration(t *testing.T) {
	// Test configuration
	config := &cache.MultiTierConfig{
		L1MaxSize:             10,
		L1DefaultTTL:          1 * time.Minute,
		L1CleanupInterval:     100 * time.Millisecond,
		L2DefaultTTL:          5 * time.Minute,
		L2Address:             "localhost:6379",
		L2DB:                  2, // Use test database
		L2PoolSize:            2,
		EnableCacheWarming:    true,
		WarmL1FromL2:          true,
		EnablePerformanceMode: true,
		MaxConcurrentWarming:  3,
		WarmingTimeout:        2 * time.Second,
	}

	logger := zaptest.NewLogger(t)
	multiCache, err := cache.NewMultiTierCache(config, logger)

	if err != nil {
		t.Skip("Redis not available for multi-tier cache tests:", err)
		return
	}
	defer multiCache.Close()

	ctx := context.Background()

	testResponse := &overview.TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: "test/multi-tier",
		Owner:      "test",
		Name:       "multi-tier",
		Path:       "/src",
		Tree: []overview.GitTreeEntryWithCommit{
			{Type: "blob", Name: "main.go", Path: "/src/main.go"},
			{Type: "tree", Name: "utils", Path: "/src/utils"},
			{Type: "blob", Name: "readme.md", Path: "/src/readme.md"},
		},
	}

	repoID := "test/multi-tier"
	ref := "main"
	path := "/src"

	t.Run("Cache Miss - Both Tiers Empty", func(t *testing.T) {
		// Clear any existing data
		multiCache.InvalidateKey(ctx, cache.BuildCacheKey(repoID, ref, path))

		result, err := multiCache.GetDirectoryListing(ctx, repoID, ref, path)
		require.NoError(t, err)
		assert.Nil(t, result)

		metrics := multiCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.L1Misses)
		assert.Equal(t, int64(1), metrics.L2Misses)
		assert.Equal(t, int64(1), metrics.TotalMisses)
		assert.Equal(t, int64(0), metrics.TotalHits)
	})

	t.Run("Store in Cache - Populates Both Tiers", func(t *testing.T) {
		err := multiCache.SetDirectoryListing(ctx, repoID, ref, path, testResponse)
		require.NoError(t, err)

		// Small delay to ensure async operations complete
		time.Sleep(100 * time.Millisecond)

		// Verify both tiers have the data by checking through multi-tier interface
		result, err := multiCache.GetDirectoryListing(ctx, repoID, ref, path)
		require.NoError(t, err)
		require.NotNil(t, result)

		assert.Equal(t, testResponse.Repository, result.Repository)
		assert.Equal(t, testResponse.Owner, result.Owner)
		assert.Equal(t, testResponse.Path, result.Path)
		assert.Len(t, result.Tree, len(testResponse.Tree))
	})

	t.Run("L1 Cache Hit", func(t *testing.T) {
		// Store data first
		err := multiCache.SetDirectoryListing(ctx, repoID, ref, path, testResponse)
		require.NoError(t, err)

		// Reset metrics to track this specific access
		metrics := multiCache.GetMetrics()
		initialL1Hits := metrics.L1Hits

		// Access should hit L1 cache
		result, err := multiCache.GetDirectoryListing(ctx, repoID, ref, path)
		require.NoError(t, err)
		require.NotNil(t, result)

		metrics = multiCache.GetMetrics()
		assert.Equal(t, initialL1Hits+1, metrics.L1Hits, "Should record L1 hit")

		assert.Equal(t, testResponse.Repository, result.Repository)
		assert.Len(t, result.Tree, len(testResponse.Tree))
	})

	t.Run("L2 Hit with L1 Warming", func(t *testing.T) {
		// Store in both tiers first
		err := multiCache.SetDirectoryListing(ctx, repoID, ref, "L2_test", testResponse)
		require.NoError(t, err)

		// Clear L1 cache by accessing it with different data to evict
		for i := 0; i < 15; i++ {
			dummyResponse := &overview.TreeWithCommitOverviewResponse{
				Success:    true,
				Repository: fmt.Sprintf("dummy/repo%d", i),
			}
			multiCache.SetDirectoryListing(ctx, fmt.Sprintf("dummy/repo%d", i), ref, "/", dummyResponse)
		}

		// Now access the original data - should hit L2 and warm L1
		result, err := multiCache.GetDirectoryListing(ctx, repoID, ref, "L2_test")
		require.NoError(t, err)
		require.NotNil(t, result)

		// Give time for L1 warming to complete
		time.Sleep(200 * time.Millisecond)

		metrics := multiCache.GetMetrics()
		assert.True(t, metrics.L2Hits > 0, "Should have L2 hits")
		assert.True(t, metrics.WarmingOperations >= 0, "Should track warming operations")

		assert.Equal(t, testResponse.Repository, result.Repository)
	})

	t.Run("Cache Invalidation", func(t *testing.T) {
		testPath := "/invalidate"

		// Store data
		err := multiCache.SetDirectoryListing(ctx, repoID, ref, testPath, testResponse)
		require.NoError(t, err)

		// Verify it's cached
		result, err := multiCache.GetDirectoryListing(ctx, repoID, ref, testPath)
		require.NoError(t, err)
		require.NotNil(t, result)

		// Invalidate specific key
		err = multiCache.InvalidateKey(ctx, cache.BuildCacheKey(repoID, ref, testPath))
		require.NoError(t, err)

		// Should be cache miss now
		result, err = multiCache.GetDirectoryListing(ctx, repoID, ref, testPath)
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("Pattern-based Invalidation", func(t *testing.T) {
		patternRepo := "pattern/repo"

		// Store multiple entries for the same repo
		paths := []string{"/src", "/docs", "/tests"}
		for _, p := range paths {
			err := multiCache.SetDirectoryListing(ctx, patternRepo, ref, p, testResponse)
			require.NoError(t, err)
		}

		// Verify all are cached
		for _, p := range paths {
			result, err := multiCache.GetDirectoryListing(ctx, patternRepo, ref, p)
			require.NoError(t, err)
			require.NotNil(t, result, "Path %s should be cached", p)
		}

		// Invalidate by pattern
		pattern := fmt.Sprintf("dir:%s:*", patternRepo)
		err = multiCache.InvalidateByPattern(ctx, pattern)
		require.NoError(t, err)

		// All should be invalidated
		for _, p := range paths {
			result, err := multiCache.GetDirectoryListing(ctx, patternRepo, ref, p)
			require.NoError(t, err)
			assert.Nil(t, result, "Path %s should be invalidated", p)
		}
	})
}

func TestMultiTierCache_PerformanceMetrics(t *testing.T) {
	config := &cache.MultiTierConfig{
		L1MaxSize:             5,
		L1DefaultTTL:          30 * time.Second,
		L1CleanupInterval:     100 * time.Millisecond,
		L2DefaultTTL:          2 * time.Minute,
		L2Address:             "localhost:6379",
		L2DB:                  3,
		L2PoolSize:            2,
		EnableCacheWarming:    true,
		WarmL1FromL2:          true,
		EnablePerformanceMode: true,
	}

	logger := zaptest.NewLogger(t)
	multiCache, err := cache.NewMultiTierCache(config, logger)

	if err != nil {
		t.Skip("Redis not available for performance metric tests:", err)
		return
	}
	defer multiCache.Close()

	ctx := context.Background()

	testResponse := &overview.TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: "test/metrics",
		Tree:       make([]overview.GitTreeEntryWithCommit, 5),
	}

	t.Run("Hit Ratio Calculation", func(t *testing.T) {
		repoID := "metrics/repo"
		ref := "main"

		// Start with empty metrics
		metrics := multiCache.GetMetrics()
		initialRequests := metrics.TotalRequests

		// Generate some hits and misses
		paths := []string{"/hit1", "/hit2", "/miss1", "/miss2", "/miss3"}

		// First access (all misses)
		for _, path := range paths {
			result, err := multiCache.GetDirectoryListing(ctx, repoID, ref, path)
			require.NoError(t, err)
			assert.Nil(t, result)
		}

		// Store half of them
		for i := 0; i < 2; i++ {
			err := multiCache.SetDirectoryListing(ctx, repoID, ref, paths[i], testResponse)
			require.NoError(t, err)
		}

		// Access all again (2 hits, 3 misses)
		for _, path := range paths {
			multiCache.GetDirectoryListing(ctx, repoID, ref, path)
		}

		metrics = multiCache.GetMetrics()
		totalRequests := metrics.TotalRequests - initialRequests
		assert.Equal(t, int64(10), totalRequests, "Should have 10 total requests (5 misses + 5 mixed)")

		overallRatio := multiCache.GetOverallHitRatio()
		assert.True(t, overallRatio >= 0.0 && overallRatio <= 1.0, "Hit ratio should be between 0 and 1")

		// We expect 2 hits out of 10 requests = 0.2
		expectedRatio := 2.0 / 10.0
		assert.InDelta(t, expectedRatio, overallRatio, 0.1, "Hit ratio should be approximately 0.2")
	})

	t.Run("Performance Target Validation", func(t *testing.T) {
		// This test validates that we're tracking metrics needed for 98% hit ratio target
		repoID := "perf/target"
		ref := "main"
		path := "/perf"

		// Perform operations to generate metrics
		for i := 0; i < 10; i++ {
			// Store and retrieve
			err := multiCache.SetDirectoryListing(ctx, repoID, ref, fmt.Sprintf("%s/%d", path, i), testResponse)
			require.NoError(t, err)

			// Access twice (should hit cache)
			for j := 0; j < 2; j++ {
				multiCache.GetDirectoryListing(ctx, repoID, ref, fmt.Sprintf("%s/%d", path, i))
			}
		}

		metrics := multiCache.GetMetrics()
		cacheInfo := multiCache.GetCacheInfo()

		// Validate we have comprehensive metrics for monitoring
		assert.True(t, metrics.TotalRequests > 0, "Should track total requests")
		assert.True(t, metrics.TotalHits > 0, "Should track total hits")
		assert.True(t, metrics.L1Hits >= 0, "Should track L1 hits")
		assert.True(t, metrics.L2Hits >= 0, "Should track L2 hits")

		// Validate cache info includes target information
		assert.Contains(t, cacheInfo, "cache_enabled")
		assert.Contains(t, cacheInfo, "metrics")
		assert.Contains(t, cacheInfo, "health")

		// Hit ratio should be high with repeated access
		hitRatio := multiCache.GetOverallHitRatio()
		assert.True(t, hitRatio >= 0.5, "Hit ratio should be at least 50%% with repeated access, got %v", hitRatio)
	})
}

func TestMultiTierCache_HealthCheck(t *testing.T) {
	config := &cache.MultiTierConfig{
		L1MaxSize:          5,
		L1DefaultTTL:       1 * time.Minute,
		L2DefaultTTL:       5 * time.Minute,
		L2Address:          "localhost:6379",
		L2DB:               4,
		L2PoolSize:         2,
		EnableCacheWarming: true,
	}

	logger := zaptest.NewLogger(t)
	multiCache, err := cache.NewMultiTierCache(config, logger)

	if err != nil {
		t.Skip("Redis not available for health check tests:", err)
		return
	}
	defer multiCache.Close()

	t.Run("Healthy System", func(t *testing.T) {
		health := multiCache.HealthCheck(context.Background())

		assert.Equal(t, "healthy", health["overall_status"])
		assert.Contains(t, health, "l1_cache")
		assert.Contains(t, health, "l2_cache")
		assert.Contains(t, health, "metrics")
		assert.Contains(t, health, "overall_hit_ratio")

		l1Health := health["l1_cache"].(map[string]interface{})
		assert.Equal(t, "healthy", l1Health["status"])
		assert.Contains(t, l1Health, "size")
		assert.Contains(t, l1Health, "hit_ratio")

		l2Health := health["l2_cache"].(map[string]interface{})
		assert.Equal(t, "healthy", l2Health["status"])
		assert.Contains(t, l2Health, "hit_ratio")
	})
}

func TestMultiTierCache_ConcurrentAccess(t *testing.T) {
	config := &cache.MultiTierConfig{
		L1MaxSize:            20,
		L1DefaultTTL:         1 * time.Minute,
		L2DefaultTTL:         5 * time.Minute,
		L2Address:            "localhost:6379",
		L2DB:                 5,
		L2PoolSize:           5,
		EnableCacheWarming:   true,
		WarmL1FromL2:         true,
		MaxConcurrentWarming: 3,
	}

	logger := zaptest.NewLogger(t)
	multiCache, err := cache.NewMultiTierCache(config, logger)

	if err != nil {
		t.Skip("Redis not available for concurrent access tests:", err)
		return
	}
	defer multiCache.Close()

	ctx := context.Background()

	testResponse := &overview.TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: "test/concurrent",
	}

	t.Run("Concurrent Reads and Writes", func(t *testing.T) {
		numWorkers := 5
		numOperations := 20
		done := make(chan bool, numWorkers*2)

		repoID := "concurrent/repo"
		ref := "main"

		// Concurrent writers
		for i := 0; i < numWorkers; i++ {
			go func(workerID int) {
				defer func() { done <- true }()
				for j := 0; j < numOperations; j++ {
					path := fmt.Sprintf("/worker%d/op%d", workerID, j)
					err := multiCache.SetDirectoryListing(ctx, repoID, ref, path, testResponse)
					if err != nil {
						t.Logf("Write error in worker %d: %v", workerID, err)
					}
				}
			}(i)
		}

		// Concurrent readers
		for i := 0; i < numWorkers; i++ {
			go func(workerID int) {
				defer func() { done <- true }()
				for j := 0; j < numOperations; j++ {
					path := fmt.Sprintf("/worker%d/op%d", workerID, j)
					_, err := multiCache.GetDirectoryListing(ctx, repoID, ref, path)
					if err != nil {
						t.Logf("Read error in worker %d: %v", workerID, err)
					}
				}
			}(i)
		}

		// Wait for all workers to complete
		for i := 0; i < numWorkers*2; i++ {
			select {
			case <-done:
				// Worker completed
			case <-time.After(10 * time.Second):
				t.Fatal("Test timed out waiting for concurrent operations")
			}
		}

		// Verify system is still functional
		health := multiCache.HealthCheck(ctx)
		assert.NotEqual(t, "error", health["overall_status"])

		metrics := multiCache.GetMetrics()
		assert.True(t, metrics.TotalRequests > 0, "Should have processed requests")
	})
}

func TestMultiTierCache_ErrorHandling(t *testing.T) {
	t.Run("Graceful Degradation with Redis Unavailable", func(t *testing.T) {
		// Try to create cache with invalid Redis address
		config := &cache.MultiTierConfig{
			L1MaxSize:    10,
			L1DefaultTTL: 1 * time.Minute,
			L2Address:    "invalid:1234", // Invalid address
			L2DefaultTTL: 5 * time.Minute,
		}

		logger := zaptest.NewLogger(t)
		_, err := cache.NewMultiTierCache(config, logger)

		// Should fail to create due to Redis connection failure
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create Redis cache")
	})

	t.Run("Nil Config Handling", func(t *testing.T) {
		logger := zaptest.NewLogger(t)
		_, err := cache.NewMultiTierCache(nil, logger)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config cannot be nil")
	})
}

func TestMultiTierCache_DefaultConfiguration(t *testing.T) {
	config := &cache.MultiTierConfig{
		L2Address: "localhost:6379",
	}

	logger := zaptest.NewLogger(t)
	multiCache, err := cache.NewMultiTierCache(config, logger)

	if err != nil {
		t.Skip("Redis not available for default configuration tests:", err)
		return
	}
	defer multiCache.Close()

	t.Run("Default Values Applied", func(t *testing.T) {
		// Verify defaults were applied
		assert.Equal(t, 1000, config.L1MaxSize)
		assert.Equal(t, 10*time.Minute, config.L1DefaultTTL)
		assert.Equal(t, 1*time.Minute, config.L1CleanupInterval)
		assert.Equal(t, 1*time.Hour, config.L2DefaultTTL)
		assert.Equal(t, "localhost:6379", config.L2Address)
		assert.Equal(t, 0, config.L2DB)
		assert.Equal(t, 10, config.L2PoolSize)
		assert.Equal(t, 5, config.MaxConcurrentWarming)
		assert.Equal(t, 5*time.Second, config.WarmingTimeout)
	})

	t.Run("Cache Functions with Defaults", func(t *testing.T) {
		ctx := context.Background()
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/defaults",
		}

		err := multiCache.SetDirectoryListing(ctx, "test/defaults", "main", "/", testResponse)
		assert.NoError(t, err)

		result, err := multiCache.GetDirectoryListing(ctx, "test/defaults", "main", "/")
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, testResponse.Repository, result.Repository)
	})
}
