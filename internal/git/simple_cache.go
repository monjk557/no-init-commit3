package git

import (
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
)

// SimpleRepoCache는 스레드 안전한 간단한 저장소 캐시입니다.
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

// GetSimpleCache는 전역 간단 캐시 인스턴스를 반환합니다.
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

// Get은 캐시에서 저장소를 가져오거나 캐시되어 있지 않으면 엽니다.
func (c *SimpleRepoCache) Get(path string) (*git.Repository, error) {
	c.mu.RLock()
	if cached, exists := c.repos[path]; exists {
		repo := cached.repo
		c.mu.RUnlock()

		// Update lastUsed with a write lock
		c.mu.Lock()
		if cached, stillExists := c.repos[path]; stillExists {
			cached.lastUsed = time.Now()
		}
		c.mu.Unlock()

		return repo, nil
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

// OpenRepositorySimple은 간단한 캐싱으로 저장소를 엽니다.
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
