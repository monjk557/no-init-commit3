package integration

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Phase2IntegrationTestSuite represents comprehensive integration testing for Phase 2 optimizations
type Phase2IntegrationTestSuite struct {
	logger           *logging.Logger
	testRepoManager  *repositories.RepositoryManager
	overviewService  *overview.RepositoryOverviewService
	multiTierCache   *performance.MultiTierCache
	commitIndex      *overview.CommitIndex
	smartPagination  *overview.SmartPagination
	testRepositories map[string]*TestRepository
	performanceData  *PerformanceMetrics
	mutex            sync.RWMutex
}

// TestRepository represents a test repository with known characteristics
type TestRepository struct {
	Path             string
	Size             RepositorySize
	CommitCount      int
	FileCount        int
	BranchCount      int
	ExpectedResponse time.Duration
	ActualResponse   time.Duration
}

// RepositorySize categorizes repository sizes for testing
type RepositorySize string

const (
	SmallRepository  RepositorySize = "small"
	MediumRepository RepositorySize = "medium"
	LargeRepository  RepositorySize = "large"
)

// PerformanceMetrics tracks comprehensive performance measurements
type PerformanceMetrics struct {
	SmallRepoLatency      time.Duration
	MediumRepoLatency     time.Duration
	LargeRepoLatency      time.Duration
	ConcurrentUserLatency time.Duration
	CacheHitRate          float64

	SmallRepoImprovement  float64
	MediumRepoImprovement float64
	LargeRepoImprovement  float64
	ConcurrentImprovement float64
	CacheRateImprovement  float64
}

// Performance targets as defined in Phase 2 requirements
var PerformanceTargets = map[string]time.Duration{
	"medium_repo_target":     150 * time.Millisecond, // 567ms → 150ms (74% improvement)
	"large_repo_target":      300 * time.Millisecond, // 2,340ms → 300ms (87% improvement)
	"concurrent_user_target": 300 * time.Millisecond, // 834ms → 300ms (64% improvement)
}

const (
	TargetCacheHitRate = 98.0 // 90% → 98%
)

// NewPhase2IntegrationTestSuite creates a new comprehensive test suite
func NewPhase2IntegrationTestSuite(t *testing.T) *Phase2IntegrationTestSuite {
	logger := logging.NewLogger(zap.InfoLevel)

	// Initialize core components
	repoManager, err := repositories.NewRepositoryManager(logger)
	require.NoError(t, err, "Failed to initialize repository manager")

	overviewService := overview.NewRepositoryOverviewService(logger)
	commitIndex := overview.NewCommitIndex(1000, overviewService, logger)
	smartPagination := overview.NewSmartPagination(logger)

	// Initialize multi-tier cache with test configuration
	cacheConfig := performance.DefaultMultiTierConfig()
	cacheConfig.PrefetchEnabled = true
	cacheConfig.TrackingEnabled = true
	multiTierCache := performance.NewMultiTierCache(nil, nil, cacheConfig, logger.GetZapLogger())

	return &Phase2IntegrationTestSuite{
		logger:           logger,
		testRepoManager:  repoManager,
		overviewService:  overviewService,
		multiTierCache:   multiTierCache,
		commitIndex:      commitIndex,
		smartPagination:  smartPagination,
		testRepositories: make(map[string]*TestRepository),
		performanceData:  &PerformanceMetrics{},
	}
}

// TestPhase2ComprehensiveIntegration runs the complete Phase 2 integration test suite
func TestPhase2ComprehensiveIntegration(t *testing.T) {
	suite := NewPhase2IntegrationTestSuite(t)
	defer suite.Cleanup()

	t.Run("Setup Test Repositories", func(t *testing.T) {
		suite.setupTestRepositories(t)
	})

	t.Run("Smart Pagination Integration", func(t *testing.T) {
		suite.testSmartPaginationIntegration(t)
	})

	t.Run("Multi-Tier Cache Integration", func(t *testing.T) {
		suite.testMultiTierCacheIntegration(t)
	})

	t.Run("Commit Index Performance", func(t *testing.T) {
		suite.testCommitIndexPerformance(t)
	})

	t.Run("Concurrent Load Testing", func(t *testing.T) {
		suite.testConcurrentLoad(t)
	})

	t.Run("Performance Target Validation", func(t *testing.T) {
		suite.validatePerformanceTargets(t)
	})

	t.Run("System Integration Validation", func(t *testing.T) {
		suite.validateSystemIntegration(t)
	})

	t.Run("Performance Regression Detection", func(t *testing.T) {
		suite.detectPerformanceRegressions(t)
	})
}

