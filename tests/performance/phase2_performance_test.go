package performance

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/collabops/git-executor/internal/performance"
	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Phase2PerformanceBenchmark provides comprehensive performance testing for Phase 2 optimizations
type Phase2PerformanceBenchmark struct {
	logger              *logging.Logger
	overviewService     *overview.RepositoryOverviewService
	commitIndex         *overview.CommitIndex
	smartPagination     *overview.SmartPagination
	multiTierCache      *performance.MultiTierCache
	performanceRecorder *PerformanceRecorder
}

// PerformanceRecorder tracks detailed performance metrics
type PerformanceRecorder struct {
	measurements map[string][]time.Duration
	counters     map[string]int64
	mutex        sync.RWMutex
}

// BenchmarkResult represents performance benchmark results
type BenchmarkResult struct {
	TestName          string
	MinDuration       time.Duration
	MaxDuration       time.Duration
	MeanDuration      time.Duration
	MedianDuration    time.Duration
	P95Duration       time.Duration
	P99Duration       time.Duration
	StandardDeviation time.Duration
	ThroughputReqSec  float64
	MemoryUsageMB     float64
	CPUUsagePercent   float64
}

// NewPhase2PerformanceBenchmark creates a new performance benchmark suite
func NewPhase2PerformanceBenchmark() *Phase2PerformanceBenchmark {
	logger := logging.NewLogger(zap.InfoLevel)
	overviewService := overview.NewRepositoryOverviewService(logger)
	commitIndex := overview.NewCommitIndex(1000, overviewService, logger)
	smartPagination := overview.NewSmartPagination(logger)

	cacheConfig := performance.DefaultMultiTierConfig()
	cacheConfig.L1MaxEntries = 1000
	cacheConfig.PrefetchEnabled = true
	multiTierCache := performance.NewMultiTierCache(nil, nil, cacheConfig, logger.GetZapLogger())

	recorder := &PerformanceRecorder{
		measurements: make(map[string][]time.Duration),
		counters:     make(map[string]int64),
	}

	return &Phase2PerformanceBenchmark{
		logger:              logger,
		overviewService:     overviewService,
		commitIndex:         commitIndex,
		smartPagination:     smartPagination,
		multiTierCache:      multiTierCache,
		performanceRecorder: recorder,
	}
}

// BenchmarkSmartPaginationPerformance tests smart pagination performance across repository sizes
func BenchmarkSmartPaginationPerformance(b *testing.B) {
	benchmark := NewPhase2PerformanceBenchmark()
	ctx := context.Background()

	testCases := []struct {
		name      string
		repoSize  string
		fileCount int
	}{
		{"SmallRepo", "small", 50},
		{"MediumRepo", "medium", 500},
		{"LargeRepo", "large", 2000},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			// Create test repository
			testRepo := createBenchmarkRepository(b, tc.name, tc.fileCount)
			defer os.RemoveAll(filepath.Dir(testRepo))

			b.ResetTimer()
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				start := time.Now()

				// Test smart pagination
				pageSize := benchmark.smartPagination.DeterminePageSize(ctx, testRepo, "", 0)

				// Simulate pagination application
				mockFiles := generateMockFiles(tc.fileCount)
				response, pagination := benchmark.smartPagination.ApplyPagination(
					mockFiles, 1, pageSize, "")

				duration := time.Since(start)
				benchmark.performanceRecorder.Record(fmt.Sprintf("pagination_%s", tc.name), duration)

				// Validate results
				if len(response.Tree) == 0 || pagination == nil {
					b.Errorf("Pagination failed for %s", tc.name)
				}
			}

			b.StopTimer()

			// Report metrics
			result := benchmark.performanceRecorder.Analyze(fmt.Sprintf("pagination_%s", tc.name))
			benchmark.logger.Info("Smart pagination benchmark completed",
				zap.String("test", tc.name),
				zap.Duration("mean", result.MeanDuration),
				zap.Duration("p95", result.P95Duration),
				zap.Float64("throughput", result.ThroughputReqSec))
		})
	}
}

