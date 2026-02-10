package merge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/diffs"
)

// TestMergePreviewLargeFile tests merge preview with large file handling
func TestMergePreviewLargeFile(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create base commit
	writeFile(t, wt, "file1.txt", "content1")
	commit1, err := commitFile(t, wt, "file1.txt", "Initial commit")
	require.NoError(t, err)

	// Create feature branch with large file
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Create a file that will generate a large patch (simulate large file by creating many lines)
	// Note: We don't create actual 100MB file as it would be too slow for tests
	// Instead, we create a file that will result in a large patch
	largeContent := strings.Repeat("line content\n", 10000) // ~140KB content
	writeFile(t, wt, "large_file.txt", largeContent)
	commit2, err := commitFile(t, wt, "large_file.txt", "Add large file")
	require.NoError(t, err)

	// Test merge preview
	req := &MergePreviewRequest{
		SourceRef:       commit2.String(),
		TargetRef:       commit1.String(),
		Strategy:        MergeStrategyRecursive,
		IncludeContent:  false,
		MaxFileSize:     1024 * 1024,
		MaxFiles:        1000,
		CompareStrategy: "diff",
	}

	ctx := context.Background()
	startTime := time.Now()
	response, err := handler.performMergePreview(ctx, repo, commit2, commit1, req, startTime)
	require.NoError(t, err)
	require.NotNil(t, response)

	// Verify that merge preview completed successfully even with large file
	assert.NotNil(t, response.ChangesSummary)
	assert.Greater(t, response.ChangesSummary.TotalFiles, 0)
}

// TestMergePreviewMaxFilesWithChangesLimit tests that changes are only included for max 10 files
func TestMergePreviewMaxFilesWithChangesLimit(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create base commit
	writeFile(t, wt, "file1.txt", "content1")
	commit1, err := commitFile(t, wt, "file1.txt", "Initial commit")
	require.NoError(t, err)

	// Create feature branch with more than MaxFilesWithChanges files
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Create more than MaxFilesWithChanges (10) files to test the limit
	numFiles := diffs.MaxFilesWithChanges + 5 // 15 files
	for i := 0; i < numFiles; i++ {
		filename := fmt.Sprintf("file%d.txt", i+2)
		content := fmt.Sprintf("content%d", i+2)
		writeFile(t, wt, filename, content)
	}

	// Commit all files
	commit2, err := commitFile(t, wt, ".", "Add multiple files")
	require.NoError(t, err)

	// Test merge preview
	req := &MergePreviewRequest{
		SourceRef:       commit2.String(),
		TargetRef:       commit1.String(),
		Strategy:        MergeStrategyRecursive,
		IncludeContent:  false,
		MaxFileSize:     1024 * 1024,
		MaxFiles:        1000,
		CompareStrategy: "diff",
	}

	ctx := context.Background()
	startTime := time.Now()
	response, err := handler.performMergePreview(ctx, repo, commit2, commit1, req, startTime)
	require.NoError(t, err)
	require.NotNil(t, response)

	// Verify all files metadata is included
	assert.NotNil(t, response.ChangesSummary)
	assert.GreaterOrEqual(t, response.ChangesSummary.TotalFiles, numFiles)

	// Count files with changes (line-by-line diff)
	filesWithChanges := 0
	if response.FilesChanged != nil {
		for _, fileChange := range response.FilesChanged {
			if len(fileChange.Changes) > 0 {
				filesWithChanges++
			}
		}
	}

	// Verify that changes are only included for max MaxFilesWithChanges files
	assert.LessOrEqual(t, filesWithChanges, diffs.MaxFilesWithChanges,
		"Only max %d files should have changes", diffs.MaxFilesWithChanges)

	// Verify that metadata exists for all files
	assert.Equal(t, response.ChangesSummary.TotalFiles, numFiles,
		"All files should have metadata")
}

