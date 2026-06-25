package performance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeL1Cache is an in-memory RepositoryCache for tests.
type fakeL1Cache struct {
	mu      sync.Mutex
	repos   map[string]*gogit.Repository
	metrics CacheMetrics
}

func newFakeL1() *fakeL1Cache {
	return &fakeL1Cache{repos: make(map[string]*gogit.Repository)}
}

func (f *fakeL1Cache) GetRepository(path string) (*gogit.Repository, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[path]
	return r, ok
}
func (f *fakeL1Cache) SetRepository(path string, repo *gogit.Repository) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[path] = repo
	f.metrics.Entries = int64(len(f.repos))
	return nil
}
func (f *fakeL1Cache) InvalidateRepository(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.repos[path]
	delete(f.repos, path)
	return ok
}
func (f *fakeL1Cache) InvalidateAll()           { f.repos = make(map[string]*gogit.Repository) }
func (f *fakeL1Cache) Close() error             { return nil }
func (f *fakeL1Cache) GetMetrics() CacheMetrics { return f.metrics }

// fakeL2Cache is an in-memory DistributedCache for tests.
type fakeL2Cache struct {
	mu     sync.Mutex
	data   map[string][]byte
	getErr error
}

func newFakeL2() *fakeL2Cache { return &fakeL2Cache{data: make(map[string][]byte)} }

func (f *fakeL2Cache) Get(ctx context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}
func (f *fakeL2Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = value
	return nil
}
func (f *fakeL2Cache) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}
func (f *fakeL2Cache) Exists(ctx context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[key]
	return ok, nil
}
func (f *fakeL2Cache) Close() error { return nil }

func TestDefaultMultiTierConfig(t *testing.T) {
	cfg := DefaultMultiTierConfig()
	assert.True(t, cfg.L1Enabled)
	assert.True(t, cfg.L2Enabled)
	assert.Equal(t, "gitcache", cfg.L2KeyPrefix)
	assert.Equal(t, 0.7, cfg.PrefetchConfidence)
}

// testMultiTierConfig disables prefetching/background goroutines for determinism.
func testMultiTierConfig() *MultiTierConfig {
	return &MultiTierConfig{
		L1Enabled:         true,
		L1MaxEntries:      10,
		L1TTL:             time.Minute,
		L2Enabled:         true,
		L2TTL:             time.Minute,
		L2KeyPrefix:       "test",
		PrefetchEnabled:   false,
		TrackingEnabled:   true,
		PatternWindowSize: 50,
	}
}

func TestMultiTierCache_NilConfigUsesDefault(t *testing.T) {
	l1 := newFakeL1()
	mtc := NewMultiTierCache(l1, nil, nil, testLogger())
	require.NotNil(t, mtc)
	// Default config enables prefetcher; ensure clean shutdown.
	require.NoError(t, mtc.Close())
}

func TestMultiTierCache_L1HitMiss(t *testing.T) {
	l1 := newFakeL1()
	l2 := newFakeL2()
	mtc := NewMultiTierCache(l1, l2, testMultiTierConfig(), testLogger())
	defer mtc.Close()

	ctx := context.Background()

	// Miss everywhere.
	_, found := mtc.GetRepository(ctx, "owner/repo")
	assert.False(t, found)

	// Set into cache, then hit L1.
	require.NoError(t, mtc.SetRepository(ctx, "owner/repo", &gogit.Repository{}))
	_, found = mtc.GetRepository(ctx, "owner/repo")
	assert.True(t, found)

	stats := mtc.GetStats()
	assert.Equal(t, int64(1), stats.L1Hits)
	assert.GreaterOrEqual(t, stats.L1Misses, int64(1))
}

func TestMultiTierCache_Invalidate(t *testing.T) {
	l1 := newFakeL1()
	l2 := newFakeL2()
	mtc := NewMultiTierCache(l1, l2, testMultiTierConfig(), testLogger())
	defer mtc.Close()

	ctx := context.Background()
	require.NoError(t, mtc.SetRepository(ctx, "a/b", &gogit.Repository{}))
	mtc.InvalidateRepository(ctx, "a/b")

	_, found := l1.GetRepository("a/b")
	assert.False(t, found)
}

func TestMultiTierCache_HitRate(t *testing.T) {
	l1 := newFakeL1()
	mtc := NewMultiTierCache(l1, nil, testMultiTierConfig(), testLogger())
	defer mtc.Close()

	ctx := context.Background()
	// Zero requests -> 0 rate.
	assert.Equal(t, 0.0, mtc.GetOptimizedHitRate())

	require.NoError(t, mtc.SetRepository(ctx, "x/y", &gogit.Repository{}))
	mtc.GetRepository(ctx, "x/y")  // hit
	mtc.GetRepository(ctx, "miss") // miss

	assert.Greater(t, mtc.GetOptimizedHitRate(), 0.0)
}

func TestMultiTierCache_L2KeyAndSerialize(t *testing.T) {
	l1 := newFakeL1()
	l2 := newFakeL2()
	mtc := NewMultiTierCache(l1, l2, testMultiTierConfig(), testLogger())
	defer mtc.Close()

	key := mtc.getL2Key("path")
	assert.Equal(t, "test:repo:path", key)

	data := mtc.serializeRepository(&gogit.Repository{})
	assert.NotEmpty(t, data)

	// deserialize returns nil placeholder by design.
	assert.Nil(t, mtc.deserializeRepository(data))
}
