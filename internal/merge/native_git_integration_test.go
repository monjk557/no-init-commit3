package merge

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// TestNativeGitOrchestrator tests the basic orchestrator functionality
func TestNativeGitOrchestrator(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: "/tmp",
		DefaultTimeout:   30 * time.Second,
		MaxConcurrency:   5,
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	orchestrator := NewNativeGitOrchestrator(config, logger)
	require.NotNil(t, orchestrator)

	// Test basic git command
	ctx := context.Background()
	result, err := orchestrator.ExecuteGitCommand(ctx, "", "--version")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, result.ExitCode)
	assert.Contains(t, string(result.Output), "git version")

	t.Logf("Git version: %s", string(result.Output))
	t.Logf("Command duration: %v", result.Duration)
}

// TestNativeGitExecutorBasics tests the executor functionality
func TestNativeGitExecutorBasics(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := &NativeGitExecutorConfig{
		EnableAtomicOperations: true,
		EnableHooks:            false, // Disable hooks for testing
		DefaultMergeStrategy:   "ort",
		WorkingDirectory:       "/tmp",
	}

	executor, err := NewNativeGitExecutor(config, logger)
	require.NoError(t, err)
	require.NotNil(t, executor)

	// Test repository validation with non-existent repo
	ctx := context.Background()
	err = executor.validateRepository(ctx, "/non/existent/path")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

// TestNativeGitWithTestRepository creates a test repository and performs operations
func TestNativeGitWithTestRepository(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temporary directory
	tempDir, err := ioutil.TempDir("", "native_git_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Initialize git repository
	config := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: tempDir,
		DefaultTimeout:   30 * time.Second,
		MaxConcurrency:   5,
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	orchestrator := NewNativeGitOrchestrator(config, logger)
	ctx := context.Background()

	// Initialize repository
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "init")
	require.NoError(t, err)

	// Configure git user
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "config", "user.name", "Test User")
	require.NoError(t, err)
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "config", "user.email", "test@example.com")
	require.NoError(t, err)

	// Create initial file and commit
	testFile := filepath.Join(tempDir, "test.txt")
	err = ioutil.WriteFile(testFile, []byte("initial content\n"), 0644)
	require.NoError(t, err)

	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "add", "test.txt")
	require.NoError(t, err)

	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "commit", "-m", "Initial commit")
	require.NoError(t, err)

	// Create a branch
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "checkout", "-b", "feature-branch")
	require.NoError(t, err)

	// Modify file in feature branch
	err = ioutil.WriteFile(testFile, []byte("initial content\nfeature change\n"), 0644)
	require.NoError(t, err)

	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "add", "test.txt")
	require.NoError(t, err)

	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "commit", "-m", "Feature commit")
	require.NoError(t, err)

	// Switch back to main
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "checkout", "main")
	require.NoError(t, err)

	// Test native executor with this repository
	executorConfig := &NativeGitExecutorConfig{
		EnableAtomicOperations: true,
		EnableHooks:            false,
		DefaultMergeStrategy:   "ort",
		WorkingDirectory:       tempDir,
	}

	executor, err := NewNativeGitExecutor(executorConfig, logger)
	require.NoError(t, err)

	// Test repository validation
	err = executor.validateRepository(ctx, tempDir)
	assert.NoError(t, err)

	// Test fast-forward check
	canFF, err := executor.checkFastForward(ctx, tempDir, "feature-branch", "main")
	assert.NoError(t, err)
	assert.True(t, canFF, "Should be able to fast-forward")

	// Test getting commit hash
	hash, err := executor.getCommitHash(ctx, tempDir, "HEAD")
	assert.NoError(t, err)
	assert.Len(t, hash, 40, "Commit hash should be 40 characters")

	t.Logf("✅ Test repository created successfully at: %s", tempDir)
	t.Logf("✅ Current HEAD: %s", hash)
	t.Logf("✅ Fast-forward possible: %t", canFF)
}

// TestNativeGitMergePreview tests merge preview functionality
func TestNativeGitMergePreview(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temporary directory
	tempDir, err := ioutil.TempDir("", "native_git_merge_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Initialize git repository with merge scenario
	config := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: tempDir,
		DefaultTimeout:   30 * time.Second,
		MaxConcurrency:   5,
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	orchestrator := NewNativeGitOrchestrator(config, logger)
	ctx := context.Background()

	// Setup repository
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "init")
	require.NoError(t, err)

	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "config", "user.name", "Test User")
	require.NoError(t, err)
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "config", "user.email", "test@example.com")
	require.NoError(t, err)

	// Create main branch content
	testFile := filepath.Join(tempDir, "config.yml")
	err = ioutil.WriteFile(testFile, []byte("key: main_value\nother: data\n"), 0644)
	require.NoError(t, err)

	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "add", ".")
	require.NoError(t, err)
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "commit", "-m", "Main commit")
	require.NoError(t, err)

	// Create feature branch
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "checkout", "-b", "feature")
	require.NoError(t, err)

	// Modify file differently
	err = ioutil.WriteFile(testFile, []byte("key: feature_value\nother: data\n"), 0644)
	require.NoError(t, err)

	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "add", ".")
	require.NoError(t, err)
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "commit", "-m", "Feature commit")
	require.NoError(t, err)

	// Switch back to main
	_, err = orchestrator.ExecuteGitCommand(ctx, tempDir, "checkout", "main")
	require.NoError(t, err)

	// Test executor preview
	executorConfig := &NativeGitExecutorConfig{
		EnableAtomicOperations: true,
		EnableHooks:            false,
		DefaultMergeStrategy:   "ort",
		WorkingDirectory:       tempDir,
	}

	executor, err := NewNativeGitExecutor(executorConfig, logger)
	require.NoError(t, err)

	// Test merge preview
	previewReq := &NativeMergePreviewRequest{
		RepositoryPath: tempDir,
		SourceRef:      "feature",
		TargetRef:      "main",
		Strategy:       "ort",
	}

	preview, err := executor.PreviewMerge(ctx, previewReq)
	assert.NoError(t, err)
	assert.NotNil(t, preview)

	t.Logf("✅ Merge preview completed")
	t.Logf("   Can merge: %t", preview.CanMerge)
	t.Logf("   Is fast-forward: %t", preview.IsFastForward)
	t.Logf("   Conflict count: %d", preview.ConflictCount)
	t.Logf("   Estimated time: %v", preview.EstimatedTime)
	t.Logf("   Recommended strategy: %s", preview.RecommendedStrategy)
}

