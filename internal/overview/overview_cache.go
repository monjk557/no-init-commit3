package overview

import (
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
)

type cacheEntry[V any] struct {
	value     V
	expiresAt time.Time
}

type ttlLRUCache[V any] struct {
	enabled bool
	ttl     time.Duration
	now     func() time.Time

	mu  sync.RWMutex
	lru *lru.Cache[string, *cacheEntry[V]]
}

func newTTLLRUCache[V any](maxEntries int, ttl time.Duration) *ttlLRUCache[V] {
	if maxEntries <= 0 || ttl <= 0 {
		return &ttlLRUCache[V]{enabled: false}
	}

	c, _ := lru.New[string, *cacheEntry[V]](maxEntries)
	return &ttlLRUCache[V]{
		enabled: true,
		ttl:     ttl,
		now:     time.Now,
		lru:     c,
	}
}

func (c *ttlLRUCache[V]) Get(key string) (V, bool) {
	var zero V
	if c == nil || !c.enabled {
		return zero, false
	}

	c.mu.RLock()
	entry, ok := c.lru.Get(key)
	c.mu.RUnlock()
	if !ok || entry == nil {
		return zero, false
	}

	if c.now().After(entry.expiresAt) {
		c.mu.Lock()
		_ = c.lru.Remove(key)
		c.mu.Unlock()
		return zero, false
	}

	return entry.value, true
}

func (c *ttlLRUCache[V]) Set(key string, value V) {
	if c == nil || !c.enabled {
		return
	}

	c.mu.Lock()
	c.lru.Add(key, &cacheEntry[V]{value: value, expiresAt: c.now().Add(c.ttl)})
	c.mu.Unlock()
}

// OverviewCache caches expensive overview computations per (repoPath, refHash, path).
// It is intentionally in-memory and TTL-based; correctness is "eventual" within TTL.
type OverviewCache struct {
	commitCountTTL time.Duration

	commitCount *ttlLRUCache[int]
	languages   *ttlLRUCache[LanguageDistribution]
	treeEntries *ttlLRUCache[[]GitTreeEntryWithCommit]

	sf singleflight.Group
}

func NewOverviewCache(maxEntries int, ttl time.Duration) *OverviewCache {
	// Keep it simple: one TTL for all sub-caches for now.
	return &OverviewCache{
		commitCountTTL: ttl,
		commitCount:    newTTLLRUCache[int](maxEntries, ttl),
		languages:      newTTLLRUCache[LanguageDistribution](maxEntries, ttl),
		treeEntries:    newTTLLRUCache[[]GitTreeEntryWithCommit](maxEntries, ttl),
	}
}

func cacheKey(kind, repoPath, refHash, path string) string {
	return fmt.Sprintf("%s|%s|%s|%s", kind, repoPath, refHash, path)
}

func (c *OverviewCache) GetCommitCount(repoPath, refHash string) (int, bool) {
	return c.commitCount.Get(cacheKey("commit_count", repoPath, refHash, ""))
}

func (c *OverviewCache) SetCommitCount(repoPath, refHash string, count int) {
	c.commitCount.Set(cacheKey("commit_count", repoPath, refHash, ""), count)
}

func (c *OverviewCache) GetLanguages(repoPath, refHash string) (LanguageDistribution, bool) {
	return c.languages.Get(cacheKey("languages", repoPath, refHash, ""))
}

func (c *OverviewCache) SetLanguages(repoPath, refHash string, dist LanguageDistribution) {
	c.languages.Set(cacheKey("languages", repoPath, refHash, ""), dist)
}

func (c *OverviewCache) GetTreeEntries(repoPath, refHash, path string) ([]GitTreeEntryWithCommit, bool) {
	return c.treeEntries.Get(cacheKey("tree_entries", repoPath, refHash, path))
}

func (c *OverviewCache) SetTreeEntries(repoPath, refHash, path string, entries []GitTreeEntryWithCommit) {
	c.treeEntries.Set(cacheKey("tree_entries", repoPath, refHash, path), entries)
}

func (c *OverviewCache) Do(key string, fn func() (any, error)) (any, error) {
	if c == nil {
		return fn()
	}
	v, err, _ := c.sf.Do(key, fn)
	return v, err
}