// BenchmarkCommitIndexPerformance tests commit index performance and hit rates
func BenchmarkCommitIndexPerformance(b *testing.B) {
	benchmark := NewPhase2PerformanceBenchmark()
	ctx := context.Background()

	testRepo := createBenchmarkRepository(b, "commit_index_test", 1000)
	defer os.RemoveAll(filepath.Dir(testRepo))

	repo, err := gogit.PlainOpen(testRepo)
	require.NoError(b, err)

	head, err := repo.Head()
	require.NoError(b, err)

	pathInfos := generateCommitPathInfos(500)

	b.Run("CommitIndexPopulation", func(b *testing.B) {
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			start := time.Now()

			commits, meta := benchmark.overviewService.GetLatestCommitsForPathsWithMeta(
				ctx, repo, head.Hash(), pathInfos, 5*time.Second, 10000)

			benchmark.commitIndex.Store(testRepo, head.Hash().String(), "", commits, meta)

			duration := time.Since(start)
			benchmark.performanceRecorder.Record("commit_index_population", duration)
		}

		result := benchmark.performanceRecorder.Analyze("commit_index_population")
		benchmark.logger.Info("Commit index population benchmark completed",
			zap.Duration("mean", result.MeanDuration),
			zap.Duration("p95", result.P95Duration))
	})

	b.Run("CommitIndexLookup", func(b *testing.B) {
		// Populate index first
		commits, meta := benchmark.overviewService.GetLatestCommitsForPathsWithMeta(
			ctx, repo, head.Hash(), pathInfos, 5*time.Second, 10000)
		benchmark.commitIndex.Store(testRepo, head.Hash().String(), "", commits, meta)

		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			start := time.Now()

			commits, meta, found := benchmark.commitIndex.Lookup(testRepo, head.Hash().String(), "")

			duration := time.Since(start)
			benchmark.performanceRecorder.Record("commit_index_lookup", duration)

			if !found {
				b.Errorf("Commit index lookup failed")
			}

			if len(commits) == 0 {
				b.Errorf("No commits returned from index")
			}
		}

		result := benchmark.performanceRecorder.Analyze("commit_index_lookup")
		benchmark.logger.Info("Commit index lookup benchmark completed",
			zap.Duration("mean", result.MeanDuration),
			zap.Duration("p95", result.P95Duration))
	})
}

// BenchmarkMultiTierCachePerformance tests multi-tier cache hit rates and performance
func BenchmarkMultiTierCachePerformance(b *testing.B) {
	benchmark := NewPhase2PerformanceBenchmark()
	ctx := context.Background()

	testRepos := make([]string, 10)
	for i := range testRepos {
		testRepos[i] = createBenchmarkRepository(b, fmt.Sprintf("cache_test_%d", i), 100)
		defer os.RemoveAll(filepath.Dir(testRepos[i]))
	}

	b.Run("CacheWarmup", func(b *testing.B) {
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			for _, repoPath := range testRepos {
				start := time.Now()

				repo, err := gogit.PlainOpen(repoPath)
				if err == nil {
					benchmark.multiTierCache.SetRepository(ctx, repoPath, repo)
				}

				duration := time.Since(start)
				benchmark.performanceRecorder.Record("cache_warmup", duration)
			}
		}

		result := benchmark.performanceRecorder.Analyze("cache_warmup")
		benchmark.logger.Info("Cache warmup benchmark completed",
			zap.Duration("mean", result.MeanDuration))
	})

	b.Run("CacheHitRate", func(b *testing.B) {
		// Warm up cache
		for _, repoPath := range testRepos {
			if repo, err := gogit.PlainOpen(repoPath); err == nil {
				benchmark.multiTierCache.SetRepository(ctx, repoPath, repo)
			}
		}

		var totalRequests int64
		var cacheHits int64

		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			for _, repoPath := range testRepos {
				start := time.Now()

				_, found := benchmark.multiTierCache.GetRepository(ctx, repoPath)

				atomic.AddInt64(&totalRequests, 1)
				if found {
					atomic.AddInt64(&cacheHits, 1)
				}

				duration := time.Since(start)
				benchmark.performanceRecorder.Record("cache_lookup", duration)
			}
		}

		hitRate := float64(cacheHits) / float64(totalRequests) * 100
		result := benchmark.performanceRecorder.Analyze("cache_lookup")

		benchmark.logger.Info("Cache hit rate benchmark completed",
			zap.Float64("hit_rate_percent", hitRate),
			zap.Int64("total_requests", totalRequests),
			zap.Int64("hits", cacheHits),
			zap.Duration("mean_lookup", result.MeanDuration))

		// Validate hit rate meets target (allowing tolerance for test environment)
		if hitRate < 80.0 { // 80% minimum for benchmark
			b.Errorf("Cache hit rate %.2f%% below minimum threshold", hitRate)
		}
	})
}

