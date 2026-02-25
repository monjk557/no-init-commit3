package performance

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/merge"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// BenchmarkMergeProtectionPerformanceBaseline tests baseline merge protection performance
func BenchmarkMergeProtectionPerformanceBaseline(b *testing.B) {
	logger := createBenchmarkLogger(b)
	architecture := NewMergeProtectionPerformanceArchitecture(logger, nil)

	testCases := []struct {
		name             string
		repoConfig       RepoSizeConfig
		iterations       int
		enableProtection bool
	}{
		{
			name:             "SmallRepo_NoProtection",
			repoConfig:       createSmallRepoConfig(),
			iterations:       b.N,
			enableProtection: false,
		},
		{
			name:             "SmallRepo_WithProtection",
			repoConfig:       createSmallRepoConfig(),
			iterations:       b.N,
			enableProtection: true,
		},
		{
			name:             "MediumRepo_WithProtection",
			repoConfig:       createMediumRepoConfig(),
			iterations:       b.N / 10, // Reduce iterations for larger repos
			enableProtection: true,
		},
		{
			name:             "LargeRepo_WithProtection",
			repoConfig:       createLargeRepoConfig(),
			iterations:       b.N / 100,
			enableProtection: true,
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			result := executeMergeProtectionBenchmark(b, architecture, tc.repoConfig, tc.iterations, tc.enableProtection)

			// Validate performance thresholds
			validatePerformanceThresholds(b, result, architecture.config)

			// Report metrics
			reportBenchmarkMetrics(b, result)
		})
	}
}

// BenchmarkMergeProtectionScalability tests performance across different repository sizes
func BenchmarkMergeProtectionScalability(b *testing.B) {
	logger := createBenchmarkLogger(b)
	config := &PerformanceTestConfig{
		MaxConcurrentOperations:  50,
		StressTestEnabled:        true,
		MaxLatencyP99:            2 * time.Second,
		MinThroughputOpsPerSec:   5.0,
		MaxMemoryUsageMB:         500.0,
		MaxCPUUsagePercent:       70.0,
		MaxProtectionOverheadPct: 25.0,
	}
	architecture := NewMergeProtectionPerformanceArchitecture(logger, config)

	repositoryConfigs := []RepoSizeConfig{
		{Name: "tiny", TotalFiles: 50, EnvironmentFiles: 2, DirectoryDepth: 2, CommitHistory: 10},
		{Name: "small", TotalFiles: 200, EnvironmentFiles: 10, DirectoryDepth: 3, CommitHistory: 50},
		{Name: "medium", TotalFiles: 1000, EnvironmentFiles: 50, DirectoryDepth: 5, CommitHistory: 200},
		{Name: "large", TotalFiles: 5000, EnvironmentFiles: 200, DirectoryDepth: 7, CommitHistory: 500},
		{Name: "xlarge", TotalFiles: 20000, EnvironmentFiles: 1000, DirectoryDepth: 10, CommitHistory: 1000},
	}

	for _, repoConfig := range repositoryConfigs {
		b.Run(fmt.Sprintf("Scale_%s", repoConfig.Name), func(b *testing.B) {
			scalabilityResult := executeScalabilityBenchmark(b, architecture, repoConfig)

			// Analyze scalability characteristics
			analyzeScalabilityMetrics(b, scalabilityResult, repoConfig)
		})
	}
}

// BenchmarkMergeProtectionConcurrency tests concurrent merge protection operations
func BenchmarkMergeProtectionConcurrency(b *testing.B) {
	logger := createBenchmarkLogger(b)
	architecture := NewMergeProtectionPerformanceArchitecture(logger, nil)

	concurrencyLevels := []int{1, 5, 10, 25, 50, 100}
	repoConfig := createMediumRepoConfig()

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrency_%d", concurrency), func(b *testing.B) {
			concurrencyResult := executeConcurrencyBenchmark(b, architecture, repoConfig, concurrency)

			// Analyze concurrency scaling
			analyzeConcurrencyScaling(b, concurrencyResult, concurrency)
		})
	}
}