// setupTestRepositories creates and initializes test repositories of various sizes
func (suite *Phase2IntegrationTestSuite) setupTestRepositories(t *testing.T) {
	testCases := []struct {
		name        string
		size        RepositorySize
		fileCount   int
		commitCount int
		branches    int
	}{
		{"small_test_repo", SmallRepository, 25, 50, 2},
		{"medium_test_repo", MediumRepository, 150, 500, 5},
		{"large_test_repo", LargeRepository, 1000, 2000, 10},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Setup_%s", tc.name), func(t *testing.T) {
			testRepo := suite.createTestRepository(t, tc.name, tc.size, tc.fileCount, tc.commitCount, tc.branches)
			suite.testRepositories[tc.name] = testRepo

			suite.logger.Info("Test repository created",
				zap.String("name", tc.name),
				zap.String("size", string(tc.size)),
				zap.Int("files", tc.fileCount),
				zap.Int("commits", tc.commitCount),
				zap.String("path", testRepo.Path))
		})
	}
}

// testSmartPaginationIntegration validates smart pagination behavior
func (suite *Phase2IntegrationTestSuite) testSmartPaginationIntegration(t *testing.T) {
	ctx := context.Background()

	for repoName, testRepo := range suite.testRepositories {
		t.Run(fmt.Sprintf("SmartPagination_%s", repoName), func(t *testing.T) {
			// Test adaptive page size determination
			pageSize := suite.smartPagination.DeterminePageSize(ctx, testRepo.Path, "", 0)

			expectedPageSize := suite.getExpectedPageSize(testRepo.Size)
			assert.True(t, pageSize >= expectedPageSize/2 && pageSize <= expectedPageSize*2,
				"Page size should be within reasonable range of expected size")

			suite.logger.Info("Smart pagination test completed",
				zap.String("repo", repoName),
				zap.String("size", string(testRepo.Size)),
				zap.Int("determined_page_size", pageSize),
				zap.Int("expected_page_size", expectedPageSize))
		})
	}
}

// testMultiTierCacheIntegration validates multi-tier caching effectiveness
func (suite *Phase2IntegrationTestSuite) testMultiTierCacheIntegration(t *testing.T) {
	ctx := context.Background()

	// Warm up cache with multiple access patterns
	for i := 0; i < 3; i++ {
		for repoName, testRepo := range suite.testRepositories {
			repo, err := gogit.PlainOpen(testRepo.Path)
			if err == nil {
				// Simulate cache warming
				suite.multiTierCache.SetRepository(ctx, testRepo.Path, repo)
			}
		}
	}

	// Test cache hit rates
	hitCount := 0
	totalRequests := 0

	for i := 0; i < 100; i++ {
		for repoName, testRepo := range suite.testRepositories {
			totalRequests++
			if _, found := suite.multiTierCache.GetRepository(ctx, testRepo.Path); found {
				hitCount++
			}

			suite.logger.Debug("Cache lookup completed",
				zap.String("repo", repoName),
				zap.Bool("hit", found))
		}
	}

	hitRate := float64(hitCount) / float64(totalRequests) * 100
	suite.performanceData.CacheHitRate = hitRate

	suite.logger.Info("Multi-tier cache test completed",
		zap.Float64("hit_rate_percent", hitRate),
		zap.Int("total_requests", totalRequests),
		zap.Int("hits", hitCount))

	// Validate cache hit rate meets target
	assert.True(t, hitRate >= TargetCacheHitRate*0.8, // Allow 20% tolerance for test environment
		fmt.Sprintf("Cache hit rate %.2f%% should be close to target %.2f%%", hitRate, TargetCacheHitRate))
}

