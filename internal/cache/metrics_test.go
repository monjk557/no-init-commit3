package cache

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPrometheusMetrics(t *testing.T) {
	metrics := NewPrometheusMetrics()

	assert.NotNil(t, metrics)
	assert.NotNil(t, metrics.CacheHitsTotal)
	assert.NotNil(t, metrics.CacheMissesTotal)
	assert.NotNil(t, metrics.CacheEvictionsTotal)
	assert.NotNil(t, metrics.CacheEntriesCurrent)
	assert.NotNil(t, metrics.CacheMemoryBytes)
}

func TestNewPrometheusMetricsWithRegistry(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPrometheusMetricsWithRegistry(registry)

	assert.NotNil(t, metrics)

	// Test registration
	err := metrics.RegisterMetrics(registry)
	assert.NoError(t, err)

	// Test double registration should fail
	err = metrics.RegisterMetrics(registry)
	assert.Error(t, err)
}

func TestPrometheusMetrics_RecordOperations(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPrometheusMetricsWithRegistry(registry)
	err := metrics.RegisterMetrics(registry)
	require.NoError(t, err)

	// Test recording hits
	metrics.RecordHit()
	metrics.RecordHit()
	hitValue := getCounterValue(metrics.CacheHitsTotal)
	assert.Equal(t, 2.0, hitValue)

	// Test recording misses
	metrics.RecordMiss()
	missValue := getCounterValue(metrics.CacheMissesTotal)
	assert.Equal(t, 1.0, missValue)

	// Test recording evictions
	metrics.RecordEviction()
	evictionValue := getCounterValue(metrics.CacheEvictionsTotal)
	assert.Equal(t, 1.0, evictionValue)

	// Test recording set operations
	metrics.RecordSet()
	setOpsValue := getCounterVecValue(metrics.CacheOperationsTotal, "set")
	assert.Equal(t, 1.0, setOpsValue)

	// Test recording cleanup operations
	metrics.RecordCleanup()
	cleanupOpsValue := getCounterVecValue(metrics.CacheOperationsTotal, "cleanup")
	assert.Equal(t, 1.0, cleanupOpsValue)
}

func TestPrometheusMetrics_GaugeMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPrometheusMetricsWithRegistry(registry)
	err := metrics.RegisterMetrics(registry)
	require.NoError(t, err)

	// Test updating entries
	metrics.UpdateEntries(5)
	entriesValue := getGaugeValue(metrics.CacheEntriesCurrent)
	assert.Equal(t, 5.0, entriesValue)

	// Test updating memory usage
	metrics.UpdateMemoryUsage(1024)
	memoryValue := getGaugeValue(metrics.CacheMemoryBytes)
	assert.Equal(t, 1024.0, memoryValue)
}

func TestPrometheusMetrics_AccessDuration(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPrometheusMetricsWithRegistry(registry)
	err := metrics.RegisterMetrics(registry)
	require.NoError(t, err)

	// Record access durations
	metrics.RecordAccessDuration("get", "hit", 0.001)
	metrics.RecordAccessDuration("get", "miss", 0.002)
	metrics.RecordAccessDuration("set", "success", 0.003)

	// Verify histogram has observations (exact values are harder to test)
	histogramValue := getHistogramCount(metrics.CacheAccessDuration, "get", "hit")
	assert.Equal(t, uint64(1), histogramValue)
}

func TestPrometheusMetrics_GetHitRate(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPrometheusMetricsWithRegistry(registry)
	err := metrics.RegisterMetrics(registry)
	require.NoError(t, err)

	// No operations yet
	hitRate := metrics.GetHitRate()
	assert.Equal(t, 0.0, hitRate)

	// Record some hits and misses
	metrics.RecordHit()
	metrics.RecordHit()
	metrics.RecordMiss()

	hitRate = metrics.GetHitRate()
	assert.InDelta(t, 0.666, hitRate, 0.01) // 2/3 = 0.666...
}

func TestLRUCache_PrometheusMetricsIntegration(t *testing.T) {
	// Create cache
	config := NewCacheConfig()
	config.MaxRepositories = 3
	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Enable Prometheus metrics with custom registry for testing
	registry := prometheus.NewRegistry()
	err = cache.EnablePrometheusMetricsWithRegistry(registry)
	require.NoError(t, err)

	// Test set operation metrics
	repoPath1, gitRepo1 := createTestRepository(t)
	err = cache.Set(repoPath1, gitRepo1)
	require.NoError(t, err)

	// Verify metrics
	setOpsValue := getCounterVecValue(cache.prometheusMetrics.CacheOperationsTotal, "set")
	assert.Equal(t, 1.0, setOpsValue)

	entriesValue := getGaugeValue(cache.prometheusMetrics.CacheEntriesCurrent)
	assert.Equal(t, 1.0, entriesValue)

	memoryValue := getGaugeValue(cache.prometheusMetrics.CacheMemoryBytes)
	assert.Greater(t, memoryValue, 0.0) // Should have some memory usage

	// Test get operation metrics (hit)
	_, found := cache.Get(repoPath1)
	assert.True(t, found)

	hitValue := getCounterValue(cache.prometheusMetrics.CacheHitsTotal)
	assert.Equal(t, 1.0, hitValue)

	// Test get operation metrics (miss)
	_, found = cache.Get("nonexistent/repo")
	assert.False(t, found)

	missValue := getCounterValue(cache.prometheusMetrics.CacheMissesTotal)
	assert.Equal(t, 1.0, missValue)
}