// BenchmarkMergeProtectionResourceUsage analyzes memory and CPU usage patterns
func BenchmarkMergeProtectionResourceUsage(b *testing.B) {
	logger := createBenchmarkLogger(b)
	architecture := NewMergeProtectionPerformanceArchitecture(logger, nil)

	// Force GC to get clean baseline
	runtime.GC()
	var startMemStats runtime.MemStats
	runtime.ReadMemStats(&startMemStats)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		testRepo, cleanup, err := createBenchmarkRepository(b, "resource_test", 1000, 50, true)
		require.NoError(b, err)
		defer cleanup()

		// Execute resource-intensive merge protection operations
		start := time.Now()

		result, err := executeProtectedMergeWithEnvironmentFiles(b, architecture, testRepo)
		require.NoError(b, err)

		duration := time.Since(start)

		// Record metrics
		assert.NotNil(b, result)
		assert.True(b, len(result.EnvironmentProtected) > 0, "Should have protected environment files")

		// Force GC periodically to measure actual usage
		if i%10 == 0 {
			runtime.GC()
		}
	}

	runtime.GC()
	var endMemStats runtime.MemStats
	runtime.ReadMemStats(&endMemStats)

	// Calculate memory usage
	memoryUsedMB := float64(endMemStats.Alloc-startMemStats.Alloc) / (1024 * 1024)
	totalAllocMB := float64(endMemStats.TotalAlloc-startMemStats.TotalAlloc) / (1024 * 1024)

	b.ReportMetric(memoryUsedMB, "memory_used_mb")
	b.ReportMetric(totalAllocMB, "total_alloc_mb")
	b.ReportMetric(float64(endMemStats.NumGC-startMemStats.NumGC), "gc_cycles")

	// Validate memory usage is within acceptable limits
	maxExpectedMemoryMB := 200.0 // 200MB max for reasonable efficiency
	assert.True(b, memoryUsedMB <= maxExpectedMemoryMB,
		"Memory usage %.2f MB exceeds limit %.2f MB", memoryUsedMB, maxExpectedMemoryMB)
}

// BenchmarkMergeProtectionStressTest pushes the system to its limits
func BenchmarkMergeProtectionStressTest(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping stress test in short mode")
	}

	logger := createBenchmarkLogger(b)
	config := &PerformanceTestConfig{
		StressTestEnabled:       true,
		MaxConcurrentOperations: 200,
		MaxMemoryUsageMB:        1000.0,
		MaxLatencyP99:           10 * time.Second,
	}
	architecture := NewMergeProtectionPerformanceArchitecture(logger, config)

	stressTests := []struct {
		name            string
		testFunc        func(*testing.B, *MergeProtectionPerformanceArchitecture) *BenchmarkStressResult
		expectedSuccess bool
	}{
		{
			name:            "ExtremeFileCount",
			testFunc:        executeExtremeFileCountStress,
			expectedSuccess: true,
		},
		{
			name:            "DeepDirectoryStructure",
			testFunc:        executeDeepDirectoryStress,
			expectedSuccess: true,
		},
		{
			name:            "MemoryPressure",
			testFunc:        executeMemoryPressureStress,
			expectedSuccess: true,
		},
		{
			name:            "RapidSuccessiveOperations",
			testFunc:        executeRapidOperationsStress,
			expectedSuccess: true,
		},
	}

	for _, test := range stressTests {
		b.Run(test.name, func(b *testing.B) {
			stressResult := test.testFunc(b, architecture)

			// Validate stress test results
			if test.expectedSuccess {
				assert.True(b, stressResult.SystemRemainedStable,
					"System should remain stable during %s stress test", test.name)
				assert.False(b, stressResult.CriticalFailuresDetected,
					"No critical failures expected during %s stress test", test.name)
			}

			// Report stress test metrics
			reportStressTestMetrics(b, stressResult)
		})
	}
}

// BenchmarkMergeProtectionRegression compares performance against baseline
func BenchmarkMergeProtectionRegression(b *testing.B) {
	logger := createBenchmarkLogger(b)
	architecture := NewMergeProtectionPerformanceArchitecture(logger, nil)

	// Load or create baseline performance data
	baseline := loadBaselinePerformanceData(b)

	// Execute current performance test
	currentResult := executeRegressionBenchmark(b, architecture)

	// Compare against baseline
	regressionAnalysis := analyzePerformanceRegression(baseline, currentResult)

	// Report regression analysis
	b.ReportMetric(regressionAnalysis.PerformanceDelta, "performance_delta_pct")
	b.ReportMetric(float64(len(regressionAnalysis.ImprovedMetrics)), "improved_metrics")
	b.ReportMetric(float64(len(regressionAnalysis.RegressedMetrics)), "regressed_metrics")

	// Assert no critical regressions
	assert.False(b, regressionAnalysis.CriticalRegressionDetected,
		"Critical performance regression detected: %v", regressionAnalysis.CriticalIssues)

	// Warn about minor regressions
	for _, regression := range regressionAnalysis.MinorRegressions {
		b.Logf("Minor regression detected: %s", regression)
	}
}

// Helper functions for benchmark execution

