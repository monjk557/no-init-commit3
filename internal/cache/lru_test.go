package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create a temporary Git repository for testing
func createTestRepository(t *testing.T) (string, *gogit.Repository) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "test-repo")

	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)

	return repoPath, repo
}

func TestNewLRUCache(t *testing.T) {
	tests := []struct {
		name      string
		config    *CacheConfig
		wantError bool
		errorMsg  string
	}{
		{
			name:      "Valid config",
			config:    NewCacheConfig(),
			wantError: false,
		},
		{
			name:      "Nil config",
			config:    nil,
			wantError: true,
			errorMsg:  "cache config cannot be nil",
		},
		{
			name: "Invalid config",
			config: &CacheConfig{
				MaxRepositories: -1,
				TTL:             DefaultTTL,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "invalid cache config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache, err := NewLRUCache(tt.config)

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, cache)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, cache)
				assert.Equal(t, 0, cache.Len())
			}
		})
	}
}

func TestLRUCache_BasicOperations(t *testing.T) {
	config := NewCacheConfig()
	config.MaxRepositories = 3
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Test Set and Get
	repoPath1, gitRepo1 := createTestRepository(t)
	err = cache.Set(repoPath1, gitRepo1)
	assert.NoError(t, err)
	assert.Equal(t, 1, cache.Len())

	// Test Get
	repo, found := cache.Get(repoPath1)
	assert.True(t, found)
	assert.NotNil(t, repo)
	assert.Equal(t, repoPath1, repo.Path)
	assert.Equal(t, gitRepo1, repo.GitRepo)
	assert.Equal(t, int64(2), repo.GetAccessCount()) // Initial set + get

	// Test Contains
	assert.True(t, cache.Contains(repoPath1))
	assert.False(t, cache.Contains("non-existent"))

	// Test Remove
	removed := cache.Remove(repoPath1)
	assert.True(t, removed)
	assert.Equal(t, 0, cache.Len())
	assert.False(t, cache.Contains(repoPath1))

	// Test Get after removal
	_, found = cache.Get(repoPath1)
	assert.False(t, found)
}

func TestLRUCache_LRUEviction(t *testing.T) {
	config := NewCacheConfig()
	config.MaxRepositories = 2
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Add two repositories
	repoPath1, gitRepo1 := createTestRepository(t)
	repoPath2, gitRepo2 := createTestRepository(t)
	repoPath3, gitRepo3 := createTestRepository(t)

	err = cache.Set(repoPath1, gitRepo1)
	require.NoError(t, err)
	err = cache.Set(repoPath2, gitRepo2)
	require.NoError(t, err)

	assert.Equal(t, 2, cache.Len())
	assert.True(t, cache.Contains(repoPath1))
	assert.True(t, cache.Contains(repoPath2))

	// Access repo1 to make it most recently used
	_, found := cache.Get(repoPath1)
	assert.True(t, found)

	// Add third repository, should evict repo2 (least recently used)
	err = cache.Set(repoPath3, gitRepo3)
	require.NoError(t, err)

	assert.Equal(t, 2, cache.Len())
	assert.True(t, cache.Contains(repoPath1))  // Should still be there (recently accessed)
	assert.False(t, cache.Contains(repoPath2)) // Should be evicted
	assert.True(t, cache.Contains(repoPath3))  // Should be there (just added)
}

func TestLRUCache_EvictionCallback(t *testing.T) {
	config := NewCacheConfig()
	config.MaxRepositories = 1
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Set up eviction callback
	var evictedKeys []string
	var evictedRepos []*Repository
	cache.SetEvictionCallback(func(key string, repo *Repository) {
		evictedKeys = append(evictedKeys, key)
		evictedRepos = append(evictedRepos, repo)
	})

	// Add first repository
	repoPath1, gitRepo1 := createTestRepository(t)
	err = cache.Set(repoPath1, gitRepo1)
	require.NoError(t, err)

	// Add second repository, should trigger eviction
	repoPath2, gitRepo2 := createTestRepository(t)
	err = cache.Set(repoPath2, gitRepo2)
	require.NoError(t, err)

	// Check eviction callback was called
	assert.Len(t, evictedKeys, 1)
	assert.Equal(t, repoPath1, evictedKeys[0])
	assert.Len(t, evictedRepos, 1)
	assert.Equal(t, repoPath1, evictedRepos[0].Path)
}

