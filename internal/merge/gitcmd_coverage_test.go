package merge

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 회귀: "native git command layer (gitcmd.go) 는 모든 merge/preview 동작의
// 기반이므로 직접 회귀 보호가 필요하다." raw bare repo 를 구성하여 각
// git wrapper 를 직접 호출한다 (already_merged_test.go 헬퍼 재사용).
//
// 환경 제약: gitMergeTree 의 --merge-base= 형식은 Apple Git 2.39.5 에서
// 미지원이므로, base 가 빈 문자열인 형식 (merge-tree --write-tree ours theirs)
// 만 직접 호출한다 — 이 형식은 2.38+ 에서 지원된다.

func Test_gitcmd_RevParse_and_Verify(t *testing.T) {
	t.Parallel()

	repoPath, commit := newBareRepoForMerge(t)
	ctx := context.Background()

	got, err := gitRevParse(ctx, repoPath, "refs/heads/main")
	require.NoError(t, err)
	require.Equal(t, commit, got)

	// empty ref → HEAD
	got, err = gitRevParse(ctx, repoPath, "")
	require.NoError(t, err)
	require.Equal(t, commit, got)

	// verify existing ref
	got, err = gitRevParseVerify(ctx, repoPath, "refs/heads/main")
	require.NoError(t, err)
	require.Equal(t, commit, got)

	// unknown ref → error
	_, err = gitRevParse(ctx, repoPath, "nonexistent-ref")
	require.Error(t, err)
}

func Test_gitcmd_MergeBase_and_Ancestor(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	left := addCommit(t, repoPath, base, "l.txt", "l\n", "left")
	right := addCommit(t, repoPath, base, "r.txt", "r\n", "right")
	ctx := context.Background()

	mb, err := gitMergeBase(ctx, repoPath, left, right)
	require.NoError(t, err)
	require.Equal(t, base, mb)

	isAnc, err := gitMergeBaseIsAncestor(ctx, repoPath, base, left)
	require.NoError(t, err)
	require.True(t, isAnc)

	isAnc, err = gitMergeBaseIsAncestor(ctx, repoPath, left, right)
	require.NoError(t, err)
	require.False(t, isAnc)
}

func Test_gitcmd_CatFileCommit(t *testing.T) {
	t.Parallel()

	repoPath, commit := newBareRepoForMerge(t)
	ctx := context.Background()

	c, err := gitCatFileCommit(ctx, repoPath, commit)
	require.NoError(t, err)
	require.Equal(t, commit, c.Hash)
	require.Equal(t, "Initial commit", strings.TrimSpace(c.Message))
	require.NotEmpty(t, c.TreeHash)
	require.Equal(t, "test", c.Author.Name)
	require.Equal(t, "test@test.com", c.Author.Email)
}

func Test_gitcmd_LsTree(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()

	// Build a nested tree manually: subdir tree + root tree pointing at it.
	nestedBlob, err := gitHashObject(ctx, repoPath, []byte("nested\n"))
	require.NoError(t, err)
	subTree, err := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "100644", Type: "blob", Hash: nestedBlob, Path: "nested.txt"},
	})
	require.NoError(t, err)
	rootBlob, err := gitHashObject(ctx, repoPath, []byte("top\n"))
	require.NoError(t, err)
	rootTree, err := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "100644", Type: "blob", Hash: rootBlob, Path: "top.txt"},
		{Mode: "040000", Type: "tree", Hash: subTree, Path: "dir"},
	})
	require.NoError(t, err)

	// non-recursive: top-level entries (top.txt + dir)
	entries, err := gitLsTree(ctx, repoPath, rootTree, false)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	var sawDir bool
	for _, e := range entries {
		if e.Path == "dir" && e.Type == "tree" {
			sawDir = true
		}
	}
	require.True(t, sawDir)

	// recursive: blob paths flattened
	rec, err := gitLsTree(ctx, repoPath, rootTree, true)
	require.NoError(t, err)
	var sawNested bool
	for _, e := range rec {
		if e.Path == "dir/nested.txt" && e.Type == "blob" {
			sawNested = true
			require.Equal(t, "100644", e.Mode)
		}
	}
	require.True(t, sawNested)
}

func Test_gitcmd_HashObject_and_Mktree(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()

	blob, err := gitHashObject(ctx, repoPath, []byte("hello world\n"))
	require.NoError(t, err)
	require.Len(t, blob, 40)

	tree, err := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Path: "hello.txt"},
	})
	require.NoError(t, err)
	require.Len(t, tree, 40)

	// blob roundtrip
	data, err := gitCatFileBlob(ctx, repoPath, blob)
	require.NoError(t, err)
	require.Equal(t, "hello world\n", string(data))

	size, err := gitCatFileBlobSize(ctx, repoPath, blob)
	require.NoError(t, err)
	require.Equal(t, int64(len("hello world\n")), size)
}

