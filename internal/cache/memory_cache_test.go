package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func newMemCache(t *testing.T, cfg *MemoryCacheConfig) *MemoryCache[string] {
	t.Helper()
	mc := NewMemoryCache[string](cfg, zaptest.NewLogger(t))
	t.Cleanup(func() { _ = mc.Close() })
	return mc
}

func TestNewMemoryCache_NilConfigDefaults(t *testing.T) {
	mc := newMemCache(t, nil)
	assert.Equal(t, 1000, mc.maxSize)
}

func TestNewMemoryCache_AppliesDefaultsForZeroValues(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{
		MaxSize:         0,
		DefaultTTL:      0,
		CleanupInterval: 0,
	})
	assert.Equal(t, 1000, mc.maxSize)
}

func TestMemoryCache_SetGet(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})

	mc.Set("k", "v", time.Minute)
	got, ok := mc.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", got)
	assert.Equal(t, 1, mc.Size())

	m := mc.GetMetrics()
	assert.Equal(t, int64(1), m.Hits)
	assert.Equal(t, int64(1), m.CurrentEntries)
}

func TestMemoryCache_GetMiss(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	_, ok := mc.Get("nope")
	assert.False(t, ok)
	assert.Equal(t, int64(1), mc.GetMetrics().Misses)
}

func TestMemoryCache_UpdateExisting(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("k", "v1", time.Minute)
	mc.Set("k", "v2", time.Minute)
	got, ok := mc.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v2", got)
	assert.Equal(t, 1, mc.Size())
}

func TestMemoryCache_Expiry(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("k", "v", 10*time.Millisecond)
	time.Sleep(25 * time.Millisecond)

	_, ok := mc.Get("k")
	assert.False(t, ok)
	assert.Equal(t, int64(1), mc.GetMetrics().Expirations)
	assert.Equal(t, 0, mc.Size())
}

func TestMemoryCache_Delete(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("k", "v", time.Minute)
	assert.True(t, mc.Delete("k"))
	assert.False(t, mc.Delete("k"))
	assert.Equal(t, 0, mc.Size())
}

func TestMemoryCache_Clear(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("a", "1", time.Minute)
	mc.Set("b", "2", time.Minute)
	mc.Clear()
	assert.Equal(t, 0, mc.Size())
	assert.Equal(t, int64(0), mc.GetMetrics().CurrentEntries)
}

func TestMemoryCache_Eviction(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 2, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("a", "1", time.Minute)
	mc.Set("b", "2", time.Minute)
	// Touch "a" so "b" becomes least-recently-used.
	_, _ = mc.Get("a")
	mc.Set("c", "3", time.Minute) // evicts "b"

	assert.Equal(t, 2, mc.Size())
	_, ok := mc.Get("b")
	assert.False(t, ok)
	assert.GreaterOrEqual(t, mc.GetMetrics().Evictions, int64(1))
}

func TestMemoryCache_HitRatio(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	assert.Equal(t, 0.0, mc.GetHitRatio())

	mc.Set("k", "v", time.Minute)
	_, _ = mc.Get("k")    // hit
	_, _ = mc.Get("miss") // miss
	assert.InDelta(t, 0.5, mc.GetHitRatio(), 1e-9)
}

func TestMemoryCache_ResetMetrics(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("k", "v", time.Minute)
	_, _ = mc.Get("k")
	mc.ResetMetrics()
	m := mc.GetMetrics()
	assert.Equal(t, int64(0), m.Hits)
	assert.Equal(t, int64(0), m.Misses)
}

func TestMemoryCache_AverageAccessTime(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("k", "v", time.Minute)
	_, _ = mc.Get("k")
	m := mc.GetMetrics()
	assert.GreaterOrEqual(t, m.AverageAccessTime, 0.0)
}

func TestMemoryCache_BackgroundCleanup(t *testing.T) {
	mc := NewMemoryCache[string](&MemoryCacheConfig{
		MaxSize:         10,
		DefaultTTL:      time.Minute,
		CleanupInterval: 10 * time.Millisecond,
	}, zap.NewNop())
	t.Cleanup(func() { _ = mc.Close() })

	mc.Set("k", "v", 5*time.Millisecond)
	// Wait long enough for at least one background cleanup tick.
	require.Eventually(t, func() bool {
		return mc.Size() == 0
	}, time.Second, 5*time.Millisecond)
	assert.GreaterOrEqual(t, mc.GetMetrics().Expirations, int64(1))
}

func TestMemoryCache_GetCacheInfo(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	mc.Set("k", "v", time.Minute)
	_, _ = mc.Get("k")

	info := mc.GetCacheInfo()
	assert.Equal(t, "memory_cache", info["type"])
	assert.Equal(t, 1, info["current_size"])
	assert.Equal(t, 10, info["max_size"])
	assert.Contains(t, info, "hit_ratio")
	assert.Contains(t, info, "utilization")
	assert.Contains(t, info, "metrics")
}

func TestMemoryCache_EstimateItemSize(t *testing.T) {
	mc := newMemCache(t, &MemoryCacheConfig{MaxSize: 10, DefaultTTL: time.Minute, CleanupInterval: time.Hour})
	size := mc.estimateItemSize(&memoryCacheItem[string]{key: "abc"})
	assert.Greater(t, size, int64(0))
}
