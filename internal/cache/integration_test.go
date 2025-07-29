package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestGitRepo creates a temporary Git repository for testing
func createTestGitRepo(t *testing.T) (string, *gogit.Repository) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "test-repo-*")
	require.NoError(t, err)

	// Initialize git repository
	repo, err := gogit.PlainInit(tmpDir, false)
	require.NoError(t, err)

	// Create a test file
	testFile := filepath.Join(tmpDir, "README.md")
	err = os.WriteFile(testFile, []byte("# Test Repository"), 0644)
	require.NoError(t, err)

	// Add and commit the file
	w, err := repo.Worktree()
	require.NoError(t, err)

	_, err = w.Add("README.md")
	require.NoError(t, err)

	_, err = w.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	return tmpDir, repo
}

func TestRepositoryCache_Interface(t *testing.T) {
	// Create test configuration
	config := &CacheConfig{
		Enabled:         true,
		MaxRepositories: 10,
		TTL:             300 * time.Second, // 5 minutes
		CleanupInterval: 60 * time.Second,  // 1 minute
	}

	// Create cache instance
	cache, err := NewRepositoryCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Create test repository
	repoPath, testRepo := createTestGitRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	t.Run("SetRepository and GetRepository", func(t *testing.T) {
		// Set repository in cache
		err := cache.SetRepository(repoPath, testRepo)
		assert.NoError(t, err)

		// Get repository from cache
		cachedRepo, found := cache.GetRepository(repoPath)
		assert.True(t, found)
		assert.NotNil(t, cachedRepo)

		// Verify it's the same repository by checking HEAD
		ref, err := cachedRepo.Head()
		assert.NoError(t, err)
		assert.NotNil(t, ref)

		originalRef, err := testRepo.Head()
		assert.NoError(t, err)
		assert.Equal(t, originalRef.Hash(), ref.Hash())
	})

	t.Run("GetRepository miss", func(t *testing.T) {
		// Try to get non-existent repository
		repo, found := cache.GetRepository("/non/existent/path")
		assert.False(t, found)
		assert.Nil(t, repo)
	})

	t.Run("InvalidateRepository", func(t *testing.T) {
		// Ensure repository is in cache
		err := cache.SetRepository(repoPath, testRepo)
		assert.NoError(t, err)

		// Verify it's cached
		_, found := cache.GetRepository(repoPath)
		assert.True(t, found)

		// Invalidate the repository
		removed := cache.InvalidateRepository(repoPath)
		assert.True(t, removed)

		// Verify it's no longer cached
		_, found = cache.GetRepository(repoPath)
		assert.False(t, found)
	})

	t.Run("InvalidateAll", func(t *testing.T) {
		// Add multiple repositories
		repoPath2, repo2 := createTestGitRepo(t)
		defer func() { _ = os.RemoveAll(repoPath2) }()

		err := cache.SetRepository(repoPath, testRepo)
		assert.NoError(t, err)
		err = cache.SetRepository(repoPath2, repo2)
		assert.NoError(t, err)

		// Verify both are cached
		_, found := cache.GetRepository(repoPath)
		assert.True(t, found)
		_, found = cache.GetRepository(repoPath2)
		assert.True(t, found)

		// Invalidate all
		cache.InvalidateAll()

		// Verify both are removed
		_, found = cache.GetRepository(repoPath)
		assert.False(t, found)
		_, found = cache.GetRepository(repoPath2)
		assert.False(t, found)
	})

	t.Run("GetMetrics", func(t *testing.T) {
		// Create a fresh cache instance for metrics testing
		metricsConfig := &CacheConfig{
			Enabled:         true,
			MaxRepositories: 10,
			TTL:             300 * time.Second,
			CleanupInterval: 60 * time.Second,
		}

		metricsCache, err := NewRepositoryCache(metricsConfig)
		require.NoError(t, err)
		defer func() { _ = metricsCache.Close() }()

		// Get initial metrics
		metrics := metricsCache.GetMetrics()
		assert.Equal(t, int64(0), metrics.Entries)

		// Add a repository
		err = metricsCache.SetRepository(repoPath, testRepo)
		assert.NoError(t, err)

		// Get from cache (hit)
		_, found := metricsCache.GetRepository(repoPath)
		assert.True(t, found)

		// Get non-existent (miss)
		_, found = metricsCache.GetRepository("/non/existent")
		assert.False(t, found)

		// Check updated metrics
		metrics = metricsCache.GetMetrics()
		assert.Equal(t, int64(1), metrics.Entries)
		assert.Equal(t, int64(1), metrics.Hits)
		assert.Equal(t, int64(1), metrics.Misses)
	})
}

