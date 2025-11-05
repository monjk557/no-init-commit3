package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/protocol"
)

func TestMergePreviewIntegration(t *testing.T) {
	// Setup test repository
	repoPath, cleanup := setupTestRepository(t)
	defer cleanup()

	// Setup handler
	handler := setupMergePreviewHandler(t, repoPath)

	// Test cases
	t.Run("NormalMergeWithFileChanges", func(t *testing.T) {
		testNormalMergeWithFileChanges(t, handler)
	})

	t.Run("MergePreviewCounts", func(t *testing.T) {
		testMergePreviewCounts(t, handler)
	})

	t.Run("FileEditSituations", func(t *testing.T) {
		testFileEditSituations(t, handler)
	})
}

func testNormalMergeWithFileChanges(t *testing.T, handler *protocol.MergePreviewHandler) {
	// Create test scenario: main branch with feature branch
	// This will be tested via API call simulation

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/repos/:owner/:repo/merges/preview", handler.HandleMergePreview)

	// Test request
	req, err := http.NewRequest("GET", "/repos/test/testrepo/merges/preview?source_ref=feature&target_ref=main", nil)
	require.NoError(t, err)

	// Execute request
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Check response
	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	// Check basic response structure
	assert.Contains(t, response, "data")
	data := response["data"].(map[string]interface{})

	// Check merge preview fields
	assert.Contains(t, data, "can_merge")
	assert.Contains(t, data, "is_fast_forward")
	assert.Contains(t, data, "has_conflicts")
	assert.Contains(t, data, "conflict_count")

	// Check count summary
	assert.Contains(t, data, "count_summary")
	countSummary := data["count_summary"].(map[string]interface{})
	assert.Contains(t, countSummary, "commits")
	assert.Contains(t, countSummary, "files")
	assert.Contains(t, countSummary, "contributors")

	// Check changes summary
	assert.Contains(t, data, "changes_summary")
	changesSummary := data["changes_summary"].(map[string]interface{})
	assert.Contains(t, changesSummary, "total_files")
	assert.Contains(t, changesSummary, "added_files")
	assert.Contains(t, changesSummary, "modified_files")
	assert.Contains(t, changesSummary, "deleted_files")

	// Check contributors
	assert.Contains(t, data, "contributors")
	contributors := data["contributors"].([]interface{})
	assert.GreaterOrEqual(t, len(contributors), 0)

	// Check commits
	assert.Contains(t, data, "commits")
	commits := data["commits"].(map[string]interface{})
	assert.Contains(t, commits, "ahead")
	assert.Contains(t, commits, "behind")

	t.Logf("✅ Merge preview response structure is correct")
	t.Logf("Count Summary: %+v", countSummary)
	t.Logf("Changes Summary: %+v", changesSummary)
}

func testMergePreviewCounts(t *testing.T, handler *protocol.MergePreviewHandler) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/repos/:owner/:repo/merges/preview", handler.HandleMergePreview)

	// Test with different scenarios
	testCases := []struct {
		name        string
		sourceRef   string
		targetRef   string
		expectError bool
	}{
		{
			name:        "ValidMerge",
			sourceRef:   "feature",
			targetRef:   "main",
			expectError: false,
		},
		{
			name:        "SameBranch",
			sourceRef:   "main",
			targetRef:   "main",
			expectError: true,
		},
		{
			name:        "NonExistentBranch",
			sourceRef:   "nonexistent",
			targetRef:   "main",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET",
				fmt.Sprintf("/repos/test/testrepo/merges/preview?source_ref=%s&target_ref=%s",
					tc.sourceRef, tc.targetRef), nil)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if tc.expectError {
				assert.NotEqual(t, http.StatusOK, w.Code)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)

				var response map[string]interface{}
				err = json.Unmarshal(w.Body.Bytes(), &response)
				require.NoError(t, err)

				// Verify count summary exists and has correct structure
				data := response["data"].(map[string]interface{})
				countSummary := data["count_summary"].(map[string]interface{})

				// Check that counts are non-negative integers
				commits, ok := countSummary["commits"].(float64)
				assert.True(t, ok)
				assert.GreaterOrEqual(t, int(commits), 0)

				files, ok := countSummary["files"].(float64)
				assert.True(t, ok)
				assert.GreaterOrEqual(t, int(files), 0)

				contributors, ok := countSummary["contributors"].(float64)
				assert.True(t, ok)
				assert.GreaterOrEqual(t, int(contributors), 0)

				t.Logf("✅ %s: commits=%d, files=%d, contributors=%d",
					tc.name, int(commits), int(files), int(contributors))
			}
		})
	}
}