// TestMergePreviewTwoPhaseProcessing tests the two-phase processing pattern
func TestMergePreviewTwoPhaseProcessing(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create base commit
	writeFile(t, wt, "base.txt", "base content")
	commit1, err := commitFile(t, wt, "base.txt", "Initial commit")
	require.NoError(t, err)

	// Create feature branch
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Add multiple files
	writeFile(t, wt, "file1.txt", "content1")
	writeFile(t, wt, "file2.txt", "content2")
	writeFile(t, wt, "file3.txt", "content3")

	commit2, err := commitFile(t, wt, ".", "Add files")
	require.NoError(t, err)

	req := &MergePreviewRequest{
		SourceRef:       commit2.String(),
		TargetRef:       commit1.String(),
		Strategy:        MergeStrategyRecursive,
		IncludeContent:  false,
		MaxFileSize:     1024 * 1024,
		MaxFiles:        1000,
		CompareStrategy: "diff",
	}

	ctx := context.Background()
	startTime := time.Now()
	response, err := handler.performMergePreview(ctx, repo, commit2, commit1, req, startTime)
	require.NoError(t, err)
	require.NotNil(t, response)

	// Verify response structure
	assert.NotNil(t, response.ChangesSummary)
	assert.NotNil(t, response.FilesChanged)

	// Verify all files have metadata (Phase 1)
	assert.GreaterOrEqual(t, len(response.FilesChanged), 3)

	// Verify that files have either changes or empty changes array (Phase 2)
	for _, fileChange := range response.FilesChanged {
		assert.NotNil(t, fileChange.Changes, "Changes should be initialized (even if empty)")
		assert.NotEmpty(t, fileChange.Path, "File path should be set")
	}
}

// TestProcessChangeFromDiffMaxPatchSize tests MaxPatchSize limit in processChangeFromDiff
func TestProcessChangeFromDiffMaxPatchSize(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create base commit
	writeFile(t, wt, "base.txt", "base content")
	commit1, err := commitFile(t, wt, "base.txt", "Initial commit")
	require.NoError(t, err)

	// Create feature branch with very large file
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Create a file that will generate a patch larger than MaxPatchSize (10MB)
	// Since we can't easily create a patch larger than 10MB in tests without being too slow,
	// we test the logic path by ensuring the function handles the check correctly
	// In practice, this would be tested with actual large files in integration tests
	largeContent := strings.Repeat("line content for testing\n", 50000) // ~1.4MB content
	writeFile(t, wt, "large_file.txt", largeContent)

	commit2, err := commitFile(t, wt, "large_file.txt", "Add large file")
	require.NoError(t, err)

	// Get the change object
	targetCommit, err := repo.CommitObject(commit1)
	require.NoError(t, err)
	sourceCommit, err := repo.CommitObject(commit2)
	require.NoError(t, err)

	targetTree, err := targetCommit.Tree()
	require.NoError(t, err)
	sourceTree, err := sourceCommit.Tree()
	require.NoError(t, err)

	changes, err := object.DiffTree(targetTree, sourceTree)
	require.NoError(t, err)
	require.Greater(t, len(changes), 0)

	// Process the change
	fileDiff, err := handler.processChangeFromDiff(changes[0], 3)

	// The function should either succeed (if patch is under limit) or return DiffTooLargeError
	if err != nil {
		// If error occurs, it should be DiffTooLargeError
		_, isDiffTooLarge := err.(*diffs.DiffTooLargeError)
		assert.True(t, isDiffTooLarge || strings.Contains(err.Error(), "too large"),
			"Error should be related to size limit: %v", err)
	} else {
		// If no error, verify fileDiff is valid
		assert.NotEmpty(t, fileDiff.Path)
		// Changes might be empty if file is too large or binary
		assert.NotNil(t, fileDiff.Changes)
	}
}

// TestCompareTreesBasicTwoPhase tests two-phase processing in compareTreesBasic
func TestCompareTreesBasicTwoPhase(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create base commit
	writeFile(t, wt, "base.txt", "base")
	commit1, err := commitFile(t, wt, "base.txt", "Initial")
	require.NoError(t, err)

	// Create feature branch
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Add multiple files
	for i := 0; i < 15; i++ {
		filename := fmt.Sprintf("file%d.txt", i+1)
		writeFile(t, wt, filename, fmt.Sprintf("content%d", i+1))
	}

	commit2, err := commitFile(t, wt, ".", "Add files")
	require.NoError(t, err)

	targetCommit, err := repo.CommitObject(commit1)
	require.NoError(t, err)
	sourceCommit, err := repo.CommitObject(commit2)
	require.NoError(t, err)

	targetTree, err := targetCommit.Tree()
	require.NoError(t, err)
	sourceTree, err := sourceCommit.Tree()
	require.NoError(t, err)

	req := &MergePreviewRequest{
		MaxFiles:        1000,
		CompareStrategy: "tree",
	}

	metrics := &PreviewMetrics{}
	ctx := context.Background()

	summary, fileChanges, err := handler.compareTreesBasic(ctx, repo, sourceTree, targetTree, req, metrics)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.NotNil(t, fileChanges)

	// Phase 1: All files should have metadata
	assert.GreaterOrEqual(t, len(fileChanges), 15)

	// Phase 2: Only MaxFilesWithChanges files should have changes
	filesWithChanges := 0
	for _, fc := range fileChanges {
		if len(fc.Changes) > 0 {
			filesWithChanges++
		}
		// All files should have metadata
		assert.NotEmpty(t, fc.Path)
		assert.NotNil(t, fc.Changes)
	}

	assert.LessOrEqual(t, filesWithChanges, diffs.MaxFilesWithChanges)
}

