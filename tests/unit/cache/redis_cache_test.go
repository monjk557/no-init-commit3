//go:build cache_integration
// +build cache_integration

package cache_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/cache"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestRedisCache_Integration(t *testing.T) {
	// Skip if Redis is not available
	config := &cache.RedisCacheConfig{
		Address:  "localhost:6379",
		DB:       1, // Use test database
		PoolSize: 2,
		Prefix:   "test:cache:",
	}

	logger := zaptest.NewLogger(t)
	redisCache, err := cache.NewRedisCache[*overview.TreeWithCommitOverviewResponse](config, logger)

	if err != nil {
		t.Skip("Redis not available for integration tests:", err)
		return
	}
	defer redisCache.Close()

	ctx := context.Background()

	t.Run("Basic Set and Get Operations", func(t *testing.T) {
		// Test data
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/repo",
			Owner:      "test",
			Name:       "repo",
			Path:       "/src",
			Tree: []overview.GitTreeEntryWithCommit{
				{
					Type: "blob",
					Name: "main.go",
					Path: "/src/main.go",
				},
				{
					Type: "tree",
					Name: "utils",
					Path: "/src/utils",
				},
			},
		}

		key := "test:directory:123"
		ttl := 5 * time.Minute

		// Test Set
		err := redisCache.SetDirectoryListing(ctx, key, testResponse, ttl)
		require.NoError(t, err)

		// Test Get
		retrieved, err := redisCache.GetDirectoryListing(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, retrieved)

		assert.Equal(t, testResponse.Success, retrieved.Success)
		assert.Equal(t, testResponse.Repository, retrieved.Repository)
		assert.Equal(t, testResponse.Owner, retrieved.Owner)
		assert.Equal(t, testResponse.Name, retrieved.Name)
		assert.Equal(t, testResponse.Path, retrieved.Path)
		assert.Len(t, retrieved.Tree, len(testResponse.Tree))
		assert.Equal(t, testResponse.Tree[0].Name, retrieved.Tree[0].Name)
	})

	t.Run("Cache Miss", func(t *testing.T) {
		// Test non-existent key
		result, err := redisCache.GetDirectoryListing(ctx, "nonexistent:key")
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("TTL and Expiration", func(t *testing.T) {
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/ttl",
		}

		key := "test:ttl:key"
		shortTTL := 100 * time.Millisecond

		// Set with short TTL
		err := redisCache.SetDirectoryListing(ctx, key, testResponse, shortTTL)
		require.NoError(t, err)

		// Immediately retrieve (should exist)
		result, err := redisCache.GetDirectoryListing(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, result)

		// Wait for expiration
		time.Sleep(150 * time.Millisecond)

		// Should be expired
		result, err = redisCache.GetDirectoryListing(ctx, key)
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("Delete Operations", func(t *testing.T) {
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/delete",
		}

		key := "test:delete:key"

		// Set value
		err := redisCache.SetDirectoryListing(ctx, key, testResponse, time.Minute)
		require.NoError(t, err)

		// Verify exists
		result, err := redisCache.GetDirectoryListing(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, result)

		// Delete
		err = redisCache.Delete(ctx, key)
		require.NoError(t, err)

		// Should not exist
		result, err = redisCache.GetDirectoryListing(ctx, key)
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("Pattern-based Deletion", func(t *testing.T) {
		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/pattern",
		}

		// Set multiple keys with same pattern
		keys := []string{
			"test:pattern:repo1",
			"test:pattern:repo2",
			"test:pattern:repo3",
			"test:other:repo4", // Different pattern
		}

		for _, key := range keys {
			err := redisCache.SetDirectoryListing(ctx, key, testResponse, time.Minute)
			require.NoError(t, err)
		}

		// Delete by pattern
		err := redisCache.DeleteByPattern(ctx, "test:pattern:*")
		require.NoError(t, err)

		// Check pattern keys are deleted
		for i := 0; i < 3; i++ {
			result, err := redisCache.GetDirectoryListing(ctx, keys[i])
			require.NoError(t, err)
			assert.Nil(t, result, "Key %s should be deleted", keys[i])
		}

		// Check other key still exists
		result, err := redisCache.GetDirectoryListing(ctx, keys[3])
		require.NoError(t, err)
		assert.NotNil(t, result, "Key %s should still exist", keys[3])
	})

	t.Run("Metrics Tracking", func(t *testing.T) {
		// Reset metrics
		redisCache.ResetMetrics()

		testResponse := &overview.TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: "test/metrics",
		}

		key := "test:metrics:key"

		// Should record miss
		result, err := redisCache.GetDirectoryListing(ctx, key)
		require.NoError(t, err)
		assert.Nil(t, result)

		metrics := redisCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.Misses)
		assert.Equal(t, int64(0), metrics.Hits)

		// Set value
		err = redisCache.SetDirectoryListing(ctx, key, testResponse, time.Minute)
		require.NoError(t, err)

		metrics = redisCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.SetOperations)

		// Should record hit
		result, err = redisCache.GetDirectoryListing(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, result)

		metrics = redisCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.Hits)
		assert.Equal(t, int64(1), metrics.Misses)

		// Hit ratio should be 0.5 (1 hit out of 2 requests)
		assert.Equal(t, 0.5, redisCache.GetHitRatio())
	})

	t.Run("Connection Health Check", func(t *testing.T) {
		err := redisCache.Ping(ctx)
		assert.NoError(t, err)

		info, err := redisCache.GetInfo(ctx)
		assert.NoError(t, err)
		assert.Contains(t, info, "raw_info")
	})

	t.Run("Error Handling", func(t *testing.T) {
		// Test with nil response
		err := redisCache.SetDirectoryListing(ctx, "test:nil", nil, time.Minute)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be nil")

		// Test with invalid JSON (this is harder to test with the current implementation)
		// The JSON marshaling should always work with valid structs
	})
}

