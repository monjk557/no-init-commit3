package git

import (
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
)

// SimpleRepoCache is a thread-safe, simple repository cache
type SimpleRepoCache struct {
	mu    sync.RWMutex
	repos map[string]*cachedRepo
}

type cachedRepo struct {
	repo     *git.Repository
	lastUsed time.Time
	path     string
}

var (
	globalSimpleCache *SimpleRepoCache
	initOnce          sync.Once
)

// GetSimpleCache returns the global simple cache instance
func GetSimpleCache() *SimpleRepoCache {
	initOnce.Do(func() {
		globalSimpleCache = &SimpleRepoCache{
			repos: make(map[string]*cachedRepo),
		}
		// Start cleanup goroutine
		go globalSimpleCache.cleanup()
	})
	return globalSimpleCache
}

// Get retrieves a repository from cache or opens it if not cached
func (c *SimpleRepoCache) Get(path string) (*git.Repository, error) {
	c.mu.RLock()
	if cached, exists := c.repos[path]; exists {
		cached.lastUsed = time.Now()
		c.mu.RUnlock()
		return cached.repo, nil
	}
	c.mu.RUnlock()

	// Open repository
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, err
	}

	// Cache it
	c.mu.Lock()
	c.repos[path] = &cachedRepo{
		repo:     repo,
		lastUsed: time.Now(),
		path:     path,
	}
	c.mu.Unlock()

	return repo, nil
}

// cleanup removes old entries every 5 minutes
func (c *SimpleRepoCache) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute) // Remove after 10 minutes of inactivity
		for path, cached := range c.repos {
			if cached.lastUsed.Before(cutoff) {
				delete(c.repos, path)
			}
		}
		c.mu.Unlock()
	}
}

// OpenRepositorySimple opens a repository with simple caching
func OpenRepositorySimple(path string) (Repository, error) {
	cache := GetSimpleCache()
	gitRepo, err := cache.Get(path)
	if err != nil {
		return nil, err
	}

	return &GoGitRepository{
		path: path,
		repo: gitRepo,
		mu:   sync.RWMutex{},
	}, nil
}
