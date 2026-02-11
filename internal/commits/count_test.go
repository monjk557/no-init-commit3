package commits

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createBareRepoWithNCommits creates a bare repo with exactly n commits for testing.
func createBareRepoWithNCommits(t *testing.T, n int) (string, *repositories.RepositoryWrapper) {
	t.Helper()

	tempWorkDir := t.TempDir()
	workRepo, err := git.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	baseTime := time.Unix(1700000000, 0).UTC()
	for i := 0; i < n; i++ {
		fpath := filepath.Join(tempWorkDir, "file.txt")
		require.NoError(t, os.WriteFile(fpath, []byte("content "+string(rune('0'+i%10))+"\n"), 0644))
		_, err := worktree.Add("file.txt")
		require.NoError(t, err)

		_, err = worktree.Commit("commit "+string(rune('0'+i%10)), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test",
				Email: "test@example.com",
				When:  baseTime.Add(time.Duration(i) * time.Second),
			},
		})
		require.NoError(t, err)
	}

	// Clone to bare
	bareDir := t.TempDir()
	barePath := filepath.Join(bareDir, "test.git")
	_, err = git.PlainClone(barePath, true, &git.CloneOptions{URL: tempWorkDir})
	require.NoError(t, err)

	rw, err := repositories.OpenRepositorySimple(barePath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rw.Close() })

	return barePath, rw
}

func Test_CountCommitsWithCap_NoCap_ReturnsExactCount(t *testing.T) {
	t.Parallel()

	_, rw := createBareRepoWithNCommits(t, 10)

	result, err := CountCommitsWithCap(rw, context.Background(), "HEAD", 0)

	require.NoError(t, err)
	assert.Equal(t, 10, result.Count)
	assert.False(t, result.Approximate, "no cap → exact count")
}

func Test_CountCommitsWithCap_CapAboveTotal_ReturnsExactCount(t *testing.T) {
	t.Parallel()

	_, rw := createBareRepoWithNCommits(t, 5)

	result, err := CountCommitsWithCap(rw, context.Background(), "HEAD", 100)

	require.NoError(t, err)
	assert.Equal(t, 5, result.Count)
	assert.False(t, result.Approximate, "cap above total → exact count")
}

func Test_CountCommitsWithCap_CapBelowTotal_ReturnsApproximate(t *testing.T) {
	t.Parallel()

	_, rw := createBareRepoWithNCommits(t, 20)

	result, err := CountCommitsWithCap(rw, context.Background(), "HEAD", 5)

	require.NoError(t, err)
	assert.Equal(t, 5, result.Count)
	assert.True(t, result.Approximate, "cap below total → approximate")
}

func Test_CountCommitsWithCap_CapEqualsTotal_ReturnsApproximate(t *testing.T) {
	t.Parallel()

	_, rw := createBareRepoWithNCommits(t, 10)

	// Cap == total: the loop stops at exactly 10, so it marks approximate (can't know if more exist without looking further)
	result, err := CountCommitsWithCap(rw, context.Background(), "HEAD", 10)

	require.NoError(t, err)
	assert.Equal(t, 10, result.Count)
	assert.True(t, result.Approximate)
}

func Test_CountCommits_BackwardCompatible_ReturnsIntOnly(t *testing.T) {
	t.Parallel()

	_, rw := createBareRepoWithNCommits(t, 7)

	count, err := CountCommits(rw, context.Background(), "HEAD")

	require.NoError(t, err)
	assert.Equal(t, 7, count)
}

func Test_CountCommitsWithCap_CancelledContext_ReturnsError(t *testing.T) {
	t.Parallel()

	_, rw := createBareRepoWithNCommits(t, 10)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := CountCommitsWithCap(rw, ctx, "HEAD", 0)

	require.Error(t, err)
}

func Test_CountCommitsWithCap_InvalidRef_ReturnsError(t *testing.T) {
	t.Parallel()

	_, rw := createBareRepoWithNCommits(t, 3)

	_, err := CountCommitsWithCap(rw, context.Background(), "nonexistent-branch", 0)

	require.Error(t, err)
}
