package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// newMultiTierL1Only builds an L1-only multi-tier cache (no Redis required).
func newMultiTierL1Only(t *testing.T) *MultiTierCache[string] {
	t.Helper()
	mtc, err := NewMultiTierCache[string](&MultiTierConfig{
		L1MaxSize:         100,
		L1DefaultTTL:      time.Minute,
		L1CleanupInterval: time.Hour,
		L2Enabled:         false,
	}, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mtc.Close() })
	return mtc
}

// newMultiTierWithRedis builds a multi-tier cache backed by miniredis.
func newMultiTierWithRedis(t *testing.T) (*MultiTierCache[string], *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	mtc, err := NewMultiTierCache[string](&MultiTierConfig{
		L1MaxSize:          100,
		L1DefaultTTL:       time.Minute,
		L1CleanupInterval:  time.Hour,
		L2Enabled:          true,
		L2DefaultTTL:       time.Minute,
		L2Address:          mr.Addr(),
		EnableCacheWarming: true,
		WarmL1FromL2:       true,
	}, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mtc.Close() })
	return mtc, mr
}

func TestNewMultiTierCache_NilConfigDefaults(t *testing.T) {
	// Nil config defaults L2Enabled to false, so no Redis is needed.
	mtc, err := NewMultiTierCache[string](nil, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mtc.Close() })
	assert.Nil(t, mtc.L2)
	assert.NotNil(t, mtc.L1)
}

func TestNewMultiTierCache_AppliesDefaults(t *testing.T) {
	mtc, err := NewMultiTierCache[string](&MultiTierConfig{L2Enabled: false}, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mtc.Close() })
	assert.Equal(t, 1000, mtc.config.L1MaxSize)
	assert.Equal(t, 10*time.Minute, mtc.config.L1DefaultTTL)
	assert.Equal(t, 30*time.Minute, mtc.config.L2DefaultTTL)
	assert.Equal(t, 5, mtc.config.MaxConcurrentWarming)
}

func TestNewMultiTierCache_L2ConnectFailure(t *testing.T) {
	mtc, err := NewMultiTierCache[string](&MultiTierConfig{
		L2Enabled: true,
		L2Address: "127.0.0.1:1",
	}, zaptest.NewLogger(t))
	require.Error(t, err)
	assert.Nil(t, mtc)
	assert.Contains(t, err.Error(), "failed to initialize L2 Redis cache")
}

func TestMultiTierCache_L1OnlySetGet(t *testing.T) {
	mtc := newMultiTierL1Only(t)
	ctx := context.Background()

	require.NoError(t, mtc.Set(ctx, "k", "v"))
	got, ok := mtc.Get(ctx, "k")
	require.True(t, ok)
	assert.Equal(t, "v", got)

	m := mtc.GetMetrics()
	assert.Equal(t, int64(1), m.L1Hits)
}

func TestMultiTierCache_L1Miss(t *testing.T) {
	mtc := newMultiTierL1Only(t)
	_, ok := mtc.Get(context.Background(), "missing")
	assert.False(t, ok)
	assert.Equal(t, int64(1), mtc.GetMetrics().L1Misses)
}

func TestMultiTierCache_L2HitWarmsL1(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	ctx := context.Background()

	// Write directly to L2, bypassing L1.
	require.NoError(t, mtc.L2.Set(ctx, "k", "from-l2", time.Minute))

	got, ok := mtc.Get(ctx, "k")
	require.True(t, ok)
	assert.Equal(t, "from-l2", got)

	m := mtc.GetMetrics()
	assert.Equal(t, int64(1), m.L1Misses)
	assert.Equal(t, int64(1), m.L2Hits)

	// L1 should eventually be warmed from L2 (async goroutine).
	require.Eventually(t, func() bool {
		v, ok := mtc.L1.Get("k")
		return ok && v == "from-l2"
	}, time.Second, 10*time.Millisecond)
}

func TestMultiTierCache_BothMiss(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	_, ok := mtc.Get(context.Background(), "missing")
	assert.False(t, ok)
	m := mtc.GetMetrics()
	assert.Equal(t, int64(1), m.L1Misses)
	assert.Equal(t, int64(1), m.L2Misses)
}

func TestMultiTierCache_SetWritesBothTiers(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	ctx := context.Background()

	require.NoError(t, mtc.Set(ctx, "k", "v"))

	// Present in L1.
	v1, ok := mtc.L1.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", v1)

	// Present in L2.
	v2, ok, err := mtc.L2.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "v", v2)
}

func TestMultiTierCache_InvalidateKey(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	ctx := context.Background()
	require.NoError(t, mtc.Set(ctx, "k", "v"))

	require.NoError(t, mtc.InvalidateKey(ctx, "k"))

	_, ok := mtc.L1.Get("k")
	assert.False(t, ok)
	_, ok, _ = mtc.L2.Get(ctx, "k")
	assert.False(t, ok)
}

func TestMultiTierCache_InvalidateKey_L1Only(t *testing.T) {
	mtc := newMultiTierL1Only(t)
	ctx := context.Background()
	require.NoError(t, mtc.Set(ctx, "k", "v"))
	require.NoError(t, mtc.InvalidateKey(ctx, "k"))
	_, ok := mtc.L1.Get("k")
	assert.False(t, ok)
}

