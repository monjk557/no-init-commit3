package overview

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func Test_EntryTypeSortKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, entryTypeSortKey("tree"))
	assert.Equal(t, 1, entryTypeSortKey("blob"))
	assert.Equal(t, 2, entryTypeSortKey("submodule"))
	assert.Equal(t, 2, entryTypeSortKey("commit"))
	assert.Equal(t, 3, entryTypeSortKey("other"))
}

func Test_ParentDirGitPath(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", parentDirGitPath(""))
	assert.Equal(t, "", parentDirGitPath("README.md"))
	assert.Equal(t, "src", parentDirGitPath("src/main.go"))
	assert.Equal(t, "a/b", parentDirGitPath("a/b/c.go"))
}

func Test_ConvertParentHashes(t *testing.T) {
	t.Parallel()
	h1 := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")
	got := convertParentHashes([]plumbing.Hash{h1})
	require.Len(t, got, 1)
	assert.Equal(t, h1.String(), got[0])

	assert.Empty(t, convertParentHashes(nil))
}

// ---------------------------------------------------------------------------
// GetTreeOnly / GetTreeWithCommit / getTreeEntries
// ---------------------------------------------------------------------------

func Test_GetTreeOnly_Root(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	entries, err := service.GetTreeOnly(context.Background(), gitRepo, "HEAD", "")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
		// GetTreeOnly 는 latest_commit 을 채우지 않는다.
		assert.Nil(t, e.LatestCommit)
	}
	assert.True(t, names["README.md"])
	assert.True(t, names["src"])
}

func Test_GetTreeOnly_Subdir(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	entries, err := service.GetTreeOnly(context.Background(), gitRepo, "HEAD", "src")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "main.go", entries[0].Name)
}

func Test_GetTreeOnly_InvalidRef(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	_, err := service.GetTreeOnly(context.Background(), gitRepo, "no-such-ref", "")
	require.Error(t, err)
}

func Test_GetTreeOnly_PathIsFile_Error(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	// README.md 는 tree 가 아니므로 에러.
	_, err := service.GetTreeOnly(context.Background(), gitRepo, "HEAD", "README.md")
	require.Error(t, err)
}

func Test_GetTreeOnly_NonExistentPath_Error(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	_, err := service.GetTreeOnly(context.Background(), gitRepo, "HEAD", "no/such/dir")
	require.Error(t, err)
}

func Test_GetTreeWithCommit_FillsLatestCommit(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	entries, meta, err := service.GetTreeWithCommit(context.Background(), gitRepo, "HEAD", "")
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	_ = meta

	// 적어도 하나의 엔트리는 latest_commit 이 채워져야 한다.
	hasCommit := false
	for _, e := range entries {
		if e.LatestCommit != nil {
			hasCommit = true
			assert.NotEmpty(t, e.LatestCommit.SHA)
		}
	}
	assert.True(t, hasCommit, "GetTreeWithCommit 은 latest_commit 을 채워야 함")
}

// ---------------------------------------------------------------------------
// GetRemotes
// ---------------------------------------------------------------------------

func Test_GetRemotes_Empty(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	remotes, err := service.GetRemotes(context.Background(), gitRepo)
	require.NoError(t, err)
	assert.Empty(t, remotes)
}

func Test_GetRemotes_WithOrigin(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	_, err := gitRepo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/example/repo.git"},
	})
	require.NoError(t, err)

	remotes, err := service.GetRemotes(context.Background(), gitRepo)
	require.NoError(t, err)
	require.Len(t, remotes, 1)
	assert.Equal(t, "origin", remotes[0].Name)
	assert.True(t, remotes[0].IsDefault)
	assert.Equal(t, "https://github.com/example/repo.git", remotes[0].URL)
}

// ---------------------------------------------------------------------------
// CountCommitsBetween
// ---------------------------------------------------------------------------

