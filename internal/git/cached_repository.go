package git

import (
	"fmt"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/cache"
	gogit "github.com/go-git/go-git/v5"
)

var (
	// globalRepoCache is the global repository cache instance
	globalRepoCache cache.RepositoryCache

	// globalCacheMutex protects initialization of the global cache
	globalCacheMutex sync.RWMutex

	// defaultCacheConfig is the default configuration for the repository cache
	defaultCacheConfig = &cache.CacheConfig{
		Enabled:         true,
		MaxRepositories: 100,
		TTL:             30 * time.Minute, // 30 minutes
		MaxMemoryMB:     512,              // 512MB max memory
		CleanupInterval: 5 * time.Minute,  // 5 minutes cleanup
	}
)

// InitializeGlobalCache initializes the global repository cache with the given configuration
func InitializeGlobalCache(config *cache.CacheConfig) error {
	globalCacheMutex.Lock()
	defer globalCacheMutex.Unlock()

	if config == nil {
		config = defaultCacheConfig
	}

	repoCache, err := cache.NewRepositoryCache(config)
	if err != nil {
		return fmt.Errorf("failed to initialize repository cache: %w", err)
	}

	// Close the old cache if it exists
	if globalRepoCache != nil {
		_ = globalRepoCache.Close()
	}

	globalRepoCache = repoCache
	return nil
}

// GetGlobalRepoCache returns the global repository cache instance
// If not initialized, it creates one with default configuration
func GetGlobalRepoCache() cache.RepositoryCache {
	globalCacheMutex.RLock()
	if globalRepoCache != nil {
		globalCacheMutex.RUnlock()
		return globalRepoCache
	}
	globalCacheMutex.RUnlock()

	// Initialize with default config if not already done
	globalCacheMutex.Lock()
	defer globalCacheMutex.Unlock()

	// Double-check after acquiring write lock
	if globalRepoCache != nil {
		return globalRepoCache
	}

	repoCache, err := cache.NewRepositoryCache(defaultCacheConfig)
	if err != nil {
		// If cache creation fails, create a disabled cache
		disabledConfig := &cache.CacheConfig{Enabled: false}
		repoCache, _ = cache.NewRepositoryCache(disabledConfig)
	}

	globalRepoCache = repoCache
	return globalRepoCache
}

// OpenRepositoryWithNewCache opens a Git repository using our new cache implementation
func OpenRepositoryWithNewCache(path string) (Repository, error) {
	cache := GetGlobalRepoCache()

	// Try to get from cache first
	if gitRepo, found := cache.GetRepository(path); found {
		// Check if the cached repository is still valid (not nil)
		if gitRepo != nil {
			// Return a properly initialized GoGitRepository
			return &GoGitRepository{
				path: path,
				repo: gitRepo,
				mu:   sync.RWMutex{}, // Important: Initialize the mutex!
			}, nil
		}
		// If gitRepo is nil, the repository was closed, so we'll re-open it below
	}

	// Not in cache or was closed, open the repository
	gitRepo, err := gogit.PlainOpen(path)
	if err != nil {
		if err == gogit.ErrRepositoryNotExists {
			return nil, &InvalidRepositoryError{
				Path:   path,
				Reason: "directory is not a Git repository",
			}
		}
		return nil, NewGitError("open", path, "", "failed to open repository", err)
	}

	// Store in cache
	if err := cache.SetRepository(path, gitRepo); err != nil {
		// Log cache error but don't fail the operation
		fmt.Printf("Warning: failed to cache repository %s: %v\n", path, err)
	}

	return &GoGitRepository{
		path: path,
		repo: gitRepo,
		mu:   sync.RWMutex{}, // Initialize the mutex!
	}, nil
}

// InvalidateCachedRepository removes a repository from the cache
// This should be called after repository modifications (e.g., push operations)
func InvalidateCachedRepository(path string) bool {
	cache := GetGlobalRepoCache()
	return cache.InvalidateRepository(path)
}

// InvalidateAllCachedRepositories clears the entire repository cache
func InvalidateAllCachedRepositories() {
	cache := GetGlobalRepoCache()
	cache.InvalidateAll()
}

// GetCacheMetrics returns metrics from the repository cache
func GetCacheMetrics() cache.CacheMetrics {
	cache := GetGlobalRepoCache()
	return cache.GetMetrics()
}

// ShutdownCache gracefully shuts down the repository cache
func ShutdownCache() error {
	globalCacheMutex.Lock()
	defer globalCacheMutex.Unlock()

	if globalRepoCache != nil {
		err := globalRepoCache.Close()
		globalRepoCache = nil
		return err
	}

	return nil
}