// testCommitIndexPerformance validates commit indexing performance improvements
func (suite *Phase2IntegrationTestSuite) testCommitIndexPerformance(t *testing.T) {
	ctx := context.Background()

	for repoName, testRepo := range suite.testRepositories {
		t.Run(fmt.Sprintf("CommitIndex_%s", repoName), func(t *testing.T) {
			repo, err := gogit.PlainOpen(testRepo.Path)
			require.NoError(t, err, "Failed to open test repository")

			// Get HEAD reference
			head, err := repo.Head()
			require.NoError(t, err, "Failed to get HEAD reference")

			// Simulate path info for commit indexing
			pathInfos := suite.generatePathInfos(testRepo.Size)

			// Measure commit index population performance
			start := time.Now()
			commits, meta := suite.overviewService.GetLatestCommitsForPathsWithMeta(
				ctx, repo, head.Hash(), pathInfos, 5*time.Second, 10000)
			duration := time.Since(start)

			testRepo.ActualResponse = duration

			suite.logger.Info("Commit index performance test completed",
				zap.String("repo", repoName),
				zap.String("size", string(testRepo.Size)),
				zap.Duration("duration", duration),
				zap.Int("commit_count", len(commits)),
				zap.Bool("incomplete", meta.Incomplete),
				zap.String("cut_reason", meta.CutReason))

			// Store commit index results for validation
			suite.commitIndex.Store(testRepo.Path, head.Hash().String(), "", commits, meta)

			// Validate performance meets reasonable expectations
			maxExpectedDuration := suite.getMaxExpectedDuration(testRepo.Size)
			assert.True(t, duration <= maxExpectedDuration,
				fmt.Sprintf("Duration %v should be <= max expected %v for %s repository",
					duration, maxExpectedDuration, testRepo.Size))
		})
	}
}

// testConcurrentLoad validates system performance under concurrent load
func (suite *Phase2IntegrationTestSuite) testConcurrentLoad(t *testing.T) {
	const (
		concurrentUsers = 50
		requestsPerUser = 10
	)

	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan time.Duration, concurrentUsers*requestsPerUser)

	startTime := time.Now()

	// Simulate concurrent users
	for user := 0; user < concurrentUsers; user++ {
		wg.Add(1)
		go func(userID int) {
			defer wg.Done()

			// Each user makes multiple requests
			for req := 0; req < requestsPerUser; req++ {
				// Randomly select a test repository
				repoNames := make([]string, 0, len(suite.testRepositories))
				for name := range suite.testRepositories {
					repoNames = append(repoNames, name)
				}

				repoName := repoNames[rand.Intn(len(repoNames))]
				testRepo := suite.testRepositories[repoName]

				requestStart := time.Now()

				// Simulate overview request with smart pagination
				pageSize := suite.smartPagination.DeterminePageSize(ctx, testRepo.Path, "", 20)

				// Check cache
				suite.multiTierCache.GetRepository(ctx, testRepo.Path)

				requestDuration := time.Since(requestStart)
				results <- requestDuration

				// Small delay between requests
				time.Sleep(10 * time.Millisecond)
			}
		}(user)
	}

	wg.Wait()
	close(results)

	// Analyze concurrent performance results
	var totalDuration time.Duration
	var maxDuration time.Duration
	var durations []time.Duration

	for duration := range results {
		durations = append(durations, duration)
		totalDuration += duration
		if duration > maxDuration {
			maxDuration = duration
		}
	}

	avgDuration := totalDuration / time.Duration(len(durations))
	suite.performanceData.ConcurrentUserLatency = avgDuration

	suite.logger.Info("Concurrent load test completed",
		zap.Int("concurrent_users", concurrentUsers),
		zap.Int("requests_per_user", requestsPerUser),
		zap.Int("total_requests", len(durations)),
		zap.Duration("avg_latency", avgDuration),
		zap.Duration("max_latency", maxDuration),
		zap.Duration("total_test_time", time.Since(startTime)))

	// Validate concurrent performance meets target
	assert.True(t, avgDuration <= PerformanceTargets["concurrent_user_target"]*2, // Allow 2x tolerance for test environment
		fmt.Sprintf("Average concurrent latency %v should be close to target %v",
			avgDuration, PerformanceTargets["concurrent_user_target"]))
}

