package performance

import (
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

func TestObjectCache_DisabledUsesDirectAccess(t *testing.T) {
	r := gitrepo.NewRepo(t)
	hash := r.Commit("file.txt", "hello world", "init")
	repo := r.Repo

	cfg := &PerformanceConfig{EnableObjectCache: false}
	oc := NewObjectCache(cfg, testLogger())

	commit, err := oc.GetCommit(repo, hash)
	require.NoError(t, err)
	require.NotNil(t, commit)

	tree, err := oc.GetTree(repo, commit.TreeHash)
	require.NoError(t, err)
	require.NotNil(t, tree)

	// Stats updated even when disabled (miss path), but no hits.
	stats := oc.GetStats()
	assert.Equal(t, int64(0), stats.BlobHits)
}

func TestObjectCache_BlobCachingHitMiss(t *testing.T) {
	r := gitrepo.NewRepo(t)
	hash := r.Commit("file.txt", "content of file", "init")
	repo := r.Repo

	commit, err := repo.CommitObject(hash)
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	require.NotEmpty(t, tree.Entries)
	blobHash := tree.Entries[0].Hash

	cfg := &PerformanceConfig{EnableObjectCache: true}
	oc := NewObjectCache(cfg, testLogger())

	// First call -> miss + store.
	blob1, err := oc.GetBlob(repo, blobHash)
	require.NoError(t, err)
	require.NotNil(t, blob1)

	// Second call -> hit.
	blob2, err := oc.GetBlob(repo, blobHash)
	require.NoError(t, err)
	assert.Equal(t, blob1, blob2)

	stats := oc.GetStats()
	assert.Equal(t, int64(1), stats.BlobHits)
	assert.Equal(t, int64(1), stats.BlobMisses)
	assert.Greater(t, stats.HitRate, 0.0)
	assert.GreaterOrEqual(t, stats.CacheSize, int64(1))
}

func TestObjectCache_TreeAndCommitCaching(t *testing.T) {
	r := gitrepo.NewRepo(t)
	hash := r.Commit("file.txt", "abc", "init")
	repo := r.Repo

	cfg := &PerformanceConfig{EnableObjectCache: true}
	oc := NewObjectCache(cfg, testLogger())

	// Commit caching: miss then hit.
	c1, err := oc.GetCommit(repo, hash)
	require.NoError(t, err)
	c2, err := oc.GetCommit(repo, hash)
	require.NoError(t, err)
	assert.Equal(t, c1, c2)

	// Tree caching: miss then hit.
	t1, err := oc.GetTree(repo, c1.TreeHash)
	require.NoError(t, err)
	t2, err := oc.GetTree(repo, c1.TreeHash)
	require.NoError(t, err)
	assert.Equal(t, t1, t2)

	stats := oc.GetStats()
	assert.Equal(t, int64(1), stats.CommitHits)
	assert.Equal(t, int64(1), stats.TreeHits)
}

func TestObjectCache_GetBlobBatch(t *testing.T) {
	r := gitrepo.NewRepo(t)
	hash := r.CommitMultiple(map[string]string{
		"a.txt": "alpha content",
		"b.txt": "beta content",
	}, "init")
	repo := r.Repo

	commit, err := repo.CommitObject(hash)
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	var hashes []plumbing.Hash
	for _, e := range tree.Entries {
		hashes = append(hashes, e.Hash)
	}
	require.Len(t, hashes, 2)

	cfg := &PerformanceConfig{EnableObjectCache: true}
	oc := NewObjectCache(cfg, testLogger())

	// First batch: all misses.
	results := mustBatch(t, oc, repo, hashes)
	assert.Len(t, results, 2)

	// Second batch: all hits.
	results2 := mustBatch(t, oc, repo, hashes)
	assert.Len(t, results2, 2)

	stats := oc.GetStats()
	assert.Equal(t, int64(2), stats.BlobHits)
	assert.Equal(t, int64(2), stats.BlobMisses)
}

func mustBatch(t *testing.T, oc *ObjectCache, repo *gogit.Repository, hashes []plumbing.Hash) map[plumbing.Hash]interface{} {
	t.Helper()
	res, err := oc.GetBlobBatch(repo, hashes)
	require.NoError(t, err)
	out := make(map[plumbing.Hash]interface{}, len(res))
	for k, v := range res {
		out[k] = v
	}
	return out
}

func TestObjectCache_MissingObjectErrors(t *testing.T) {
	r := gitrepo.NewRepo(t)
	r.Commit("file.txt", "x", "init")
	repo := r.Repo

	cfg := &PerformanceConfig{EnableObjectCache: true}
	oc := NewObjectCache(cfg, testLogger())

	bogus := plumbing.NewHash("0123456789012345678901234567890123456789")
	_, err := oc.GetBlob(repo, bogus)
	assert.Error(t, err)
	_, err = oc.GetTree(repo, bogus)
	assert.Error(t, err)
	_, err = oc.GetCommit(repo, bogus)
	assert.Error(t, err)
}

func TestObjectCache_Eviction(t *testing.T) {
	// Directly exercise the LRU eviction logic with a tiny cache.
	cache := newLRUCache(2)
	cfg := &PerformanceConfig{EnableObjectCache: true}
	oc := NewObjectCache(cfg, testLogger())

	h1 := plumbing.NewHash("1111111111111111111111111111111111111111")
	h2 := plumbing.NewHash("2222222222222222222222222222222222222222")
	h3 := plumbing.NewHash("3333333333333333333333333333333333333333")

	oc.putInCache(cache, h1, "v1", 1)
	oc.putInCache(cache, h2, "v2", 1)
	// Access h1 to make it most-recently-used.
	_, found := oc.getFromCache(cache, h1)
	assert.True(t, found)
	// Insert h3 -> should evict h2 (least recently used).
	oc.putInCache(cache, h3, "v3", 1)

	_, foundH2 := oc.getFromCache(cache, h2)
	assert.False(t, foundH2)
	_, foundH1 := oc.getFromCache(cache, h1)
	assert.True(t, foundH1)

	stats := oc.GetStats()
	assert.GreaterOrEqual(t, stats.EvictedItems, int64(1))

	// Updating an existing key moves it to front without growing.
	oc.putInCache(cache, h1, "v1-updated", 2)
	val, _ := oc.getFromCache(cache, h1)
	assert.Equal(t, "v1-updated", val)
}

func TestObjectCache_ClearCache(t *testing.T) {
	cfg := &PerformanceConfig{EnableObjectCache: true}
	oc := NewObjectCache(cfg, testLogger())

	cache := oc.blobCache
	h := plumbing.NewHash("4444444444444444444444444444444444444444")
	oc.putInCache(cache, h, "v", 1)

	oc.ClearCache()
	stats := oc.GetStats()
	assert.Equal(t, int64(0), stats.CacheSize)
	assert.Equal(t, int64(0), stats.BlobHits)
}

func TestEstimateSizeHelpers(t *testing.T) {
	r := gitrepo.NewRepo(t)
	hash := r.Commit("file.txt", "some content here", "a commit message")
	repo := r.Repo

	commit, err := repo.CommitObject(hash)
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	assert.Greater(t, estimateTreeSize(tree), int64(0))
	assert.Greater(t, estimateCommitSize(commit), int64(0))
}