func TestLRUCache_ConcurrentAccess(t *testing.T) {
	config := NewCacheConfig()
	config.MaxRepositories = 100
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	const numGoroutines = 20
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Concurrent Set operations
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				repoPath := fmt.Sprintf("repo-%d-%d", id, j)
				repoDir := filepath.Join(t.TempDir(), repoPath)
				err := os.MkdirAll(repoDir, 0755)
				require.NoError(t, err)

				gitRepo, err := gogit.PlainInit(repoDir, false)
				require.NoError(t, err)

				err = cache.Set(repoPath, gitRepo)
				assert.NoError(t, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify cache state
	assert.LessOrEqual(t, cache.Len(), config.MaxRepositories)

	// Concurrent Get operations
	keys := cache.Keys()
	if len(keys) > 0 {
		wg.Add(numGoroutines)
		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < opsPerGoroutine; j++ {
					if len(keys) > 0 {
						key := keys[j%len(keys)]
						_, _ = cache.Get(key) // Don't assert as it might be evicted
					}
				}
			}()
		}
		wg.Wait()
	}
}

func TestLRUCache_Metrics(t *testing.T) {
	config := NewCacheConfig()
	config.MaxRepositories = 2
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Initial metrics
	metrics := cache.GetMetrics()
	assert.Equal(t, int64(0), metrics.Hits)
	assert.Equal(t, int64(0), metrics.Misses)
	assert.Equal(t, int64(0), metrics.Evictions)
	assert.Equal(t, int64(0), metrics.Entries)
	assert.Equal(t, 0.0, cache.GetHitRatio())

	// Add repository and test metrics
	repoPath1, gitRepo1 := createTestRepository(t)
	err = cache.Set(repoPath1, gitRepo1)
	require.NoError(t, err)

	metrics = cache.GetMetrics()
	assert.Equal(t, int64(1), metrics.Entries)

	// Test cache hit
	_, found := cache.Get(repoPath1)
	assert.True(t, found)

	metrics = cache.GetMetrics()
	assert.Equal(t, int64(1), metrics.Hits)
	assert.Equal(t, int64(0), metrics.Misses)
	assert.Equal(t, 1.0, cache.GetHitRatio())

	// Test cache miss
	_, found = cache.Get("non-existent")
	assert.False(t, found)

	metrics = cache.GetMetrics()
	assert.Equal(t, int64(1), metrics.Hits)
	assert.Equal(t, int64(1), metrics.Misses)
	assert.Equal(t, 0.5, cache.GetHitRatio())

	// Test eviction metrics
	repoPath2, gitRepo2 := createTestRepository(t)
	repoPath3, gitRepo3 := createTestRepository(t)

	err = cache.Set(repoPath2, gitRepo2)
	require.NoError(t, err)
	err = cache.Set(repoPath3, gitRepo3) // Should trigger eviction

	require.NoError(t, err)

	metrics = cache.GetMetrics()
	assert.Equal(t, int64(1), metrics.Evictions)
	assert.Equal(t, int64(2), metrics.Entries)

	// Test reset metrics
	cache.ResetMetrics()
	metrics = cache.GetMetrics()
	assert.Equal(t, int64(0), metrics.Hits)
	assert.Equal(t, int64(0), metrics.Misses)
	assert.Equal(t, int64(0), metrics.Evictions)
	assert.Equal(t, int64(0), metrics.Entries)
}

func TestLRUCache_DisabledCache(t *testing.T) {
	config := NewCacheConfig()
	config.Enabled = false
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	repoPath, gitRepo := createTestRepository(t)

	// Operations should work but not cache anything
	err = cache.Set(repoPath, gitRepo)
	assert.NoError(t, err)
	assert.Equal(t, 0, cache.Len())

	_, found := cache.Get(repoPath)
	assert.False(t, found)

	assert.False(t, cache.Contains(repoPath))

	removed := cache.Remove(repoPath)
	assert.False(t, removed)

	// Metrics should still track misses
	metrics := cache.GetMetrics()
	assert.Equal(t, int64(1), metrics.Misses)
}