func TestLRUCache_PrometheusMetricsEviction(t *testing.T) {
	// Create small cache that will trigger evictions
	config := NewCacheConfig()
	config.MaxRepositories = 2
	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Enable Prometheus metrics
	registry := prometheus.NewRegistry()
	err = cache.EnablePrometheusMetricsWithRegistry(registry)
	require.NoError(t, err)

	// Add repositories to trigger eviction
	repoPath1, gitRepo1 := createTestRepository(t)
	repoPath2, gitRepo2 := createTestRepository(t)
	repoPath3, gitRepo3 := createTestRepository(t)

	err = cache.Set(repoPath1, gitRepo1)
	require.NoError(t, err)
	err = cache.Set(repoPath2, gitRepo2)
	require.NoError(t, err)
	err = cache.Set(repoPath3, gitRepo3) // This should trigger eviction
	require.NoError(t, err)

	// Verify eviction metrics
	evictionValue := getCounterValue(cache.prometheusMetrics.CacheEvictionsTotal)
	assert.Equal(t, 1.0, evictionValue)

	// Verify entries count stayed at maximum
	entriesValue := getGaugeValue(cache.prometheusMetrics.CacheEntriesCurrent)
	assert.Equal(t, 2.0, entriesValue)
}

func TestLRUCache_PrometheusMetricsTTLCleanup(t *testing.T) {
	// Create cache with very short TTL for testing
	config := NewCacheConfig()
	config.MaxRepositories = 5
	config.TTL = 100 * time.Millisecond
	config.CleanupInterval = 50 * time.Millisecond

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Enable Prometheus metrics
	registry := prometheus.NewRegistry()
	err = cache.EnablePrometheusMetricsWithRegistry(registry)
	require.NoError(t, err)

	// Add a repository
	repoPath1, gitRepo1 := createTestRepository(t)
	err = cache.Set(repoPath1, gitRepo1)
	require.NoError(t, err)

	// Wait for TTL expiration and cleanup
	time.Sleep(200 * time.Millisecond)

	// Verify cleanup was recorded
	cleanupOpsValue := getCounterVecValue(cache.prometheusMetrics.CacheOperationsTotal, "cleanup")
	assert.GreaterOrEqual(t, cleanupOpsValue, 1.0)

	// Verify entries count was updated
	entriesValue := getGaugeValue(cache.prometheusMetrics.CacheEntriesCurrent)
	assert.Equal(t, 0.0, entriesValue)
}

func TestLRUCache_EnablePrometheusMetrics(t *testing.T) {
	config := NewCacheConfig()
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Initially no Prometheus metrics
	assert.Nil(t, cache.prometheusMetrics)

	// Enable Prometheus metrics
	cache.EnablePrometheusMetrics()
	assert.NotNil(t, cache.prometheusMetrics)

	// Calling again should not create new metrics
	oldMetrics := cache.prometheusMetrics
	cache.EnablePrometheusMetrics()
	assert.Equal(t, oldMetrics, cache.prometheusMetrics)
}

func TestLRUCache_EstimateMemoryUsage(t *testing.T) {
	config := NewCacheConfig()
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Empty cache should have zero memory usage
	memUsage := cache.estimateMemoryUsage()
	assert.Equal(t, int64(0), memUsage)

	// Add some entries
	repoPath1, gitRepo1 := createTestRepository(t)
	err = cache.Set(repoPath1, gitRepo1)
	require.NoError(t, err)

	memUsage = cache.estimateMemoryUsage()
	assert.Greater(t, memUsage, int64(0))
	assert.Equal(t, int64(11*1024), memUsage) // Should be 11KB per entry

	repoPath2, gitRepo2 := createTestRepository(t)
	err = cache.Set(repoPath2, gitRepo2)
	require.NoError(t, err)

	memUsage = cache.estimateMemoryUsage()
	assert.Equal(t, int64(2*11*1024), memUsage) // Should be 22KB for 2 entries
}

// Helper functions for testing
// Note: getCounterValue is already defined in metrics.go

func getCounterVecValue(counterVec prometheus.CounterVec, labelValue string) float64 {
	counter, err := counterVec.GetMetricWithLabelValues(labelValue)
	if err != nil {
		return 0
	}
	return getCounterValue(counter)
}

func getGaugeValue(gauge prometheus.Gauge) float64 {
	pb := &dto.Metric{}
	if err := gauge.Write(pb); err != nil {
		return 0
	}
	return pb.GetGauge().GetValue()
}

func getHistogramCount(histogramVec prometheus.HistogramVec, labelValues ...string) uint64 {
	observer, err := histogramVec.GetMetricWithLabelValues(labelValues...)
	if err != nil {
		return 0
	}

	// Cast Observer to Histogram to access Write method
	histogram, ok := observer.(prometheus.Histogram)
	if !ok {
		return 0
	}

	pb := &dto.Metric{}
	if err := histogram.Write(pb); err != nil {
		return 0
	}
	return pb.GetHistogram().GetSampleCount()
}
