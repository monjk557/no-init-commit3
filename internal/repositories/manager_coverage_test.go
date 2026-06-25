package repositories

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRepoManager(t *testing.T, root string) *DefaultRepositoryManager {
	t.Helper()
	logger, err := logging.NewLogger(config.LoggingConfig{Level: "error", Format: "json"})
	require.NoError(t, err)
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)
	rm, err := NewRepositoryManager(cfg, logger)
	require.NoError(t, err)
	return rm
}

// =============================================================================
// DefaultRepositoryManager — 전체 라이프사이클
// =============================================================================

func TestManager_FullLifecycle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rm := newTestRepoManager(t, root)
	repoPath := filepath.Join(root, "owner", "repo.git")

	// 생성
	info, err := rm.CreateRepository(&CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		DefaultBranch: "main",
		InitialCommit: true,
		InitialFiles: map[string]string{
			"README.md":      "# repo",
			"dir/nested.txt": "nested content", // 중첩 경로 → buildTreeFromFiles 재귀
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "main", info.DefaultBranch)
	assert.True(t, info.IsBare)
	assert.Equal(t, 1, info.BranchCount)
	assert.GreaterOrEqual(t, info.CommitCount, 1)
	assert.Greater(t, info.Size, int64(0))

	// 존재 확인
	exists, err := rm.RepositoryExists(repoPath)
	require.NoError(t, err)
	assert.True(t, exists)

	// 열기 (wrapper)
	wrapper, err := rm.OpenRepository(repoPath)
	require.NoError(t, err)
	assert.Equal(t, repoPath, wrapper.Path())
	assert.NoError(t, wrapper.Close())
	assert.NotNil(t, wrapper.GetRepo())

	// 목록 (생성된 repo 1개 발견)
	list, err := rm.ListRepositories(root)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].IsBare)

	// 삭제
	require.NoError(t, rm.DeleteRepository(repoPath))
	exists, err = rm.RepositoryExists(repoPath)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestManager_ListRepositoriesEmptyRootDefaults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rm := newTestRepoManager(t, root)
	// rootPath="" → repo root 사용
	list, err := rm.ListRepositories("")
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestManager_CreateNoInitialCommit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rm := newTestRepoManager(t, root)
	repoPath := filepath.Join(root, "owner", "noinit.git")

	info, err := rm.CreateRepository(&CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		DefaultBranch: "trunk",
		InitialCommit: false,
	})
	require.NoError(t, err)
	assert.Equal(t, "trunk", info.DefaultBranch)
	assert.Equal(t, 0, info.CommitCount)
}

func TestManager_CreateDefaultBranchFallsBackToMain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rm := newTestRepoManager(t, root)
	repoPath := filepath.Join(root, "owner", "fallback.git")

	info, err := rm.CreateRepository(&CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		DefaultBranch: "",
		InitialCommit: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "main", info.DefaultBranch)
}