func TestRepositoryCache_PathNormalization(t *testing.T) {
	config := &CacheConfig{
		Enabled:         true,
		MaxRepositories: 10,
	}

	cache, err := NewRepositoryCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Create test repository
	repoPath, testRepo := createTestGitRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	// Store with absolute path
	err = cache.SetRepository(repoPath, testRepo)
	assert.NoError(t, err)

	// Try to get with different path variations
	t.Run("Same path retrieval", func(t *testing.T) {
		repo, found := cache.GetRepository(repoPath)
		assert.True(t, found)
		assert.NotNil(t, repo)
	})

	t.Run("Path with trailing slash", func(t *testing.T) {
		// Note: This test shows that exact path matching is used
		// In a real implementation, you might want to normalize paths
		repo, found := cache.GetRepository(repoPath + "/")
		assert.False(t, found) // Different key
		assert.Nil(t, repo)
	})
}

func TestRepositoryCache_TTLExpiration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping TTL test in short mode")
	}

	config := &CacheConfig{
		Enabled:         true,
		MaxRepositories: 10,
		TTL:             1 * time.Second, // 1 second TTL for testing
		CleanupInterval: 1 * time.Second, // 1 second cleanup
	}

	cache, err := NewRepositoryCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Create test repository
	repoPath, testRepo := createTestGitRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()

	// Add repository to cache
	err = cache.SetRepository(repoPath, testRepo)
	assert.NoError(t, err)

	// Verify it's cached
	repo, found := cache.GetRepository(repoPath)
	assert.True(t, found)
	assert.NotNil(t, repo)

	// Wait for TTL to expire
	time.Sleep(2 * time.Second)

	// Should be expired now
	repo, found = cache.GetRepository(repoPath)
	assert.False(t, found)
	assert.Nil(t, repo)
}

func TestRepositoryCache_Eviction(t *testing.T) {
	config := &CacheConfig{
		Enabled:         true,
		MaxRepositories: 2, // Small cache for testing eviction
	}

	cache, err := NewRepositoryCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Create three test repositories
	repos := make(map[string]*gogit.Repository)
	paths := make([]string, 3)

	for i := 0; i < 3; i++ {
		path, repo := createTestGitRepo(t)
		defer func(p string) { _ = os.RemoveAll(p) }(path)
		paths[i] = path
		repos[path] = repo
	}

	// Add first two repositories
	err = cache.SetRepository(paths[0], repos[paths[0]])
	assert.NoError(t, err)
	err = cache.SetRepository(paths[1], repos[paths[1]])
	assert.NoError(t, err)

	// Both should be cached
	_, found := cache.GetRepository(paths[0])
	assert.True(t, found)
	_, found = cache.GetRepository(paths[1])
	assert.True(t, found)

	// Add third repository - should evict the first one (LRU)
	err = cache.SetRepository(paths[2], repos[paths[2]])
	assert.NoError(t, err)

	// First should be evicted
	_, found = cache.GetRepository(paths[0])
	assert.False(t, found)

	// Second and third should still be cached
	_, found = cache.GetRepository(paths[1])
	assert.True(t, found)
	_, found = cache.GetRepository(paths[2])
	assert.True(t, found)
}
