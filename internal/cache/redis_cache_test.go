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

func newRedisCache(t *testing.T) (*RedisCache[string], *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc, err := NewRedisCache[string](&RedisCacheConfig{
		Address: mr.Addr(),
		Prefix:  "test:",
	}, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rc.Close() })
	return rc, mr
}

func TestNewRedisCache_NilConfig(t *testing.T) {
	rc, err := NewRedisCache[string](nil, zaptest.NewLogger(t))
	require.Error(t, err)
	assert.Nil(t, rc)
}

func TestNewRedisCache_AppliesDefaults(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := &RedisCacheConfig{Address: mr.Addr()}
	rc, err := NewRedisCache[string](cfg, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rc.Close() })

	// Defaults filled in by the constructor.
	assert.Equal(t, "collabops:", cfg.Prefix)
	assert.Equal(t, 10, cfg.PoolSize)
	assert.Equal(t, 3, cfg.MaxRetries)
	assert.Equal(t, 5*time.Second, cfg.DialTimeout)
}

func TestNewRedisCache_ConnectFailure(t *testing.T) {
	rc, err := NewRedisCache[string](&RedisCacheConfig{
		Address:     "127.0.0.1:1", // nothing listening
		DialTimeout: 200 * time.Millisecond,
		MaxRetries:  1,
	}, zaptest.NewLogger(t))
	require.Error(t, err)
	assert.Nil(t, rc)
	assert.Contains(t, err.Error(), "failed to connect to Redis")
}

func TestRedisCache_SetGet(t *testing.T) {
	rc, _ := newRedisCache(t)
	ctx := context.Background()

	require.NoError(t, rc.Set(ctx, "k", "hello", time.Minute))

	got, ok, err := rc.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "hello", got)
	assert.Equal(t, int64(1), rc.GetMetrics().Hits)
}

func TestRedisCache_GetMiss(t *testing.T) {
	rc, _ := newRedisCache(t)
	got, ok, err := rc.Get(context.Background(), "absent")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, got)
	assert.Equal(t, int64(1), rc.GetMetrics().Misses)
}

func TestRedisCache_GetDeserializationError(t *testing.T) {
	rc, mr := newRedisCache(t)
	// Store a non-JSON value directly under the prefixed key.
	require.NoError(t, mr.Set("test:bad", "not-json-{"))

	_, ok, err := rc.Get(context.Background(), "bad")
	require.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, int64(1), rc.GetMetrics().SerializationErrors)
}

func TestRedisCache_SetSerializationError(t *testing.T) {
	mr := miniredis.RunT(t)
	// chan types cannot be JSON-marshalled, forcing a serialization error.
	rc, err := NewRedisCache[chan int](&RedisCacheConfig{Address: mr.Addr(), Prefix: "t:"}, zaptest.NewLogger(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rc.Close() })

	err = rc.Set(context.Background(), "k", make(chan int), time.Minute)
	require.Error(t, err)
	assert.Equal(t, int64(1), rc.GetMetrics().SerializationErrors)
}

func TestRedisCache_Delete(t *testing.T) {
	rc, _ := newRedisCache(t)
	ctx := context.Background()
	require.NoError(t, rc.Set(ctx, "k", "v", time.Minute))
	require.NoError(t, rc.Delete(ctx, "k"))

	_, ok, err := rc.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRedisCache_DeleteByPattern(t *testing.T) {
	rc, _ := newRedisCache(t)
	ctx := context.Background()
	require.NoError(t, rc.Set(ctx, "user:1", "a", time.Minute))
	require.NoError(t, rc.Set(ctx, "user:2", "b", time.Minute))
	require.NoError(t, rc.Set(ctx, "other", "c", time.Minute))

	require.NoError(t, rc.DeleteByPattern(ctx, "user:*"))

	_, ok, _ := rc.Get(ctx, "user:1")
	assert.False(t, ok)
	_, ok, _ = rc.Get(ctx, "other")
	assert.True(t, ok)

	// No matching keys is not an error.
	require.NoError(t, rc.DeleteByPattern(ctx, "nomatch:*"))
}

func TestRedisCache_Exists(t *testing.T) {
	rc, _ := newRedisCache(t)
	ctx := context.Background()
	require.NoError(t, rc.Set(ctx, "k", "v", time.Minute))

	exists, err := rc.Exists(ctx, "k")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = rc.Exists(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestRedisCache_TTLOperations(t *testing.T) {
	rc, mr := newRedisCache(t)
	ctx := context.Background()
	require.NoError(t, rc.Set(ctx, "k", "v", time.Hour))

	ttl, err := rc.GetTTL(ctx, "k")
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))

	require.NoError(t, rc.SetTTL(ctx, "k", 30*time.Minute))
	ttl, err = rc.GetTTL(ctx, "k")
	require.NoError(t, err)
	assert.LessOrEqual(t, ttl, 30*time.Minute)

	// FastForward expires the key in miniredis.
	mr.FastForward(31 * time.Minute)
	_, ok, err := rc.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRedisCache_Ping(t *testing.T) {
	rc, _ := newRedisCache(t)
	require.NoError(t, rc.Ping(context.Background()))
}

func TestRedisCache_GetInfo(t *testing.T) {
	rc, _ := newRedisCache(t)
	info, err := rc.GetInfo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test:", info["prefix"])
	assert.Contains(t, info, "hit_ratio")
	assert.Contains(t, info, "redis_info")
}

func TestRedisCache_HitRatioAndReset(t *testing.T) {
	rc, _ := newRedisCache(t)
	ctx := context.Background()
	assert.Equal(t, 0.0, rc.GetHitRatio())

	require.NoError(t, rc.Set(ctx, "k", "v", time.Minute))
	_, _, _ = rc.Get(ctx, "k")       // hit
	_, _, _ = rc.Get(ctx, "missing") // miss
	assert.InDelta(t, 0.5, rc.GetHitRatio(), 1e-9)

	rc.ResetMetrics()
	assert.Equal(t, int64(0), rc.GetMetrics().Hits)
	assert.Equal(t, 0.0, rc.GetHitRatio())
}

func TestRedisCache_AverageResponseTime(t *testing.T) {
	rc, _ := newRedisCache(t)
	ctx := context.Background()
	require.NoError(t, rc.Set(ctx, "a", "1", time.Minute))
	require.NoError(t, rc.Set(ctx, "b", "2", time.Minute))
	// More than one request triggers the moving-average branch.
	assert.GreaterOrEqual(t, rc.GetMetrics().TotalRequests, int64(2))
}

func TestRedisCache_ErrorAfterClose(t *testing.T) {
	rc, _ := newRedisCache(t)
	require.NoError(t, rc.Close())

	// Operations after Close should surface an error and bump error metrics.
	err := rc.Set(context.Background(), "k", "v", time.Minute)
	require.Error(t, err)
	assert.GreaterOrEqual(t, rc.GetMetrics().Errors, int64(1))
}
