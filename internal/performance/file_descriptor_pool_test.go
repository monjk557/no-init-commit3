package performance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

func TestFileDescriptorPool_DisabledDirectOpen(t *testing.T) {
	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")

	cfg := &PerformanceConfig{} // both flags false -> optimization disabled
	pool := NewFileDescriptorPool(cfg, testLogger())
	defer closePoolSafely(pool)

	assert.False(t, pool.isOptimizationEnabled())

	repo, err := pool.GetRepository(r.Dir)
	require.NoError(t, err)
	require.NotNil(t, repo)

	// Release is a no-op when disabled (should not panic).
	pool.ReleaseRepository(r.Dir)

	stats := pool.GetStats()
	assert.Equal(t, int64(1), stats.PoolMisses)
}

func TestFileDescriptorPool_PoolHitMiss(t *testing.T) {
	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")

	cfg := &PerformanceConfig{EnableObjectCache: true}
	pool := NewFileDescriptorPool(cfg, testLogger())
	defer closePoolSafely(pool)

	assert.True(t, pool.isOptimizationEnabled())

	// First open: miss + add to pool.
	repo1, err := pool.GetRepository(r.Dir)
	require.NoError(t, err)
	require.NotNil(t, repo1)

	// Second open: hit, same instance.
	repo2, err := pool.GetRepository(r.Dir)
	require.NoError(t, err)
	assert.Same(t, repo1, repo2)

	stats := pool.GetStats()
	assert.Equal(t, int64(1), stats.PoolHits)
	assert.Equal(t, int64(1), stats.PoolMisses)
	assert.Greater(t, stats.HitRate, 0.0)

	// Release decrements ref count.
	pool.ReleaseRepository(r.Dir)
	pool.ReleaseRepository(r.Dir)
	// Extra release does not go below zero.
	pool.ReleaseRepository(r.Dir)

	assert.Greater(t, pool.GetPoolUtilization(), 0.0)
}

func TestFileDescriptorPool_OpenError(t *testing.T) {
	cfg := &PerformanceConfig{EnableObjectCache: true}
	pool := NewFileDescriptorPool(cfg, testLogger())
	defer closePoolSafely(pool)

	_, err := pool.GetRepository("/nonexistent/path/to/repo")
	assert.Error(t, err)
}

func TestFileDescriptorPool_Eviction(t *testing.T) {
	cfg := &PerformanceConfig{EnableObjectCache: true}
	pool := NewFileDescriptorPool(cfg, testLogger())
	defer closePoolSafely(pool)
	pool.maxPoolSize = 2

	// Create 3 distinct repos to force eviction.
	for i := 0; i < 3; i++ {
		r := gitrepo.NewRepo(t)
		r.Commit("f.txt", "x", "init")
		_, err := pool.GetRepository(r.Dir)
		require.NoError(t, err)
		// Release so ref count is zero -> evictable.
		pool.ReleaseRepository(r.Dir)
	}

	pool.mutex.RLock()
	size := len(pool.repositories)
	pool.mutex.RUnlock()
	assert.LessOrEqual(t, size, 2)
	assert.GreaterOrEqual(t, pool.GetStats().EvictedFDs, int64(1))
}

func TestFileDescriptorPool_Cleanup(t *testing.T) {
	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")

	cfg := &PerformanceConfig{EnableObjectCache: true}
	pool := NewFileDescriptorPool(cfg, testLogger())
	defer closePoolSafely(pool)

	_, err := pool.GetRepository(r.Dir)
	require.NoError(t, err)
	pool.ReleaseRepository(r.Dir)

	// Force the entry to be idle.
	pool.mutex.Lock()
	pool.maxIdleTime = time.Nanosecond
	for _, p := range pool.repositories {
		p.lastUsed = time.Now().Add(-time.Hour)
		p.refCount = 0
	}
	pool.mutex.Unlock()

	pool.cleanup()

	pool.mutex.RLock()
	size := len(pool.repositories)
	pool.mutex.RUnlock()
	assert.Equal(t, 0, size)
}

func TestFileDescriptorPool_GetPoolUtilizationZeroMax(t *testing.T) {
	cfg := &PerformanceConfig{}
	pool := NewFileDescriptorPool(cfg, testLogger())
	defer closePoolSafely(pool)
	pool.maxPoolSize = 0
	assert.Equal(t, float64(0), pool.GetPoolUtilization())
}

// closePoolSafely closes the pool, which starts a cleanup worker only when
// optimization is enabled. Close always closes the stop channel.
func closePoolSafely(pool *FileDescriptorPool) {
	defer func() { _ = recover() }()
	pool.Close()
}
