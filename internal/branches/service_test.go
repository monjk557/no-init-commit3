package branches

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func newBranchService(t *testing.T) *BranchService {
	t.Helper()

	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "info",
		Format:      "text",
		Output:      "stdout",
		Development: true,
	})
	require.NoError(t, err)
	return NewBranchService(logger)
}

func newBareRepoWithMain(t *testing.T) (*gogit.Repository, plumbing.Hash) {
	t.Helper()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// blob
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(3)
	w, err := blobObj.Writer()
	require.NoError(t, err)
	_, err = w.Write([]byte("hi\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	blobHash, err := repo.Storer.SetEncodedObject(blobObj)
	require.NoError(t, err)

	// tree
	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "f", Mode: filemode.Regular, Hash: blobHash},
	}}
	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	when := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	commit := &object.Commit{
		Author:       object.Signature{Name: "t", Email: "t@t.com", When: when},
		Committer:    object.Signature{Name: "t", Email: "t@t.com", When: when},
		Message:      "init",
		TreeHash:     treeHash,
		ParentHashes: nil,
	}
	commitObj := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", commitHash)))
	require.NoError(t, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))

	return repo, commitHash
}

func Test_BranchService_GetBranches_EmptyRepo_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "bare.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	svc := newBranchService(t)

	branches, err := svc.GetBranches(repo)

	require.NoError(t, err)
	require.Empty(t, branches)
}

func Test_BranchService_GetBranches_WithBranches_ReturnsAll(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	branches, err := svc.GetBranches(repo)

	require.NoError(t, err)
	require.Len(t, branches, 1)
	require.Equal(t, "main", branches[0].Name)
}

func Test_BranchService_CreateBranch_ValidRequest_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	result, err := svc.CreateBranch(context.Background(), repo, CreateBranchRequest{
		Name:   "feature",
		Source: "main",
	})

	require.NoError(t, err)
	require.Equal(t, "feature", result.BranchName)
	require.True(t, result.IsNewBranch)

	ref, err := repo.Reference("refs/heads/feature", false)
	require.NoError(t, err)
	require.NotEqual(t, plumbing.ZeroHash, ref.Hash())
}

func Test_BranchService_CreateBranch_DuplicateName_WithoutForce_ReturnsError(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	_, err := svc.CreateBranch(context.Background(), repo, CreateBranchRequest{Name: "main", Source: "HEAD"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}

func Test_BranchService_CreateBranch_DuplicateName_WithForce_Overwrites(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	result, err := svc.CreateBranch(context.Background(), repo, CreateBranchRequest{
		Name:   "main",
		Source: "HEAD",
		Force:  true,
	})

	require.NoError(t, err)
	require.Equal(t, "main", result.BranchName)
}

func Test_BranchService_DeleteBranch_Existing_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	_, err := svc.CreateBranch(context.Background(), repo, CreateBranchRequest{Name: "to-delete", Source: "main"})
	require.NoError(t, err)

	result, err := svc.DeleteBranch(context.Background(), repo, "to-delete")

	require.NoError(t, err)
	require.Equal(t, "to-delete", result.BranchName)

	_, err = repo.Reference("refs/heads/to-delete", false)
	require.Error(t, err)
}

func Test_BranchService_DeleteBranch_NonExistent_ReturnsError(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	_, err := svc.DeleteBranch(context.Background(), repo, "ghost")

	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exist")
}

func Test_BranchService_checkBranchExists_Existing_ReturnsTrueAndHash(t *testing.T) {
	t.Parallel()

	repo, mainHash := newBareRepoWithMain(t)
	svc := newBranchService(t)

	exists, hash, err := svc.checkBranchExists(repo, "main")

	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, mainHash, hash)
}

func Test_BranchService_checkBranchExists_NonExistent_ReturnsFalse(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	exists, hash, err := svc.checkBranchExists(repo, "ghost")

	require.NoError(t, err)
	require.False(t, exists)
	require.Equal(t, plumbing.ZeroHash, hash)
}

func Test_BranchService_resolveSourceReference_HEAD_ReturnsCommitHash(t *testing.T) {
	t.Parallel()

	repo, mainHash := newBareRepoWithMain(t)
	svc := newBranchService(t)

	hash, err := svc.resolveSourceReference(repo, "HEAD")

	require.NoError(t, err)
	require.Equal(t, mainHash, hash)
}

func Test_BranchService_resolveSourceReference_BranchName_ReturnsCommitHash(t *testing.T) {
	t.Parallel()

	repo, mainHash := newBareRepoWithMain(t)
	svc := newBranchService(t)

	hash, err := svc.resolveSourceReference(repo, "main")

	require.NoError(t, err)
	require.Equal(t, mainHash, hash)
}

func Test_BranchService_resolveSourceReference_InvalidRef_ReturnsError(t *testing.T) {
	t.Parallel()

	repo, _ := newBareRepoWithMain(t)
	svc := newBranchService(t)

	_, err := svc.resolveSourceReference(repo, "nonexistent")

	require.Error(t, err)
	require.Contains(t, err.Error(), "unable to resolve")
}