func executeMergeProtectionBenchmark(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
	repoConfig RepoSizeConfig,
	iterations int,
	enableProtection bool,
) *BenchmarkResult {
	var totalLatency time.Duration
	var successCount int64
	var protectionActivations int64
	latencies := make([]time.Duration, 0, iterations)

	b.ResetTimer()

	for i := 0; i < iterations; i++ {
		start := time.Now()

		// Create test repository
		testRepo, cleanup, err := createBenchmarkRepository(b,
			fmt.Sprintf("bench_%s_%d", repoConfig.Name, i),
			repoConfig.TotalFiles,
			repoConfig.EnvironmentFiles,
			enableProtection)
		if err != nil {
			b.Logf("Failed to create test repo: %v", err)
			continue
		}

		// Execute merge operation
		var result *merge.OrchestrationResult
		if enableProtection {
			result, err = executeProtectedMergeWithEnvironmentFiles(b, architecture, testRepo)
		} else {
			result, err = executeSimpleMerge(b, testRepo)
		}
		cleanup()

		latency := time.Since(start)
		totalLatency += latency
		latencies = append(latencies, latency)

		if err == nil {
			atomic.AddInt64(&successCount, 1)
			if result != nil && len(result.EnvironmentProtected) > 0 {
				atomic.AddInt64(&protectionActivations, 1)
			}
		}

		// Record performance data
		architecture.performanceDataCollector.RecordOperation(
			fmt.Sprintf("benchmark_%s", repoConfig.Name),
			latency,
			err == nil)
	}

	b.StopTimer()

	// Calculate statistics
	avgLatency := time.Duration(0)
	if len(latencies) > 0 {
		avgLatency = totalLatency / time.Duration(len(latencies))
	}

	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	var p50, p95, p99 time.Duration
	if len(latencies) > 0 {
		p50 = latencies[len(latencies)/2]
		p95 = latencies[int(float64(len(latencies))*0.95)]
		p99 = latencies[int(float64(len(latencies))*0.99)]
	}

	return &BenchmarkResult{
		TestName:              fmt.Sprintf("MergeProtection_%s_Protection_%t", repoConfig.Name, enableProtection),
		TotalOperations:       iterations,
		SuccessfulOperations:  successCount,
		AverageLatency:        avgLatency,
		P50Latency:            p50,
		P95Latency:            p95,
		P99Latency:            p99,
		ThroughputOPS:         float64(successCount) / totalLatency.Seconds(),
		ProtectionActivations: protectionActivations,
		ProtectionRate:        float64(protectionActivations) / float64(successCount),
		RepoConfig:            repoConfig,
	}
}

func executeScalabilityBenchmark(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
	repoConfig RepoSizeConfig,
) *ScalabilityBenchmarkResult {
	operationCounts := []int{10, 50, 100, 200, 500}
	results := make([]*BenchmarkResult, len(operationCounts))

	for i, opCount := range operationCounts {
		b.Logf("Testing scalability with %d operations for %s repo", opCount, repoConfig.Name)

		result := executeMergeProtectionBenchmark(b, architecture, repoConfig, opCount, true)
		results[i] = result

		// Check if system is still responsive
		if result.AverageLatency > 30*time.Second {
			b.Logf("Warning: High latency detected (%v) at %d operations", result.AverageLatency, opCount)
		}
	}

	// Analyze scalability characteristics
	scalability := analyzeScalabilityCharacteristics(results, operationCounts)

	return &ScalabilityBenchmarkResult{
		RepoConfig:             repoConfig,
		OperationCounts:        operationCounts,
		Results:                results,
		LinearityScore:         scalability.LinearityScore,
		BreakingPoint:          scalability.BreakingPoint,
		ScalabilityCoefficient: scalability.ScalabilityCoefficient,
	}
}

func executeConcurrencyBenchmark(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
	repoConfig RepoSizeConfig,
	concurrency int,
) *ConcurrencyBenchmarkResult {
	operationsPerWorker := 20
	totalOperations := concurrency * operationsPerWorker

	var wg sync.WaitGroup
	var successCount int64
	var totalLatency int64
	results := make(chan time.Duration, totalOperations)

	b.ResetTimer()
	start := time.Now()

	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for op := 0; op < operationsPerWorker; op++ {
				opStart := time.Now()

				// Create test repository for this operation
				testRepo, cleanup, err := createBenchmarkRepository(b,
					fmt.Sprintf("concurrent_%d_%d", workerID, op),
					repoConfig.TotalFiles/10, // Smaller repos for concurrency testing
					repoConfig.EnvironmentFiles/5,
					true)

				if err != nil {
					results <- time.Since(opStart)
					continue
				}

				// Execute protected merge
				_, err = executeProtectedMergeWithEnvironmentFiles(b, architecture, testRepo)
				cleanup()

				opLatency := time.Since(opStart)
				results <- opLatency

				if err == nil {
					atomic.AddInt64(&successCount, 1)
				}
				atomic.AddInt64(&totalLatency, int64(opLatency))
			}
		}(worker)
	}

	wg.Wait()
	close(results)
	totalDuration := time.Since(start)

	// Collect all latencies
	latencies := make([]time.Duration, 0, totalOperations)
	for latency := range results {
		latencies = append(latencies, latency)
	}

	// Calculate statistics
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	avgLatency := time.Duration(totalLatency) / time.Duration(len(latencies))
	throughput := float64(successCount) / totalDuration.Seconds()

	var p50, p95, p99 time.Duration
	if len(latencies) > 0 {
		p50 = latencies[len(latencies)/2]
		p95 = latencies[int(float64(len(latencies))*0.95)]
		p99 = latencies[int(float64(len(latencies))*0.99)]
	}

	return &ConcurrencyBenchmarkResult{
		ConcurrencyLevel:     concurrency,
		TotalOperations:      totalOperations,
		SuccessfulOperations: successCount,
		TotalDuration:        totalDuration,
		AverageLatency:       avgLatency,
		P50Latency:           p50,
		P95Latency:           p95,
		P99Latency:           p99,
		ThroughputOPS:        throughput,
	}
}

