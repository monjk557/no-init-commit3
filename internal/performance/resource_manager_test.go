package performance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
)

// createTestConfig creates a test configuration
func createTestConfig() *config.Config {
	return &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/Users/atci/Documents/GitHub/collabops-git-executor/test-repos",
		},
	}
}

func TestDefaultResourceConfig(t *testing.T) {
	cfg := DefaultResourceConfig()

	assert.NotNil(t, cfg)
	assert.Equal(t, 100, cfg.MaxRepositories)
	assert.Equal(t, 30*time.Minute, cfg.RepositoryTTL)
	assert.Equal(t, 5*time.Minute, cfg.CleanupInterval)
	assert.Equal(t, int64(1024*1024*1024), cfg.MaxMemoryUsage) // 1GB
	assert.Equal(t, 0.8, cfg.GCThreshold)
	assert.Equal(t, 30*time.Second, cfg.MemoryCheckInterval)
	assert.Equal(t, 1000, cfg.MaxConnections)
	assert.Equal(t, 30*time.Second, cfg.ConnectionTimeout)
	assert.Equal(t, 5*time.Minute, cfg.IdleTimeout)
	assert.Equal(t, 10*time.Minute, cfg.MaxRequestDuration)
	assert.Equal(t, 10000, cfg.RequestQueueSize)
	assert.Equal(t, 500, cfg.ConcurrentRequests)
	assert.True(t, cfg.EnableRepositoryCache)
	assert.True(t, cfg.EnableObjectCache)
	assert.Equal(t, 6, cfg.CacheCompressionLevel)
	assert.Equal(t, 64*1024, cfg.StreamingBufferSize)
	assert.Equal(t, int64(100*1024*1024), cfg.MaxStreamingFile) // 100MB
	assert.True(t, cfg.EnableStreaming)
}

func TestNewResourceManager(t *testing.T) {
	cfg := createTestConfig()

	rm, err := NewResourceManager(cfg)

	require.NoError(t, err)
	assert.NotNil(t, rm)
	assert.NotNil(t, rm.config)
	assert.NotNil(t, rm.logger)
	assert.NotNil(t, rm.repositoryPool)
	assert.NotNil(t, rm.memoryMonitor)
	assert.NotNil(t, rm.connectionPool)
	assert.NotNil(t, rm.requestTracker)
	assert.NotNil(t, rm.cacheManager)
	assert.NotNil(t, rm.metricsCollector)

	// Cleanup
	rm.Stop()
}

