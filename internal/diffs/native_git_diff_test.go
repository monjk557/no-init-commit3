package diffs

// native_git.go / service.go 의 git 실행 경로를 실제 bare 저장소 fixture 로 검증한다.
// handler_test.go 의 makeBlob/makeTree/makeCommit 헬퍼(go-git 기반)로 bare repo 를
// 구성하면 native git(exec) 이 동일 저장소를 읽을 수 있다.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoCommitRepo 는 existing.txt 를 한 줄에서 두 줄로 수정하는 두 커밋을 가진
// bare 저장소를 만들고 (repoPath, initialSHA, secondSHA) 를 반환한다.
func twoCommitRepo(t *testing.T) (string, string, string) {
	t.Helper()
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "owner", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	b1 := makeBlob(t, repo, []byte("First line\n"))
	t1 := makeTree(t, repo, []object.TreeEntry{{Name: "existing.txt", Mode: 0100644, Hash: b1}})
	c1 := makeCommit(t, repo, "Initial commit", t1, nil, now)
	require.NoError(t, repo.Storer.SetReference(
		plumbing.NewHashReference("refs/heads/master", c1)))

	b2 := makeBlob(t, repo, []byte("First line\nSecond line\n"))
	t2 := makeTree(t, repo, []object.TreeEntry{{Name: "existing.txt", Mode: 0100644, Hash: b2}})
	c2 := makeCommit(t, repo, "Add second line", t2, []plumbing.Hash{c1}, now)
	require.NoError(t, repo.Storer.SetReference(
		plumbing.NewHashReference("refs/heads/master", c2)))

	return repoPath, c1.String(), c2.String()
}

// ----------------------------------------------------------------------------
// nativeCalculateSingleCommitDiff (service.calculateSingleCommitDiff 경유 포함)
// ----------------------------------------------------------------------------

func TestNativeCalculateSingleCommitDiff_ParentDiff(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	ctx := context.Background()

	resp, err := nativeCalculateSingleCommitDiff(ctx, repoPath, secondSHA, 3)
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.NotNil(t, resp.Commit)
	assert.Equal(t, secondSHA, resp.Commit.SHA)
	assert.Equal(t, "Add second line", resp.Commit.Message)
	require.NotNil(t, resp.DiffResponse)
	assert.Equal(t, secondSHA, resp.Head)

	// existing.txt 에 한 줄 추가 → additions=1.
	var ef *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "existing.txt" {
			ef = &resp.Files[i]
		}
	}
	require.NotNil(t, ef)
	assert.Equal(t, 1, ef.Additions)
}

func TestNativeCalculateSingleCommitDiff_InitialCommitVsEmptyTree(t *testing.T) {
	repoPath, initialSHA, _ := twoCommitRepo(t)
	ctx := context.Background()

	resp, err := nativeCalculateSingleCommitDiff(ctx, repoPath, initialSHA, 3)
	require.NoError(t, err)
	require.True(t, resp.Success)
	// 부모 없는 초기 커밋 → base 가 "empty_tree" 로 표기.
	assert.Equal(t, "empty_tree", resp.Base)
	// existing.txt 가 새로 추가됨.
	require.NotEmpty(t, resp.Files)
}

