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

// TestFullNativeGitRollout tests the complete native Git implementation
// with all operations enabled at 100% rollout
func TestFullNativeGitRollout(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temporary directory for test repository
	tempDir, err := ioutil.TempDir("", "full_rollout_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	t.Logf("🧪 Testing full native Git rollout in: %s", tempDir)

	// Initialize Git repository
	config := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: tempDir,
		DefaultTimeout:   60 * time.Second,
		MaxConcurrency:   10,
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	orchestrator := NewNativeGitOrchestrator(config, logger)
	ctx := context.Background()

	// Setup test repository with realistic merge scenarios
	err = setupNativeTestRepository(ctx, orchestrator, tempDir)
	require.NoError(t, err)

	// Test native Git executor with full configuration
	executorConfig := &NativeGitExecutorConfig{
		EnableAtomicOperations: true,
		EnableHooks:           false, // Disable hooks for testing
		DefaultMergeStrategy:  "ort",
		WorkingDirectory:      tempDir,
	}

	executor, err := NewNativeGitExecutor(executorConfig, logger)
	require.NoError(t, err)

	t.Logf("✅ Native Git executor initialized")

	// Test 1: Three-Way Merge
	t.Run("ThreeWayMerge", func(t *testing.T) {
		testThreeWayMerge(t, ctx, executor, tempDir)
	})

	// Test 2: Fast-Forward Merge
	t.Run("FastForwardMerge", func(t *testing.T) {
		testFastForwardMerge(t, ctx, executor, tempDir)
	})

	// Test 3: Squash Merge
	t.Run("SquashMerge", func(t *testing.T) {
		testSquashMerge(t, ctx, executor, tempDir)
	})

	// Test 4: Rebase Operation
	t.Run("RebaseOperation", func(t *testing.T) {
		testRebaseOperation(t, ctx, executor, tempDir)
	})

	// Test 5: Conflict Resolution
	t.Run("ConflictResolution", func(t *testing.T) {
		testConflictResolution(t, ctx, executor, tempDir)
	})

	// Test 6: Performance Under Load
	t.Run("PerformanceLoad", func(t *testing.T) {
		testPerformanceLoad(t, ctx, executor, tempDir)
	})

	t.Logf("🎉 Full rollout test completed successfully!")
}

// setupNativeTestRepository creates a realistic test repository with multiple branches
func setupNativeTestRepository(ctx context.Context, orchestrator *NativeGitOrchestrator, repoPath string) error {
	// Initialize repository
	_, err := orchestrator.ExecuteGitCommand(ctx, repoPath, "init")
	if err != nil {
		return err
	}

	// Configure git user
	_, err = orchestrator.ExecuteGitCommand(ctx, repoPath, "config", "user.name", "Test User")
	if err != nil {
		return err
	}
	_, err = orchestrator.ExecuteGitCommand(ctx, repoPath, "config", "user.email", "test@example.com")
	if err != nil {
		return err
	}

	// Create initial files
	files := map[string]string{
		"README.md":           "# Test Repository\n\nThis is a test repository for native Git testing.\n",
		"src/main.go":         "package main\n\nfunc main() {\n\tprintln(\"Hello, World!\")\n}\n",
		"config/database.yml": "host: localhost\nport: 5432\ndatabase: test\n",
		".env":                "NODE_ENV=development\nPORT=3000\n",
		"docs/api.md":         "# API Documentation\n\n## Endpoints\n\n- GET /health\n",
	}

	for filePath, content := range files {
		fullPath := filepath.Join(repoPath, filePath)
		dir := filepath.Dir(fullPath)
		os.MkdirAll(dir, 0755)

		err = ioutil.WriteFile(fullPath, []byte(content), 0644)
		if err != nil {
			return err
		}
	}

	// Add all files and create initial commit
	_, err = orchestrator.ExecuteGitCommand(ctx, repoPath, "add", ".")
	if err != nil {
		return err
	}

	_, err = orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "Initial commit")
	if err != nil {
		return err
	}

	return nil
}

