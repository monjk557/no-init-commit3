package repositories

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBareRepoWithCommit 는 bare repo 를 만들고 main 브랜치에 단일 commit 을
// native git plumbing 으로 생성한 뒤 (repoPath, commitHash) 를 반환한다.
func newBareRepoWithCommit(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	_, repoPath := newBareRepoAt(t, "owner", "repo")

	blob, err := nativeHashObject(ctx, repoPath, []byte("hello\n"))
	require.NoError(t, err)

	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: "README.md"},
	})
	require.NoError(t, err)

	sig := &nativeSignature{Name: "Tester", Email: "t@example.com", When: time.Now()}
	commit, err := nativeCommitTree(ctx, repoPath, tree, nil, "init", sig, sig)
	require.NoError(t, err)

	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", commit))
	require.NoError(t, nativeSetSymbolicRef(ctx, repoPath, "refs/heads/main"))

	return repoPath, commit
}

func TestNative_FullPlumbingRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoPath, commit := newBareRepoWithCommit(t)

	// resolve / verify
	resolved, err := nativeResolveRef(ctx, repoPath, "main")
	require.NoError(t, err)
	assert.Equal(t, commit, resolved)

	verified, ok := nativeRevParseVerify(ctx, repoPath, "HEAD")
	assert.True(t, ok)
	assert.Equal(t, commit, verified)

	_, ok = nativeRevParseVerify(ctx, repoPath, "refs/heads/does-not-exist")
	assert.False(t, ok)

	// symbolic-ref
	sym, err := nativeSymbolicRef(ctx, repoPath)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", sym)

	// cat-file -t → commit
	typ, err := nativeCatFileType(ctx, repoPath, commit)
	require.NoError(t, err)
	assert.Equal(t, "commit", typ)

	// counts
	branchCount, err := nativeForEachRefCount(ctx, repoPath, "refs/heads/")
	require.NoError(t, err)
	assert.Equal(t, 1, branchCount)

	tagCount, err := nativeForEachRefCount(ctx, repoPath, "refs/tags/")
	require.NoError(t, err)
	assert.Equal(t, 0, tagCount)

	commitCount, err := nativeRevListCount(ctx, repoPath, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, 1, commitCount)

	// has any refs
	assert.True(t, nativeHasAnyRefs(ctx, repoPath))
}

func TestNative_EmptyRepoHasNoRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, repoPath := newBareRepoAt(t, "owner", "empty")

	assert.False(t, nativeHasAnyRefs(ctx, repoPath))

	count, err := nativeForEachRefCount(ctx, repoPath, "refs/heads/")
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// detached/없는 HEAD → symbolic-ref 은 set 되어있어 main 가리킴 (init --bare)
	sym, err := nativeSymbolicRef(ctx, repoPath)
	require.NoError(t, err)
	assert.NotEmpty(t, sym)
}

func TestNative_DereferenceTag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoPath, commit := newBareRepoWithCommit(t)

	// lightweight tag → commit 가리킴
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/tags/v1", commit))
	deref, err := nativeDereferenceTag(ctx, repoPath, "refs/tags/v1")
	require.NoError(t, err)
	assert.Equal(t, commit, deref)

	// 존재하지 않는 tag → 에러
	_, err = nativeDereferenceTag(ctx, repoPath, "refs/tags/nope")
	assert.Error(t, err)
}

func TestNative_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoPath, _ := newBareRepoWithCommit(t)

	// 존재하지 않는 ref resolve → 에러
	_, err := nativeResolveRef(ctx, repoPath, "refs/heads/ghost")
	assert.Error(t, err)

	// cat-file 잘못된 hash → 에러
	_, err = nativeCatFileType(ctx, repoPath, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	assert.Error(t, err)

	// rev-list count 없는 ref → 에러
	_, err = nativeRevListCount(ctx, repoPath, "refs/heads/ghost")
	assert.Error(t, err)

	// 잘못된 repoPath → init/symbolic-ref 등 실패
	bad := "/nonexistent/repo.git"
	assert.Error(t, nativeUpdateRef(ctx, bad, "refs/heads/x", "deadbeef"))
	_, err = nativeForEachRefCount(ctx, bad, "refs/heads/")
	assert.Error(t, err)
}

func TestNative_CommitTreeWithParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoPath, parent := newBareRepoWithCommit(t)

	blob, err := nativeHashObject(ctx, repoPath, []byte("v2\n"))
	require.NoError(t, err)
	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: "README.md"},
	})
	require.NoError(t, err)

	sig := &nativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	child, err := nativeCommitTree(ctx, repoPath, tree, []string{parent}, "second", sig, sig)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", child))

	count, err := nativeRevListCount(ctx, repoPath, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, 2, count, "parent 연결 시 commit 2개")
}

func TestNative_CommitTreeNilSignatures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoPath, _ := newBareRepoWithCommit(t)

	blob, err := nativeHashObject(ctx, repoPath, []byte("x\n"))
	require.NoError(t, err)
	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: "x.txt"},
	})
	require.NoError(t, err)

	// author/committer nil → 환경 기본값 사용 분기
	commit, err := nativeCommitTree(ctx, repoPath, tree, nil, "no sig", nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, commit)
}
