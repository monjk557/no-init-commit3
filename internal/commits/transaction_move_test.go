package commits

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateTreeFromChanges_Move_Success(t *testing.T) {
	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	// Create base with old.txt
	blobHash, err := nativeHashObject(ctx, repoPath, []byte("hello"))
	require.NoError(t, err)

	treeHash, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobHash, Name: "old.txt"},
	})
	require.NoError(t, err)

	author := &NativeSignature{Name: "Test", Email: "test@example.com", When: time.Now()}
	commitHash, err := nativeCommitTree(ctx, repoPath, treeHash, nil, "init", author, author)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", commitHash))

	baseTreeHash, err := nativeGetTreeHash(ctx, repoPath, commitHash)
	require.NoError(t, err)

	// Move old.txt -> new.txt
	res, err := createTreeFromChanges(ctx, repoPath, baseTreeHash, []FileChange{
		{Operation: FileOperationMove, OriginalPath: "old.txt", Path: "new.txt"},
	})
	require.NoError(t, err)

	// Verify: old.txt gone, new.txt exists with same content
	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, e := range entries {
		paths[e.Path] = true
	}
	require.False(t, paths["old.txt"], "old.txt should not exist")
	require.True(t, paths["new.txt"], "new.txt should exist")

	// Check content
	cmd := exec.Command("git", "--git-dir", repoPath, "cat-file", "-p", findEntryHash(entries, "new.txt"))
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "hello", string(out))
}

func TestCreateTreeFromChanges_Move_WithContentChange(t *testing.T) {
	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	// Create base with old.txt
	blobHash, err := nativeHashObject(ctx, repoPath, []byte("hello"))
	require.NoError(t, err)

	treeHash, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobHash, Name: "old.txt"},
	})
	require.NoError(t, err)

	author := &NativeSignature{Name: "Test", Email: "test@example.com", When: time.Now()}
	commitHash, err := nativeCommitTree(ctx, repoPath, treeHash, nil, "init", author, author)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", commitHash))

	baseTreeHash, err := nativeGetTreeHash(ctx, repoPath, commitHash)
	require.NoError(t, err)

	// Move old.txt -> new.txt with content change
	res, err := createTreeFromChanges(ctx, repoPath, baseTreeHash, []FileChange{
		{Operation: FileOperationMove, OriginalPath: "old.txt", Path: "new.txt", Content: []byte("world")},
	})
	require.NoError(t, err)

	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)

	cmd := exec.Command("git", "--git-dir", repoPath, "cat-file", "-p", findEntryHash(entries, "new.txt"))
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "world", string(out))
}