// BenchmarkConcurrentLoad tests system performance under concurrent load
func BenchmarkConcurrentLoad(b *testing.B) {
	benchmark := NewPhase2PerformanceBenchmark()
	ctx := context.Background()

	testRepo := createBenchmarkRepository(b, "concurrent_test", 500)
	defer os.RemoveAll(filepath.Dir(testRepo))

	// Warm up components
	repo, err := gogit.PlainOpen(testRepo)
	require.NoError(b, err)
	benchmark.multiTierCache.SetRepository(ctx, testRepo, repo)

	concurrencyLevels := []int{10, 50, 100, 200}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrent_%d", concurrency), func(b *testing.B) {
			var wg sync.WaitGroup
			results := make(chan time.Duration, concurrency*b.N)

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				for worker := 0; worker < concurrency; worker++ {
					wg.Add(1)
					go func() {
						defer wg.Done()

						start := time.Now()

						// Simulate typical workload
						pageSize := benchmark.smartPagination.DeterminePageSize(ctx, testRepo, "", 0)
						_, found := benchmark.multiTierCache.GetRepository(ctx, testRepo)
						_, _, indexed := benchmark.commitIndex.Lookup(testRepo, "dummy_hash", "")

						duration := time.Since(start)
						results <- duration

						// Use results to prevent optimization
						_ = pageSize > 0 || found || indexed
					}()
				}
			}

			wg.Wait()
			close(results)

			// Analyze concurrent performance
			var totalDuration time.Duration
			var maxDuration time.Duration
			var count int

			for duration := range results {
				totalDuration += duration
				count++
				if duration > maxDuration {
					maxDuration = duration
				}
			}

			avgDuration := totalDuration / time.Duration(count)
			throughput := float64(count) / totalDuration.Seconds()

			benchmark.logger.Info("Concurrent load benchmark completed",
				zap.Int("concurrency", concurrency),
				zap.Int("total_operations", count),
				zap.Duration("avg_latency", avgDuration),
				zap.Duration("max_latency", maxDuration),
				zap.Float64("throughput_ops_sec", throughput))

			// Validate performance under load
			maxExpected := 500 * time.Millisecond // Allow 500ms max for concurrent operations
			if avgDuration > maxExpected {
				b.Errorf("Average duration %v exceeds maximum expected %v for concurrency %d",
					avgDuration, maxExpected, concurrency)
			}
		})
	}
}

// BenchmarkMemoryEfficiency tests memory usage patterns and efficiency
func BenchmarkMemoryEfficiency(b *testing.B) {
	benchmark := NewPhase2PerformanceBenchmark()
	ctx := context.Background()

	testRepo := createBenchmarkRepository(b, "memory_test", 1000)
	defer os.RemoveAll(filepath.Dir(testRepo))

	// Force GC to get baseline
	runtime.GC()
	var startMemStats, endMemStats runtime.MemStats
	runtime.ReadMemStats(&startMemStats)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Simulate memory-intensive operations
		pageSize := benchmark.smartPagination.DeterminePageSize(ctx, testRepo, "", 0)

		if repo, err := gogit.PlainOpen(testRepo); err == nil {
			benchmark.multiTierCache.SetRepository(ctx, testRepo, repo)
		}

		// Create and populate commit index
		pathInfos := generateCommitPathInfos(100)
		if repo, err := gogit.PlainOpen(testRepo); err == nil {
			if head, err := repo.Head(); err == nil {
				commits, meta := benchmark.overviewService.GetLatestCommitsForPathsWithMeta(
					ctx, repo, head.Hash(), pathInfos, 1*time.Second, 1000)
				benchmark.commitIndex.Store(testRepo, head.Hash().String(), "", commits, meta)
			}
		}

		// Force periodic GC to measure actual usage
		if i%100 == 0 {
			runtime.GC()
		}
	}

	runtime.GC()
	runtime.ReadMemStats(&endMemStats)

	memoryUsedMB := float64(endMemStats.Alloc-startMemStats.Alloc) / (1024 * 1024)
	totalAllocMB := float64(endMemStats.TotalAlloc-startMemStats.TotalAlloc) / (1024 * 1024)

	benchmark.logger.Info("Memory efficiency benchmark completed",
		zap.Float64("memory_used_mb", memoryUsedMB),
		zap.Float64("total_alloc_mb", totalAllocMB),
		zap.Uint64("gc_cycles", endMemStats.NumGC-startMemStats.NumGC))

	// Validate memory efficiency
	maxExpectedMemoryMB := 100.0 // 100MB max for reasonable efficiency
	if memoryUsedMB > maxExpectedMemoryMB {
		b.Errorf("Memory usage %.2f MB exceeds maximum expected %.2f MB",
			memoryUsedMB, maxExpectedMemoryMB)
	}
}

