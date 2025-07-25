package cache

import (
	"fmt"
)

// NewRepositoryCache creates a new repository cache instance with the given configuration
func NewRepositoryCache(config *CacheConfig) (RepositoryCache, error) {
	if config == nil {
		config = NewCacheConfig()
	}

	// Create LRU cache implementation
	lruCache, err := NewLRUCache(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	return lruCache, nil
}

// NewDefaultRepositoryCache creates a repository cache with default configuration
func NewDefaultRepositoryCache() (RepositoryCache, error) {
	config := NewCacheConfig()
	return NewRepositoryCache(config)
}
