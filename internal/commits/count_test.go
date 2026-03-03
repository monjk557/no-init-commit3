package commits

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createBareRepoWithNCommitsNative creates a bare repo with exactly n commits using native git.
func createBareRepoWithNCommitsNative(t *testing.T, n int) string {
	t.Helper()

	tempWorkDir := t.TempDir()

	// git init
	cmd := exec.Command("git", "init", tempWorkDir)
	require.NoError(t, cmd.Run())

	// Configure git user
	cmd = exec.Command("git", "-C", tempWorkDir, "config", "user.name", "Test")
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "-C", tempWorkDir, "config", "user.email", "test@example.com")
	require.NoError(t, cmd.Run())

	baseTime := time.Unix(1700000000, 0).UTC()
	for i := 0; i < n; i++ {
		fpath := filepath.Join(tempWorkDir, "file.txt")
		require.NoError(t, os.WriteFile(fpath, []byte("content "+string(rune('0'+i%10))+"\n"), 0644))

		cmd = exec.Command("git", "-C", tempWorkDir, "add", "file.txt")
		require.NoError(t, cmd.Run())

		commitTime := baseTime.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		cmd = exec.Command("git", "-C", tempWorkDir, "commit", "-m", "commit "+string(rune('0'+i%10)))
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+commitTime,
			"GIT_COMMITTER_DATE="+commitTime,
		)
		require.NoError(t, cmd.Run())
	}

	// Clone to bare
	bareDir := t.TempDir()
	barePath := filepath.Join(bareDir, "test.git")
	cmd = exec.Command("git", "clone", "--bare", tempWorkDir, barePath)
	require.NoError(t, cmd.Run())

	return barePath
}

func Test_CountCommitsWithCap_NoCap_ReturnsExactCount(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 10)

	result, err := CountCommitsWithCap(context.Background(), barePath, "HEAD", 0)

	require.NoError(t, err)
	assert.Equal(t, 10, result.Count)
	assert.False(t, result.Approximate, "no cap → exact count")
}

func Test_CountCommitsWithCap_CapAboveTotal_ReturnsExactCount(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 5)

	result, err := CountCommitsWithCap(context.Background(), barePath, "HEAD", 100)

	require.NoError(t, err)
	assert.Equal(t, 5, result.Count)
	assert.False(t, result.Approximate, "cap above total → exact count")
}

func Test_CountCommitsWithCap_CapBelowTotal_ReturnsApproximate(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 20)

	result, err := CountCommitsWithCap(context.Background(), barePath, "HEAD", 5)

	require.NoError(t, err)
	assert.Equal(t, 5, result.Count)
	assert.True(t, result.Approximate, "cap below total → approximate")
}

func Test_CountCommitsWithCap_CapEqualsTotal_ReturnsApproximate(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 10)

	result, err := CountCommitsWithCap(context.Background(), barePath, "HEAD", 10)

	require.NoError(t, err)
	assert.Equal(t, 10, result.Count)
	assert.True(t, result.Approximate)
}

func Test_CountCommits_BackwardCompatible_ReturnsIntOnly(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 7)

	count, err := CountCommits(context.Background(), barePath, "HEAD")

	require.NoError(t, err)
	assert.Equal(t, 7, count)
}

func Test_CountCommitsWithCap_CancelledContext_ReturnsError(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 10)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := CountCommitsWithCap(ctx, barePath, "HEAD", 0)

	require.Error(t, err)
}

func Test_CountCommitsWithCap_InvalidRef_ReturnsError(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 3)

	_, err := CountCommitsWithCap(context.Background(), barePath, "nonexistent-branch", 0)

	require.Error(t, err)
}
