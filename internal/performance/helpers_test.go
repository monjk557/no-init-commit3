package performance

import (
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

// testLogger returns a no-op zap logger for tests.
func testLogger() *zap.Logger {
	return zap.NewNop()
}

// treeFromCommit opens the given repository with go-git and returns the
// *object.Tree for the provided commit hash.
func treeFromCommit(t *testing.T, dir string, hash plumbing.Hash) *object.Tree {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	require.NoError(t, err)
	commit, err := repo.CommitObject(hash)
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	return tree
}

// newTreeWithFiles builds a repo with the given files in a single commit and
// returns the commit tree.
func newTreeWithFiles(t *testing.T, files map[string]string) *object.Tree {
	t.Helper()
	r := gitrepo.NewRepo(t)
	hash := r.CommitMultiple(files, "fixture commit")
	return treeFromCommit(t, r.Dir, hash)
}