func TestLRUCache_ValidationErrors(t *testing.T) {
	config := NewCacheConfig()
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	repoPath, gitRepo := createTestRepository(t)

	// Test empty path
	err = cache.Set("", gitRepo)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository path cannot be empty")

	// Test nil repository
	err = cache.Set(repoPath, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git repository cannot be nil")
}

func TestLRUCache_PurgeAndClose(t *testing.T) {
	config := NewCacheConfig()
	config.MaxRepositories = 5
	cache, err := NewLRUCache(config)
	require.NoError(t, err)

	// Add multiple repositories
	for i := 0; i < 3; i++ {
		repoPath, gitRepo := createTestRepository(t)
		err = cache.Set(fmt.Sprintf("repo-%d-%s", i, repoPath), gitRepo)
		require.NoError(t, err)
	}

	assert.Equal(t, 3, cache.Len())

	// Test purge
	cache.Purge()
	assert.Equal(t, 0, cache.Len())

	// Add repositories again
	for i := 0; i < 2; i++ {
		repoPath, gitRepo := createTestRepository(t)
		err = cache.Set(fmt.Sprintf("repo-new-%d-%s", i, repoPath), gitRepo)
		require.NoError(t, err)
	}

	assert.Equal(t, 2, cache.Len())

	// Test close
	err = cache.Close()
	assert.NoError(t, err)
	assert.Equal(t, 0, cache.Len())
}

func TestRepository_UpdateAccess(t *testing.T) {
	repoPath, gitRepo := createTestRepository(t)

	repo := &Repository{
		GitRepo:      gitRepo,
		Path:         repoPath,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now().Add(-time.Hour),
		AccessCount:  0,
	}

	initialAccess := repo.GetLastAccessed()
	initialCount := repo.GetAccessCount()

	time.Sleep(time.Millisecond) // Ensure time difference
	repo.UpdateAccess()

	assert.True(t, repo.GetLastAccessed().After(initialAccess))
	assert.Equal(t, initialCount+1, repo.GetAccessCount())
}

func TestRepository_Close(t *testing.T) {
	repoPath, gitRepo := createTestRepository(t)

	repo := &Repository{
		GitRepo: gitRepo,
		Path:    repoPath,
	}

	err := repo.Close()
	assert.NoError(t, err)
	// GitRepo is not set to nil anymore to prevent "repository is closed" errors
	// for existing references. The Go garbage collector handles cleanup.
	assert.NotNil(t, repo.GitRepo)
}

func TestLRUCache_ConcurrentEviction(t *testing.T) {
	config := NewCacheConfig()
	config.MaxRepositories = 5
	cache, err := NewLRUCache(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	var evictionMutex sync.Mutex
	var evictedCount int

	cache.SetEvictionCallback(func(key string, repo *Repository) {
		evictionMutex.Lock()
		evictedCount++
		evictionMutex.Unlock()
		// Simulate some work during eviction
		time.Sleep(time.Millisecond)
	})

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Concurrent operations that will cause evictions
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				repoPath := fmt.Sprintf("concurrent-repo-%d-%d", id, j)
				repoDir := filepath.Join(t.TempDir(), repoPath)
				err := os.MkdirAll(repoDir, 0755)
				require.NoError(t, err)

				gitRepo, err := gogit.PlainInit(repoDir, false)
				require.NoError(t, err)

				err = cache.Set(repoPath, gitRepo)
				assert.NoError(t, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify final state
	assert.LessOrEqual(t, cache.Len(), config.MaxRepositories)

	evictionMutex.Lock()
	finalEvictedCount := evictedCount
	evictionMutex.Unlock()

	// Should have evicted many repositories
	assert.Greater(t, finalEvictedCount, 0)
}

// Benchmark tests for performance evaluation
func BenchmarkLRUCache_Set(b *testing.B) {
	config := NewCacheConfig()
	config.MaxRepositories = 1000
	cache, err := NewLRUCache(config)
	require.NoError(b, err)
	defer func() { _ = cache.Close() }()

	repos := make([]*gogit.Repository, b.N)
	paths := make([]string, b.N)

	for i := 0; i < b.N; i++ {
		tempDir := b.TempDir()
		repoPath := filepath.Join(tempDir, fmt.Sprintf("bench-repo-%d", i))
		repo, err := gogit.PlainInit(repoPath, false)
		require.NoError(b, err)
		repos[i] = repo
		paths[i] = repoPath
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.Set(paths[i], repos[i])
	}
}

func BenchmarkLRUCache_Get(b *testing.B) {
	config := NewCacheConfig()
	config.MaxRepositories = 1000
	cache, err := NewLRUCache(config)
	require.NoError(b, err)
	defer func() { _ = cache.Close() }()

	// Pre-populate cache
	const numRepos = 100
	paths := make([]string, numRepos)
	for i := 0; i < numRepos; i++ {
		tempDir := b.TempDir()
		repoPath := filepath.Join(tempDir, fmt.Sprintf("bench-repo-%d", i))
		repo, err := gogit.PlainInit(repoPath, false)
		require.NoError(b, err)
		paths[i] = repoPath
		_ = cache.Set(repoPath, repo)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := paths[i%numRepos]
		_, _ = cache.Get(path)
	}
}

// TTL-specific tests

func TestRepository_IsExpired(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		createdAt time.Time
		ttl       time.Duration
		expected  bool
	}{
		{
			name:      "Not expired - within TTL",
			createdAt: now.Add(-5 * time.Minute),
			ttl:       10 * time.Minute,
			expected:  false,
		},
		{
			name:      "Expired - beyond TTL",
			createdAt: now.Add(-15 * time.Minute),
			ttl:       10 * time.Minute,
			expected:  true,
		},
		{
			name:      "No expiration - zero TTL",
			createdAt: now.Add(-1 * time.Hour),
			ttl:       0,
			expected:  false,
		},
		{
			name:      "No expiration - negative TTL",
			createdAt: now.Add(-1 * time.Hour),
			ttl:       -1 * time.Minute,
			expected:  false,
		},
		{
			name:      "Just expired - exactly at TTL boundary",
			createdAt: now.Add(-10*time.Minute - time.Millisecond),
			ttl:       10 * time.Minute,
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &Repository{
				CreatedAt: tt.createdAt,
			}

			assert.Equal(t, tt.expected, repo.IsExpired(tt.ttl))
		})
	}
}

func TestLRUCache_TTLExpiration(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 10,
		TTL:             100 * time.Millisecond, // Short TTL for fast testing
		CleanupInterval: 50 * time.Millisecond,
		Enabled:         true,
	}

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	repoPath, gitRepo := createTestRepository(t)

	// Set a repository
	err = cache.Set(repoPath, gitRepo)
	require.NoError(t, err)

	// Should be found immediately
	repo, found := cache.Get(repoPath)
	assert.True(t, found)
	assert.NotNil(t, repo)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should not be found after expiration
	repo, found = cache.Get(repoPath)
	assert.False(t, found)
	assert.Nil(t, repo)

	// Metrics should show a miss
	metrics := cache.GetMetrics()
	assert.Greater(t, metrics.Misses, int64(0))
}