func TestNewResourceManager_NilConfig(t *testing.T) {
	rm, err := NewResourceManager(nil)

	assert.Error(t, err)
	assert.Nil(t, rm)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

func TestRepositoryPool_NewRepositoryPool(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	pool := NewRepositoryPool(cfg, nil, testLogger)

	assert.NotNil(t, pool)
	assert.Equal(t, cfg, pool.config)
	assert.NotNil(t, pool.repos)
	assert.Empty(t, pool.repos)
}

func TestRepositoryPool_EstimateMemoryUsage(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	pool := NewRepositoryPool(cfg, nil, testLogger)

	// Test with nil repository (simplified test)
	usage := pool.estimateRepositoryMemoryUsage(nil)
	assert.Equal(t, int64(1024*1024), usage) // 1MB base estimate
}

func TestRepositoryPool_EvictLeastRecentlyUsed(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	pool := NewRepositoryPool(cfg, nil, testLogger)

	// Create mock entries
	now := time.Now()
	pool.repos["repo1"] = &RepositoryCacheEntry{
		LastAccess: now.Add(-10 * time.Minute),
	}
	pool.repos["repo2"] = &RepositoryCacheEntry{
		LastAccess: now.Add(-5 * time.Minute),
	}
	pool.repos["repo3"] = &RepositoryCacheEntry{
		LastAccess: now.Add(-15 * time.Minute), // Oldest
	}

	// Should evict repo3 (oldest)
	pool.evictLeastRecentlyUsed()

	assert.Len(t, pool.repos, 2)
	assert.Contains(t, pool.repos, "repo1")
	assert.Contains(t, pool.repos, "repo2")
	assert.NotContains(t, pool.repos, "repo3")
}

func TestRepositoryPool_Cleanup(t *testing.T) {
	cfg := DefaultResourceConfig()
	cfg.RepositoryTTL = 1 * time.Minute // Short TTL for testing
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	pool := NewRepositoryPool(cfg, nil, testLogger)

	// Create mock entries
	now := time.Now()
	pool.repos["fresh"] = &RepositoryCacheEntry{
		LastAccess: now.Add(-30 * time.Second), // Fresh
	}
	pool.repos["expired"] = &RepositoryCacheEntry{
		LastAccess: now.Add(-2 * time.Minute), // Expired
	}

	pool.Cleanup()

	assert.Len(t, pool.repos, 1)
	assert.Contains(t, pool.repos, "fresh")
	assert.NotContains(t, pool.repos, "expired")
}

func TestMemoryMonitor_NewMemoryMonitor(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	monitor := NewMemoryMonitor(cfg, testLogger)

	assert.NotNil(t, monitor)
	assert.Equal(t, cfg, monitor.config)
	assert.NotNil(t, monitor.stats)
	assert.NotNil(t, monitor.stopChan)
}

func TestMemoryMonitor_UpdateStats(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	monitor := NewMemoryMonitor(cfg, testLogger)

	monitor.updateStats()

	stats := monitor.GetStats()
	assert.NotNil(t, stats)
	assert.True(t, stats.Alloc > 0)
	assert.True(t, stats.TotalAlloc > 0)
	assert.True(t, stats.Sys > 0)
	assert.True(t, stats.HeapAlloc > 0)
	assert.True(t, stats.UsagePercent >= 0)
	assert.False(t, stats.LastUpdated.IsZero())
}

func TestMemoryMonitor_GetStats(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	monitor := NewMemoryMonitor(cfg, testLogger)

	monitor.updateStats()
	stats1 := monitor.GetStats()
	stats2 := monitor.GetStats()

	// Should return copies, not the same pointer
	assert.NotSame(t, stats1, stats2)
	assert.Equal(t, stats1.Alloc, stats2.Alloc)
}

func TestConnectionPool_NewConnectionPool(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	pool := NewConnectionPool(cfg, testLogger)

	assert.NotNil(t, pool)
	assert.Equal(t, cfg, pool.config)
	assert.Equal(t, 0, pool.activeConns)
	assert.Equal(t, int64(0), pool.totalConns)
	assert.Equal(t, int64(0), pool.rejectedConns)
}

func TestConnectionPool_AcquireAndReleaseConnection(t *testing.T) {
	cfg := DefaultResourceConfig()
	cfg.MaxConnections = 2 // Small limit for testing
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	pool := NewConnectionPool(cfg, testLogger)
	ctx := context.Background()

	// First connection should succeed
	err := pool.AcquireConnection(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, pool.activeConns)
	assert.Equal(t, int64(1), pool.totalConns)

	// Second connection should succeed
	err = pool.AcquireConnection(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, pool.activeConns)
	assert.Equal(t, int64(2), pool.totalConns)

	// Third connection should fail (limit exceeded)
	err = pool.AcquireConnection(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection limit exceeded")
	assert.Equal(t, 2, pool.activeConns)
	assert.Equal(t, int64(2), pool.totalConns)
	assert.Equal(t, int64(1), pool.rejectedConns)

	// Release a connection
	pool.ReleaseConnection()
	assert.Equal(t, 1, pool.activeConns)

	// Should be able to acquire again
	err = pool.AcquireConnection(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, pool.activeConns)
	assert.Equal(t, int64(3), pool.totalConns)
}

func TestConnectionPool_GetStats(t *testing.T) {
	cfg := DefaultResourceConfig()
	cfg.MaxConnections = 10
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	pool := NewConnectionPool(cfg, testLogger)
	ctx := context.Background()

	// Acquire some connections
	_ = pool.AcquireConnection(ctx)
	_ = pool.AcquireConnection(ctx)

	stats := pool.GetStats()

	assert.Equal(t, 2, stats["active_connections"])
	assert.Equal(t, 10, stats["max_connections"])
	assert.Equal(t, int64(2), stats["total_connections"])
	assert.Equal(t, int64(0), stats["rejected_connections"])
	assert.Equal(t, 20.0, stats["utilization_percent"])
}

func TestRequestTracker_NewRequestTracker(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	tracker := NewRequestTracker(cfg, testLogger)

	assert.NotNil(t, tracker)
	assert.Equal(t, cfg, tracker.config)
	assert.NotNil(t, tracker.activeRequests)
	assert.NotNil(t, tracker.requestQueue)
	assert.Equal(t, int64(0), tracker.totalRequests)
	assert.Equal(t, int64(0), tracker.completedReqs)
	assert.Equal(t, int64(0), tracker.timeoutReqs)
}

func TestCacheManager_NewCacheManager(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	cache := NewCacheManager(cfg, testLogger)

	assert.NotNil(t, cache)
	assert.Equal(t, cfg, cache.config)
	assert.NotNil(t, cache.objectCache)
	assert.Empty(t, cache.objectCache)
}

func TestMetricsCollector_NewMetricsCollector(t *testing.T) {
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	collector := NewMetricsCollector(testLogger)

	assert.NotNil(t, collector)
	assert.NotNil(t, collector.metrics)
	assert.Empty(t, collector.metrics)
}

func TestStreamingManager_NewStreamingManager(t *testing.T) {
	cfg := DefaultResourceConfig()
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	manager := NewStreamingManager(cfg, testLogger)

	assert.NotNil(t, manager)
	assert.Equal(t, cfg, manager.config)
}

func TestStreamingManager_ShouldStream(t *testing.T) {
	cfg := DefaultResourceConfig()
	cfg.EnableStreaming = true
	cfg.MaxStreamingFile = 1024 * 1024 // 1MB
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	manager := NewStreamingManager(cfg, testLogger)

	tests := []struct {
		name     string
		fileSize int64
		expected bool
	}{
		{
			name:     "Small file - no streaming",
			fileSize: 512 * 1024, // 512KB
			expected: false,
		},
		{
			name:     "Large file - should stream",
			fileSize: 2 * 1024 * 1024, // 2MB
			expected: true,
		},
		{
			name:     "Exact threshold - no streaming",
			fileSize: 1024 * 1024, // 1MB
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.ShouldStream(tt.fileSize)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestStreamingManager_ShouldStream_Disabled(t *testing.T) {
	cfg := DefaultResourceConfig()
	cfg.EnableStreaming = false
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	manager := NewStreamingManager(cfg, testLogger)

	// Even large files should not stream when disabled
	result := manager.ShouldStream(10 * 1024 * 1024) // 10MB
	assert.False(t, result)
}

func TestResourceManager_GetResourceStats(t *testing.T) {
	cfg := createTestConfig()
	rm, err := NewResourceManager(cfg)
	require.NoError(t, err)
	defer rm.Stop()

	// Let it run briefly to collect some stats
	time.Sleep(100 * time.Millisecond)

	stats := rm.GetResourceStats()

	assert.NotNil(t, stats)
	assert.Contains(t, stats, "memory")
	assert.Contains(t, stats, "connections")
	assert.Contains(t, stats, "repository_pool")

	memoryStats := stats["memory"]
	assert.NotNil(t, memoryStats)

	connectionStats := stats["connections"]
	assert.NotNil(t, connectionStats)

	repoPoolStats := stats["repository_pool"].(map[string]interface{})
	assert.Equal(t, 0, repoPoolStats["size"])
	assert.Equal(t, 100, repoPoolStats["max"])
}

func TestMemoryStats_Structure(t *testing.T) {
	stats := &MemoryStats{
		Alloc:        1024,
		TotalAlloc:   2048,
		Sys:          4096,
		NumGC:        5,
		LastGC:       time.Now(),
		GCPauseTotal: 1000000,
		HeapAlloc:    512,
		HeapSys:      1024,
		HeapInuse:    256,
		StackInuse:   128,
		UsagePercent: 25.5,
		LastUpdated:  time.Now(),
	}

	assert.Equal(t, uint64(1024), stats.Alloc)
	assert.Equal(t, uint64(2048), stats.TotalAlloc)
	assert.Equal(t, uint64(4096), stats.Sys)
	assert.Equal(t, uint32(5), stats.NumGC)
	assert.Equal(t, uint64(1000000), stats.GCPauseTotal)
	assert.Equal(t, uint64(512), stats.HeapAlloc)
	assert.Equal(t, uint64(1024), stats.HeapSys)
	assert.Equal(t, uint64(256), stats.HeapInuse)
	assert.Equal(t, uint64(128), stats.StackInuse)
	assert.Equal(t, 25.5, stats.UsagePercent)
	assert.False(t, stats.LastGC.IsZero())
	assert.False(t, stats.LastUpdated.IsZero())
}

func TestRequestInfo_Structure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	info := &RequestInfo{
		ID:        "test-request-123",
		StartTime: time.Now(),
		Context:   ctx,
		Cancel:    cancel,
		Type:      "merge_preview",
		Resource:  "owner/repo",
	}

	assert.Equal(t, "test-request-123", info.ID)
	assert.Equal(t, "merge_preview", info.Type)
	assert.Equal(t, "owner/repo", info.Resource)
	assert.NotNil(t, info.Context)
	assert.NotNil(t, info.Cancel)
	assert.False(t, info.StartTime.IsZero())
}

func TestCacheEntry_Structure(t *testing.T) {
	now := time.Now()
	entry := &CacheEntry{
		Data:        "test data",
		Size:        1024,
		CreatedAt:   now,
		LastAccess:  now,
		AccessCount: 5,
	}

	assert.Equal(t, "test data", entry.Data)
	assert.Equal(t, int64(1024), entry.Size)
	assert.Equal(t, now, entry.CreatedAt)
	assert.Equal(t, now, entry.LastAccess)
	assert.Equal(t, int64(5), entry.AccessCount)
}

func TestMetricValue_Structure(t *testing.T) {
	now := time.Now()
	metric := &MetricValue{
		Value:     100.5,
		Count:     10,
		Sum:       1005.0,
		Min:       50.0,
		Max:       150.0,
		LastValue: 100.5,
		UpdatedAt: now,
	}

	assert.Equal(t, 100.5, metric.Value)
	assert.Equal(t, int64(10), metric.Count)
	assert.Equal(t, 1005.0, metric.Sum)
	assert.Equal(t, 50.0, metric.Min)
	assert.Equal(t, 150.0, metric.Max)
	assert.Equal(t, 100.5, metric.LastValue)
	assert.Equal(t, now, metric.UpdatedAt)
}

// Integration test for resource management lifecycle
func TestResourceManager_Lifecycle(t *testing.T) {
	cfg := createTestConfig()
	rm, err := NewResourceManager(cfg)
	require.NoError(t, err)

	// Verify manager is running
	assert.NotNil(t, rm)

	// Get initial stats
	stats := rm.GetResourceStats()
	assert.NotNil(t, stats)

	// Test connection pool
	ctx := context.Background()
	err = rm.connectionPool.AcquireConnection(ctx)
	assert.NoError(t, err)

	connectionStats := rm.connectionPool.GetStats()
	assert.Equal(t, 1, connectionStats["active_connections"])

	rm.connectionPool.ReleaseConnection()
	connectionStats = rm.connectionPool.GetStats()
	assert.Equal(t, 0, connectionStats["active_connections"])

	// Stop manager gracefully
	rm.Stop()
}

// Performance test for memory monitoring
func TestMemoryMonitor_Performance(t *testing.T) {
	cfg := DefaultResourceConfig()
	cfg.MemoryCheckInterval = 10 * time.Millisecond // Fast for testing
	logger := createTestConfig().Logging
	testLogger, _ := logging.NewLogger(logger)
	monitor := NewMemoryMonitor(cfg, testLogger)

	// Start monitoring
	monitor.Start()
	defer monitor.Stop()

	// Let it run for a bit
	time.Sleep(50 * time.Millisecond)

	// Verify stats are being updated
	stats := monitor.GetStats()
	assert.False(t, stats.LastUpdated.IsZero())
	assert.True(t, stats.Alloc > 0)

	// Stop and verify it stops cleanly
	monitor.Stop()
	time.Sleep(20 * time.Millisecond) // Allow cleanup
}