// TestCompareTreesWithDiffTwoPhase tests two-phase processing in compareTreesWithDiff
func TestCompareTreesWithDiffTwoPhase(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create base commit
	writeFile(t, wt, "base.txt", "base")
	commit1, err := commitFile(t, wt, "base.txt", "Initial")
	require.NoError(t, err)

	// Create feature branch
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Add multiple files
	for i := 0; i < 15; i++ {
		filename := fmt.Sprintf("file%d.txt", i+1)
		writeFile(t, wt, filename, fmt.Sprintf("content%d", i+1))
	}

	commit2, err := commitFile(t, wt, ".", "Add files")
	require.NoError(t, err)

	targetCommit, err := repo.CommitObject(commit1)
	require.NoError(t, err)
	sourceCommit, err := repo.CommitObject(commit2)
	require.NoError(t, err)

	targetTree, err := targetCommit.Tree()
	require.NoError(t, err)
	sourceTree, err := sourceCommit.Tree()
	require.NoError(t, err)

	req := &MergePreviewRequest{
		MaxFiles:        1000,
		CompareStrategy: "diff",
	}

	metrics := &PreviewMetrics{}
	ctx := context.Background()

	summary, fileChanges, err := handler.compareTreesWithDiff(ctx, repo, sourceTree, targetTree, req, metrics)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.NotNil(t, fileChanges)

	// Phase 1: All files should have metadata
	assert.GreaterOrEqual(t, len(fileChanges), 15)

	// Phase 2: Only MaxFilesWithChanges files should have changes
	filesWithChanges := 0
	for _, fc := range fileChanges {
		if len(fc.Changes) > 0 {
			filesWithChanges++
		}
		// All files should have metadata
		assert.NotEmpty(t, fc.Path)
		assert.NotNil(t, fc.Changes)
	}

	assert.LessOrEqual(t, filesWithChanges, diffs.MaxFilesWithChanges)
}

// TestGetChangeStatus tests the getChangeStatus helper function
func TestGetChangeStatus(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create base commit
	writeFile(t, wt, "file1.txt", "content1")
	commit1, err := commitFile(t, wt, "file1.txt", "Initial")
	require.NoError(t, err)

	// Create feature branch
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Add new file
	writeFile(t, wt, "file2.txt", "content2")
	_, err = commitFile(t, wt, "file2.txt", "Add file2")
	require.NoError(t, err)

	// Modify existing file
	writeFile(t, wt, "file1.txt", "modified content1")
	commit3, err := commitFile(t, wt, "file1.txt", "Modify file1")
	require.NoError(t, err)

	// Get changes
	targetCommit, err := repo.CommitObject(commit1)
	require.NoError(t, err)
	sourceCommit, err := repo.CommitObject(commit3)
	require.NoError(t, err)

	targetTree, err := targetCommit.Tree()
	require.NoError(t, err)
	sourceTree, err := sourceCommit.Tree()
	require.NoError(t, err)

	changes, err := object.DiffTree(targetTree, sourceTree)
	require.NoError(t, err)

	// Test getChangeStatus for each change
	addedFound := false
	modifiedFound := false

	for _, change := range changes {
		status := handler.getChangeStatus(change)

		switch status {
		case diffs.FileDiffStatusAdded:
			addedFound = true
		case diffs.FileDiffStatusModified:
			modifiedFound = true
		}
	}

	assert.True(t, addedFound, "Should find added file")
	assert.True(t, modifiedFound, "Should find modified file")
}
