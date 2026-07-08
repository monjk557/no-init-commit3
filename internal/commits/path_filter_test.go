package commits

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// commitChangeOnMain applies the given file changes on top of the current
// refs/heads/main tip and advances main to the new commit, returning its hash.
func commitChangeOnMain(t *testing.T, repoPath, parent, message string, changes []FileChange) string {
	t.Helper()
	ctx := context.Background()

	baseTree, err := nativeGetTreeHash(ctx, repoPath, parent)
	require.NoError(t, err)

	res, err := createTreeFromChanges(ctx, repoPath, baseTree, changes)
	require.NoError(t, err)

	author := &NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	hash, err := nativeCommitTree(ctx, repoPath, res.TreeHash, []string{parent}, message, author, author)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", hash))

	return hash
}

// shaSet collects the SHAs from a commit slice for order-independent assertions.
func shaSet(commits []Commit) map[string]bool {
	set := make(map[string]bool, len(commits))
	for _, c := range commits {
		set[c.Sha] = true
	}
	return set
}

// Test_GetCommits_PathFilter_ScopesToSubtree verifies that a non-empty path
// restricts the log to commits touching that pathspec (git log <ref> -- <path>),
// while an empty path returns the full history.
func Test_GetCommits_PathFilter_ScopesToSubtree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repoPath := newBareRepoNative(t)

	// c0: root.txt created (no subtree).
	c0 := createInitialCommitNative(t, repoPath, "root.txt", []byte("R0"))
	// c1: dir/a.txt created (touches dir).
	c1 := commitChangeOnMain(t, repoPath, c0, "add dir/a.txt", []FileChange{
		{Operation: FileOperationCreate, Path: "dir/a.txt", Content: []byte("A0")},
	})
	// c2: root.txt modified (does NOT touch dir).
	c2 := commitChangeOnMain(t, repoPath, c1, "edit root.txt", []FileChange{
		{Operation: FileOperationUpdate, Path: "root.txt", Content: []byte("R1")},
	})
	// c3: dir/a.txt modified (touches dir).
	c3 := commitChangeOnMain(t, repoPath, c2, "edit dir/a.txt", []FileChange{
		{Operation: FileOperationUpdate, Path: "dir/a.txt", Content: []byte("A1")},
	})

	t.Run("subtree path returns only commits touching it", func(t *testing.T) {
		commits, err := GetCommits(ctx, repoPath, "main", 50, 0, "dir")
		require.NoError(t, err)
		got := shaSet(commits)
		require.Len(t, commits, 2, "only c1 and c3 touch dir/")
		require.True(t, got[c1], "c1 (created dir/a.txt) should be present")
		require.True(t, got[c3], "c3 (edited dir/a.txt) should be present")
		require.False(t, got[c0], "c0 (root.txt only) should be excluded")
		require.False(t, got[c2], "c2 (root.txt only) should be excluded")
	})

	t.Run("file path returns only commits touching that file", func(t *testing.T) {
		commits, err := GetCommits(ctx, repoPath, "main", 50, 0, "root.txt")
		require.NoError(t, err)
		got := shaSet(commits)
		require.Len(t, commits, 2, "only c0 and c2 touch root.txt")
		require.True(t, got[c0])
		require.True(t, got[c2])
		require.False(t, got[c1])
		require.False(t, got[c3])
	})

	t.Run("empty path returns full history", func(t *testing.T) {
		commits, err := GetCommits(ctx, repoPath, "main", 50, 0, "")
		require.NoError(t, err)
		require.Len(t, commits, 4, "all four commits reachable from main")
	})

	t.Run("path with no matching commits returns empty", func(t *testing.T) {
		commits, err := GetCommits(ctx, repoPath, "main", 50, 0, "nonexistent-dir")
		require.NoError(t, err)
		require.Empty(t, commits)
	})
}
