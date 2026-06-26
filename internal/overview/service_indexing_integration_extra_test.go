package overview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 인덱싱 시스템은 테스트 service 에서 in-memory sqlite 로 enable 되지만,
// 실제 인덱스는 빌드되지 않은 상태이므로 IsIndexAvailable 은 false → Git
// traversal fallback 경로를 탄다. 아래 테스트는 그 fallback contract 를 검증한다.

func Test_GetLatestCommitsWithIndexing_FallbackEmpty(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	result, err := service.GetLatestCommitsWithIndexing(
		context.Background(), "owner/repo", []string{"README.md", "src/main.go"})
	require.NoError(t, err)
	// 인덱스 미빌드 → fallback 은 빈 map 반환.
	assert.Empty(t, result)
}

func Test_GetCommitInfoWithIndexing_EmptyEntries(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	entries, meta, err := service.getCommitInfoWithIndexing(
		context.Background(), "owner/repo", "main", nil)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Empty(t, meta.CutReason)
}

func Test_GetCommitInfoWithIndexing_GitFallback(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	entries := []GitTreeEntryWithCommit{
		{Type: "blob", Name: "a.go", Path: "a.go"},
		{Type: "blob", Name: "b.go", Path: "b.go"},
	}

	got, meta, err := service.getCommitInfoWithIndexing(
		context.Background(), "owner/repo", "main", entries)
	require.NoError(t, err)
	require.Len(t, got, 2)
	// 인덱스 미가용 → git_fallback.
	assert.Equal(t, "git_fallback", meta.CutReason)
	assert.Equal(t, 2, meta.GitFallbacks)
}

func Test_GetTreeWithCommitIndexed_ReturnsResponse(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	req := &TreeDirectoryRequest{
		Owner:   "owner",
		Repo:    "repo",
		Ref:     "HEAD",
		Path:    "",
		Page:    1,
		PerPage: 10,
	}

	resp, err := service.GetTreeWithCommitIndexed(context.Background(), gitRepo, req, "https://x/tree")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Equal(t, "owner", resp.Owner)
	assert.NotEmpty(t, resp.Tree)
	require.NotNil(t, resp.Pagination)
}

func Test_GetTreeWithCommitIndexed_InvalidRef_Error(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	req := &TreeDirectoryRequest{Owner: "o", Repo: "r", Ref: "nope", Page: 1}

	_, err := service.GetTreeWithCommitIndexed(context.Background(), gitRepo, req, "")
	require.Error(t, err)
}

func Test_UpdateIndexForPush_EnabledNoError(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	_ = createTestRepositoryWithCommits(t, tempDir)
	repoPath := tempDir + "/test.git"

	// indexBuilder 가 enable 되어 있으나 빈 SHA 목록이면 빠르게 통과.
	err := service.UpdateIndexForPush(context.Background(), "owner/repo", repoPath, nil)
	require.NoError(t, err)
}

func Test_GetIndexingStats_Enabled(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	stats, err := service.GetIndexingStats(context.Background(), "owner/repo")
	require.NoError(t, err)
	assert.Equal(t, true, stats["enabled"])
	assert.Contains(t, stats, "query_metrics")
}

// ---------------------------------------------------------------------------
// GitServiceImpl adapter
// ---------------------------------------------------------------------------

func Test_GitServiceImpl_GetAllCommits_Empty(t *testing.T) {
	t.Parallel()
	g := &GitServiceImpl{}
	commits, err := g.GetAllCommits("/some/path")
	require.NoError(t, err)
	assert.Empty(t, commits)
}

func Test_GitServiceImpl_GetCommitsBySHAs_Empty(t *testing.T) {
	t.Parallel()
	g := &GitServiceImpl{}
	m, err := g.GetCommitsBySHAs("/some/path", []string{"abc"})
	require.NoError(t, err)
	assert.Empty(t, m)
}

func Test_GitServiceImpl_OpenRepository(t *testing.T) {
	t.Parallel()
	_, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	_ = createTestRepositoryWithCommits(t, tempDir)
	g := &GitServiceImpl{}

	repo, err := g.OpenRepository(tempDir + "/test.git")
	require.NoError(t, err)
	require.NotNil(t, repo)
}

func Test_GitServiceImpl_OpenRepository_Invalid_Error(t *testing.T) {
	t.Parallel()
	g := &GitServiceImpl{}
	_, err := g.OpenRepository(t.TempDir() + "/nope.git")
	require.Error(t, err)
}