// Helper methods for performance recording and analysis

func (pr *PerformanceRecorder) Record(testName string, duration time.Duration) {
	pr.mutex.Lock()
	defer pr.mutex.Unlock()

	pr.measurements[testName] = append(pr.measurements[testName], duration)
	pr.counters[testName]++
}

func (pr *PerformanceRecorder) Analyze(testName string) *BenchmarkResult {
	pr.mutex.RLock()
	defer pr.mutex.RUnlock()

	measurements, exists := pr.measurements[testName]
	if !exists || len(measurements) == 0 {
		return &BenchmarkResult{TestName: testName}
	}

	// Sort measurements for percentile calculations
	sortedMeasurements := make([]time.Duration, len(measurements))
	copy(sortedMeasurements, measurements)

	// Simple sorting (for small datasets)
	for i := 0; i < len(sortedMeasurements); i++ {
		for j := i + 1; j < len(sortedMeasurements); j++ {
			if sortedMeasurements[i] > sortedMeasurements[j] {
				sortedMeasurements[i], sortedMeasurements[j] = sortedMeasurements[j], sortedMeasurements[i]
			}
		}
	}

	// Calculate statistics
	minDuration := sortedMeasurements[0]
	maxDuration := sortedMeasurements[len(sortedMeasurements)-1]

	var totalDuration time.Duration
	for _, d := range measurements {
		totalDuration += d
	}
	meanDuration := totalDuration / time.Duration(len(measurements))

	medianDuration := sortedMeasurements[len(sortedMeasurements)/2]
	p95Duration := sortedMeasurements[int(float64(len(sortedMeasurements))*0.95)]
	p99Duration := sortedMeasurements[int(float64(len(sortedMeasurements))*0.99)]

	// Calculate standard deviation
	var sumSquaredDiffs float64
	meanSeconds := meanDuration.Seconds()
	for _, d := range measurements {
		diff := d.Seconds() - meanSeconds
		sumSquaredDiffs += diff * diff
	}
	stdDev := time.Duration(math.Sqrt(sumSquaredDiffs/float64(len(measurements))) * float64(time.Second))

	// Calculate throughput
	throughput := float64(len(measurements)) / totalDuration.Seconds()

	return &BenchmarkResult{
		TestName:          testName,
		MinDuration:       minDuration,
		MaxDuration:       maxDuration,
		MeanDuration:      meanDuration,
		MedianDuration:    medianDuration,
		P95Duration:       p95Duration,
		P99Duration:       p99Duration,
		StandardDeviation: stdDev,
		ThroughputReqSec:  throughput,
	}
}

// Utility functions

func createBenchmarkRepository(b *testing.B, name string, fileCount int) string {
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("bench_%s_", name))
	require.NoError(b, err)

	repoPath := filepath.Join(tmpDir, name)
	_, err = gogit.PlainInit(repoPath, false)
	require.NoError(b, err)

	// Create test files
	for i := 0; i < fileCount; i++ {
		filename := fmt.Sprintf("file_%d.txt", i)
		filepath := filepath.Join(repoPath, filename)
		err := os.WriteFile(filepath, []byte(fmt.Sprintf("Content %d\n", i)), 0644)
		require.NoError(b, err)
	}

	return repoPath
}

func generateMockFiles(count int) []overview.GitTreeEntryWithCommit {
	files := make([]overview.GitTreeEntryWithCommit, count)
	for i := 0; i < count; i++ {
		files[i] = overview.GitTreeEntryWithCommit{
			Path: fmt.Sprintf("file_%d.txt", i),
			Type: "blob",
			Size: int64(100 + i),
		}
	}
	return files
}

func generateCommitPathInfos(count int) []overview.CommitPathInfo {
	pathInfos := make([]overview.CommitPathInfo, count)
	for i := 0; i < count; i++ {
		pathInfos[i] = overview.CommitPathInfo{
			Path: fmt.Sprintf("path_%d.txt", i),
			Type: "blob",
		}
	}
	return pathInfos
}
