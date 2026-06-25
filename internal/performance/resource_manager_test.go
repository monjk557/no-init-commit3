package performance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

// newPathConfig builds a PathConfig rooted at root for repository pool tests.
func newPathConfig(t *testing.T, root string) (*repositories.PathConfig, error) {
	t.Helper()
	return repositories.NewPathConfig(root)
}

// makeOwnerRepo creates a real non-bare git repo with one commit at the layout
// expected by repositories.GetRepoPath ({root}/{owner}/{repo}.git) and returns
// the owner and repo identifiers.
func makeOwnerRepo(t *testing.T, root, owner, name string) (string, string) {
	t.Helper()
	repoPath := filepath.Join(root, owner, name+".git")
	require.NoError(t, os.MkdirAll(repoPath, 0755))

	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	fp := filepath.Join(repoPath, "README.md")
	require.NoError(t, os.WriteFile(fp, []byte("hello"), 0644))
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	return owner, name
}

func testLoggingConfig() config.LoggingConfig {
	return config.LoggingConfig{Level: "info", Format: "json"}
}

func newTestLoggingLogger(t *testing.T) *logging.Logger {
	t.Helper()
	l, err := logging.NewLogger(testLoggingConfig())
	require.NoError(t, err)
	return l
}

func TestDefaultResourceConfig(t *testing.T) {
	cfg := DefaultResourceConfig()
	assert.Equal(t, 100, cfg.MaxRepositories)
	assert.Equal(t, 1000, cfg.MaxConnections)
	assert.True(t, cfg.EnableStreaming)
	assert.Equal(t, int64(1024*1024*1024), cfg.MaxMemoryUsage)
}

func TestNewResourceManager_NilConfig(t *testing.T) {
	_, err := NewResourceManager(nil)
	assert.Error(t, err)
}

func TestNewResourceManager_Success(t *testing.T) {
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
		Logging: testLoggingConfig(),
	}
	rm, err := NewResourceManager(cfg)
	require.NoError(t, err)
	require.NotNil(t, rm)
	defer rm.Stop()

	stats := rm.GetResourceStats()
	assert.Contains(t, stats, "memory")
	assert.Contains(t, stats, "connections")
	assert.Contains(t, stats, "repository_pool")
}

func TestRepositoryPool_GetAndEvict(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()

	root := t.TempDir()
	cfg := DefaultResourceConfig()
	cfg.MaxRepositories = 1

	pathCfg, err := newPathConfig(t, root)
	require.NoError(t, err)

	pool := NewRepositoryPool(cfg, pathCfg, logger)

	// Create two real repos under the repo root layout used by GetRepoPath.
	owner1, repo1 := makeOwnerRepo(t, root, "alice", "proj1")
	owner2, repo2 := makeOwnerRepo(t, root, "bob", "proj2")

	r, err := pool.GetRepository(context.Background(), owner1, repo1)
	require.NoError(t, err)
	require.NotNil(t, r)

	// Second access of the same repo hits the cache.
	r2, err := pool.GetRepository(context.Background(), owner1, repo1)
	require.NoError(t, err)
	assert.Same(t, r, r2)

	// Adding a second distinct repo evicts the first (MaxRepositories=1).
	_, err = pool.GetRepository(context.Background(), owner2, repo2)
	require.NoError(t, err)

	pool.mu.RLock()
	size := len(pool.repos)
	pool.mu.RUnlock()
	assert.LessOrEqual(t, size, 1)
}

func TestRepositoryPool_GetMissingRepoErrors(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()

	root := t.TempDir()
	cfg := DefaultResourceConfig()
	pathCfg, err := newPathConfig(t, root)
	require.NoError(t, err)
	pool := NewRepositoryPool(cfg, pathCfg, logger)

	_, err = pool.GetRepository(context.Background(), "nobody", "nothing")
	assert.Error(t, err)
}

func TestRepositoryPool_Cleanup(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()

	root := t.TempDir()
	cfg := DefaultResourceConfig()
	cfg.RepositoryTTL = time.Nanosecond
	pathCfg, err := newPathConfig(t, root)
	require.NoError(t, err)
	pool := NewRepositoryPool(cfg, pathCfg, logger)

	owner, repo := makeOwnerRepo(t, root, "carol", "p")
	_, err = pool.GetRepository(context.Background(), owner, repo)
	require.NoError(t, err)

	// Force last access into the past.
	pool.mu.Lock()
	for _, e := range pool.repos {
		e.LastAccess = time.Now().Add(-time.Hour)
	}
	pool.mu.Unlock()

	pool.Cleanup()

	pool.mu.RLock()
	size := len(pool.repos)
	pool.mu.RUnlock()
	assert.Equal(t, 0, size)
}

func TestMemoryMonitor(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()

	cfg := DefaultResourceConfig()
	cfg.MemoryCheckInterval = 5 * time.Millisecond
	cfg.GCThreshold = 0.0 // force pressure path
	mm := NewMemoryMonitor(cfg, logger)

	mm.updateStats()
	stats := mm.GetStats()
	assert.GreaterOrEqual(t, stats.Sys, uint64(0))
	assert.False(t, stats.LastUpdated.IsZero())

	// checkMemoryPressure with threshold 0 triggers GC.
	mm.checkMemoryPressure()

	mm.Start()
	time.Sleep(15 * time.Millisecond)
	mm.Stop()
	// Double stop is safe.
	mm.Stop()
}

func TestConnectionPool(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()

	cfg := DefaultResourceConfig()
	cfg.MaxConnections = 2
	cp := NewConnectionPool(cfg, logger)

	require.NoError(t, cp.AcquireConnection(context.Background()))
	require.NoError(t, cp.AcquireConnection(context.Background()))
	// Third exceeds the limit.
	err := cp.AcquireConnection(context.Background())
	assert.Error(t, err)

	stats := cp.GetStats()
	assert.Equal(t, 2, stats["active_connections"])
	assert.Equal(t, int64(1), stats["rejected_connections"])

	cp.ReleaseConnection()
	cp.ReleaseConnection()
	// Extra release stays at zero.
	cp.ReleaseConnection()

	stats2 := cp.GetStats()
	assert.Equal(t, 0, stats2["active_connections"])
}

func TestConstructors(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()

	cfg := DefaultResourceConfig()
	assert.NotNil(t, NewRequestTracker(cfg, logger))
	assert.NotNil(t, NewCacheManager(cfg, logger))
	assert.NotNil(t, NewMetricsCollector(logger))
	assert.NotNil(t, NewStreamingManager(cfg, logger))
}

func TestStreamingManager_ShouldStream(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()

	cfg := DefaultResourceConfig()
	cfg.EnableStreaming = true
	cfg.MaxStreamingFile = 100
	sm := NewStreamingManager(cfg, logger)

	assert.True(t, sm.ShouldStream(200))
	assert.False(t, sm.ShouldStream(50))

	cfg.EnableStreaming = false
	sm2 := NewStreamingManager(cfg, logger)
	assert.False(t, sm2.ShouldStream(200))
}

func TestEstimateRepositoryMemoryUsage(t *testing.T) {
	logger := newTestLoggingLogger(t)
	defer logger.Sync()
	cfg := DefaultResourceConfig()
	root := t.TempDir()
	pathCfg, err := newPathConfig(t, root)
	require.NoError(t, err)
	pool := NewRepositoryPool(cfg, pathCfg, logger)

	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")
	assert.Equal(t, int64(1024*1024), pool.estimateRepositoryMemoryUsage(r.Repo))
}
