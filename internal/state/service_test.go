package state

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 회귀: "bare repo 에서 native git 기반 repository state 계산이 정확히 동작해야 함"

func gitEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t.com",
		"GIT_AUTHOR_DATE=2024-01-01T00:00:00+00:00",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t.com",
		"GIT_COMMITTER_DATE=2024-01-01T00:00:00+00:00",
	}
}

func trim(b []byte) string { return strings.TrimRight(string(b), "\r\n") }

func hashObject(t *testing.T, repoPath, content string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	require.NoError(t, err)
	return trim(out)
}

func makeTree(t *testing.T, repoPath, spec string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "mktree")
	cmd.Stdin = strings.NewReader(spec)
	out, err := cmd.Output()
	require.NoError(t, err)
	return trim(out)
}

func commitTree(t *testing.T, repoPath, treeHash, msg string, parents ...string) string {
	t.Helper()
	args := []string{"--git-dir", repoPath, "commit-tree", treeHash, "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(cmd.Environ(), gitEnv()...)
	out, err := cmd.Output()
	require.NoError(t, err)
	return trim(out)
}

func testLogger(t *testing.T) *logging.Logger {
	t.Helper()
	l, err := logging.NewLogger(config.LoggingConfig{
		Level:       "error",
		Format:      "text",
		Output:      "stdout",
		Development: true,
	})
	require.NoError(t, err)
	return l
}

// newBareRepo creates a bare repo with one commit on main + a lightweight tag.
func newBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo.git")
	out, err := exec.Command("git", "init", "--bare", repoPath).CombinedOutput()
	require.NoError(t, err, string(out))

	blob := hashObject(t, repoPath, "hi\n")
	tree := makeTree(t, repoPath, "100644 blob "+blob+"\tf.txt\n")
	c1 := commitTree(t, repoPath, tree, "init")
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/heads/main", c1).Run())
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run())
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/tags/v1.0.0", c1).Run())
	return repoPath
}

func TestNewStateService(t *testing.T) {
	t.Run("nil logger returns error", func(t *testing.T) {
		svc, err := NewStateService(nil)
		assert.Error(t, err)
		assert.Nil(t, svc)
	})

	t.Run("valid logger succeeds", func(t *testing.T) {
		svc, err := NewStateService(testLogger(t))
		require.NoError(t, err)
		assert.NotNil(t, svc)
	})
}

func TestGetRepositoryState_Success(t *testing.T) {
	repoPath := newBareRepo(t)
	svc, err := NewStateService(testLogger(t))
	require.NoError(t, err)

	state, err := svc.GetRepositoryState("owner", "repo", repoPath)
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.True(t, state.Success)
	assert.Equal(t, "repo", state.RepositoryName)
	assert.Equal(t, "owner", state.Owner)

	// HEAD info
	assert.Equal(t, "main", state.Head.Name)
	assert.Equal(t, "branch", state.Head.Type)
	assert.True(t, state.Head.IsBranch)
	assert.False(t, state.Head.IsDetached)
	assert.NotEmpty(t, state.Head.CommitSHA)
	assert.Equal(t, "init", state.Head.Commit.Message)
	assert.Equal(t, "t", state.Head.Commit.Author.Name)

	// References: at least one branch + one tag
	assert.GreaterOrEqual(t, state.References.Count.Branches, 1)
	assert.GreaterOrEqual(t, state.References.Count.Tags, 1)
	assert.GreaterOrEqual(t, len(state.References.Branches), 1)
	assert.GreaterOrEqual(t, len(state.References.Tags), 1)

	// One of the branches is HEAD ("main")
	foundHead := false
	for _, b := range state.References.Branches {
		if b.Name == "main" {
			assert.True(t, b.IsHead)
			foundHead = true
		}
	}
	assert.True(t, foundHead, "main branch should be present and marked as HEAD")

	// Repository info
	assert.True(t, state.RepositoryInfo.IsBare)
	assert.Equal(t, repoPath, state.RepositoryInfo.GitDirectory)
	assert.Equal(t, "main", state.RepositoryInfo.DefaultBranch)
	assert.GreaterOrEqual(t, state.RepositoryInfo.CommitCount, 1)
	assert.GreaterOrEqual(t, state.RepositoryInfo.BranchCount, 1)
	assert.GreaterOrEqual(t, state.RepositoryInfo.TagCount, 1)
	assert.False(t, state.RepositoryInfo.HasWorktree)

	// Details
	assert.Equal(t, "GET", state.Details.Method)
	assert.Equal(t, "stats", state.Details.RequestType)
	assert.Equal(t, "/repos/owner/repo/stats", state.Details.RequestPath)
}

func TestGetRepositoryState_MissingRepo(t *testing.T) {
	svc, err := NewStateService(testLogger(t))
	require.NoError(t, err)

	state, err := svc.GetRepositoryState("owner", "repo", filepath.Join(t.TempDir(), "does-not-exist"))
	assert.Error(t, err)
	assert.Nil(t, state)
	assert.Contains(t, err.Error(), "repository does not exist")
}

func TestIsBareRepository(t *testing.T) {
	svc, err := NewStateService(testLogger(t))
	require.NoError(t, err)

	t.Run("bare repo is bare", func(t *testing.T) {
		repoPath := newBareRepo(t)
		assert.True(t, svc.isBareRepository(repoPath))
	})

	t.Run("non-bare repo is not bare", func(t *testing.T) {
		dir := t.TempDir()
		out, err := exec.Command("git", "init", dir).CombinedOutput()
		require.NoError(t, err, string(out))
		assert.False(t, svc.isBareRepository(dir))
	})

	t.Run("empty dir is not bare", func(t *testing.T) {
		assert.False(t, svc.isBareRepository(t.TempDir()))
	})
}

func TestGetRepositoryState_NonBareRepo(t *testing.T) {
	// 비-bare 저장소 경로(.git 분기)를 거치도록 한다.
	dir := t.TempDir()
	out, err := exec.Command("git", "init", dir).CombinedOutput()
	require.NoError(t, err, string(out))

	// Configure identity + create a commit in the working tree.
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), gitEnv()...)
		o, e := cmd.CombinedOutput()
		require.NoError(t, e, string(o))
	}
	run("config", "user.name", "t")
	run("config", "user.email", "t@t.com")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "main").Run())
	require.NoError(t, exec.Command("sh", "-c", "echo hi > "+filepath.Join(dir, "f.txt")).Run())
	run("add", "f.txt")
	run("commit", "-m", "init")

	svc, err := NewStateService(testLogger(t))
	require.NoError(t, err)

	state, err := svc.GetRepositoryState("owner", "repo", dir)
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.False(t, state.RepositoryInfo.IsBare)
	assert.Equal(t, filepath.Join(dir, ".git"), state.RepositoryInfo.GitDirectory)
	assert.Equal(t, "main", state.Head.Name)
	assert.GreaterOrEqual(t, state.RepositoryInfo.CommitCount, 1)
}