func TestNativeCalculateSingleCommitDiff_BadSHA(t *testing.T) {
	repoPath, _, _ := twoCommitRepo(t)
	ctx := context.Background()

	_, err := nativeCalculateSingleCommitDiff(ctx, repoPath, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", 3)
	require.Error(t, err)
}

func TestService_CalculateSingleCommitDiff_Delegates(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	h := &DiffHandler{}
	resp, err := h.calculateSingleCommitDiff(context.Background(), repoPath, secondSHA, 3)
	require.NoError(t, err)
	require.NotNil(t, resp.Commit)
	assert.Equal(t, secondSHA, resp.Commit.SHA)
}

// ----------------------------------------------------------------------------
// nativeGetCommitMetadata
// ----------------------------------------------------------------------------

func TestNativeGetCommitMetadata_Success(t *testing.T) {
	repoPath, initialSHA, secondSHA := twoCommitRepo(t)
	ctx := context.Background()

	meta, err := nativeGetCommitMetadata(ctx, repoPath, secondSHA)
	require.NoError(t, err)
	assert.Equal(t, secondSHA, meta.SHA)
	assert.Equal(t, "Add second line", meta.Message)
	assert.Equal(t, "Test", meta.Author)
	assert.Equal(t, "test@example.com", meta.Email)
	// 두 번째 커밋은 첫 커밋을 부모로 가짐.
	require.Len(t, meta.Parents, 1)
	assert.Equal(t, initialSHA, meta.Parents[0])
}

func TestNativeGetCommitMetadata_InitialCommitNoParents(t *testing.T) {
	repoPath, initialSHA, _ := twoCommitRepo(t)
	meta, err := nativeGetCommitMetadata(context.Background(), repoPath, initialSHA)
	require.NoError(t, err)
	assert.Empty(t, meta.Parents)
}

func TestNativeGetCommitMetadata_BadSHA(t *testing.T) {
	repoPath, _, _ := twoCommitRepo(t)
	_, err := nativeGetCommitMetadata(context.Background(), repoPath, "nonexistentref123")
	require.Error(t, err)
}

// ----------------------------------------------------------------------------
// nativeExpandContextLines
// ----------------------------------------------------------------------------

func TestNativeExpandContextLines_ReturnsRequestedRange(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	ctx := context.Background()

	// secondSHA 의 existing.txt 는 "First line\nSecond line\n" (2줄).
	resp, err := nativeExpandContextLines(ctx, repoPath, secondSHA, "existing.txt", 1, 2, 1, true)
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.Equal(t, "existing.txt", resp.Path)
	assert.Equal(t, 2, resp.Count)
	require.Len(t, resp.Lines, 2)
	assert.Equal(t, "First line", resp.Lines[0].Text)
	assert.Equal(t, "Second line", resp.Lines[1].Text)
	for _, ln := range resp.Lines {
		assert.Equal(t, LineChangeTypeContext, ln.Type)
	}
}

func TestNativeExpandContextLines_ClampsToFileLength(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	ctx := context.Background()

	// toOld=100 은 파일 길이(2)로 클램핑.
	resp, err := nativeExpandContextLines(ctx, repoPath, secondSHA, "existing.txt", 1, 100, 1, true)
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Count)
}

func TestNativeExpandContextLines_EmptyWhenFromGreaterThanTo(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	ctx := context.Background()

	// fromOld(5) > 클램핑된 toOld(2) → 빈 결과.
	resp, err := nativeExpandContextLines(ctx, repoPath, secondSHA, "existing.txt", 5, 100, 5, true)
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Count)
	assert.Empty(t, resp.Lines)
}

func TestNativeExpandContextLines_BadRef(t *testing.T) {
	repoPath, _, _ := twoCommitRepo(t)
	_, err := nativeExpandContextLines(context.Background(), repoPath, "no-such-ref", "existing.txt", 1, 2, 1, true)
	require.Error(t, err)
}

func TestNativeExpandContextLines_FileNotFound(t *testing.T) {
	repoPath, _, secondSHA := twoCommitRepo(t)
	_, err := nativeExpandContextLines(context.Background(), repoPath, secondSHA, "missing.txt", 1, 2, 1, true)
	require.Error(t, err)
}

// ----------------------------------------------------------------------------
// service.go — GenerateLineChanges / CalculateMergeBaseDiff / generatePatch
// ----------------------------------------------------------------------------

func TestGenerateLineChanges_Standalone(t *testing.T) {
	t.Parallel()
	lines := []string{"+added", "-removed", " context", "noprefix"}
	got := GenerateLineChanges(lines, 0)
	require.Len(t, got, 4)
	assert.Equal(t, LineChangeTypeAdd, got[0].Type)
	assert.Equal(t, "added", got[0].Text)
	assert.Equal(t, LineChangeTypeDel, got[1].Type)
	assert.Equal(t, "removed", got[1].Text)
	assert.Equal(t, LineChangeTypeContext, got[2].Type)
	assert.Equal(t, "context", got[2].Text)
	// 접두사 없는 라인은 CONTEXT 로 보존 (text 그대로).
	assert.Equal(t, LineChangeTypeContext, got[3].Type)
	assert.Equal(t, "noprefix", got[3].Text)
}

