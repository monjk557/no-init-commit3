package merge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/config"
)

// =============================================================================
// 테스트: FindMergeBase
// 회귀 방지: "merge base 계산이 정확해야 merge preview가 올바른 diff를 보여줌"
// =============================================================================

// Test_FindMergeBase_NormalBranching_ReturnsBranchPoint
// 회귀: main에서 feature 분기 후 양쪽에 커밋이 있을 때, merge base는 분기점이어야 함
func Test_FindMergeBase_NormalBranching_ReturnsBranchPoint(t *testing.T) {
	t.Parallel()

	// Given: A---B---C (main)
	//             \
	//              D---E (feature)
	// merge base는 B여야 함
	tempDir := t.TempDir()
	repo := initRepo(t, tempDir)
	wt := worktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	commit(t, wt, tempDir, "file1.txt", "A", "Commit A", now)
	commitB := commit(t, wt, tempDir, "file2.txt", "B", "Commit B", now)

	// feature 브랜치 생성
	checkout(t, wt, "feature", true)
	commit(t, wt, tempDir, "file3.txt", "D", "Commit D", now)
	commitE := commit(t, wt, tempDir, "file4.txt", "E", "Commit E", now)

	// main으로 돌아가서 C 커밋
	checkout(t, wt, "main", false)
	commitC := commit(t, wt, tempDir, "file5.txt", "C", "Commit C", now)

	handler := newMergePreviewHandler(t)

	// When: E와 C의 merge base 찾기
	mergeBase, err := handler.findMergeBase(repo, commitE, commitC)

	// Then: merge base는 B
	require.NoError(t, err)
	require.NotNil(t, mergeBase)
	require.Equal(t, commitB.String(), mergeBase.Hash.String(),
		"merge base should be commit B (branch point)")
}

// Test_FindMergeBase_SameCommit_ReturnsSelf
// 회귀: 같은 커밋의 merge base는 자기 자신이어야 함
func Test_FindMergeBase_SameCommit_ReturnsSelf(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	repo := initRepo(t, tempDir)
	wt := worktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	commitA := commit(t, wt, tempDir, "file1.txt", "A", "Commit A", now)
	handler := newMergePreviewHandler(t)

	// When: 같은 커밋의 merge base 찾기
	mergeBase, err := handler.findMergeBase(repo, commitA, commitA)

	// Then: merge base는 자기 자신
	require.NoError(t, err)
	require.NotNil(t, mergeBase)
	require.Equal(t, commitA.String(), mergeBase.Hash.String(),
		"merge base of same commit should be itself")
}

// Test_FindMergeBase_LinearHistory_ReturnsAncestor
// 회귀: 선형 히스토리에서 merge base는 ancestor
func Test_FindMergeBase_LinearHistory_ReturnsAncestor(t *testing.T) {
	t.Parallel()

	// Given: A---B---C (선형)
	tempDir := t.TempDir()
	repo := initRepo(t, tempDir)
	wt := worktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	commit(t, wt, tempDir, "file1.txt", "A", "Commit A", now)
	commitB := commit(t, wt, tempDir, "file2.txt", "B", "Commit B", now)
	commitC := commit(t, wt, tempDir, "file3.txt", "C", "Commit C", now)

	handler := newMergePreviewHandler(t)

	// When: C와 B의 merge base 찾기
	mergeBase, err := handler.findMergeBase(repo, commitC, commitB)

	// Then: merge base는 B (ancestor)
	require.NoError(t, err)
	require.NotNil(t, mergeBase)
	require.Equal(t, commitB.String(), mergeBase.Hash.String(),
		"merge base should be B (the ancestor)")
}

// =============================================================================
// 테스트: CalculateCommitCounts
// 회귀 방지: "ahead/behind 계산이 정확해야 UI에서 올바른 정보를 보여줌"
// =============================================================================

// Test_CalculateCommitCounts_BranchDiverged_ReturnsAheadBehind
// 회귀: 분기 후 양쪽에 커밋이 있으면 ahead/behind가 정확해야 함
func Test_CalculateCommitCounts_BranchDiverged_ReturnsAheadBehind(t *testing.T) {
	t.Parallel()

	// Given: A---B---C (main)
	//             \
	//              D---E (feature)
	// feature는 main 대비 ahead=2 (D, E), behind=1 (C)
	tempDir := t.TempDir()
	repo := initRepo(t, tempDir)
	wt := worktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	commit(t, wt, tempDir, "file1.txt", "A", "Commit A", now)
	commitB := commit(t, wt, tempDir, "file2.txt", "B", "Commit B", now)

	checkout(t, wt, "feature", true)
	commit(t, wt, tempDir, "file3.txt", "D", "Commit D", now)
	commitE := commit(t, wt, tempDir, "file4.txt", "E", "Commit E", now)

	checkout(t, wt, "main", false)
	commitC := commit(t, wt, tempDir, "file5.txt", "C", "Commit C", now)

	handler := newMergePreviewHandler(t)

	// commit objects 가져오기
	commitEObj, err := repo.CommitObject(commitE)
	require.NoError(t, err)
	commitCObj, err := repo.CommitObject(commitC)
	require.NoError(t, err)
	commitBObj, err := repo.CommitObject(commitB)
	require.NoError(t, err)

	// When: commit counts 계산
	stats, err := handler.calculateCommitCounts(repo, commitEObj, commitCObj, commitBObj)

	// Then: ahead=2, behind=1
	require.NoError(t, err)
	require.NotNil(t, stats)
	require.Equal(t, 2, stats.Ahead, "feature should be 2 commits ahead")
	require.Equal(t, 1, stats.Behind, "feature should be 1 commit behind")
}