func TestMultiTierCache_InvalidateByPattern(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	ctx := context.Background()
	require.NoError(t, mtc.Set(ctx, "user:1", "a"))
	require.NoError(t, mtc.Set(ctx, "user:2", "b"))

	require.NoError(t, mtc.InvalidateByPattern(ctx, "user:*"))

	// L1 is fully cleared.
	assert.Equal(t, 0, mtc.L1.Size())
	// L2 pattern keys removed.
	_, ok, _ := mtc.L2.Get(ctx, "user:1")
	assert.False(t, ok)
}

func TestMultiTierCache_InvalidateByPattern_L1Only(t *testing.T) {
	mtc := newMultiTierL1Only(t)
	ctx := context.Background()
	require.NoError(t, mtc.Set(ctx, "a", "1"))
	require.NoError(t, mtc.InvalidateByPattern(ctx, "a*"))
	assert.Equal(t, 0, mtc.L1.Size())
}

func TestMultiTierCache_OverallHitRatio(t *testing.T) {
	mtc := newMultiTierL1Only(t)
	ctx := context.Background()
	assert.Equal(t, 0.0, mtc.GetOverallHitRatio())

	require.NoError(t, mtc.Set(ctx, "k", "v"))
	_, _ = mtc.Get(ctx, "k")       // hit
	_, _ = mtc.Get(ctx, "missing") // miss
	assert.InDelta(t, 0.5, mtc.GetOverallHitRatio(), 1e-9)
}

func TestMultiTierCache_HealthCheck_L1Only(t *testing.T) {
	mtc := newMultiTierL1Only(t)
	health := mtc.HealthCheck(context.Background())
	assert.Equal(t, "healthy", health["overall_status"])
	l2 := health["l2_cache"].(map[string]interface{})
	assert.Equal(t, "disabled", l2["status"])
}

func TestMultiTierCache_HealthCheck_WithRedis(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	health := mtc.HealthCheck(context.Background())
	assert.Equal(t, "healthy", health["overall_status"])
	assert.Contains(t, health, "l1_cache")
	assert.Contains(t, health, "metrics")
}

func TestMultiTierCache_HealthCheck_Degraded(t *testing.T) {
	mtc, mr := newMultiTierWithRedis(t)
	mr.Close() // kill Redis to force degraded status

	health := mtc.HealthCheck(context.Background())
	assert.Equal(t, "degraded", health["overall_status"])
}

func TestMultiTierCache_GetCacheInfo(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	info := mtc.GetCacheInfo()
	assert.Equal(t, "multi_tier_cache", info["type"])
	assert.Equal(t, true, info["l2_enabled"])
	assert.Contains(t, info, "l2_hit_ratio")
	assert.Contains(t, info, "overall_hit_ratio")
}

func TestMultiTierCache_GetCacheInfo_L1Only(t *testing.T) {
	mtc := newMultiTierL1Only(t)
	info := mtc.GetCacheInfo()
	assert.Equal(t, false, info["l2_enabled"])
	assert.NotContains(t, info, "l2_hit_ratio")
}

func TestMultiTierCache_WarmL1Cache_DisabledWarming(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	mtc.config.EnableCacheWarming = false
	// Should return early without recording a warming event.
	mtc.warmL1Cache("k", "v")
	assert.Equal(t, int64(0), mtc.GetMetrics().WarmingEvents)
}

func TestMultiTierCache_WarmL1Cache_Deduplicates(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	// Pre-mark the key as warming so the call short-circuits.
	mtc.warmingMu.Lock()
	mtc.warmingKeys["k"] = true
	mtc.warmingMu.Unlock()

	mtc.warmL1Cache("k", "v")
	assert.Equal(t, int64(0), mtc.GetMetrics().WarmingEvents)
}

func TestMultiTierCache_WarmL1Cache_Success(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	mtc.warmL1Cache("k", "v")
	v, ok := mtc.L1.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", v)
	assert.Equal(t, int64(1), mtc.GetMetrics().WarmingEvents)
}

func TestMultiTierCache_ResponseTimeAverages(t *testing.T) {
	mtc, _ := newMultiTierWithRedis(t)
	ctx := context.Background()

	require.NoError(t, mtc.Set(ctx, "k", "v"))
	// Two L1 hits exercises the moving-average branch.
	_, _ = mtc.Get(ctx, "k")
	_, _ = mtc.Get(ctx, "k")

	// Two L2 hits via direct L2 seeding + L1 miss.
	require.NoError(t, mtc.L2.Set(ctx, "x", "y", time.Minute))
	require.NoError(t, mtc.L2.Set(ctx, "x2", "y2", time.Minute))
	_, _ = mtc.Get(ctx, "x")
	_, _ = mtc.Get(ctx, "x2")

	m := mtc.GetMetrics()
	assert.GreaterOrEqual(t, m.L1Hits, int64(2))
	assert.GreaterOrEqual(t, m.L2Hits, int64(2))
}