func TestRedisCacheConfig_Validation(t *testing.T) {
	t.Run("Default Values Applied", func(t *testing.T) {
		config := &cache.RedisCacheConfig{
			Address: "localhost:6379",
		}

		logger := zaptest.NewLogger(t)
		_, err := cache.NewRedisCache[*overview.TreeWithCommitOverviewResponse](config, logger)

		// Should set defaults even if creation fails due to no Redis
		assert.Equal(t, 10, config.PoolSize)
		assert.Equal(t, 5*time.Second, config.DialTimeout)
		assert.Equal(t, 3*time.Second, config.ReadTimeout)
		assert.Equal(t, 3*time.Second, config.WriteTimeout)
		assert.Equal(t, 300*time.Second, config.IdleTimeout)
		assert.Equal(t, 3, config.MaxRetries)
		assert.Equal(t, "collabops:cache:", config.Prefix)

		if err != nil {
			// Expected if Redis is not available
			assert.Contains(t, err.Error(), "failed to connect to Redis")
		}
	})

	t.Run("Custom Values Preserved", func(t *testing.T) {
		config := &cache.RedisCacheConfig{
			Address:      "localhost:6379",
			PoolSize:     5,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  1 * time.Second,
			WriteTimeout: 1 * time.Second,
			IdleTimeout:  60 * time.Second,
			MaxRetries:   2,
			Prefix:       "custom:prefix:",
		}

		logger := zaptest.NewLogger(t)
		_, err := cache.NewRedisCache[*overview.TreeWithCommitOverviewResponse](config, logger)

		// Should preserve custom values
		assert.Equal(t, 5, config.PoolSize)
		assert.Equal(t, 2*time.Second, config.DialTimeout)
		assert.Equal(t, 1*time.Second, config.ReadTimeout)
		assert.Equal(t, 1*time.Second, config.WriteTimeout)
		assert.Equal(t, 60*time.Second, config.IdleTimeout)
		assert.Equal(t, 2, config.MaxRetries)
		assert.Equal(t, "custom:prefix:", config.Prefix)

		if err != nil {
			// Expected if Redis is not available
			assert.Contains(t, err.Error(), "failed to connect to Redis")
		}
	})
}

func TestBuildCacheKey(t *testing.T) {
	tests := []struct {
		name     string
		repoID   string
		ref      string
		path     string
		expected string
	}{
		{
			name:     "Basic key",
			repoID:   "owner/repo",
			ref:      "main",
			path:     "/src",
			expected: "dir:owner/repo:main:/src",
		},
		{
			name:     "Long ref truncated",
			repoID:   "owner/repo",
			ref:      "1234567890abcdef",
			path:     "/src",
			expected: "dir:owner/repo:12345678:/src",
		},
		{
			name:     "Empty path normalized",
			repoID:   "owner/repo",
			ref:      "main",
			path:     "",
			expected: "dir:owner/repo:main:/",
		},
		{
			name:     "Root path",
			repoID:   "owner/repo",
			ref:      "develop",
			path:     "/",
			expected: "dir:owner/repo:develop:/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cache.BuildCacheKey(tt.repoID, tt.ref, tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildCommitCacheKey(t *testing.T) {
	tests := []struct {
		name     string
		repoID   string
		ref      string
		path     string
		expected string
	}{
		{
			name:     "Basic commit key",
			repoID:   "owner/repo",
			ref:      "main",
			path:     "/src",
			expected: "commit:owner/repo:main:/src",
		},
		{
			name:     "Long ref truncated",
			repoID:   "owner/repo",
			ref:      "1234567890abcdef",
			path:     "/src/main.go",
			expected: "commit:owner/repo:12345678:/src/main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cache.BuildCommitCacheKey(tt.repoID, tt.ref, tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}