// TestMigrationAdapterIntegration tests basic migration concepts
func TestMigrationAdapterIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Test basic migration flags logic
	flags := &MigrationFlags{
		EnableNativeGit:      true,
		EnableNativeThreeWay: true,
		EnableNativeSquash:   false,
		RolloutPercentage:    50,
		ValidateResults:      true,
		FallbackOnError:      true,
	}

	assert.True(t, flags.EnableNativeGit)
	assert.True(t, flags.EnableNativeThreeWay)
	assert.False(t, flags.EnableNativeSquash)
	assert.Equal(t, 50, flags.RolloutPercentage)

	t.Logf("✅ Migration flags structure validated")
	t.Logf("   Native Git enabled: %t", flags.EnableNativeGit)
	t.Logf("   Three-way enabled: %t", flags.EnableNativeThreeWay)
	t.Logf("   Rollout percentage: %d%%", flags.RolloutPercentage)
}

// TestPerformanceComparison runs a basic performance comparison
func TestNativeGitPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	logger := zaptest.NewLogger(t)

	// Test multiple git version calls to check performance
	config := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: "/tmp",
		DefaultTimeout:   30 * time.Second,
		MaxConcurrency:   10,
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	orchestrator := NewNativeGitOrchestrator(config, logger)
	ctx := context.Background()

	numOperations := 5
	totalDuration := time.Duration(0)

	for i := 0; i < numOperations; i++ {
		start := time.Now()
		result, err := orchestrator.ExecuteGitCommand(ctx, "", "--version")
		duration := time.Since(start)

		assert.NoError(t, err)
		assert.Equal(t, 0, result.ExitCode)

		totalDuration += duration
		t.Logf("Operation %d: %v", i+1, duration)
	}

	avgDuration := totalDuration / time.Duration(numOperations)
	t.Logf("✅ Performance test completed")
	t.Logf("   Operations: %d", numOperations)
	t.Logf("   Total time: %v", totalDuration)
	t.Logf("   Average time: %v", avgDuration)
	t.Logf("   Ops/second: %.2f", float64(numOperations)/totalDuration.Seconds())

	// Basic performance assertions
	assert.Less(t, avgDuration, 1*time.Second, "Average operation should be under 1 second")
}

// TestCircuitBreakerFunctionality tests the circuit breaker
func TestCircuitBreakerFunctionality(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: "/tmp",
		DefaultTimeout:   1 * time.Second, // Short timeout for testing
		MaxConcurrency:   1,
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	orchestrator := NewNativeGitOrchestrator(config, logger)
	ctx := context.Background()

	// Test that circuit breaker starts closed
	assert.False(t, orchestrator.circuitBreaker.ShouldBlock())

	// Test successful operation
	result, err := orchestrator.ExecuteGitCommand(ctx, "", "--version")
	assert.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.False(t, orchestrator.circuitBreaker.ShouldBlock())

	t.Logf("✅ Circuit breaker test completed")
	t.Logf("   Circuit breaker state: %s", orchestrator.circuitBreaker.state)
	t.Logf("   Failure count: %d", orchestrator.circuitBreaker.failures)
}

// TestResourcePoolLimits tests the resource pool functionality
func TestResourcePoolLimits(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: "/tmp",
		DefaultTimeout:   30 * time.Second,
		MaxConcurrency:   2, // Limited concurrency for testing
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	orchestrator := NewNativeGitOrchestrator(config, logger)

	// Test that we can acquire resources up to the limit
	opCtx1 := orchestrator.createOperationContext("", "--version")
	err := orchestrator.resourcePool.AcquireResources(opCtx1)
	assert.NoError(t, err)

	opCtx2 := orchestrator.createOperationContext("", "--version")
	err = orchestrator.resourcePool.AcquireResources(opCtx2)
	assert.NoError(t, err)

	// Third acquisition should fail (pool exhausted)
	opCtx3 := orchestrator.createOperationContext("", "--version")
	err = orchestrator.resourcePool.AcquireResources(opCtx3)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "resource pool exhausted")

	// Release resources
	orchestrator.resourcePool.ReleaseResources(opCtx1)
	orchestrator.resourcePool.ReleaseResources(opCtx2)

	// Now acquisition should work again
	err = orchestrator.resourcePool.AcquireResources(opCtx3)
	assert.NoError(t, err)
	orchestrator.resourcePool.ReleaseResources(opCtx3)

	t.Logf("✅ Resource pool test completed")
	t.Logf("   Max concurrency: %d", config.MaxConcurrency)
	t.Logf("   Resource pool working correctly")
}
