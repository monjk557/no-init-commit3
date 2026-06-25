package cache

import (
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRepo returns an in-memory go-git repository for use as a cache value.
func newTestRepo(t *testing.T) *gogit.Repository {
	t.Helper()
	repo, err := gogit.Init(memory.NewStorage(), nil)
	require.NoError(t, err)
	return repo
}

func newTestLRU(t *testing.T, cfg *CacheConfig) *LRUCache {
	t.Helper()
	if cfg == nil {
		cfg = NewCacheConfig()
	}
	c, err := NewLRUCache(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestNewLRUCache_NilConfig(t *testing.T) {
	c, err := NewLRUCache(nil)
	require.Error(t, err)
	assert.Nil(t, c)
}

func TestNewLRUCache_InvalidConfig(t *testing.T) {
	c, err := NewLRUCache(&CacheConfig{MaxRepositories: -1, TTL: DefaultTTL, CleanupInterval: DefaultCleanupInterval})
	require.Error(t, err)
	assert.Nil(t, c)
}

func TestNewLRUCacheForTesting(t *testing.T) {
	// Bypasses validation; negative-ish values get defaults applied but no error.
	c, err := NewLRUCacheForTesting(&CacheConfig{Enabled: true})
	require.NoError(t, err)
	require.NotNil(t, c)
	t.Cleanup(func() { _ = c.Close() })
	assert.Equal(t, DefaultMaxRepositories, c.config.MaxRepositories)

	_, err = NewLRUCacheForTesting(nil)
	require.Error(t, err)
}

func TestLRUCache_SetGet(t *testing.T) {
	c := newTestLRU(t, nil)
	repo := newTestRepo(t)

	require.NoError(t, c.Set("/repo/a", repo))

	got, ok := c.Get("/repo/a")
	require.True(t, ok)
	require.NotNil(t, got)
	assert.Same(t, repo, got.GitRepo)

	// Access count incremented on Get.
	assert.GreaterOrEqual(t, got.GetAccessCount(), int64(2))

	m := c.GetMetrics()
	assert.Equal(t, int64(1), m.Hits)
	assert.Equal(t, int64(1), m.Entries)
}

func TestLRUCache_GetMiss(t *testing.T) {
	c := newTestLRU(t, nil)
	got, ok := c.Get("/nope")
	assert.False(t, ok)
	assert.Nil(t, got)
	assert.Equal(t, int64(1), c.GetMetrics().Misses)
}

func TestLRUCache_SetValidation(t *testing.T) {
	c := newTestLRU(t, nil)
	require.Error(t, c.Set("", newTestRepo(t)))
	require.Error(t, c.Set("/x", nil))
}

func TestLRUCache_SetUpdateExisting(t *testing.T) {
	c := newTestLRU(t, nil)
	require.NoError(t, c.Set("/repo", newTestRepo(t)))
	require.NoError(t, c.Set("/repo", newTestRepo(t))) // overwrite, no double count
	assert.Equal(t, int64(1), c.GetMetrics().Entries)
	assert.Equal(t, 1, c.Len())
}

func TestLRUCache_Disabled(t *testing.T) {
	cfg := NewCacheConfig()
	cfg.Enabled = false
	c := newTestLRU(t, cfg)

	// Set is a no-op when disabled.
	require.NoError(t, c.Set("/repo", newTestRepo(t)))
	_, ok := c.Get("/repo")
	assert.False(t, ok)
	assert.False(t, c.Contains("/repo"))
	assert.False(t, c.Remove("/repo"))
	assert.False(t, c.InvalidateRepository("/repo"))
}

func TestLRUCache_RemoveAndContains(t *testing.T) {
	c := newTestLRU(t, nil)
	require.NoError(t, c.Set("/repo", newTestRepo(t)))
	assert.True(t, c.Contains("/repo"))

	assert.True(t, c.Remove("/repo"))
	assert.False(t, c.Contains("/repo"))
	assert.False(t, c.Remove("/repo"))
}

func TestLRUCache_KeysAndLen(t *testing.T) {
	c := newTestLRU(t, nil)
	require.NoError(t, c.Set("/a", newTestRepo(t)))
	require.NoError(t, c.Set("/b", newTestRepo(t)))
	assert.Equal(t, 2, c.Len())
	assert.ElementsMatch(t, []string{"/a", "/b"}, c.Keys())
}

func TestLRUCache_PurgeAndInvalidateAll(t *testing.T) {
	c := newTestLRU(t, nil)
	require.NoError(t, c.Set("/a", newTestRepo(t)))
	require.NoError(t, c.Set("/b", newTestRepo(t)))

	c.InvalidateAll()
	assert.Equal(t, 0, c.Len())
	assert.Equal(t, int64(0), c.GetMetrics().Entries)
}

func TestLRUCache_InvalidateRepository(t *testing.T) {
	c := newTestLRU(t, nil)
	require.NoError(t, c.Set("/a", newTestRepo(t)))

	assert.True(t, c.InvalidateRepository("/a"))
	assert.False(t, c.InvalidateRepository("/a")) // already gone
	assert.False(t, c.InvalidateRepository("/missing"))
}

func TestLRUCache_Eviction(t *testing.T) {
	cfg := NewCacheConfig()
	cfg.MaxRepositories = 2
	c := newTestLRU(t, cfg)

	evicted := make([]string, 0)
	c.SetEvictionCallback(func(key string, _ *Repository) {
		evicted = append(evicted, key)
	})

	require.NoError(t, c.Set("/a", newTestRepo(t)))
	require.NoError(t, c.Set("/b", newTestRepo(t)))
	require.NoError(t, c.Set("/c", newTestRepo(t))) // evicts LRU "/a"

	assert.Equal(t, 2, c.Len())
	assert.Contains(t, evicted, "/a")
	assert.GreaterOrEqual(t, c.GetMetrics().Evictions, int64(1))
}

func TestLRUCache_ExpiryOnGet(t *testing.T) {
	cfg := &CacheConfig{
		MaxRepositories: 10,
		TTL:             20 * time.Millisecond,
		CleanupInterval: time.Hour, // keep background cleanup out of the way
		Enabled:         true,
	}
	// Use the testing constructor to allow a sub-minute TTL.
	c, err := NewLRUCacheForTesting(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Set("/a", newTestRepo(t)))
	time.Sleep(40 * time.Millisecond)

	_, ok := c.Get("/a")
	assert.False(t, ok, "entry should be expired and removed on Get")
	assert.Equal(t, 0, c.Len())
}

func TestRepository_IsExpired(t *testing.T) {
	r := &Repository{CreatedAt: time.Now().Add(-time.Hour)}
	assert.True(t, r.IsExpired(time.Minute))
	assert.False(t, r.IsExpired(2*time.Hour))
	assert.False(t, r.IsExpired(0), "non-positive TTL never expires")
}

func TestRepository_AccessTracking(t *testing.T) {
	r := &Repository{CreatedAt: time.Now()}
	before := r.GetAccessCount()
	r.UpdateAccess()
	assert.Equal(t, before+1, r.GetAccessCount())
	assert.False(t, r.GetLastAccessed().IsZero())
	assert.NoError(t, r.Close())
}

func TestLRUCache_RemoveExpiredEntries(t *testing.T) {
	cfg := &CacheConfig{
		MaxRepositories: 10,
		TTL:             10 * time.Millisecond,
		CleanupInterval: time.Hour,
		Enabled:         true,
	}
	c, err := NewLRUCacheForTesting(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Set("/a", newTestRepo(t)))
	require.NoError(t, c.Set("/b", newTestRepo(t)))
	time.Sleep(25 * time.Millisecond)

	c.removeExpiredEntries()
	assert.Equal(t, 0, c.Len())
}

func TestLRUCache_RemoveExpiredEntries_Disabled(t *testing.T) {
	cfg := NewCacheConfig()
	cfg.Enabled = false
	c := newTestLRU(t, cfg)
	// Should be a no-op and not panic.
	c.removeExpiredEntries()
}

func TestLRUCache_Metrics(t *testing.T) {
	c := newTestLRU(t, nil)
	require.NoError(t, c.Set("/a", newTestRepo(t)))
	c.Get("/a")     // hit
	c.Get("/miss")  // miss
	c.Get("/miss2") // miss

	m := c.GetMetrics()
	assert.Equal(t, int64(1), m.Hits)
	assert.Equal(t, int64(2), m.Misses)

	ratio := c.GetHitRatio()
	assert.InDelta(t, 1.0/3.0, ratio, 1e-9)

	c.ResetMetrics()
	m = c.GetMetrics()
	assert.Equal(t, int64(0), m.Hits)
	assert.Equal(t, int64(0), m.Misses)
	assert.Equal(t, 0.0, c.GetHitRatio())
}

func TestLRUCache_RepositoryCacheInterface(t *testing.T) {
	c := newTestLRU(t, nil)
	repo := newTestRepo(t)

	require.NoError(t, c.SetRepository("/r", repo))
	got, ok := c.GetRepository("/r")
	require.True(t, ok)
	assert.Same(t, repo, got)

	_, ok = c.GetRepository("/missing")
	assert.False(t, ok)
}

func TestLRUCache_PrometheusMetrics(t *testing.T) {
	c := newTestLRU(t, nil)
	c.EnablePrometheusMetrics()
	c.EnablePrometheusMetrics() // idempotent
	require.NoError(t, c.EnablePrometheusMetricsWithRegistry(nil))

	// Exercise paths that update prometheus metrics.
	require.NoError(t, c.Set("/a", newTestRepo(t)))
	c.Get("/a")
	c.Get("/missing")
	assert.True(t, c.Remove("/a"))

	assert.NotNil(t, c.prometheusMetrics)
}

func TestLRUCache_EstimateMemoryUsage(t *testing.T) {
	c := newTestLRU(t, nil)
	require.NoError(t, c.Set("/a", newTestRepo(t)))
	require.NoError(t, c.Set("/b", newTestRepo(t)))
	assert.Equal(t, int64(2*11*1024), c.estimateMemoryUsage())
	assert.Equal(t, int64(3*11*1024), c.estimateMemoryUsageWithEntries(3))
}

func TestLRUCache_HandleEvictionNilRepo(t *testing.T) {
	c := newTestLRU(t, nil)
	// Should not panic with a nil repository.
	assert.NotPanics(t, func() { c.handleEviction("k", nil) })
}