// =============================================================================
// 테스트: 삭제된 브랜치 시나리오
// 회귀 방지: "삭제된 브랜치로 접근 시 적절한 에러를 반환해야 함"
// =============================================================================

// Test_ResolveReference_DeletedBranch_ReturnsError
// 회귀: 브랜치 삭제 후 해당 ref로 resolve하면 에러가 발생해야 함
func Test_ResolveReference_DeletedBranch_ReturnsError(t *testing.T) {
	t.Parallel()

	// Given: feature 브랜치가 있는 레포
	tempDir := t.TempDir()
	repo := initRepo(t, tempDir)
	wt := worktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main에 초기 커밋
	commit(t, wt, tempDir, "file1.txt", "initial", "Initial commit", now)

	// feature 브랜치 생성 및 커밋
	checkout(t, wt, "feature/test", true)
	featureHash := commit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// When: feature 브랜치 삭제
	err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("feature/test"))
	require.NoError(t, err, "branch deletion should succeed")

	// Then: 삭제된 브랜치 ref로 조회하면 에러
	_, err = repo.Reference(plumbing.NewBranchReferenceName("feature/test"), true)
	require.Error(t, err, "deleted branch ref should return error")

	// But: 커밋 해시로는 여전히 접근 가능 (객체는 남아있음)
	commitObj, err := repo.CommitObject(featureHash)
	require.NoError(t, err, "commit object should still exist")
	require.NotNil(t, commitObj, "commit object should not be nil")
}

// Test_ResolveReference_DeletedBranch_CommitStillAccessible
// 회귀: 브랜치 삭제 후에도 커밋 SHA로는 접근 가능해야 함 (executor 정책 검증)
func Test_ResolveReference_DeletedBranch_CommitStillAccessible(t *testing.T) {
	t.Parallel()

	// Given: feature에서 main으로 머지 후 브랜치 삭제
	tempDir := t.TempDir()
	repo := initRepo(t, tempDir)
	wt := worktree(t, repo)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// main 커밋
	mainHash := commit(t, wt, tempDir, "main.txt", "main", "Main commit", now)

	// feature 브랜치에서 커밋
	checkout(t, wt, "feature/merged", true)
	featureHash := commit(t, wt, tempDir, "feature.txt", "feature", "Feature commit", now)

	// main으로 돌아가기
	checkout(t, wt, "main", false)

	// When: feature 브랜치 삭제 (머지 후 삭제 시나리오 시뮬레이션)
	err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("feature/merged"))
	require.NoError(t, err)

	// Then: 브랜치 ref 조회는 실패
	_, err = repo.Reference(plumbing.NewBranchReferenceName("feature/merged"), true)
	require.Error(t, err, "deleted branch should not be found")

	// But: 커밋 SHA로는 모든 커밋 접근 가능
	_, err = repo.CommitObject(mainHash)
	require.NoError(t, err, "main commit should be accessible by SHA")

	_, err = repo.CommitObject(featureHash)
	require.NoError(t, err, "feature commit should still be accessible by SHA")

	// And: 해시 기반 merge base 계산도 가능
	handler := newMergePreviewHandler(t)
	mergeBase, err := handler.findMergeBase(repo, featureHash, mainHash)
	require.NoError(t, err, "merge base should work with commit hashes")
	require.NotNil(t, mergeBase)
}

// =============================================================================
// Helper functions
// 원칙: boilerplate만 추상화, 핵심 로직은 테스트 본문에
// =============================================================================

func initRepo(t *testing.T, dir string) *gogit.Repository {
	t.Helper()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err, "repo init failed")

	// main 브랜치로 HEAD 설정
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err, "set HEAD failed")

	return repo
}

func worktree(t *testing.T, repo *gogit.Repository) *gogit.Worktree {
	t.Helper()
	wt, err := repo.Worktree()
	require.NoError(t, err, "worktree failed")
	return wt
}

func commit(t *testing.T, wt *gogit.Worktree, repoPath, filename, content, message string, when time.Time) plumbing.Hash {
	t.Helper()

	filePath := filepath.Join(repoPath, filename)
	err := os.MkdirAll(filepath.Dir(filePath), 0755)
	require.NoError(t, err, "mkdir failed")

	err = os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err, "write file failed")

	_, err = wt.Add(filename)
	require.NoError(t, err, "git add failed")

	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  when,
		},
	})
	require.NoError(t, err, "commit failed")

	return hash
}

func checkout(t *testing.T, wt *gogit.Worktree, branch string, create bool) {
	t.Helper()
	err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Create: create,
	})
	require.NoError(t, err, "checkout %s failed", branch)
}

func newMergePreviewHandler(t *testing.T) *MergePreviewHandler {
	t.Helper()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: t.TempDir(),
		},
	}
	handler, err := NewMergePreviewHandler(cfg)
	require.NoError(t, err, "handler creation failed")
	return handler
}