func testFileEditSituations(t *testing.T, handler *protocol.MergePreviewHandler) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/repos/:owner/:repo/merges/preview", handler.HandleMergePreview)

	// Test file edit scenarios
	req, err := http.NewRequest("GET", "/repos/test/testrepo/merges/preview?source_ref=feature&target_ref=main", nil)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	data := response["data"].(map[string]interface{})

	// Check files_changed array
	assert.Contains(t, data, "files_changed")
	filesChanged := data["files_changed"].([]interface{})

	// Check changes_summary consistency
	changesSummary := data["changes_summary"].(map[string]interface{})
	totalFiles := int(changesSummary["total_files"].(float64))

	// Verify that total_files matches the length of files_changed
	assert.Equal(t, totalFiles, len(filesChanged),
		"total_files should match the length of files_changed array")

	// Check each file change
	for i, fileChange := range filesChanged {
		file := fileChange.(map[string]interface{})

		// Required fields
		assert.Contains(t, file, "path")
		assert.Contains(t, file, "status")
		assert.Contains(t, file, "additions")
		assert.Contains(t, file, "deletions")

		// Status should be one of: added, modified, deleted, renamed
		status := file["status"].(string)
		validStatuses := []string{"added", "modified", "deleted", "renamed"}
		assert.Contains(t, validStatuses, status,
			"File %d status should be one of: added, modified, deleted, renamed", i)

		// Additions and deletions should be non-negative
		additions := int(file["additions"].(float64))
		deletions := int(file["deletions"].(float64))
		assert.GreaterOrEqual(t, additions, 0)
		assert.GreaterOrEqual(t, deletions, 0)

		t.Logf("File %d: %s (%s) +%d -%d",
			i, file["path"], status, additions, deletions)
	}

	// Verify count summary matches changes summary
	countSummary := data["count_summary"].(map[string]interface{})
	countFiles := int(countSummary["files"].(float64))
	assert.Equal(t, totalFiles, countFiles,
		"count_summary.files should match changes_summary.total_files")

	t.Logf("✅ File edit situations verified: %d files changed", totalFiles)
}

func setupTestRepository(t *testing.T) (string, func()) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "git-executor-test-*")
	require.NoError(t, err)

	// Create test repository structure
	repoDir := filepath.Join(tempDir, "testrepo")
	err = os.MkdirAll(repoDir, 0755)
	require.NoError(t, err)

	// Initialize git repository (non-bare for test setup)
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)

	// Create initial commit
	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create test files
	testFiles := map[string]string{
		"README.md": "# Test Repository\nThis is a test repository for merge preview testing.",
		"main.go":   "package main\n\nfunc main() {\n    println(\"Hello, World!\")\n}",
	}

	for filename, content := range testFiles {
		filePath := filepath.Join(repoDir, filename)
		err = os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = worktree.Add(filename)
		require.NoError(t, err)
	}

	// Initial commit
	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Get the default branch name (could be master or main)
	headRef, err := repo.Head()
	require.NoError(t, err)
	defaultBranchName := headRef.Name().Short()

	// Create main branch if it doesn't exist (rename default branch to main)
	if defaultBranchName != "main" {
		// Create main branch from initial commit
		mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), initialCommit)
		err = repo.Storer.SetReference(mainRef)
		require.NoError(t, err)

		// Update HEAD to point to main
		err = repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")))
		require.NoError(t, err)

		// Checkout main
		err = worktree.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName("main"),
		})
		require.NoError(t, err)
	}

	// Create feature branch
	err = worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Add feature-specific files
	featureFiles := map[string]string{
		"feature.go": "package main\n\nfunc feature() {\n    println(\"New feature!\")\n}",
		"test.txt":   "This is a test file for the feature branch.",
	}

	for filename, content := range featureFiles {
		filePath := filepath.Join(repoDir, filename)
		err = os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)

		_, err = worktree.Add(filename)
		require.NoError(t, err)
	}

	// Feature commit
	_, err = worktree.Commit("Add feature files", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Feature Author",
			Email: "feature@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Modify existing file
	mainGoPath := filepath.Join(repoDir, "main.go")
	modifiedContent := "package main\n\nfunc main() {\n    println(\"Hello, World!\")\n    println(\"Modified in feature branch\")\n}"
	err = os.WriteFile(mainGoPath, []byte(modifiedContent), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("main.go")
	require.NoError(t, err)

	// Modified file commit
	_, err = worktree.Commit("Modify main.go in feature", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Feature Author",
			Email: "feature@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Switch back to main
	err = worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	// Add some changes to main
	readmePath := filepath.Join(repoDir, "README.md")
	mainReadmeContent := "# Test Repository\nThis is a test repository for merge preview testing.\n\nUpdated in main branch."
	err = os.WriteFile(readmePath, []byte(mainReadmeContent), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	// Main branch commit
	_, err = worktree.Commit("Update README in main", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Main Author",
			Email: "main@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create owner directory structure
	ownerDir := filepath.Join(tempDir, "test")
	err = os.MkdirAll(ownerDir, 0755)
	require.NoError(t, err)

	// Move repo to owner directory with .git extension (required for API)
	finalRepoDir := filepath.Join(ownerDir, "testrepo.git")
	err = os.Rename(repoDir, finalRepoDir)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return tempDir, cleanup
}

func setupMergePreviewHandler(t *testing.T, repoRoot string) *protocol.MergePreviewHandler {
	// Create test config
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
		Logging: config.LoggingConfig{
			Level:  "debug",
			Format: "json",
		},
	}

	// Create handler
	handler, err := protocol.NewMergePreviewHandler(cfg)
	require.NoError(t, err)

	return handler
}