func Test_CountCommitsBetween_SameCommit_Zero(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	head, err := gitRepo.Head()
	require.NoError(t, err)
	headCommit, err := gitRepo.CommitObject(head.Hash())
	require.NoError(t, err)

	n, err := service.CountCommitsBetween(gitRepo, headCommit, headCommit)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func Test_CountCommitsBetween_HeadVsParent(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	head, err := gitRepo.Head()
	require.NoError(t, err)
	headCommit, err := gitRepo.CommitObject(head.Hash())
	require.NoError(t, err)

	// HEAD 의 부모 (3 커밋 repo → HEAD~2 가 첫 커밋).
	parent, err := headCommit.Parent(0)
	require.NoError(t, err)

	n, err := service.CountCommitsBetween(gitRepo, parent, headCommit)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "from=parent..to=head 사이에는 head 1개")
}

// ---------------------------------------------------------------------------
// Cache management (multiTierCache enabled in test service)
// ---------------------------------------------------------------------------

func Test_GetCacheMetrics_Enabled(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	metrics := service.GetCacheMetrics()
	assert.Equal(t, true, metrics["cache_enabled"])
}

func Test_InvalidateRepositoryCache_NoError(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	err := service.InvalidateRepositoryCache(context.Background(), "owner/repo")
	require.NoError(t, err)
}

func Test_InvalidateDirectoryCache_NoError(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	err := service.InvalidateDirectoryCache(context.Background(), "owner/repo", "main", "src")
	require.NoError(t, err)
}