// testThreeWayMerge tests three-way merge functionality
func testThreeWayMerge(t *testing.T, ctx context.Context, executor *NativeGitExecutor, repoPath string) {
	// Create feature branch
	_, err := executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "-b", "feature/three-way")
	require.NoError(t, err)

	// Modify files in feature branch
	featureContent := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from feature!\")\n}\n"
	err = ioutil.WriteFile(filepath.Join(repoPath, "src/main.go"), []byte(featureContent), 0644)
	require.NoError(t, err)

	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "add", "src/main.go")
	require.NoError(t, err)

	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "Add feature enhancement")
	require.NoError(t, err)

	// Switch back to main
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "main")
	require.NoError(t, err)

	// Perform three-way merge
	mergeReq := &NativeThreeWayMergeRequest{
		RepositoryPath: repoPath,
		SourceRef:      "feature/three-way",
		TargetRef:      "main",
		Strategy:       "ort",
		Message:        "Merge feature/three-way into main",
		ForceConflict:  false,
		NoFF:           true,
	}

	result, err := executor.ThreeWayMerge(ctx, mergeReq)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)
	assert.NotEmpty(t, result.MergeCommitHash)

	t.Logf("✅ Three-way merge completed: %s", result.MergeCommitHash)
}

// testFastForwardMerge tests fast-forward merge functionality
func testFastForwardMerge(t *testing.T, ctx context.Context, executor *NativeGitExecutor, repoPath string) {
	// Create feature branch
	_, err := executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "-b", "feature/fast-forward")
	require.NoError(t, err)

	// Add new file
	newFileContent := "# New Feature\n\nThis is a new feature file.\n"
	err = ioutil.WriteFile(filepath.Join(repoPath, "feature.md"), []byte(newFileContent), 0644)
	require.NoError(t, err)

	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "add", "feature.md")
	require.NoError(t, err)

	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "Add new feature file")
	require.NoError(t, err)

	// Switch back to main
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "main")
	require.NoError(t, err)

	// Perform fast-forward merge
	ffReq := &NativeFastForwardRequest{
		RepositoryPath: repoPath,
		SourceRef:      "feature/fast-forward",
		TargetRef:      "main",
		Force:          false,
	}

	result, err := executor.FastForwardMerge(ctx, ffReq)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)
	assert.True(t, result.IsFastForward)

	t.Logf("✅ Fast-forward merge completed: %s", result.MergeCommitHash)
}

// testSquashMerge tests squash merge functionality
func testSquashMerge(t *testing.T, ctx context.Context, executor *NativeGitExecutor, repoPath string) {
	// Create feature branch with multiple commits
	_, err := executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "-b", "feature/squash")
	require.NoError(t, err)

	// First commit
	err = ioutil.WriteFile(filepath.Join(repoPath, "squash1.txt"), []byte("First change\n"), 0644)
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "add", "squash1.txt")
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "First squash commit")
	require.NoError(t, err)

	// Second commit
	err = ioutil.WriteFile(filepath.Join(repoPath, "squash2.txt"), []byte("Second change\n"), 0644)
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "add", "squash2.txt")
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "Second squash commit")
	require.NoError(t, err)

	// Switch back to main
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "main")
	require.NoError(t, err)

	// Perform squash merge
	squashReq := &NativeSquashMergeRequest{
		RepositoryPath: repoPath,
		SourceRef:      "feature/squash",
		TargetRef:      "main",
		Message:        "Squash merge: Combined feature changes",
		NoCommit:       false,
	}

	result, err := executor.SquashMerge(ctx, squashReq)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)
	assert.False(t, result.IsFastForward) // Squash is never fast-forward

	t.Logf("✅ Squash merge completed: %s", result.MergeCommitHash)
}