func TestLRUCache_BackgroundCleanup(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 10,
		TTL:             50 * time.Millisecond, // Very short TTL
		CleanupInterval: 25 * time.Millisecond, // Frequent cleanup
		Enabled:         true,
	}

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Add multiple repositories
	repoPaths := make([]string, 3)
	for i := 0; i < 3; i++ {
		repoPath, gitRepo := createTestRepository(t)
		repoPaths[i] = repoPath
		err = cache.Set(repoPath, gitRepo)
		require.NoError(t, err)
	}

	// Should have 3 entries
	assert.Equal(t, 3, cache.Len())

	// Wait for background cleanup to run multiple times
	time.Sleep(100 * time.Millisecond)

	// All entries should be cleaned up
	assert.Equal(t, 0, cache.Len())

	// Verify none can be retrieved
	for _, path := range repoPaths {
		repo, found := cache.Get(path)
		assert.False(t, found)
		assert.Nil(t, repo)
	}
}

func TestLRUCache_TTLWithAccessUpdates(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 10,
		TTL:             100 * time.Millisecond,
		CleanupInterval: 50 * time.Millisecond,
		Enabled:         true,
	}

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	repoPath, gitRepo := createTestRepository(t)

	// Set repository
	err = cache.Set(repoPath, gitRepo)
	require.NoError(t, err)

	// Access it before expiration to update LastAccessed
	time.Sleep(25 * time.Millisecond)
	repo, found := cache.Get(repoPath)
	assert.True(t, found)
	assert.NotNil(t, repo)

	// Note: TTL is still based on CreatedAt, not LastAccessed
	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should still be expired based on CreatedAt
	repo, found = cache.Get(repoPath)
	assert.False(t, found)
	assert.Nil(t, repo)
}

