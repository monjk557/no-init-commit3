package context

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 회귀: "bare repo 에서 native git 컨텍스트 추출이 정확히 동작해야 함"

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

func trim(b []byte) string {
	return strings.TrimRight(string(b), "\r\n")
}

// hashObject writes a blob and returns its hash.
func hashObject(t *testing.T, repoPath, content string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	require.NoError(t, err)
	return trim(out)
}

// makeTree builds a tree from "mode type hash\tname" lines.
func makeTree(t *testing.T, repoPath, treeSpec string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "mktree")
	cmd.Stdin = strings.NewReader(treeSpec)
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

// newRepoWithWorkflow creates a bare repo with two commits; the second adds a
// workflow file under .collabops/workflows. Returns (repoPath, oldRev, newRev).
func newRepoWithWorkflow(t *testing.T) (repoPath, oldRev, newRev string) {
	t.Helper()
	dir := t.TempDir()
	repoPath = filepath.Join(dir, "repo.git")
	out, err := exec.Command("git", "init", "--bare", repoPath).CombinedOutput()
	require.NoError(t, err, string(out))

	// First commit: a single file.
	blob1 := hashObject(t, repoPath, "hello\n")
	tree1 := makeTree(t, repoPath, "100644 blob "+blob1+"\treadme.txt\n")
	oldRev = commitTree(t, repoPath, tree1, "init")

	// Second commit: add a workflow file + modify readme.
	wfBlob := hashObject(t, repoPath, "name: ci\n")
	blob2 := hashObject(t, repoPath, "hello world\n")
	workflowsTree := makeTree(t, repoPath, "100644 blob "+wfBlob+"\tci.yaml\n")
	collabopsTree := makeTree(t, repoPath, "040000 tree "+workflowsTree+"\tworkflows\n")
	rootTree := makeTree(t, repoPath,
		"040000 tree "+collabopsTree+"\t.collabops\n"+
			"100644 blob "+blob2+"\treadme.txt\n")
	newRev = commitTree(t, repoPath, rootTree, "add workflow", oldRev)

	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/heads/main", newRev).Run())
	return repoPath, oldRev, newRev
}

func TestIsNullSHA(t *testing.T) {
	assert.True(t, isNullSHA(nullSHA))
	assert.False(t, isNullSHA("abc123"))
}

func TestNativeValidateSHA(t *testing.T) {
	repoPath, oldRev, _ := newRepoWithWorkflow(t)
	assert.NoError(t, nativeValidateSHA(context.Background(), repoPath, oldRev))
	assert.Error(t, nativeValidateSHA(context.Background(), repoPath, "deadbeef"+strings.Repeat("0", 32)))
}

func TestNativeDiffFiles(t *testing.T) {
	repoPath, oldRev, newRev := newRepoWithWorkflow(t)

	t.Run("between two revs", func(t *testing.T) {
		files, err := nativeDiffFiles(context.Background(), repoPath, oldRev, newRev)
		require.NoError(t, err)
		assert.Contains(t, files, "readme.txt")
		assert.Contains(t, files, ".collabops/workflows/ci.yaml")
	})

	t.Run("null oldRev lists all files in newRev", func(t *testing.T) {
		files, err := nativeDiffFiles(context.Background(), repoPath, nullSHA, newRev)
		require.NoError(t, err)
		assert.Contains(t, files, "readme.txt")
	})

	t.Run("invalid rev errors", func(t *testing.T) {
		_, err := nativeDiffFiles(context.Background(), repoPath, oldRev, "nonexistent")
		assert.Error(t, err)
	})
}

func TestNativeListTreeFilesAndShow(t *testing.T) {
	repoPath, _, newRev := newRepoWithWorkflow(t)

	files, err := nativeListTreeFiles(context.Background(), repoPath, newRev, ".collabops/workflows/")
	require.NoError(t, err)
	assert.Contains(t, files, ".collabops/workflows/ci.yaml")

	content, err := nativeShowFile(context.Background(), repoPath, newRev, ".collabops/workflows/ci.yaml")
	require.NoError(t, err)
	assert.Equal(t, "name: ci\n", content)

	t.Run("missing path returns empty", func(t *testing.T) {
		files, err := nativeListTreeFiles(context.Background(), repoPath, newRev, "no/such/path/")
		require.NoError(t, err)
		assert.Empty(t, files)
	})

	t.Run("missing file errors", func(t *testing.T) {
		_, err := nativeShowFile(context.Background(), repoPath, newRev, "no-such-file.txt")
		assert.Error(t, err)
	})
}
