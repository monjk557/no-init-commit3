package cache

import gogit "github.com/go-git/go-git/v5"

// RepositoryCache defines the interface for caching Git repositories
type RepositoryCache interface {
	// GetRepository retrieves a cached repository by its filesystem path.
	// Returns the repository and true if found, or nil and false if not cached.
	GetRepository(path string) (*gogit.Repository, bool)

	// SetRepository stores a repository in the cache with its filesystem path as the key.
	// The cache implementation handles eviction according to its policy.
	SetRepository(path string, repo *gogit.Repository) error

	// InvalidateRepository removes a repository from the cache.
	// This should be called when a repository is modified (e.g., after a push).
	InvalidateRepository(path string) bool

	// InvalidateAll removes all repositories from the cache.
	// Useful for cache maintenance or when global repository changes occur.
	InvalidateAll()

	// Close gracefully shuts down the cache, releasing any resources.
	// This should be called when the cache is no longer needed.
	Close() error

	// GetMetrics returns cache performance metrics for monitoring.
	GetMetrics() CacheMetrics
}

// RepositoryCacheFactory creates a new RepositoryCache instance
type RepositoryCacheFactory func(config *CacheConfig) (RepositoryCache, error)