func TestLRUCache_TTLIntegrationWithLRUEviction(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 2, // Small capacity for LRU eviction
		TTL:             200 * time.Millisecond,
		CleanupInterval: 50 * time.Millisecond,
		Enabled:         true,
	}

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	// Add 3 repositories (will trigger LRU eviction)
	repoPaths := make([]string, 3)
	for i := 0; i < 3; i++ {
		repoPath, gitRepo := createTestRepository(t)
		repoPaths[i] = repoPath
		err = cache.Set(repoPath, gitRepo)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond) // Small delay to ensure different timestamps
	}

	// Should only have 2 entries due to LRU eviction
	assert.Equal(t, 2, cache.Len())

	// First repository should be evicted by LRU
	repo, found := cache.Get(repoPaths[0])
	assert.False(t, found)
	assert.Nil(t, repo)

	// Other two should still be present
	repo, found = cache.Get(repoPaths[1])
	assert.True(t, found)
	assert.NotNil(t, repo)

	repo, found = cache.Get(repoPaths[2])
	assert.True(t, found)
	assert.NotNil(t, repo)
}

func TestLRUCache_TTLDisabled(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 10,
		TTL:             0, // TTL disabled
		CleanupInterval: 50 * time.Millisecond,
		Enabled:         true,
	}

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	repoPath, gitRepo := createTestRepository(t)

	// Set repository
	err = cache.Set(repoPath, gitRepo)
	require.NoError(t, err)

	// Wait longer than typical TTL would be
	time.Sleep(150 * time.Millisecond)

	// Should still be found since TTL is disabled
	repo, found := cache.Get(repoPath)
	assert.True(t, found)
	assert.NotNil(t, repo)
}

func TestLRUCache_VeryShortTTL(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 10,
		TTL:             1 * time.Millisecond, // Extremely short TTL
		CleanupInterval: 5 * time.Millisecond,
		Enabled:         true,
	}

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)
	defer func() { _ = cache.Close() }()

	repoPath, gitRepo := createTestRepository(t)

	// Set repository
	err = cache.Set(repoPath, gitRepo)
	require.NoError(t, err)

	// Even immediate access might miss due to very short TTL
	time.Sleep(2 * time.Millisecond)
	repo, found := cache.Get(repoPath)
	assert.False(t, found)
	assert.Nil(t, repo)
}

func TestLRUCache_CleanupShutdown(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 10,
		TTL:             1 * time.Second, // Long TTL to avoid automatic expiration
		CleanupInterval: 10 * time.Millisecond,
		Enabled:         true,
	}

	cache, err := NewLRUCacheForTesting(config)
	require.NoError(t, err)

	repoPath, gitRepo := createTestRepository(t)
	err = cache.Set(repoPath, gitRepo)
	require.NoError(t, err)

	// Close the cache
	err = cache.Close()
	require.NoError(t, err)

	// Give some time to ensure cleanup goroutine has stopped
	time.Sleep(50 * time.Millisecond)

	// Test that cache operations still work after shutdown (cache should be purged)
	repo, found := cache.Get(repoPath)
	assert.False(t, found)
	assert.Nil(t, repo)
}