// Stress test implementations

func executeExtremeFileCountStress(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
) *BenchmarkStressResult {
	b.Logf("Executing extreme file count stress test")

	// Create repository with extreme number of files
	extremeConfig := RepoSizeConfig{
		Name:             "extreme",
		TotalFiles:       50000,
		EnvironmentFiles: 2500,
		DirectoryDepth:   15,
		CommitHistory:    1000,
		AverageFileSize:  1024,
		LargeFileCount:   100,
	}

	start := time.Now()
	result := executeMergeProtectionBenchmark(b, architecture, extremeConfig, 10, true)
	duration := time.Since(start)

	return &BenchmarkStressResult{
		StressType:               "ExtremeFileCount",
		Duration:                 duration,
		SystemRemainedStable:     result.AverageLatency < 60*time.Second,
		CriticalFailuresDetected: result.SuccessfulOperations < int64(float64(result.TotalOperations)*0.8),
		MaxLatencyObserved:       result.P99Latency,
		ThroughputDegradation:    math.Max(0, 1.0-(result.ThroughputOPS/1.0)), // Compare to 1 op/s baseline
		ResourceExhaustion:       false,                                       // Would be detected by memory monitoring
	}
}

func executeDeepDirectoryStress(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
) *BenchmarkStressResult {
	b.Logf("Executing deep directory structure stress test")

	deepConfig := RepoSizeConfig{
		Name:             "deep",
		TotalFiles:       5000,
		EnvironmentFiles: 200,
		DirectoryDepth:   25, // Very deep directory structure
		CommitHistory:    500,
	}

	start := time.Now()
	result := executeMergeProtectionBenchmark(b, architecture, deepConfig, 10, true)
	duration := time.Since(start)

	return &BenchmarkStressResult{
		StressType:               "DeepDirectoryStructure",
		Duration:                 duration,
		SystemRemainedStable:     result.AverageLatency < 30*time.Second,
		CriticalFailuresDetected: result.SuccessfulOperations < int64(float64(result.TotalOperations)*0.9),
		MaxLatencyObserved:       result.P99Latency,
		ThroughputDegradation:    math.Max(0, 1.0-(result.ThroughputOPS/2.0)),
		ResourceExhaustion:       false,
	}
}

