package merge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 회귀: "snapshot adapter / apply_tree 의 tree 조작은 native git 으로 정확히
// 동작해야 한다." raw bare repo fixture 로 직접 검증.

func Test_TreeToSnapshot(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	commit := addCommit(t, repoPath, base, "added.txt", "content\n", "add")
	ctx := context.Background()

	treeHash := trimGitOutput(t, "git", "--git-dir", repoPath, "rev-parse", commit+"^{tree}")

	snap, err := TreeToSnapshot(ctx, repoPath, treeHash)
	require.NoError(t, err)
	require.NotNil(t, snap)
	fs, ok := snap.GetEntry("added.txt")
	require.True(t, ok)
	require.False(t, fs.IsDir)
	require.Equal(t, uint32(0o100644), fs.Mode)
	require.NotEmpty(t, fs.Hash)

	// empty tree hash → empty snapshot, no error
	empty, err := TreeToSnapshot(ctx, repoPath, "")
	require.NoError(t, err)
	require.NotNil(t, empty)
}

func Test_CommitToSnapshots(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	source := addCommit(t, repoPath, base, "src.txt", "src\n", "source")
	target := addCommit(t, repoPath, base, "tgt.txt", "tgt\n", "target")
	ctx := context.Background()

	baseSnap, srcSnap, tgtSnap, err := CommitToSnapshots(ctx, repoPath, source, target)
	require.NoError(t, err)
	require.NotNil(t, baseSnap)
	require.NotNil(t, srcSnap)
	require.NotNil(t, tgtSnap)

	// source has src.txt, target has tgt.txt
	_, ok := srcSnap.GetEntry("src.txt")
	require.True(t, ok)
	_, ok = tgtSnap.GetEntry("tgt.txt")
	require.True(t, ok)
}

func Test_CommitToSnapshots_BadRef(t *testing.T) {
	t.Parallel()

	repoPath, base := newBareRepoForMerge(t)
	ctx := context.Background()

	_, _, _, err := CommitToSnapshots(ctx, repoPath, "deadbeef00000000000000000000000000000000", base)
	require.Error(t, err)
}

func Test_replaceEntryInTree_TopLevel(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()

	blobA, _ := gitHashObject(ctx, repoPath, []byte("A\n"))
	rootTree, err := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobA, Path: "file.txt"},
	})
	require.NoError(t, err)

	blobB, _ := gitHashObject(ctx, repoPath, []byte("B\n"))
	newTree, err := replaceEntryInTree(ctx, repoPath, rootTree, "file.txt", &NativeTreeEntry{
		Mode: "100644", Type: "blob", Hash: blobB, Path: "file.txt",
	})
	require.NoError(t, err)
	require.NotEqual(t, rootTree, newTree)

	// verify replacement
	data, err := gitShowFile(ctx, repoPath, newTree, "file.txt")
	require.NoError(t, err)
	require.Equal(t, "B\n", string(data))
}

func Test_replaceEntryInTree_Nested(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()

	blobA, _ := gitHashObject(ctx, repoPath, []byte("A\n"))
	subTree, _ := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobA, Path: "inner.txt"},
	})
	rootTree, err := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "040000", Type: "tree", Hash: subTree, Path: "dir"},
	})
	require.NoError(t, err)

	blobB, _ := gitHashObject(ctx, repoPath, []byte("B\n"))
	newTree, err := replaceEntryInTree(ctx, repoPath, rootTree, "dir/inner.txt", &NativeTreeEntry{
		Mode: "100644", Type: "blob", Hash: blobB, Path: "inner.txt",
	})
	require.NoError(t, err)

	data, err := gitShowFile(ctx, repoPath, newTree, "dir/inner.txt")
	require.NoError(t, err)
	require.Equal(t, "B\n", string(data))
}

func Test_replaceEntryInTree_EmptyPath(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()

	_, err := replaceEntryInTree(ctx, repoPath, "sometree", "", &NativeTreeEntry{})
	require.Error(t, err)
}

func Test_replaceEntryInTree_AddsWhenMissing(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoForMerge(t)
	ctx := context.Background()

	blobA, _ := gitHashObject(ctx, repoPath, []byte("A\n"))
	rootTree, _ := gitMktree(ctx, repoPath, []NativeTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobA, Path: "existing.txt"},
	})

	blobNew, _ := gitHashObject(ctx, repoPath, []byte("NEW\n"))
	newTree, err := replaceEntryInTree(ctx, repoPath, rootTree, "brand-new.txt", &NativeTreeEntry{
		Mode: "100644", Type: "blob", Hash: blobNew, Path: "brand-new.txt",
	})
	require.NoError(t, err)

	data, err := gitShowFile(ctx, repoPath, newTree, "brand-new.txt")
	require.NoError(t, err)
	require.Equal(t, "NEW\n", string(data))
}