func Test_gitcmd_CommitTree_and_UpdateRef(t *testing.T) {
	t.Parallel()

	repoPath, parent := newBareRepoForMerge(t)
	ctx := context.Background()

	blob, err := gitHashObject(ctx, repoPath, []byte("new content\n"))
	require.NoError(t, err)
	tree, err := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Path: "new.txt"},
	})
	require.NoError(t, err)

	sig := NativeSignature{Name: "Author", Email: "a@x.com"}
	commit, err := gitCommitTree(ctx, repoPath, tree, []string{parent}, sig, sig, "new commit")
	require.NoError(t, err)
	require.Len(t, commit, 40)

	// update ref
	require.NoError(t, gitUpdateRef(ctx, repoPath, "refs/heads/feature", commit))
	resolved, err := gitRevParse(ctx, repoPath, "refs/heads/feature")
	require.NoError(t, err)
	require.Equal(t, commit, resolved)

	// CAS: correct oldHash succeeds
	blob2, _ := gitHashObject(ctx, repoPath, []byte("v2\n"))
	tree2, _ := gitMktree(ctx, repoPath, []NativeTreeEntry{{Mode: "100644", Type: "blob", Hash: blob2, Path: "new.txt"}})
	commit2, err := gitCommitTree(ctx, repoPath, tree2, []string{commit}, sig, sig, "v2")
	require.NoError(t, err)
	require.NoError(t, gitUpdateRefCAS(ctx, repoPath, "refs/heads/feature", commit2, commit))

	// CAS: wrong oldHash fails
	require.Error(t, gitUpdateRefCAS(ctx, repoPath, "refs/heads/feature", commit, "0000000000000000000000000000000000000000"))
}

func Test_gitcmd_RevList(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	c1 := addCommit(t, repoPath, base, "a.txt", "a\n", "c1")
	c2 := addCommit(t, repoPath, c1, "b.txt", "b\n", "c2")
	ctx := context.Background()

	list, err := gitRevList(ctx, repoPath, base, c2)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, c2, list[0]) // newest first

	rev, err := gitRevListReverse(ctx, repoPath, base, c2)
	require.NoError(t, err)
	require.Len(t, rev, 2)
	require.Equal(t, c1, rev[0]) // oldest first

	count, err := gitRevListCount(ctx, repoPath, base, c2)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// empty range
	empty, err := gitRevList(ctx, repoPath, c2, c2)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func Test_gitcmd_Log(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	c1 := addCommit(t, repoPath, base, "a.txt", "a\n", "first")
	c2 := addCommit(t, repoPath, c1, "b.txt", "b\n", "second")
	ctx := context.Background()

	commits, err := gitLog(ctx, repoPath, c2, 10)
	require.NoError(t, err)
	require.Len(t, commits, 3) // c2, c1, base
	require.Equal(t, "second", commits[0].Message)

	rangeCommits, err := gitLogRange(ctx, repoPath, base, c2, 10, 0)
	require.NoError(t, err)
	require.Len(t, rangeCommits, 2)

	// pagination: limit 1
	limited, err := gitLogRange(ctx, repoPath, base, c2, 1, 0)
	require.NoError(t, err)
	require.Len(t, limited, 1)

	// skip 1
	skipped, err := gitLogRange(ctx, repoPath, base, c2, 10, 1)
	require.NoError(t, err)
	require.Len(t, skipped, 1)
}

func Test_gitcmd_DiffTree(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	commit := addCommit(t, repoPath, base, "added.txt", "new\n", "add file")
	ctx := context.Background()

	diffs, err := gitDiffTree(ctx, repoPath, base, commit)
	require.NoError(t, err)
	require.NotEmpty(t, diffs)
	var sawAdd bool
	for _, de := range diffs {
		if de.Path == "added.txt" && de.Status == "A" {
			sawAdd = true
		}
	}
	require.True(t, sawAdd)
}

func Test_gitcmd_ShowFile(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	commit := addCommit(t, repoPath, base, "readme.txt", "hello\n", "add readme")
	ctx := context.Background()

	data, err := gitShowFile(ctx, repoPath, commit, "readme.txt")
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(data))
}

func Test_gitcmd_MergeTree_NoBase_Clean(t *testing.T) {
	t.Parallel()

	// base 빈 문자열 형식 (merge-tree --write-tree ours theirs) — git 2.38+.
	// 서로 다른 파일을 추가하는 두 브랜치 → 충돌 없는 머지.
	repoPath, base := newBareRepoForMerge(t)
	ours := addCommit(t, repoPath, base, "ours.txt", "ours\n", "ours")
	theirs := addCommit(t, repoPath, base, "theirs.txt", "theirs\n", "theirs")
	ctx := context.Background()

	res, err := gitMergeTree(ctx, repoPath, "", ours, theirs)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.TreeHash, 40)
	require.False(t, res.HasConflicts)
}

func Test_gitcmd_MergeTree_NoBase_Conflict(t *testing.T) {
	t.Parallel()

	// 같은 파일을 양쪽에서 다르게 수정 → 충돌. base="" 형식이라 git 2.39
	// 에서도 동작 (--merge-base 미사용).
	repoPath, base := newBareRepoForMerge(t)
	base = addCommit(t, repoPath, base, "f.txt", "l1\nl2\nl3\n", "shared")
	ours := addCommit(t, repoPath, base, "f.txt", "l1\nOURS\nl3\n", "ours")
	theirs := addCommit(t, repoPath, base, "f.txt", "l1\nTHEIRS\nl3\n", "theirs")
	ctx := context.Background()

	res, err := gitMergeTree(ctx, repoPath, "", ours, theirs)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.HasConflicts)
	require.Len(t, res.TreeHash, 40)
}

func Test_gitcmd_BatchCatFile(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()

	blob, err := gitHashObject(ctx, repoPath, []byte("batch content here\n"))
	require.NoError(t, err)

	batch, err := NewBatchCatFile(ctx, repoPath)
	require.NoError(t, err)
	defer batch.Close()

	data, err := batch.ReadBlob(blob, 0)
	require.NoError(t, err)
	require.Equal(t, "batch content here\n", string(data))

	// maxBytes truncation
	truncated, err := batch.ReadBlob(blob, 5)
	require.NoError(t, err)
	require.Equal(t, "batch", string(truncated))

	// missing object → error
	_, err = batch.ReadBlob("0000000000000000000000000000000000000000", 0)
	require.Error(t, err)
}
