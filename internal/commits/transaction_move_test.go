package commits

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func createBaseCommitWithFile(t *testing.T, repo *gogit.Repository, filePath string, content []byte) (plumbing.Hash, *object.Tree) {
	t.Helper()

	c := newObjectCollector()

	blobHash, err := createBlob(content, c)
	require.NoError(t, err)

	files := map[string]TreeFileInfo{
		filePath: {
			Hash: blobHash,
			Mode: filemode.Regular,
		},
	}
	treeHash, err := buildTreeRecursive(files, "", c)
	require.NoError(t, err)

	commit := &object.Commit{
		Author: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Message:  "init",
		TreeHash: treeHash,
	}
	commitHash, err := c.encodeCommit(commit)
	require.NoError(t, err)
	require.NoError(t, c.flush(repo.Storer))

	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", commitHash)))
	require.NoError(t, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))

	commitRead, err := repo.CommitObject(commitHash)
	require.NoError(t, err)
	baseTree, err := commitRead.Tree()
	require.NoError(t, err)

	return commitHash, baseTree
}

func TestCreateTreeFromChanges_Move_Success(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "o", "r.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))

	_, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	require.NoError(t, err)
	repo := repoWrapper.GetRepo()

	_, baseTree := createBaseCommitWithFile(t, repo, "old.txt", []byte("hello"))

	res, c, err := createTreeFromChanges(repoWrapper, baseTree, []FileChange{
		{Operation: FileOperationMove, OriginalPath: "old.txt", Path: "new.txt"},
	})
	require.NoError(t, err)
	require.NoError(t, c.flush(repo.Storer))

	newTree, err := repo.TreeObject(res.TreeHash)
	require.NoError(t, err)

	// old 없어야 함, new 있어야 함
	_, err = newTree.File("old.txt")
	require.Error(t, err)

	f, err := newTree.File("new.txt")
	require.NoError(t, err)
	txt, err := f.Contents()
	require.NoError(t, err)
	require.Equal(t, "hello", txt)
}

func TestCreateTreeFromChanges_Move_WithContentChange(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "o", "r.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))

	_, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	repoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	require.NoError(t, err)
	repo := repoWrapper.GetRepo()

	_, baseTree := createBaseCommitWithFile(t, repo, "old.txt", []byte("hello"))

	res, c, err := createTreeFromChanges(repoWrapper, baseTree, []FileChange{
		{Operation: FileOperationMove, OriginalPath: "old.txt", Path: "new.txt", Content: []byte("world")},
	})
	require.NoError(t, err)
	require.NoError(t, c.flush(repo.Storer))

	newTree, err := repo.TreeObject(res.TreeHash)
	require.NoError(t, err)

	f, err := newTree.File("new.txt")
	require.NoError(t, err)
	txt, err := f.Contents()
	require.NoError(t, err)
	require.Equal(t, "world", txt)
}