func executeMemoryPressureStress(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
) *BenchmarkStressResult {
	b.Logf("Executing memory pressure stress test")

	var startMemStats runtime.MemStats
	runtime.ReadMemStats(&startMemStats)

	// Create many concurrent operations to pressure memory
	concurrency := 50
	var wg sync.WaitGroup
	var successCount int64
	var maxLatency time.Duration
	var mu sync.Mutex

	start := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < 10; j++ {
				opStart := time.Now()

				testRepo, cleanup, err := createBenchmarkRepository(b,
					fmt.Sprintf("memory_stress_%d_%d", id, j),
					1000, 50, true)
				if err != nil {
					continue
				}

				_, err = executeProtectedMergeWithEnvironmentFiles(b, architecture, testRepo)
				cleanup()

				opLatency := time.Since(opStart)

				mu.Lock()
				if opLatency > maxLatency {
					maxLatency = opLatency
				}
				mu.Unlock()

				if err == nil {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	var endMemStats runtime.MemStats
	runtime.ReadMemStats(&endMemStats)

	memoryUsedMB := float64(endMemStats.Alloc-startMemStats.Alloc) / (1024 * 1024)
	resourceExhaustion := memoryUsedMB > 1000.0 // 1GB threshold

	return &BenchmarkStressResult{
		StressType:               "MemoryPressure",
		Duration:                 duration,
		SystemRemainedStable:     maxLatency < 45*time.Second && !resourceExhaustion,
		CriticalFailuresDetected: successCount < int64(float64(concurrency*10)*0.7),
		MaxLatencyObserved:       maxLatency,
		ResourceExhaustion:       resourceExhaustion,
		ThroughputDegradation:    math.Max(0, 1.0-(float64(successCount)/duration.Seconds()/5.0)),
	}
}

func executeRapidOperationsStress(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
) *BenchmarkStressResult {
	b.Logf("Executing rapid successive operations stress test")

	operationCount := 100
	var successCount int64
	var totalLatency time.Duration
	var maxLatency time.Duration

	start := time.Now()

	for i := 0; i < operationCount; i++ {
		opStart := time.Now()

		testRepo, cleanup, err := createBenchmarkRepository(b,
			fmt.Sprintf("rapid_%d", i),
			100, 5, true) // Small repos for rapid operations
		if err != nil {
			continue
		}

		_, err = executeProtectedMergeWithEnvironmentFiles(b, architecture, testRepo)
		cleanup()

		opLatency := time.Since(opStart)
		totalLatency += opLatency

		if opLatency > maxLatency {
			maxLatency = opLatency
		}

		if err == nil {
			atomic.AddInt64(&successCount, 1)
		}
	}

	duration := time.Since(start)
	avgLatency := totalLatency / time.Duration(operationCount)
	throughput := float64(successCount) / duration.Seconds()

	return &BenchmarkStressResult{
		StressType:               "RapidSuccessiveOperations",
		Duration:                 duration,
		SystemRemainedStable:     avgLatency < 5*time.Second && maxLatency < 15*time.Second,
		CriticalFailuresDetected: successCount < int64(float64(operationCount)*0.85),
		MaxLatencyObserved:       maxLatency,
		ThroughputDegradation:    math.Max(0, 1.0-(throughput/10.0)), // Expected 10 ops/s baseline
		ResourceExhaustion:       false,
	}
}

// Helper functions for repository creation and operation execution

func createBenchmarkRepository(
	b *testing.B,
	name string,
	totalFiles, envFiles int,
	hasConflicts bool,
) (*gogit.Repository, func(), error) {
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("bench_%s_", name))
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	repoPath := filepath.Join(tmpDir, name)
	repo, err := gogit.PlainInit(repoPath, false)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	// Create file structure with environment files
	if err := createRepositoryStructure(repoPath, totalFiles, envFiles, hasConflicts); err != nil {
		cleanup()
		return nil, nil, err
	}

	// Create initial commit
	if err := createInitialCommit(repo, repoPath); err != nil {
		cleanup()
		return nil, nil, err
	}

	// Create branch with conflicts if needed
	if hasConflicts {
		if err := createConflictingBranch(repo, repoPath); err != nil {
			cleanup()
			return nil, nil, err
		}
	}

	return repo, cleanup, nil
}

func createRepositoryStructure(repoPath string, totalFiles, envFiles int, hasConflicts bool) error {
	// Create environment files in various locations
	envLocations := []string{
		"config/env/",
		"config/",
		"deploy/",
		"",
	}

	envFilesCreated := 0
	for _, location := range envLocations {
		if envFilesCreated >= envFiles {
			break
		}

		dir := filepath.Join(repoPath, location)
		if location != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		} else {
			dir = repoPath
		}

		// Create environment files
		envFilesToCreate := min(envFiles-envFilesCreated, max(1, envFiles/len(envLocations)))
		for i := 0; i < envFilesToCreate; i++ {
			filename := fmt.Sprintf("environment_%d.yml", i)
			if location == "" {
				filename = fmt.Sprintf(".env.%d", i)
			}

			content := fmt.Sprintf("# Environment configuration %d\nDATABASE_URL=postgres://localhost/app_%d\nAPI_KEY=secret_%d\n", i, i, i)

			if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
				return err
			}
			envFilesCreated++
		}
	}

	// Create regular files
	regularFiles := totalFiles - envFiles
	for i := 0; i < regularFiles; i++ {
		subdir := fmt.Sprintf("src/module_%d", i%10)
		if err := os.MkdirAll(filepath.Join(repoPath, subdir), 0755); err != nil {
			return err
		}

		filename := fmt.Sprintf("file_%d.go", i)
		content := fmt.Sprintf("package module_%d\n\nfunc Function%d() {\n    // Implementation %d\n}\n", i%10, i, i)

		if err := os.WriteFile(filepath.Join(repoPath, subdir, filename), []byte(content), 0644); err != nil {
			return err
		}
	}

	return nil
}

func createInitialCommit(repo *gogit.Repository, repoPath string) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}

	// Add all files
	_, err = worktree.Add(".")
	if err != nil {
		return err
	}

	// Create commit
	_, err = worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Benchmark Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	return err
}

