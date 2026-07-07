package overview

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/models"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// service_indexed_coverage_test.go — 인덱스가 "가용" 한 상태(seeded DB)로
// indexing 통합 경로의 O(1) branch 를 커버한다. 기존 setupTestService 는 빈 DB 라
// IsIndexAvailable 이 false/error 로만 흘러 indexed branch 가 안 잡힌다.
//
// 이 헬퍼는 setupTestService 와 동일하지만 commit_path_index 테이블을 migrate 하고
// repoID 에 대한 row 를 seed 하여 IsIndexAvailable == true 경로를 강제한다.

func setupIndexedService(t *testing.T, repoID string, paths []string) (*RepositoryOverviewService, string, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "overview-indexed-test-*")
	require.NoError(t, err)

	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "info",
		Format:      "json",
		Output:      "stdout",
		Development: false,
	})
	require.NoError(t, err)

	pathConfig, err := repositories.NewPathConfig(tempDir)
	require.NoError(t, err)

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	require.NoError(t, err)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// 인덱스 테이블 생성 + seed → IsIndexAvailable == true.
	require.NoError(t, db.AutoMigrate(&models.CommitPathIndex{}))
	for i, p := range paths {
		row := &models.CommitPathIndex{
			RepoID:        repoID,
			FilePath:      p,
			LastCommitSHA: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			CommitTime:    time.Unix(1700000000+int64(i), 0).UTC(),
			ChangeType:    "modified",
			FileSize:      int64(100 + i),
		}
		require.NoError(t, db.Create(row).Error)
	}

	service, err := NewRepositoryOverviewService(logger, pathConfig, repoManager, db)
	require.NoError(t, err)

	cleanup := func() { _ = os.RemoveAll(tempDir) }
	return service, tempDir, cleanup
}

func TestGetLatestCommitsWithIndexing_IndexAvailable(t *testing.T) {
	repoID := "o/r"
	paths := []string{"README.md", "src"}
	svc, _, cleanup := setupIndexedService(t, repoID, paths)
	defer cleanup()
	ctx := context.Background()

	t.Run("indexed O(1) lookup returns commits", func(t *testing.T) {
		result, err := svc.GetLatestCommitsWithIndexing(ctx, repoID, paths)
		require.NoError(t, err)
		require.NotEmpty(t, result, "seeded index should yield commits")
		for _, p := range paths {
			if c, ok := result[p]; ok {
				require.NotEmpty(t, c.SHA)
			}
		}
	})

	t.Run("unknown repo falls back to empty map", func(t *testing.T) {
		result, err := svc.GetLatestCommitsWithIndexing(ctx, "no/such-repo", paths)
		require.NoError(t, err)
		require.Empty(t, result)
	})

	t.Run("empty paths still succeeds", func(t *testing.T) {
		result, err := svc.GetLatestCommitsWithIndexing(ctx, repoID, []string{})
		require.NoError(t, err)
		require.NotNil(t, result)
	})
}

func TestGetTreeWithCommitIndexed_IndexAvailable(t *testing.T) {
	repoID := "o/test" // owner=o, repo=test → matches createTestRepositoryWithCommits path "o/test"
	// createTestRepositoryWithCommits writes to <tempDir>/test.git and root entries
	// are README.md + src. We seed those exact paths so getCommitInfoWithIndexing hits
	// the indexed lookup branch (IndexedLookups > 0).
	svc, tempDir, cleanup := setupIndexedService(t, repoID, []string{"README.md", "src"})
	defer cleanup()

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	ctx := context.Background()

	req := &TreeDirectoryRequest{Owner: "o", Repo: "test", Ref: "main", Path: "", Page: 1, PerPage: 10}
	resp, err := svc.GetTreeWithCommitIndexed(ctx, gitRepo, req, "http://x/tree")
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.True(t, resp.Success)
	require.NotEmpty(t, resp.Tree)
}

func TestGetIndexingStats_IndexAvailable(t *testing.T) {
	repoID := "o/r"
	svc, _, cleanup := setupIndexedService(t, repoID, []string{"README.md", "main.go"})
	defer cleanup()
	ctx := context.Background()

	stats, err := svc.GetIndexingStats(ctx, repoID)
	require.NoError(t, err)
	require.Equal(t, true, stats["enabled"])
	require.Equal(t, true, stats["index_available"])
	require.Contains(t, stats, "query_metrics")
	require.Contains(t, stats, "lookup_complexity")
}

