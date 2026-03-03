package commits

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/stretchr/testify/require"
)

func newBareRepoNative(t *testing.T) string {
	t.Helper()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "o", "r.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))

	cmd := exec.Command("git", "init", "--bare", repoPath)
	require.NoError(t, cmd.Run())

	return repoPath
}

// createInitialCommitNative creates a commit with a single file using native git and returns the commit hash.
func createInitialCommitNative(t *testing.T, repoPath, filename string, content []byte) string {
	t.Helper()
	ctx := context.Background()

	// Hash the blob
	blobHash, err := nativeHashObject(ctx, repoPath, content)
	require.NoError(t, err)

	// Create tree with the file
	treeHash, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobHash, Name: filename},
	})
	require.NoError(t, err)

	// Create commit
	author := &NativeSignature{
		Name:  "Test",
		Email: "test@example.com",
		When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	commitHash, err := nativeCommitTree(ctx, repoPath, treeHash, nil, "init", author, author)
	require.NoError(t, err)

	// Update refs
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", commitHash))
	require.NoError(t, nativeSetSymbolicRef(ctx, repoPath, "refs/heads/main"))

	return commitHash
}

func Test_TransactionManager_ValidateTransaction_RootCommitRejectsNonCreate(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "info",
		Format:      "text",
		Output:      "stdout",
		Development: true,
	})
	require.NoError(t, err)
	tm := NewTransactionManager(logger)

	commitInfo := CommitInfo{
		Message: "first commit",
		Author: NativeSignature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Parents: nil,
	}

	txn, err := tm.BeginTransaction(context.Background(), repoPath, "", commitInfo, TransactionOptions{
		Timeout:            30 * time.Second,
		ValidateOnly:       false,
		AllowPartialCommit: false,
	})
	require.NoError(t, err)

	err = tm.AddOperation(context.Background(), txn, TransactionOperation{
		Type: FileOperationDelete,
		FileChange: FileChange{
			Path:      "ghost.txt",
			Operation: FileOperationDelete,
		},
	})
	require.NoError(t, err)

	validateErr := tm.ValidateTransaction(context.Background(), txn)

	require.Error(t, validateErr)
	require.Contains(t, validateErr.Error(), "root commit allows only create operations")
}

func Test_TransactionManager_CommitTransaction_ValidateOnly_ReturnsCommittedWithoutCommitResult(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "info",
		Format:      "text",
		Output:      "stdout",
		Development: true,
	})
	require.NoError(t, err)
	tm := NewTransactionManager(logger)

	commitInfo := CommitInfo{
		Message: "validate only",
		Author: NativeSignature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		Parents: []string{baseHash},
	}

	txn, err := tm.BeginTransaction(context.Background(), repoPath, baseHash, commitInfo, TransactionOptions{
		Timeout:            30 * time.Second,
		ValidateOnly:       true,
		AllowPartialCommit: false,
	})
	require.NoError(t, err)

	err = tm.AddOperation(context.Background(), txn, TransactionOperation{
		Type: FileOperationUpdate,
		FileChange: FileChange{
			Path:      "a.txt",
			Operation: FileOperationUpdate,
			Content:   []byte("A2"),
		},
	})
	require.NoError(t, err)

	require.NoError(t, tm.ValidateTransaction(context.Background(), txn))

	result, commitErr := tm.CommitTransaction(context.Background(), txn)

	require.NoError(t, commitErr)
	require.Equal(t, TransactionStateCommitted, result.State)
	require.Nil(t, result.CommitResult)
	require.Equal(t, TransactionStateCommitted, txn.State)
}

func Test_CreateTreeFromChanges_CreateUpdateDelete_AppliesAllChanges(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	// Create base with two files: old.txt and keep.txt
	blobOld, err := nativeHashObject(ctx, repoPath, []byte("old"))
	require.NoError(t, err)
	blobKeep, err := nativeHashObject(ctx, repoPath, []byte("keep"))
	require.NoError(t, err)

	treeHash, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobKeep, Name: "keep.txt"},
		{Mode: "100644", Type: "blob", Hash: blobOld, Name: "old.txt"},
	})
	require.NoError(t, err)

	author := &NativeSignature{Name: "Test", Email: "test@example.com", When: time.Now()}
	commitHash, err := nativeCommitTree(ctx, repoPath, treeHash, nil, "base", author, author)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/main", commitHash))

	// Get base tree hash
	baseTreeHash, err := nativeGetTreeHash(ctx, repoPath, commitHash)
	require.NoError(t, err)

	// Apply changes: create new.txt, update keep.txt, delete old.txt
	res, err := createTreeFromChanges(ctx, repoPath, baseTreeHash, []FileChange{
		{Operation: FileOperationCreate, Path: "new.txt", Content: []byte("new")},
		{Operation: FileOperationUpdate, Path: "keep.txt", Content: []byte("keep2")},
		{Operation: FileOperationDelete, Path: "old.txt"},
	})
	require.NoError(t, err)

	// Verify: tree should contain new.txt and keep.txt, not old.txt
	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, e := range entries {
		paths[e.Path] = true
	}

	require.False(t, paths["old.txt"], "old.txt should be deleted")
	require.True(t, paths["keep.txt"], "keep.txt should exist")
	require.True(t, paths["new.txt"], "new.txt should exist")

	// Verify content of keep.txt (read via cat-file)
	cmd := exec.Command("git", "--git-dir", repoPath, "cat-file", "-p", findEntryHash(entries, "keep.txt"))
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "keep2", string(out))

	// Verify content of new.txt
	cmd = exec.Command("git", "--git-dir", repoPath, "cat-file", "-p", findEntryHash(entries, "new.txt"))
	out, err = cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "new", string(out))
}

func findEntryHash(entries []nativeLsTreeEntry, path string) string {
	for _, e := range entries {
		if e.Path == path {
			return e.Hash
		}
	}
	return ""
}

func Test_SortNativeTreeEntries_DirsBeforeFiles(t *testing.T) {
	t.Parallel()

	entries := []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: strings.Repeat("a", 40), Name: "z.txt"},
		{Mode: "040000", Type: "tree", Hash: strings.Repeat("b", 40), Name: "a"},
		{Mode: "100644", Type: "blob", Hash: strings.Repeat("c", 40), Name: "b.txt"},
	}

	sortNativeTreeEntries(entries)

	// Directory "a" should sort before "b.txt" and "z.txt"
	require.Equal(t, "a", entries[0].Name)
	require.Equal(t, "b.txt", entries[1].Name)
	require.Equal(t, "z.txt", entries[2].Name)
}