func TestService_GenerateLineChanges_Method(t *testing.T) {
	t.Parallel()
	h := &DiffHandler{}
	got := h.GenerateLineChanges([]string{"+x"}, 0)
	require.Len(t, got, 1)
	assert.Equal(t, LineChangeTypeAdd, got[0].Type)
}

func TestCalculateMergeBaseDiff_LinearHistory(t *testing.T) {
	repoPath, initialSHA, secondSHA := twoCommitRepo(t)
	ctx := context.Background()

	// 선형 히스토리에서 merge-base(initial, second) = initial → second 도입 변경만.
	resp, err := CalculateMergeBaseDiff(ctx, repoPath, initialSHA, secondSHA, 3)
	require.NoError(t, err)
	require.True(t, resp.Success)
	assert.Equal(t, string(DiffModeMergeBase), resp.DiffMode)
	require.NotEmpty(t, resp.Files)
}

func TestService_GeneratePatch_ProducesUnifiedDiff(t *testing.T) {
	repoPath, initialSHA, secondSHA := twoCommitRepo(t)
	h := &DiffHandler{}
	patch, err := h.generatePatch(repoPath, initialSHA, secondSHA)
	require.NoError(t, err)
	assert.Contains(t, patch, "existing.txt")
	assert.Contains(t, patch, "+Second line")
}

// TestNativeExpandContextLines_HeadBlobSlicedByNewSide 는 Issue-6404 회귀 박제:
// hunk 앞쪽에서 순증한 라인 때문에 old/new 번호가 어긋난 파일에서 head blob 을
// old 번호로 자르면 엉뚱한 구간이 나온다. 반드시 new 번호로 잘라야 한다.
func TestNativeExpandContextLines_HeadBlobSlicedByNewSide(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "owner", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// base: 3 줄. head: 앞에 2 줄이 추가되어 old N == new N+2.
	b1 := makeBlob(t, repo, []byte("old1\nold2\nold3\n"))
	t1 := makeTree(t, repo, []object.TreeEntry{{Name: "f.txt", Mode: 0100644, Hash: b1}})
	c1 := makeCommit(t, repo, "base", t1, nil, now)

	b2 := makeBlob(t, repo, []byte("added1\nadded2\nold1\nold2\nold3\n"))
	t2 := makeTree(t, repo, []object.TreeEntry{{Name: "f.txt", Mode: 0100644, Hash: b2}})
	c2 := makeCommit(t, repo, "head", t2, []plumbing.Hash{c1}, now)

	// old 2..3 == new 4..5 구간을 head blob 에서 펼친다.
	resp, err := nativeExpandContextLines(context.Background(), repoPath,
		c2.String(), "f.txt", 2, 3, 4, true)
	require.NoError(t, err)
	require.Len(t, resp.Lines, 2)
	assert.Equal(t, "old2", resp.Lines[0].Text)
	assert.Equal(t, "old3", resp.Lines[1].Text)
	// 라벨은 요청한 old/new 번호를 그대로 유지.
	assert.Equal(t, 2, *resp.Lines[0].Left)
	assert.Equal(t, 4, *resp.Lines[0].Right)

	// side=old (base blob) 는 old 번호로 잘라야 같은 구간이 나온다.
	oldResp, err := nativeExpandContextLines(context.Background(), repoPath,
		c1.String(), "f.txt", 2, 3, 4, false)
	require.NoError(t, err)
	require.Len(t, oldResp.Lines, 2)
	assert.Equal(t, "old2", oldResp.Lines[0].Text)
	assert.Equal(t, "old3", oldResp.Lines[1].Text)
}