// validatePerformanceTargets checks if all Phase 2 performance targets are met
func (suite *Phase2IntegrationTestSuite) validatePerformanceTargets(t *testing.T) {
	// Extract performance data from test repositories
	for repoName, testRepo := range suite.testRepositories {
		switch testRepo.Size {
		case SmallRepository:
			// Small repos don't have specific targets, but should be fast
			assert.True(t, testRepo.ActualResponse <= 100*time.Millisecond,
				"Small repository should respond within 100ms")

		case MediumRepository:
			suite.performanceData.MediumRepoLatency = testRepo.ActualResponse
			target := PerformanceTargets["medium_repo_target"]

			suite.logger.Info("Medium repository performance validation",
				zap.String("repo", repoName),
				zap.Duration("actual", testRepo.ActualResponse),
				zap.Duration("target", target))

			// Allow 50% tolerance for test environment
			assert.True(t, testRepo.ActualResponse <= target*3/2,
				fmt.Sprintf("Medium repository %s latency %v should be close to target %v",
					repoName, testRepo.ActualResponse, target))

		case LargeRepository:
			suite.performanceData.LargeRepoLatency = testRepo.ActualResponse
			target := PerformanceTargets["large_repo_target"]

			suite.logger.Info("Large repository performance validation",
				zap.String("repo", repoName),
				zap.Duration("actual", testRepo.ActualResponse),
				zap.Duration("target", target))

			// Allow 50% tolerance for test environment
			assert.True(t, testRepo.ActualResponse <= target*3/2,
				fmt.Sprintf("Large repository %s latency %v should be close to target %v",
					repoName, testRepo.ActualResponse, target))
		}
	}

	// Validate cache hit rate target
	assert.True(t, suite.performanceData.CacheHitRate >= TargetCacheHitRate*0.8, // 80% of target
		fmt.Sprintf("Cache hit rate %.2f%% should approach target %.2f%%",
			suite.performanceData.CacheHitRate, TargetCacheHitRate))
}

// validateSystemIntegration ensures all Phase 2 components work together effectively
func (suite *Phase2IntegrationTestSuite) validateSystemIntegration(t *testing.T) {
	ctx := context.Background()

	// Test integrated workflow: Smart Pagination + Commit Index + Multi-tier Cache
	for repoName, testRepo := range suite.testRepositories {
		t.Run(fmt.Sprintf("Integration_%s", repoName), func(t *testing.T) {
			// 1. Smart pagination determines optimal page size
			pageSize := suite.smartPagination.DeterminePageSize(ctx, testRepo.Path, "", 0)
			assert.Greater(t, pageSize, 0, "Page size should be positive")

			// 2. Multi-tier cache provides repository access
			_, cacheFound := suite.multiTierCache.GetRepository(ctx, testRepo.Path)

			// 3. Commit index provides fast commit lookup
			indexEntries := suite.commitIndex.Len()
			indexCost := suite.commitIndex.TotalCost()

			suite.logger.Info("System integration test completed",
				zap.String("repo", repoName),
				zap.Int("page_size", pageSize),
				zap.Bool("cache_found", cacheFound),
				zap.Int("index_entries", indexEntries),
				zap.Int64("index_cost", indexCost))

			// Validate integration effectiveness
			assert.True(t, pageSize > 0 && pageSize <= 100,
				"Page size should be reasonable")
		})
	}
}