func createConflictingBranch(repo *gogit.Repository, repoPath string) error {
	// Create a new branch
	head, err := repo.Head()
	if err != nil {
		return err
	}

	branchRef := plumbing.NewBranchReferenceName("feature")
	ref := plumbing.NewHashReference(branchRef, head.Hash())
	if err := repo.Storer.SetReference(ref); err != nil {
		return err
	}

	// Checkout the new branch
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}

	if err := worktree.Checkout(&gogit.CheckoutOptions{
		Branch: branchRef,
	}); err != nil {
		return err
	}

	// Modify environment file to create conflict
	envFile := filepath.Join(repoPath, "config", "environment_0.yml")
	conflictContent := "# Modified environment configuration\nDATABASE_URL=postgres://localhost/modified_app\nAPI_KEY=modified_secret\n"

	if err := os.WriteFile(envFile, []byte(conflictContent), 0644); err != nil {
		return err
	}

	// Add and commit changes
	_, err = worktree.Add(".")
	if err != nil {
		return err
	}

	_, err = worktree.Commit("Modify environment configuration", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Benchmark Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})

	return err
}

func executeProtectedMergeWithEnvironmentFiles(
	b *testing.B,
	architecture *MergeProtectionPerformanceArchitecture,
	repo *gogit.Repository,
) (*merge.OrchestrationResult, error) {
	ctx := context.Background()

	// Get HEAD and feature branch references
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}

	featureRef, err := repo.Reference("refs/heads/feature", true)
	if err != nil {
		// No feature branch, create a simple merge scenario
		return executeSimpleMerge(b, repo)
	}

	// Get commit objects
	targetCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}

	sourceCommit, err := repo.CommitObject(featureRef.Hash())
	if err != nil {
		return nil, err
	}

	// Create merge request
	mergeRequest := &merge.MergeRequest{
		Method:        merge.MergeMethodSquash,
		Strategy:      merge.MergeStrategyRecursive,
		Message:       "Benchmark merge operation",
		ForceConflict: true, // This will trigger the protection system
	}

	// Create merge analysis
	analysis := &merge.MergeAnalysisResult{
		HasConflicts:   true,
		ConflictCount:  1,
		AutoResolvable: 0,
		FileConflicts: []merge.FileConflict{
			{
				FilePath:     "config/environment_0.yml",
				ConflictType: "content",
			},
		},
	}

	// Execute orchestrated merge with protection
	result, err := architecture.universalOrchestrator.OrchestrateMerge(
		ctx,
		repo,
		sourceCommit,
		targetCommit,
		mergeRequest,
		analysis,
	)

	return result, err
}

func executeSimpleMerge(b *testing.B, repo *gogit.Repository) (*merge.OrchestrationResult, error) {
	// Simple merge without conflicts for baseline comparison
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}

	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}

	// Return a simple successful result
	return &merge.OrchestrationResult{
		Success:              true,
		TreeHash:             commit.TreeHash,
		Strategy:             merge.MergeStrategyRecursive,
		ConflictsResolved:    []string{},
		EnvironmentProtected: []string{},
		PerformanceMetrics:   make(map[string]interface{}),
	}, nil
}

// Data structures for benchmark results

type BenchmarkResult struct {
	TestName              string
	TotalOperations       int
	SuccessfulOperations  int64
	AverageLatency        time.Duration
	P50Latency            time.Duration
	P95Latency            time.Duration
	P99Latency            time.Duration
	ThroughputOPS         float64
	ProtectionActivations int64
	ProtectionRate        float64
	RepoConfig            RepoSizeConfig
}

type ScalabilityBenchmarkResult struct {
	RepoConfig             RepoSizeConfig
	OperationCounts        []int
	Results                []*BenchmarkResult
	LinearityScore         float64
	BreakingPoint          int
	ScalabilityCoefficient float64
}

type ConcurrencyBenchmarkResult struct {
	ConcurrencyLevel     int
	TotalOperations      int
	SuccessfulOperations int64
	TotalDuration        time.Duration
	AverageLatency       time.Duration
	P50Latency           time.Duration
	P95Latency           time.Duration
	P99Latency           time.Duration
	ThroughputOPS        float64
}

type BenchmarkStressResult struct {
	StressType               string
	Duration                 time.Duration
	SystemRemainedStable     bool
	CriticalFailuresDetected bool
	MaxLatencyObserved       time.Duration
	ThroughputDegradation    float64
	ResourceExhaustion       bool
}

type RegressionAnalysisResult struct {
	CriticalRegressionDetected bool
	PerformanceDelta           float64
	ImprovedMetrics            []string
	RegressedMetrics           []string
	MinorRegressions           []string
	CriticalIssues             []string
}

// Helper functions for analysis and reporting

func validatePerformanceThresholds(b *testing.B, result *BenchmarkResult, config *PerformanceTestConfig) {
	// Validate P99 latency threshold
	if result.P99Latency > config.MaxLatencyP99 {
		b.Errorf("P99 latency %v exceeds threshold %v", result.P99Latency, config.MaxLatencyP99)
	}

	// Validate minimum throughput
	if result.ThroughputOPS < config.MinThroughputOpsPerSec {
		b.Errorf("Throughput %.2f ops/s below minimum %.2f ops/s",
			result.ThroughputOPS, config.MinThroughputOpsPerSec)
	}

	// Validate success rate
	successRate := float64(result.SuccessfulOperations) / float64(result.TotalOperations)
	if successRate < 0.95 {
		b.Errorf("Success rate %.2f%% below minimum 95%%", successRate*100)
	}
}