func Test_WarmDirectoryCache_PopulatesCache(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	err := service.WarmDirectoryCache(context.Background(), gitRepo, "owner/repo", "HEAD", "")
	require.NoError(t, err)

	// 두 번째 warm 은 already-cached 분기를 탄다.
	err = service.WarmDirectoryCache(context.Background(), gitRepo, "owner/repo", "HEAD", "")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// GetTreeWithCommitPaginated
// ---------------------------------------------------------------------------

func Test_GetTreeWithCommitPaginated_ReturnsPaginatedTree(t *testing.T) {
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

	resp, err := service.GetTreeWithCommitPaginated(context.Background(), gitRepo, req, "https://x/tree")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Equal(t, "owner", resp.Owner)
	assert.NotEmpty(t, resp.Tree)
	require.NotNil(t, resp.Pagination)

	// 두 번째 호출은 cache hit 분기를 탄다 (비동기 저장 후 best-effort).
	_, err = service.GetTreeWithCommitPaginated(context.Background(), gitRepo, req, "https://x/tree")
	require.NoError(t, err)
}

func Test_GetTreeWithCommitPaginated_InvalidRef_Error(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	req := &TreeDirectoryRequest{
		Owner: "owner",
		Repo:  "repo",
		Ref:   "no-such-ref",
		Page:  1,
	}

	_, err := service.GetTreeWithCommitPaginated(context.Background(), gitRepo, req, "")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// loadGitmodulesFromTree
// ---------------------------------------------------------------------------

func Test_LoadGitmodulesFromTree_NilTree(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	assert.Nil(t, service.loadGitmodulesFromTree(nil, nil))
}

func Test_LoadGitmodulesFromTree_NoGitmodules(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	head, err := gitRepo.Head()
	require.NoError(t, err)
	commit, err := gitRepo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	// .gitmodules 가 없는 repo → nil.
	assert.Nil(t, service.loadGitmodulesFromTree(gitRepo, tree))
}

// ---------------------------------------------------------------------------
// EnsureRepositoryOptimized / GetPerformanceStats
// ---------------------------------------------------------------------------

func Test_GetPerformanceStats_ReturnsKeys(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	_ = createTestRepositoryWithCommits(t, tempDir)
	repoPath := tempDir + "/test.git"

	stats := service.GetPerformanceStats(repoPath)
	assert.Contains(t, stats, "object_cache_hit_rate")
	assert.Contains(t, stats, "fd_pool_hit_rate")
}

func Test_EnsureRepositoryOptimized_DisabledByDefault(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	_ = createTestRepositoryWithCommits(t, tempDir)
	repoPath := tempDir + "/test.git"

	// 기본적으로 commit-graph 최적화가 비활성이므로 즉시 nil 반환.
	err := service.EnsureRepositoryOptimized(repoPath)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// loadGitmodulesFromTree (with actual .gitmodules)
// ---------------------------------------------------------------------------

func Test_LoadGitmodulesFromTree_WithGitmodules(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	// createTestRepoWithSubmodule 은 root 에 .gitmodules blob 을 둔다 (handler_test.go).
	repoPath := tempDir + "/sub.git"
	createTestRepoWithSubmodule(t, repoPath)

	gitRepo, err := (&GitServiceImpl{}).OpenRepository(repoPath)
	require.NoError(t, err)

	head, err := gitRepo.Head()
	require.NoError(t, err)
	commit, err := gitRepo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	idx := service.loadGitmodulesFromTree(gitRepo, tree)
	require.NotNil(t, idx, ".gitmodules 가 있으면 파싱 결과를 반환해야 함")
}

func Test_GetTreeWithCommit_SubmoduleEnrichment(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	repoPath := tempDir + "/sub2.git"
	createTestRepoWithSubmodule(t, repoPath)

	gitRepo, err := (&GitServiceImpl{}).OpenRepository(repoPath)
	require.NoError(t, err)

	// submodule 은 vendor/hello 에 있다.
	entries, _, err := service.GetTreeWithCommit(context.Background(), gitRepo, "HEAD", "vendor")
	require.NoError(t, err)

	var sub *GitTreeEntryWithCommit
	for i := range entries {
		if entries[i].Type == "submodule" {
			sub = &entries[i]
			break
		}
	}
	require.NotNil(t, sub, "submodule 엔트리가 있어야 함")
	require.NotNil(t, sub.Submodule)
	assert.NotEmpty(t, sub.Submodule.Commit)
}

// ---------------------------------------------------------------------------
// GetLatestCommitsForPathsWithMeta — matchInTree branch coverage
// ---------------------------------------------------------------------------

func Test_GetLatestCommitsForPathsWithMeta_MixedPathTypes(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	refHash, err := gitRepo.ResolveRevision("HEAD")
	require.NoError(t, err)

	pathInfos := []CommitPathInfo{
		{Path: "README.md", IsTree: false}, // 존재하는 blob
		{Path: "src", IsTree: true},        // 존재하는 tree → matchInTree isTree 분기
		{Path: "missing", IsTree: true},    // 없는 tree → false 반환
		{Path: "nope.txt", IsTree: false},  // 없는 blob → false 반환
	}

	commitMap, meta := service.GetLatestCommitsForPathsWithMeta(
		context.Background(), gitRepo, *refHash, pathInfos, 5*1e9, 20_000)

	// 존재하는 경로들은 커밋이 채워져야 한다.
	require.Contains(t, commitMap, "README.md")
	require.Contains(t, commitMap, "src")
	assert.NotNil(t, commitMap["README.md"])
	assert.NotNil(t, commitMap["src"])
	_ = meta
}

// ---------------------------------------------------------------------------
// BuildFullIndex
// ---------------------------------------------------------------------------

func Test_BuildFullIndex_Enabled(t *testing.T) {
	t.Parallel()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	_ = createTestRepositoryWithCommits(t, tempDir)
	repoPath := tempDir + "/test.git"

	// indexBuilder 가 enable 되어 있으므로 빌드 경로를 실행한다. 테스트용
	// in-memory sqlite 에는 commit_path_index 마이그레이션이 없으므로 upsert
	// 단계에서 에러가 나는 것이 정상 — 핵심은 "indexing 비활성" early-return 이
	// 아니라 실제 빌드 경로가 실행됐다는 것 (에러는 wrap 되어 전파).
	err := service.BuildFullIndex(context.Background(), "owner/repo", repoPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to build commit index")
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func Test_Service_Close(t *testing.T) {
	t.Parallel()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	require.NoError(t, service.Close())
}
