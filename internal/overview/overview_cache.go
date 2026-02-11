package overview

import (
	"fmt"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/observability"
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

func (c *ttlLRUCache[V]) Len() int {
	if c == nil || !c.enabled || c.lru == nil {
		return 0
	}
	c.mu.RLock()
	n := c.lru.Len()
	c.mu.RUnlock()
	return n
}

// OverviewCache caches expensive overview computations per (repoPath, refHash, path).
// It is intentionally in-memory and TTL-based; correctness is "eventual" within TTL.
//
// Partial cache policy (plan2 Action 1):
//   - complete results → main cache (long TTL)
//   - partial results → partialCommits cache (short TTL=2s, anti-thundering only)
//   - singleflight is enforced for BOTH partial and complete (thundering herd prevention)
type OverviewCache struct {
	commitCountTTL time.Duration

	commitCount    *ttlLRUCache[int]
	languages      *ttlLRUCache[LanguageDistribution]
	treeEntries    *ttlLRUCache[[]GitTreeEntryWithCommit]
	latestCommits  *ttlLRUCache[map[string]*GitCommit]
	partialCommits *ttlLRUCache[map[string]*GitCommit] // short TTL, anti-thundering

	sf singleflight.Group
}

const partialCacheTTL = 2 * time.Second

func NewOverviewCache(maxEntries int, ttl time.Duration) *OverviewCache {
	return &OverviewCache{
		commitCountTTL: ttl,
		commitCount:    newTTLLRUCache[int](maxEntries, ttl),
		languages:      newTTLLRUCache[LanguageDistribution](maxEntries, ttl),
		treeEntries:    newTTLLRUCache[[]GitTreeEntryWithCommit](maxEntries, ttl),
		latestCommits:  newTTLLRUCache[map[string]*GitCommit](maxEntries, ttl),
		partialCommits: newTTLLRUCache[map[string]*GitCommit](maxEntries, partialCacheTTL),
	}
}

func cacheKey(kind, repoPath, refHash, path string) string {
	return fmt.Sprintf("%s|%s|%s|%s", kind, repoPath, refHash, path)
}

func (c *OverviewCache) GetCommitCount(repoPath, refHash string) (int, bool) {
	m := observability.GetApplicationMetrics()
	v, ok := c.commitCount.Get(cacheKey("commit_count", repoPath, refHash, ""))
	if ok {
		m.RecordOverviewCacheOp("commit_count", "hit")
	} else {
		m.RecordOverviewCacheOp("commit_count", "miss")
	}
	m.SetOverviewCacheEntries("commit_count", c.commitCount.Len())
	return v, ok
}

func (c *OverviewCache) SetCommitCount(repoPath, refHash string, count int) {
	c.commitCount.Set(cacheKey("commit_count", repoPath, refHash, ""), count)
	m := observability.GetApplicationMetrics()
	m.RecordOverviewCacheOp("commit_count", "set")
	m.SetOverviewCacheEntries("commit_count", c.commitCount.Len())
}

func (c *OverviewCache) GetLanguages(repoPath, refHash string) (LanguageDistribution, bool) {
	m := observability.GetApplicationMetrics()
	v, ok := c.languages.Get(cacheKey("languages", repoPath, refHash, ""))
	if ok {
		m.RecordOverviewCacheOp("languages", "hit")
	} else {
		m.RecordOverviewCacheOp("languages", "miss")
	}
	m.SetOverviewCacheEntries("languages", c.languages.Len())
	return v, ok
}

func (c *OverviewCache) SetLanguages(repoPath, refHash string, dist LanguageDistribution) {
	c.languages.Set(cacheKey("languages", repoPath, refHash, ""), dist)
	m := observability.GetApplicationMetrics()
	m.RecordOverviewCacheOp("languages", "set")
	m.SetOverviewCacheEntries("languages", c.languages.Len())
}

func (c *OverviewCache) GetTreeEntries(repoPath, refHash, path string) ([]GitTreeEntryWithCommit, bool) {
	m := observability.GetApplicationMetrics()
	v, ok := c.treeEntries.Get(cacheKey("tree_entries", repoPath, refHash, path))
	if ok {
		m.RecordOverviewCacheOp("tree_entries", "hit")
	} else {
		m.RecordOverviewCacheOp("tree_entries", "miss")
	}
	m.SetOverviewCacheEntries("tree_entries", c.treeEntries.Len())
	return v, ok
}

func (c *OverviewCache) SetTreeEntries(repoPath, refHash, path string, entries []GitTreeEntryWithCommit) {
	c.treeEntries.Set(cacheKey("tree_entries", repoPath, refHash, path), entries)
	m := observability.GetApplicationMetrics()
	m.RecordOverviewCacheOp("tree_entries", "set")
	m.SetOverviewCacheEntries("tree_entries", c.treeEntries.Len())
}

func (c *OverviewCache) GetLatestCommitsBatch(repoPath, refHash, key string) (map[string]*GitCommit, bool) {
	m := observability.GetApplicationMetrics()
	v, ok := c.latestCommits.Get(cacheKey("latest_commits_batch", repoPath, refHash, key))
	if ok {
		m.RecordOverviewCacheOp("latest_commits_batch", "hit")
	} else {
		m.RecordOverviewCacheOp("latest_commits_batch", "miss")
	}
	m.SetOverviewCacheEntries("latest_commits_batch", c.latestCommits.Len())
	return v, ok
}

func (c *OverviewCache) SetLatestCommitsBatch(repoPath, refHash, key string, commits map[string]*GitCommit) {
	c.latestCommits.Set(cacheKey("latest_commits_batch", repoPath, refHash, key), commits)
	m := observability.GetApplicationMetrics()
	m.RecordOverviewCacheOp("latest_commits_batch", "set")
	m.SetOverviewCacheEntries("latest_commits_batch", c.latestCommits.Len())
}

// SetLatestCommitsBatchPartial stores a partial result with short TTL (anti-thundering).
func (c *OverviewCache) SetLatestCommitsBatchPartial(repoPath, refHash, key string, commits map[string]*GitCommit) {
	c.partialCommits.Set(cacheKey("latest_commits_partial", repoPath, refHash, key), commits)
	m := observability.GetApplicationMetrics()
	m.RecordOverviewCacheOp("latest_commits_partial", "set")
}

// GetLatestCommitsBatchAny checks complete cache first, then partial.
// Returns (commits, isComplete, found).
func (c *OverviewCache) GetLatestCommitsBatchAny(repoPath, refHash, key string) (map[string]*GitCommit, bool, bool) {
	// Try complete first.
	if v, ok := c.GetLatestCommitsBatch(repoPath, refHash, key); ok {
		return v, true, true
	}
	// Try partial (short TTL).
	k := cacheKey("latest_commits_partial", repoPath, refHash, key)
	v, ok := c.partialCommits.Get(k)
	if ok {
		m := observability.GetApplicationMetrics()
		m.RecordOverviewCacheOp("latest_commits_partial", "hit")
		return v, false, true
	}
	return nil, false, false
}

func (c *OverviewCache) Do(key string, fn func() (any, error)) (any, error, bool) {
	if c == nil {
		v, err := fn()
		return v, err, false
	}
	v, err, shared := c.sf.Do(key, fn)
	return v, err, shared
}