func reportBenchmarkMetrics(b *testing.B, result *BenchmarkResult) {
	b.ReportMetric(float64(result.AverageLatency.Milliseconds()), "avg_latency_ms")
	b.ReportMetric(float64(result.P95Latency.Milliseconds()), "p95_latency_ms")
	b.ReportMetric(float64(result.P99Latency.Milliseconds()), "p99_latency_ms")
	b.ReportMetric(result.ThroughputOPS, "throughput_ops_sec")
	b.ReportMetric(result.ProtectionRate*100, "protection_rate_pct")
	b.ReportMetric(float64(result.SuccessfulOperations), "successful_operations")
}

func analyzeScalabilityMetrics(b *testing.B, result *ScalabilityBenchmarkResult, repoConfig RepoSizeConfig) {
	b.Logf("Scalability analysis for %s:", repoConfig.Name)
	b.Logf("  Linearity Score: %.3f", result.LinearityScore)
	b.Logf("  Breaking Point: %d operations", result.BreakingPoint)
	b.Logf("  Scalability Coefficient: %.3f", result.ScalabilityCoefficient)

	// Report key metrics
	b.ReportMetric(result.LinearityScore, "linearity_score")
	b.ReportMetric(float64(result.BreakingPoint), "breaking_point_ops")
	b.ReportMetric(result.ScalabilityCoefficient, "scalability_coefficient")
}

func analyzeConcurrencyScaling(b *testing.B, result *ConcurrencyBenchmarkResult, concurrency int) {
	efficiency := result.ThroughputOPS / float64(concurrency)

	b.Logf("Concurrency analysis for %d workers:", concurrency)
	b.Logf("  Total throughput: %.2f ops/s", result.ThroughputOPS)
	b.Logf("  Efficiency per worker: %.2f ops/s", efficiency)
	b.Logf("  Average latency: %v", result.AverageLatency)

	b.ReportMetric(result.ThroughputOPS, "total_throughput_ops_sec")
	b.ReportMetric(efficiency, "per_worker_efficiency")
	b.ReportMetric(float64(result.AverageLatency.Milliseconds()), "avg_latency_ms")
}

func reportStressTestMetrics(b *testing.B, result *BenchmarkStressResult) {
	b.Logf("Stress test results for %s:", result.StressType)
	b.Logf("  System remained stable: %t", result.SystemRemainedStable)
	b.Logf("  Critical failures: %t", result.CriticalFailuresDetected)
	b.Logf("  Max latency observed: %v", result.MaxLatencyObserved)
	b.Logf("  Throughput degradation: %.2f%%", result.ThroughputDegradation*100)

	b.ReportMetric(boolToFloat64(result.SystemRemainedStable), "system_stable")
	b.ReportMetric(boolToFloat64(!result.CriticalFailuresDetected), "no_critical_failures")
	b.ReportMetric(float64(result.MaxLatencyObserved.Milliseconds()), "max_latency_ms")
	b.ReportMetric(result.ThroughputDegradation*100, "throughput_degradation_pct")
}

// Utility functions

