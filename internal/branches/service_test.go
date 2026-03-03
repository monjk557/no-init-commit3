package branches

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
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

// newBareRepoWithMainNative creates a bare repo with a single commit on "main" using native git.
func newBareRepoWithMainNative(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo.git")

	// git init --bare
	cmd := exec.Command("git", "init", "--bare", repoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	// Create a blob
	cmd = exec.Command("git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader("hi\n")
	out, err = cmd.Output()
	require.NoError(t, err)
	blobHash := trimOutput(out)

	// Create a tree
	treeInput := "100644 blob " + blobHash + "\tf\n"
	cmd = exec.Command("git", "--git-dir", repoPath, "mktree")
	cmd.Stdin = strings.NewReader(treeInput)
	out, err = cmd.Output()
	require.NoError(t, err)
	treeHash := trimOutput(out)

	// Create a commit
	cmd = exec.Command("git", "--git-dir", repoPath, "commit-tree", treeHash, "-m", "init")
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t.com",
		"GIT_AUTHOR_DATE=2024-01-01T00:00:00+00:00",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t.com",
		"GIT_COMMITTER_DATE=2024-01-01T00:00:00+00:00",
	)
	out, err = cmd.Output()
	require.NoError(t, err)
	commitHash := trimOutput(out)

	// Set refs/heads/main and HEAD
	cmd = exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/heads/main", commitHash)
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main")
	require.NoError(t, cmd.Run())

	return repoPath, commitHash
}

func trimOutput(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func Test_BranchService_GetBranches_EmptyRepo_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "bare.git")
	cmd := exec.Command("git", "init", "--bare", repoPath)
	require.NoError(t, cmd.Run())

	svc := newBranchService(t)

	branches, err := svc.GetBranches(context.Background(), repoPath)

	require.NoError(t, err)
	require.Empty(t, branches)
}

func Test_BranchService_GetBranches_WithBranches_ReturnsAll(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)
	svc := newBranchService(t)

	branches, err := svc.GetBranches(context.Background(), repoPath)

	require.NoError(t, err)
	require.Len(t, branches, 1)
	require.Equal(t, "main", branches[0].Name)
}

func Test_BranchService_CreateBranch_ValidRequest_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)
	svc := newBranchService(t)

	result, err := svc.CreateBranch(context.Background(), repoPath, CreateBranchRequest{
		Name:   "feature",
		Source: "main",
	})

	require.NoError(t, err)
	require.Equal(t, "feature", result.BranchName)
	require.True(t, result.IsNewBranch)

	// Verify via native git
	exists, _, err := NativeCheckBranchExists(context.Background(), repoPath, "feature")
	require.NoError(t, err)
	require.True(t, exists)
}

func Test_BranchService_CreateBranch_DuplicateName_WithoutForce_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)
	svc := newBranchService(t)

	_, err := svc.CreateBranch(context.Background(), repoPath, CreateBranchRequest{Name: "main", Source: "HEAD"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}

func Test_BranchService_CreateBranch_DuplicateName_WithForce_Overwrites(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)
	svc := newBranchService(t)

	result, err := svc.CreateBranch(context.Background(), repoPath, CreateBranchRequest{
		Name:   "main",
		Source: "HEAD",
		Force:  true,
	})

	require.NoError(t, err)
	require.Equal(t, "main", result.BranchName)
}

func Test_BranchService_DeleteBranch_Existing_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)
	svc := newBranchService(t)

	_, err := svc.CreateBranch(context.Background(), repoPath, CreateBranchRequest{Name: "to-delete", Source: "main"})
	require.NoError(t, err)

	result, err := svc.DeleteBranch(context.Background(), repoPath, "to-delete")

	require.NoError(t, err)
	require.Equal(t, "to-delete", result.BranchName)

	// Verify via native git
	exists, _, err := NativeCheckBranchExists(context.Background(), repoPath, "to-delete")
	require.NoError(t, err)
	require.False(t, exists)
}

func Test_BranchService_DeleteBranch_NonExistent_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)
	svc := newBranchService(t)

	_, err := svc.DeleteBranch(context.Background(), repoPath, "ghost")

	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exist")
}

func Test_NativeCheckBranchExists_Existing_ReturnsTrueAndHash(t *testing.T) {
	t.Parallel()

	repoPath, commitHash := newBareRepoWithMainNative(t)

	exists, hash, err := NativeCheckBranchExists(context.Background(), repoPath, "main")

	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, commitHash, hash)
}

func Test_NativeCheckBranchExists_NonExistent_ReturnsFalse(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)

	exists, hash, err := NativeCheckBranchExists(context.Background(), repoPath, "ghost")

	require.NoError(t, err)
	require.False(t, exists)
	require.Empty(t, hash)
}

func Test_NativeResolveRef_HEAD_ReturnsCommitHash(t *testing.T) {
	t.Parallel()

	repoPath, commitHash := newBareRepoWithMainNative(t)

	hash, err := NativeResolveRef(context.Background(), repoPath, "HEAD")

	require.NoError(t, err)
	require.Equal(t, commitHash, hash)
}

func Test_NativeResolveRef_BranchName_ReturnsCommitHash(t *testing.T) {
	t.Parallel()

	repoPath, commitHash := newBareRepoWithMainNative(t)

	hash, err := NativeResolveRef(context.Background(), repoPath, "refs/heads/main")

	require.NoError(t, err)
	require.Equal(t, commitHash, hash)
}

func Test_NativeResolveRef_InvalidRef_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath, _ := newBareRepoWithMainNative(t)

	_, err := NativeResolveRef(context.Background(), repoPath, "nonexistent")

	require.Error(t, err)
}
