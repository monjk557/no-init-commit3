package overview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// setupLargeRepository creates a repository with at least 100MB of content
func setupLargeRepository(t *testing.T, repoPath string, targetSizeMB int) {
	// Initialize bare repository
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// Create multiple large files to reach target size
	// Each file will be approximately 10MB
	fileSize := 10 * 1024 * 1024 // 10MB per file
	numFiles := (targetSizeMB / 10) + 1

	t.Logf("Creating repository with %d files of ~10MB each (target: %dMB)", numFiles, targetSizeMB)

	// Create a large blob content (10MB of data)
	largeContent := make([]byte, fileSize)
	for i := range largeContent {
		largeContent[i] = byte(i % 256) // Fill with pattern
	}

	// Create tree entries for all files
	treeEntries := make([]object.TreeEntry, 0, numFiles)

	for i := 0; i < numFiles; i++ {
		// Create blob
		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(largeContent)))
		writer, err := blob.Writer()
		require.NoError(t, err)
		_, err = writer.Write(largeContent)
		require.NoError(t, err)
		_ = writer.Close()

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(t, err)

		// Add to tree
		fileName := fmt.Sprintf("large_file_%d.bin", i)
		treeEntries = append(treeEntries, object.TreeEntry{
			Name: fileName,
			Mode: filemode.Regular,
			Hash: blobHash,
		})
	}

	// Create tree with all files
	tree := &object.Tree{
		Entries: treeEntries,
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create initial commit
	commit := &object.Commit{
		Message: fmt.Sprintf("Initial commit with %d large files", numFiles),
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		TreeHash: treeHash,
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create and update master branch reference
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = repo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	// Update HEAD to be a symbolic reference to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)

	// Verify repository size
	repoSize := getRepositorySize(t, repoPath)
	t.Logf("Repository created with size: %.2f MB", float64(repoSize)/(1024*1024))
	require.GreaterOrEqual(t, repoSize, int64(targetSizeMB*1024*1024), "Repository should be at least %dMB", targetSizeMB)
}

// getRepositorySize calculates the total size of a repository directory
func getRepositorySize(t *testing.T, repoPath string) int64 {
	var size int64
	err := filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	require.NoError(t, err)
	return size
}

// setupTestHandlerForPerf creates a test handler for performance testing
func setupTestHandlerForPerf(t *testing.T) (*RepositoryOverviewHandler, string, func()) {
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "overview-perf-test-*")
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "warn", // Reduce logging for performance tests
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Events: config.EventsConfig{
			Enabled: false, // Disable events for testing
		},
	}

	handler, err := NewRepositoryOverviewHandler(cfg)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return handler, tempDir, cleanup
}

// TestOverviewAPI_Performance_LargeRepository tests overview API performance with 100MB+ repository
func TestOverviewAPI_Performance_LargeRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	handler, tempDir, cleanup := setupTestHandlerForPerf(t)
	defer cleanup()

	// Create large repository (100MB)
	owner := "testowner"
	repo := "large-repo"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	t.Log("Creating 100MB repository (this may take a while)...")
	startSetup := time.Now()
	setupLargeRepository(t, repoPath, 100)
	setupDuration := time.Since(startSetup)
	t.Logf("Repository setup completed in: %v", setupDuration)

	// Setup router
	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", handler.HandleGetRepositoryOverview)

	// Test overview API without optional features (fastest)
	t.Run("Overview_WithoutOptionalFeatures", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview", owner, repo), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (without optional features) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
		assert.Greater(t, response.Count.Commits, 0)

		// Performance assertion: should complete in reasonable time
		// For 100MB repo, expect under 5 seconds (adjust based on your requirements)
		assert.Less(t, duration, 10*time.Second, "Overview API should complete in reasonable time")
	})

	// Test overview API with commits (slower)
	t.Run("Overview_WithCommits", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview?include_commits=true", owner, repo), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (with commits) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
	})

	// Test overview API with languages (slowest)
	t.Run("Overview_WithLanguages", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview?include_languages=true", owner, repo), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (with languages) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
	})

	// Test overview API with all features
	t.Run("Overview_WithAllFeatures", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview?include_commits=true&include_languages=true", owner, repo), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (with all features) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
	})
}

// TestOverviewAPI_Performance_CommitCounting tests commit counting performance
func TestOverviewAPI_Performance_CommitCounting(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	handler, tempDir, cleanup := setupTestHandlerForPerf(t)
	defer cleanup()

	// Create repository with many commits
	owner := "testowner"
	repo := "many-commits-repo"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	// Initialize repository
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// Create many commits (100 commits)
	numCommits := 100
	t.Logf("Creating repository with %d commits...", numCommits)

	// Create initial commit
	blob := gitRepo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(100)
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write(bytes.Repeat([]byte("A"), 100))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := gitRepo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	var lastCommitHash plumbing.Hash
	for i := 0; i < numCommits; i++ {
		// Create tree
		tree := &object.Tree{
			Entries: []object.TreeEntry{
				{
					Name: fmt.Sprintf("file_%d.txt", i),
					Mode: filemode.Regular,
					Hash: blobHash,
				},
			},
		}

		treeObj := gitRepo.Storer.NewEncodedObject()
		err = tree.Encode(treeObj)
		require.NoError(t, err)

		treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
		require.NoError(t, err)

		// Create commit
		var parentHashes []plumbing.Hash
		if !lastCommitHash.IsZero() {
			parentHashes = []plumbing.Hash{lastCommitHash}
		}

		commit := &object.Commit{
			Message:      fmt.Sprintf("Commit %d", i),
			Author:       object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
			Committer:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
			TreeHash:     treeHash,
			ParentHashes: parentHashes,
		}

		commitObj := gitRepo.Storer.NewEncodedObject()
		err = commit.Encode(commitObj)
		require.NoError(t, err)

		commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
		require.NoError(t, err)

		lastCommitHash = commitHash
	}

	// Update master branch
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), lastCommitHash)
	err = gitRepo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = gitRepo.Storer.SetReference(headRef)
	require.NoError(t, err)

	// Setup router
	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", handler.HandleGetRepositoryOverview)

	// Test commit counting performance
	req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview", owner, repo), nil)
	w := httptest.NewRecorder()

	start := time.Now()
	router.ServeHTTP(w, req)
	duration := time.Since(start)

	assert.Equal(t, http.StatusOK, w.Code)
	t.Logf("Overview API with %d commits took: %v", numCommits, duration)

	var response RepositoryOverviewResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.True(t, response.Success)
	assert.Equal(t, numCommits, response.Count.Commits, "Should count all commits correctly")

	// Performance assertion: counting 100 commits should be fast
	assert.Less(t, duration, 2*time.Second, "Commit counting should be fast even with many commits")
}

// BenchmarkOverviewAPI benchmarks the overview API performance
func BenchmarkOverviewAPI(b *testing.B) {
	handler, tempDir, cleanup := setupTestHandlerForPerf(&testing.T{})
	defer cleanup()

	// Create repository
	owner := "testowner"
	repo := "bench-repo"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(&testing.T{}, err)

	// Create a smaller repository for benchmarking (10MB)
	setupLargeRepository(&testing.T{}, repoPath, 10)

	// Setup router
	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", handler.HandleGetRepositoryOverview)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview", owner, repo), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