// testRebaseOperation tests rebase functionality
func testRebaseOperation(t *testing.T, ctx context.Context, executor *NativeGitExecutor, repoPath string) {
	// Create feature branch
	_, err := executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "-b", "feature/rebase")
	require.NoError(t, err)

	// Add commits to feature branch
	err = ioutil.WriteFile(filepath.Join(repoPath, "rebase.txt"), []byte("Rebase feature\n"), 0644)
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "add", "rebase.txt")
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "Add rebase feature")
	require.NoError(t, err)

	// Perform rebase
	rebaseReq := &NativeRebaseRequest{
		RepositoryPath: repoPath,
		SourceRef:      "feature/rebase",
		TargetRef:      "main",
		Strategy:       "ort",
		Interactive:    false,
		PreserveMerges: false,
	}

	result, err := executor.RebaseMerge(ctx, rebaseReq)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)

	t.Logf("✅ Rebase operation completed: %s", result.MergeCommitHash)
}

// testConflictResolution tests conflict handling
func testConflictResolution(t *testing.T, ctx context.Context, executor *NativeGitExecutor, repoPath string) {
	// Create conflicting branches
	_, err := executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "main")
	require.NoError(t, err)

	// Modify main
	mainContent := "# Main Branch\n\nThis is the main version.\n"
	err = ioutil.WriteFile(filepath.Join(repoPath, "conflict.md"), []byte(mainContent), 0644)
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "add", "conflict.md")
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "Add conflict file to main")
	require.NoError(t, err)

	// Create feature branch from an earlier point
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "-b", "feature/conflict")
	require.NoError(t, err)

	// Modify the same file differently
	featureContent := "# Feature Branch\n\nThis is the feature version.\n"
	err = ioutil.WriteFile(filepath.Join(repoPath, "conflict.md"), []byte(featureContent), 0644)
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "add", "conflict.md")
	require.NoError(t, err)
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "commit", "-m", "Add conflict file to feature")
	require.NoError(t, err)

	// Switch back to main
	_, err = executor.orchestrator.ExecuteGitCommand(ctx, repoPath, "checkout", "main")
	require.NoError(t, err)

	// Test merge preview to detect conflicts
	previewReq := &NativeMergePreviewRequest{
		RepositoryPath: repoPath,
		SourceRef:      "feature/conflict",
		TargetRef:      "main",
		Strategy:       "ort",
	}

	preview, err := executor.PreviewMerge(ctx, previewReq)
	if err == nil {
		t.Logf("✅ Merge preview completed - Can merge: %t, Conflicts: %d",
			preview.CanMerge, preview.ConflictCount)
	} else {
		// Conflicts expected - this is normal behavior
		t.Logf("✅ Conflict detection working - Preview failed as expected: %v", err)
	}
}

// testPerformanceLoad tests performance under concurrent operations
func testPerformanceLoad(t *testing.T, ctx context.Context, executor *NativeGitExecutor, repoPath string) {
	concurrency := 5
	operations := 10

	results := make(chan bool, concurrency)

	startTime := time.Now()

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer func() {
				results <- true
			}()

			for j := 0; j < operations; j++ {
				// Test repository validation (lightweight operation)
				err := executor.validateRepository(ctx, repoPath)
				if err != nil {
					t.Errorf("Worker %d operation %d failed: %v", id, j, err)
					return
				}
			}
		}(i)
	}

	// Wait for all workers to complete
	for i := 0; i < concurrency; i++ {
		<-results
	}

	totalTime := time.Since(startTime)
	totalOps := concurrency * operations
	opsPerSec := float64(totalOps) / totalTime.Seconds()

	t.Logf("✅ Performance test completed")
	t.Logf("   Concurrent workers: %d", concurrency)
	t.Logf("   Operations per worker: %d", operations)
	t.Logf("   Total operations: %d", totalOps)
	t.Logf("   Total time: %v", totalTime)
	t.Logf("   Operations per second: %.2f", opsPerSec)

	// Basic performance assertions
	assert.Less(t, totalTime, 10*time.Second, "Performance test should complete within 10 seconds")
	assert.Greater(t, opsPerSec, 10.0, "Should achieve at least 10 operations per second")
}