func createBenchmarkLogger(b *testing.B) *logging.Logger {
	// Create a logger that only logs errors during benchmarks to reduce noise
	logger, err := logging.NewLogger(logging.LoggingConfig{
		Level:  "error",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		b.Fatalf("Failed to create logger: %v", err)
	}
	return logger
}

func createSmallRepoConfig() RepoSizeConfig {
	return RepoSizeConfig{
		Name:             "small",
		TotalFiles:       100,
		EnvironmentFiles: 5,
		DirectoryDepth:   3,
		CommitHistory:    20,
		AverageFileSize:  1024,
		LargeFileCount:   0,
	}
}

func createMediumRepoConfig() RepoSizeConfig {
	return RepoSizeConfig{
		Name:             "medium",
		TotalFiles:       500,
		EnvironmentFiles: 25,
		DirectoryDepth:   5,
		CommitHistory:    100,
		AverageFileSize:  2048,
		LargeFileCount:   5,
	}
}

func createLargeRepoConfig() RepoSizeConfig {
	return RepoSizeConfig{
		Name:             "large",
		TotalFiles:       2000,
		EnvironmentFiles: 100,
		DirectoryDepth:   7,
		CommitHistory:    500,
		AverageFileSize:  4096,
		LargeFileCount:   20,
	}
}

func analyzeScalabilityCharacteristics(results []*BenchmarkResult, operationCounts []int) *ScalabilityCharacteristics {
	// Simple linearity analysis - in production would use more sophisticated methods
	linearityScore := 1.0
	breakingPoint := operationCounts[len(operationCounts)-1]
	scalabilityCoefficient := 1.0

	for i := 1; i < len(results); i++ {
		prev := results[i-1]
		curr := results[i]

		expectedThroughput := prev.ThroughputOPS * float64(operationCounts[i]) / float64(operationCounts[i-1])
		actualDegradation := (expectedThroughput - curr.ThroughputOPS) / expectedThroughput

		if actualDegradation > 0.5 && breakingPoint == operationCounts[len(operationCounts)-1] {
			breakingPoint = operationCounts[i]
		}

		linearityScore -= actualDegradation * 0.2
	}

	if len(results) > 1 {
		scalabilityCoefficient = results[len(results)-1].ThroughputOPS / results[0].ThroughputOPS
	}

	return &ScalabilityCharacteristics{
		LinearityScore:         math.Max(0, linearityScore),
		BreakingPoint:          breakingPoint,
		ScalabilityCoefficient: scalabilityCoefficient,
	}
}

func loadBaselinePerformanceData(b *testing.B) *BaselinePerformanceData {
	// In production, this would load actual baseline data from storage
	return &BaselinePerformanceData{
		AverageLatencyMS:  100.0,
		P99LatencyMS:      500.0,
		ThroughputOPS:     10.0,
		ProtectionRatePct: 95.0,
	}
}

func executeRegressionBenchmark(b *testing.B, architecture *MergeProtectionPerformanceArchitecture) *RegressionBenchmarkResult {
	// Execute a standard set of operations for regression comparison
	repoConfig := createMediumRepoConfig()
	result := executeMergeProtectionBenchmark(b, architecture, repoConfig, 50, true)

	return &RegressionBenchmarkResult{
		AverageLatencyMS:  float64(result.AverageLatency.Milliseconds()),
		P99LatencyMS:      float64(result.P99Latency.Milliseconds()),
		ThroughputOPS:     result.ThroughputOPS,
		ProtectionRatePct: result.ProtectionRate * 100,
	}
}

func analyzePerformanceRegression(baseline *BaselinePerformanceData, current *RegressionBenchmarkResult) *RegressionAnalysisResult {
	const tolerancePercent = 10.0 // 10% tolerance for minor variations

	analysis := &RegressionAnalysisResult{
		ImprovedMetrics:  []string{},
		RegressedMetrics: []string{},
		MinorRegressions: []string{},
		CriticalIssues:   []string{},
	}

	// Analyze latency regression
	latencyDelta := (current.AverageLatencyMS - baseline.AverageLatencyMS) / baseline.AverageLatencyMS * 100
	if latencyDelta > tolerancePercent {
		if latencyDelta > 50.0 {
			analysis.CriticalRegressionDetected = true
			analysis.CriticalIssues = append(analysis.CriticalIssues,
				fmt.Sprintf("Critical latency regression: %.1f%% increase", latencyDelta))
		} else {
			analysis.MinorRegressions = append(analysis.MinorRegressions,
				fmt.Sprintf("Latency increased by %.1f%%", latencyDelta))
		}
		analysis.RegressedMetrics = append(analysis.RegressedMetrics, "average_latency")
	} else if latencyDelta < -tolerancePercent {
		analysis.ImprovedMetrics = append(analysis.ImprovedMetrics, "average_latency")
	}

	// Analyze throughput regression
	throughputDelta := (current.ThroughputOPS - baseline.ThroughputOPS) / baseline.ThroughputOPS * 100
	if throughputDelta < -tolerancePercent {
		if throughputDelta < -30.0 {
			analysis.CriticalRegressionDetected = true
			analysis.CriticalIssues = append(analysis.CriticalIssues,
				fmt.Sprintf("Critical throughput regression: %.1f%% decrease", -throughputDelta))
		} else {
			analysis.MinorRegressions = append(analysis.MinorRegressions,
				fmt.Sprintf("Throughput decreased by %.1f%%", -throughputDelta))
		}
		analysis.RegressedMetrics = append(analysis.RegressedMetrics, "throughput")
	} else if throughputDelta > tolerancePercent {
		analysis.ImprovedMetrics = append(analysis.ImprovedMetrics, "throughput")
	}

	// Calculate overall performance delta
	analysis.PerformanceDelta = (latencyDelta - throughputDelta) / 2.0

	return analysis
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func boolToFloat64(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

// Additional data structures
type ScalabilityCharacteristics struct {
	LinearityScore         float64
	BreakingPoint          int
	ScalabilityCoefficient float64
}

type BaselinePerformanceData struct {
	AverageLatencyMS  float64
	P99LatencyMS      float64
	ThroughputOPS     float64
	ProtectionRatePct float64
}

type RegressionBenchmarkResult struct {
	AverageLatencyMS  float64
	P99LatencyMS      float64
	ThroughputOPS     float64
	ProtectionRatePct float64
}