func TestManager_CreateErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rm := newTestRepoManager(t, root)

	// nil options
	_, err := rm.CreateRepository(nil)
	assert.Error(t, err)

	// invalid path (root 밖)
	_, err = rm.CreateRepository(&CreateRepositoryOptions{Path: "/etc/passwd.git", Bare: true})
	assert.Error(t, err)

	// 이미 존재
	repoPath := filepath.Join(root, "owner", "dup.git")
	_, err = rm.CreateRepository(&CreateRepositoryOptions{Path: repoPath, Bare: true, InitialCommit: false})
	require.NoError(t, err)
	_, err = rm.CreateRepository(&CreateRepositoryOptions{Path: repoPath, Bare: true, InitialCommit: false})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestManager_RepositoryExistsErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rm := newTestRepoManager(t, root)

	_, err := rm.RepositoryExists("")
	assert.Error(t, err)

	exists, err := rm.RepositoryExists(filepath.Join(root, "ghost.git"))
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestManager_DeleteErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rm := newTestRepoManager(t, root)

	// 빈 경로
	assert.Error(t, rm.DeleteRepository(""))

	// invalid path
	assert.Error(t, rm.DeleteRepository("/etc/passwd"))

	// 존재하지 않음 (단, root 하위의 유효 경로 형태)
	err := rm.DeleteRepository(filepath.Join(root, "owner", "ghost.git"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestManager_IsBareRepository(t *testing.T) {
	t.Parallel()
	root, bare := newBareRepoAt(t, "owner", "bare")
	rm := newTestRepoManager(t, root)
	assert.True(t, rm.isBareRepository(bare))

	// 빈 디렉토리는 bare 아님
	assert.False(t, rm.isBareRepository(root))
}

// =============================================================================
// RepositoryWrapper — Resolve/Commit/SetReference (thread-safe wrappers)
// =============================================================================

func TestWrapper_ResolveAndCommit(t *testing.T) {
	t.Parallel()
	repoPath, commit := newBareRepoWithCommit(t)

	wrapper, err := OpenRepositoryNoCache(repoPath)
	require.NoError(t, err)

	// HEAD
	h, err := wrapper.ResolveReference("HEAD")
	require.NoError(t, err)
	assert.Equal(t, commit, h.String())

	// 빈 문자열 → HEAD
	h2, err := wrapper.ResolveReference("")
	require.NoError(t, err)
	assert.Equal(t, commit, h2.String())

	// 브랜치 이름
	h3, err := wrapper.ResolveReference("main")
	require.NoError(t, err)
	assert.Equal(t, commit, h3.String())

	// full ref
	h4, err := wrapper.ResolveReference("refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, commit, h4.String())

	// CommitObject
	co, err := wrapper.CommitObject(plumbing.NewHash(commit))
	require.NoError(t, err)
	assert.Equal(t, "init", co.Message)

	// SetReference
	newRef := plumbing.NewHashReference("refs/heads/feature", plumbing.NewHash(commit))
	require.NoError(t, wrapper.SetReference(newRef))
	hf, err := wrapper.ResolveReference("feature")
	require.NoError(t, err)
	assert.Equal(t, commit, hf.String())

	// 존재하지 않는 ref
	_, err = wrapper.ResolveReference("nonexistent-branch-xyz")
	assert.Error(t, err)
}

func TestWrapper_TagResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoPath, commit := newBareRepoWithCommit(t)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/tags/v1", commit))

	wrapper, err := OpenRepositoryNoCache(repoPath)
	require.NoError(t, err)

	h, err := wrapper.ResolveReference("v1")
	require.NoError(t, err)
	assert.Equal(t, commit, h.String())

	hFull, err := wrapper.ResolveReference("refs/tags/v1")
	require.NoError(t, err)
	assert.Equal(t, commit, hFull.String())
}

func TestOpenRepositorySimple_Caching(t *testing.T) {
	t.Parallel()
	repoPath, _ := newBareRepoWithCommit(t)

	w1, err := OpenRepositorySimple(repoPath)
	require.NoError(t, err)
	w2, err := OpenRepositorySimple(repoPath)
	require.NoError(t, err)
	// 캐시 hit → 동일 underlying repo
	assert.Same(t, w1.GetRepo(), w2.GetRepo())

	// invalidate
	assert.True(t, GetSimpleCache().Invalidate(repoPath))
	assert.False(t, GetSimpleCache().Invalidate(repoPath), "두 번째 invalidate 는 false")
}

func TestOpenRepository_NonexistentError(t *testing.T) {
	t.Parallel()
	_, err := OpenRepositorySimple("/nonexistent/repo.git")
	assert.Error(t, err)
	_, err = OpenRepositoryNoCache("/nonexistent/repo.git")
	assert.Error(t, err)
}

// =============================================================================
// GitObjectManager (deprecated) — GetRepositoryHead / ResolveReference /
// GetTreeFromCommit / GetFileFromTree / EnsureProperHEAD / SHA 헬퍼
// =============================================================================

func TestGOM_HeadAndResolve(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoWithCommitAt(t)
	manager := newTestManager(t, root)

	repo, err := manager.OpenRepository("owner", "repo")
	require.NoError(t, err)

	// HEAD
	head, err := manager.GetRepositoryHead(repo)
	require.NoError(t, err)
	assert.NotEmpty(t, head.Hash())

	// ResolveReference: HEAD
	h, err := manager.ResolveReference(repo, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head.Hash(), h)

	// 브랜치 이름
	hb, err := manager.ResolveReference(repo, "main")
	require.NoError(t, err)
	assert.Equal(t, head.Hash(), hb)

	// full SHA
	hs, err := manager.ResolveReference(repo, head.Hash().String())
	require.NoError(t, err)
	assert.Equal(t, head.Hash(), hs)

	// short SHA
	short := head.Hash().String()[:8]
	hsh, err := manager.ResolveReference(repo, short)
	require.NoError(t, err)
	assert.Equal(t, head.Hash(), hsh)
}

func TestGOM_ResolveReference_Errors(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoWithCommitAt(t)
	manager := newTestManager(t, root)
	repo, err := manager.OpenRepository("owner", "repo")
	require.NoError(t, err)

	// refs/ prefix 거부
	_, err = manager.ResolveReference(repo, "refs/heads/main")
	assert.ErrorIs(t, err, ErrUnsupportedRef)

	// remote-style 거부
	_, err = manager.ResolveReference(repo, "origin/main")
	assert.ErrorIs(t, err, ErrUnsupportedRef)

	// rev-parse 표현식 거부
	_, err = manager.ResolveReference(repo, "main^")
	assert.ErrorIs(t, err, ErrUnsupportedRef)

	// 공백 포함 거부
	_, err = manager.ResolveReference(repo, "bad ref")
	assert.ErrorIs(t, err, ErrInvalidRef)

	// 존재하지 않는 ref
	_, err = manager.ResolveReference(repo, "ghostbranch")
	assert.ErrorIs(t, err, ErrRefNotFound)

	// 존재하지 않는 full SHA → object not found
	_, err = manager.ResolveReference(repo, "0123456789abcdef0123456789abcdef01234567")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// 존재하지 않는 short SHA → object not found
	_, err = manager.ResolveReference(repo, "abcdef0")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// 로컬 state ref 거부
	_, err = manager.ResolveReference(repo, "MERGE_HEAD")
	assert.ErrorIs(t, err, ErrUnsupportedRef)
}

func TestGOM_TreeAndFile(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoWithCommitAt(t)
	manager := newTestManager(t, root)
	repo, err := manager.OpenRepository("owner", "repo")
	require.NoError(t, err)

	head, err := manager.GetRepositoryHead(repo)
	require.NoError(t, err)

	tree, err := manager.GetTreeFromCommit(repo, head.Hash())
	require.NoError(t, err)
	require.NotNil(t, tree)

	// 존재하는 파일
	f, err := manager.GetFileFromTree(tree, "README.md")
	require.NoError(t, err)
	assert.NotNil(t, f)

	// 존재하지 않는 파일 → nil, nil
	missing, err := manager.GetFileFromTree(tree, "nope.txt")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// 잘못된 commit hash → 에러
	_, err = manager.GetTreeFromCommit(repo, plumbing.NewHash("0123456789abcdef0123456789abcdef01234567"))
	assert.Error(t, err)
}

func TestGOM_EnsureProperHEAD(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoWithCommitAt(t)
	manager := newTestManager(t, root)
	repo, err := manager.OpenRepository("owner", "repo")
	require.NoError(t, err)

	// 이미 main 가리킴 → no-op 성공
	require.NoError(t, manager.EnsureProperHEAD(repo, "main"))

	// 존재하지 않는 브랜치 → 에러
	assert.Error(t, manager.EnsureProperHEAD(repo, "nonexistent"))
}

func TestGOM_OpenRepository_Errors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager := newTestManager(t, root)

	// invalid path components
	_, err := manager.OpenRepository("..", "repo")
	assert.Error(t, err)

	// 존재하지 않음
	_, err = manager.OpenRepository("owner", "ghost")
	assert.Error(t, err)
}

func TestNewGitObjectManager_NilLogger(t *testing.T) {
	t.Parallel()
	cfg, err := NewPathConfig(t.TempDir())
	require.NoError(t, err)
	_, err = NewGitObjectManager(cfg, nil)
	assert.Error(t, err)
}

// newBareRepoWithCommitAt 는 owner/repo.git bare repo + main commit 을 만들고
// root 와 commit 을 반환한다 (GitObjectManager 테스트용 — owner/repo 고정).
func newBareRepoWithCommitAt(t *testing.T) (root, commit string) {
	t.Helper()
	ctx := context.Background()
	root, repoPath := newBareRepoAt(t, "owner", "repo")

	blob, err := nativeHashObject(ctx, repoPath, []byte("hello\n"))
	require.NoError(t, err)
	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: "README.md"},
	})
	require.NoError(t, err)
	sig := &nativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	commit, err = nativeCommitTree(ctx, repoPath, tree, nil, "init", sig, sig)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", commit))
	require.NoError(t, nativeSetSymbolicRef(ctx, repoPath, "refs/heads/main"))
	return root, commit
}