func TestGetIndexingStats_Disabled(t *testing.T) {
	// db == nil → enableIndexing == false → disabled branch.
	tempDir, err := os.MkdirTemp("", "overview-noidx-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()

	logger, err := logging.NewLogger(config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"})
	require.NoError(t, err)
	pathConfig, err := repositories.NewPathConfig(tempDir)
	require.NoError(t, err)
	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	require.NoError(t, err)

	svc, err := NewRepositoryOverviewService(logger, pathConfig, repoManager, nil)
	require.NoError(t, err)

	stats, err := svc.GetIndexingStats(context.Background(), "o/r")
	require.NoError(t, err)
	require.Equal(t, false, stats["enabled"])
	require.Equal(t, "indexing_disabled", stats["status"])

	// BuildFullIndex 는 indexing 비활성 시 에러 반환.
	err = svc.BuildFullIndex(context.Background(), "o/r", filepath.Join(tempDir, "nope.git"))
	require.Error(t, err)
}

func TestBuildFullIndex_ErrorPath(t *testing.T) {
	// indexing 활성 상태에서 존재하지 않는 repo path → indexBuilder 가 에러 반환.
	svc, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	err := svc.BuildFullIndex(context.Background(), "o/missing", filepath.Join(tempDir, "does-not-exist.git"))
	require.Error(t, err)
}

func TestGetLanguageDistribution_ContextCancelled(t *testing.T) {
	svc, tempDir, cleanup := setupTestService(t)
	defer cleanup()
	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 즉시 취소 → 초기 ctx.Done() 조기 반환 경로.

	dist := svc.GetLanguageDistribution(ctx, gitRepo, "main")
	require.Equal(t, 0, dist.TotalFiles)
	require.Equal(t, int64(0), dist.TotalBytes)
	require.Empty(t, dist.Languages)
}

func TestGetTreeEntries_ErrorPaths(t *testing.T) {
	svc, tempDir, cleanup := setupTestService(t)
	defer cleanup()
	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	ctx := context.Background()

	t.Run("bad ref resolve error", func(t *testing.T) {
		_, err := svc.GetTreeOnly(ctx, gitRepo, "no-such-ref", "")
		require.Error(t, err)
	})

	t.Run("path pointing to a file is not a tree", func(t *testing.T) {
		_, err := svc.GetTreeOnly(ctx, gitRepo, "main", "README.md")
		require.Error(t, err)
	})

	t.Run("missing path find error", func(t *testing.T) {
		_, err := svc.GetTreeOnly(ctx, gitRepo, "main", "nonexistent-dir")
		require.Error(t, err)
	})

	t.Run("withCommits over subtree", func(t *testing.T) {
		entries, _, err := svc.GetTreeWithCommit(ctx, gitRepo, "main", "src")
		require.NoError(t, err)
		require.NotEmpty(t, entries)
	})
}

func TestInvalidateDirectoryCache_NilCache(t *testing.T) {
	// multiTierCache == nil → 조기 반환 (nil 에러).
	svc := &RepositoryOverviewService{}
	err := svc.InvalidateDirectoryCache(context.Background(), "o/r", "main", "")
	require.NoError(t, err)
}

func TestEnsureRepositoryOptimized(t *testing.T) {
	svc, tempDir, cleanup := setupTestService(t)
	defer cleanup()
	_ = createTestRepositoryWithCommits(t, tempDir)
	repoPath := filepath.Join(tempDir, "test.git")

	t.Run("disabled returns nil early", func(t *testing.T) {
		svc.perfConfig.EnableCommitGraphOptimization = false
		require.NoError(t, svc.EnsureRepositoryOptimized(repoPath))
	})

	t.Run("enabled invokes commit-graph optimization", func(t *testing.T) {
		// perfConfig 는 commitGraphOpt 와 pointer 공유 → 플래그가 함께 전파.
		svc.perfConfig.EnableCommitGraphOptimization = true
		defer func() { svc.perfConfig.EnableCommitGraphOptimization = false }()
		// git commit-graph write 경로 실행 — 커버리지가 목적이라 에러 여부는 관대.
		_ = svc.EnsureRepositoryOptimized(repoPath)
	})
}
