package commits

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func newBareRepoWrapper(t *testing.T) (*repositories.RepositoryWrapper, *gogit.Repository) {
	t.Helper()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "o", "r.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))

	_, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	require.NoError(t, err)

	gitRepo := repoWrapper.GetRepo()
	require.NotNil(t, gitRepo)

	return repoWrapper, gitRepo
}

func createCommitWithSingleFile(t *testing.T, gitRepo *gogit.Repository, path string, content []byte, when time.Time) plumbing.Hash {
	t.Helper()

	blobHash, err := createBlob(gitRepo, content)
	require.NoError(t, err)

	files := map[string]TreeFileInfo{
		path: {
			Hash: blobHash,
			Mode: filemode.Regular,
		},
	}
	treeHash, err := buildTreeRecursive(gitRepo, files, "")
	require.NoError(t, err)

	commit := &object.Commit{
		Author: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  when,
		},
		Committer: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  when,
		},
		Message:  "init",
		TreeHash: treeHash,
	}

	commitObj := gitRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))

	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", commitHash)))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))

	return commitHash
}

func Test_TransactionManager_ValidateTransaction_RootCommitRejectsNonCreate(t *testing.T) {
	t.Parallel()

	// Given: empty repo and root commit transaction (baseCommit=ZeroHash)
	repoWrapper, _ := newBareRepoWrapper(t)
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
		Author: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Parents: nil,
	}

	txn, err := tm.BeginTransaction(context.Background(), repoWrapper, plumbing.ZeroHash, commitInfo, TransactionOptions{
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

	// When: validate
	validateErr := tm.ValidateTransaction(context.Background(), txn)

	// Then: validation fails with root commit rule
	require.Error(t, validateErr)
	require.Contains(t, validateErr.Error(), "root commit allows only create operations")
}

func Test_TransactionManager_CommitTransaction_ValidateOnly_ReturnsCommittedWithoutCommitResult(t *testing.T) {
	t.Parallel()

	// Given: repo with one commit and a validate-only transaction
	repoWrapper, gitRepo := newBareRepoWrapper(t)
	baseHash := createCommitWithSingleFile(t, gitRepo, "a.txt", []byte("A"), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

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
		Author: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		Parents: []plumbing.Hash{baseHash},
	}

	txn, err := tm.BeginTransaction(context.Background(), repoWrapper, baseHash, commitInfo, TransactionOptions{
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

	// When: commit validate-only
	result, commitErr := tm.CommitTransaction(context.Background(), txn)

	// Then: committed without producing a commit
	require.NoError(t, commitErr)
	require.Equal(t, TransactionStateCommitted, result.State)
	require.Nil(t, result.CommitResult)
	require.Equal(t, TransactionStateCommitted, txn.State)
}

func Test_CreateTreeFromChanges_CreateUpdateDelete_AppliesAllChanges(t *testing.T) {
	t.Parallel()

	// Given: repo with base tree containing old.txt and keep.txt
	repoWrapper, gitRepo := newBareRepoWrapper(t)
	_ = createCommitWithSingleFile(t, gitRepo, "old.txt", []byte("old"), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	baseCommit, err := gitRepo.Head()
	require.NoError(t, err)
	baseObj, err := gitRepo.CommitObject(baseCommit.Hash())
	require.NoError(t, err)
	baseTree, err := baseObj.Tree()
	require.NoError(t, err)

	// And: also add keep.txt to base tree by creating a new commit (still a deterministic base)
	blobHash, err := createBlob(gitRepo, []byte("keep"))
	require.NoError(t, err)
	files := map[string]TreeFileInfo{
		"old.txt": {
			Hash: mustFileHash(t, baseTree, "old.txt"),
			Mode: filemode.Regular,
		},
		"keep.txt": {
			Hash: blobHash,
			Mode: filemode.Regular,
		},
	}
	treeHash, err := buildTreeRecursive(gitRepo, files, "")
	require.NoError(t, err)

	commitHash, err := storeCommitObject(t, gitRepo, treeHash, time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC), "base2", baseObj.Hash)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", commitHash)))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))

	base2, err := gitRepo.CommitObject(commitHash)
	require.NoError(t, err)
	baseTree2, err := base2.Tree()
	require.NoError(t, err)

	// When: create new.txt, update keep.txt, delete old.txt
	res, err := createTreeFromChanges(repoWrapper, baseTree2, []FileChange{
		{Operation: FileOperationCreate, Path: "new.txt", Content: []byte("new")},
		{Operation: FileOperationUpdate, Path: "keep.txt", Content: []byte("keep2")},
		{Operation: FileOperationDelete, Path: "old.txt"},
	})
	require.NoError(t, err)

	// Then: tree contains new.txt and updated keep.txt, and old.txt is gone
	newTree, err := gitRepo.TreeObject(res.TreeHash)
	require.NoError(t, err)

	_, err = newTree.File("old.txt")
	require.Error(t, err)

	keepFile, err := newTree.File("keep.txt")
	require.NoError(t, err)
	keepContents, err := keepFile.Contents()
	require.NoError(t, err)
	require.Equal(t, "keep2", keepContents)

	newFile, err := newTree.File("new.txt")
	require.NoError(t, err)
	newContents, err := newFile.Contents()
	require.NoError(t, err)
	require.Equal(t, "new", newContents)
}

func mustFileHash(t *testing.T, tree *object.Tree, name string) plumbing.Hash {
	t.Helper()
	f, err := tree.File(name)
	require.NoError(t, err)
	return f.Hash
}

func storeCommitObject(t *testing.T, gitRepo *gogit.Repository, treeHash plumbing.Hash, when time.Time, msg string, parent plumbing.Hash) (plumbing.Hash, error) {
	t.Helper()
	commit := &object.Commit{
		Author: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  when,
		},
		Committer: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  when,
		},
		Message:      msg,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parent},
	}
	obj := gitRepo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return gitRepo.Storer.SetEncodedObject(obj)
}