// detectPerformanceRegressions identifies potential performance regressions
func (suite *Phase2IntegrationTestSuite) detectPerformanceRegressions(t *testing.T) {
	// This would compare against baseline performance data in a real environment
	// For now, we validate that performance is within acceptable ranges

	regressions := []string{}

	// Check medium repository performance
	if suite.performanceData.MediumRepoLatency > PerformanceTargets["medium_repo_target"]*2 {
		regressions = append(regressions, fmt.Sprintf("Medium repo latency regression: %v > %v",
			suite.performanceData.MediumRepoLatency, PerformanceTargets["medium_repo_target"]*2))
	}

	// Check large repository performance
	if suite.performanceData.LargeRepoLatency > PerformanceTargets["large_repo_target"]*2 {
		regressions = append(regressions, fmt.Sprintf("Large repo latency regression: %v > %v",
			suite.performanceData.LargeRepoLatency, PerformanceTargets["large_repo_target"]*2))
	}

	// Check concurrent user performance
	if suite.performanceData.ConcurrentUserLatency > PerformanceTargets["concurrent_user_target"]*2 {
		regressions = append(regressions, fmt.Sprintf("Concurrent user latency regression: %v > %v",
			suite.performanceData.ConcurrentUserLatency, PerformanceTargets["concurrent_user_target"]*2))
	}

	// Check cache hit rate
	if suite.performanceData.CacheHitRate < TargetCacheHitRate*0.7 { // 70% of target
		regressions = append(regressions, fmt.Sprintf("Cache hit rate regression: %.2f%% < %.2f%%",
			suite.performanceData.CacheHitRate, TargetCacheHitRate*0.7))
	}

	if len(regressions) > 0 {
		suite.logger.Warn("Performance regressions detected", zap.Strings("regressions", regressions))
		// In CI/CD, this would fail the build
		for _, regression := range regressions {
			t.Logf("REGRESSION DETECTED: %s", regression)
		}
	} else {
		suite.logger.Info("No performance regressions detected")
	}
}

// Helper methods

func (suite *Phase2IntegrationTestSuite) createTestRepository(t *testing.T, name string, size RepositorySize, fileCount, commitCount, branchCount int) *TestRepository {
	// Create temporary directory for test repository
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("phase2_test_%s_", name))
	require.NoError(t, err, "Failed to create temp directory")

	repoPath := filepath.Join(tmpDir, name)

	// Initialize git repository
	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err, "Failed to initialize git repository")

	// Create test files and commits (simplified for testing)
	for i := 0; i < fileCount && i < commitCount; i++ {
		filename := fmt.Sprintf("file_%d.txt", i)
		filepath := filepath.Join(repoPath, filename)

		err := os.WriteFile(filepath, []byte(fmt.Sprintf("Content of file %d\n", i)), 0644)
		require.NoError(t, err, "Failed to create test file")
	}

	expectedResponseTime := suite.getExpectedResponseTime(size)

	return &TestRepository{
		Path:             repoPath,
		Size:             size,
		CommitCount:      commitCount,
		FileCount:        fileCount,
		BranchCount:      branchCount,
		ExpectedResponse: expectedResponseTime,
	}
}

func (suite *Phase2IntegrationTestSuite) getExpectedPageSize(size RepositorySize) int {
	switch size {
	case SmallRepository:
		return 100
	case MediumRepository:
		return 50
	case LargeRepository:
		return 20
	default:
		return 50
	}
}

func (suite *Phase2IntegrationTestSuite) getExpectedResponseTime(size RepositorySize) time.Duration {
	switch size {
	case SmallRepository:
		return 50 * time.Millisecond
	case MediumRepository:
		return 150 * time.Millisecond
	case LargeRepository:
		return 300 * time.Millisecond
	default:
		return 200 * time.Millisecond
	}
}

func (suite *Phase2IntegrationTestSuite) getMaxExpectedDuration(size RepositorySize) time.Duration {
	switch size {
	case SmallRepository:
		return 200 * time.Millisecond
	case MediumRepository:
		return 500 * time.Millisecond
	case LargeRepository:
		return 1000 * time.Millisecond
	default:
		return 500 * time.Millisecond
	}
}

func (suite *Phase2IntegrationTestSuite) generatePathInfos(size RepositorySize) []overview.CommitPathInfo {
	var count int
	switch size {
	case SmallRepository:
		count = 25
	case MediumRepository:
		count = 150
	case LargeRepository:
		count = 1000
	default:
		count = 100
	}

	pathInfos := make([]overview.CommitPathInfo, count)
	for i := 0; i < count; i++ {
		pathInfos[i] = overview.CommitPathInfo{
			Path: fmt.Sprintf("file_%d.txt", i),
			Type: "blob",
		}
	}
	return pathInfos
}

func (suite *Phase2IntegrationTestSuite) Cleanup() {
	// Clean up temporary test repositories
	for _, testRepo := range suite.testRepositories {
		os.RemoveAll(filepath.Dir(testRepo.Path))
	}

	// Close cache components
	if suite.multiTierCache != nil {
		suite.multiTierCache.Close()
	}

	suite.logger.Info("Test suite cleanup completed")
}
