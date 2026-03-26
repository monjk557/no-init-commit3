package revert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers — create git repos with native commands
// ---------------------------------------------------------------------------

// initWorkRepo creates a non-bare git repo with an initial commit on main.
// Returns (repoPath pointing to .git dir, workDir).
func initWorkRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()

	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.name", "Test Author")
	run(t, dir, "git", "config", "user.email", "test@example.com")

	writeFile(t, dir, "README.md", "# init")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial commit")

	return filepath.Join(dir, ".git"), dir
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "cmd %s %v failed: %s", name, args, string(out))
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func getHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	return run(t, dir, "git", "rev-parse", "HEAD")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPerformRevert_RegularCommit(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	// Add a file in a second commit
	writeFile(t, workDir, "feature.txt", "feature content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add feature")
	commitToRevert := getHeadSHA(t, workDir)

	ctx := context.Background()
	resp, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  commitToRevert,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/feature",
	})
	require.NoError(t, err)

	assert.True(t, resp.Success)
	assert.Equal(t, "revert/feature", resp.RevertBranch)
	assert.NotNil(t, resp.RevertCommit)
	assert.NotNil(t, resp.RevertedCommit)
	assert.Equal(t, commitToRevert, resp.RevertedCommit.SHA)

	// Verify the revert branch exists and the reverted file is gone
	revertTree := run(t, workDir, "git", "ls-tree", "-r", "--name-only", "refs/heads/revert/feature")
	assert.NotContains(t, revertTree, "feature.txt", "feature.txt should be removed by revert")
	assert.Contains(t, revertTree, "README.md", "README.md should still exist")
}

func TestPerformRevert_MergeCommit(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	// Create feature branch and add a file
	run(t, workDir, "git", "checkout", "-b", "feature")
	writeFile(t, workDir, "feature.txt", "from feature branch")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feature commit")

	// Switch back to main, add another file to make it diverge
	run(t, workDir, "git", "checkout", "main")
	writeFile(t, workDir, "main-only.txt", "main content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "main commit")

	// Merge feature into main
	run(t, workDir, "git", "merge", "feature", "--no-edit")
	mergeCommitSHA := getHeadSHA(t, workDir)

	// Verify merge commit has 2 parents
	parents := run(t, workDir, "git", "cat-file", "-p", mergeCommitSHA)
	assert.Equal(t, 2, strings.Count(parents, "parent "), "merge commit should have 2 parents")

	ctx := context.Background()
	resp, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:    mergeCommitSHA,
		TargetRef:    "refs/heads/main",
		BranchName:   "revert/CR-42",
		ParentNumber: 1, // mainline parent = first parent (main before merge)
	})
	require.NoError(t, err)

	assert.True(t, resp.Success)
	assert.Equal(t, "revert/CR-42", resp.RevertBranch)

	// The revert should undo the feature branch's changes (feature.txt gone)
	// but keep main's changes (main-only.txt)
	revertTree := run(t, workDir, "git", "ls-tree", "-r", "--name-only", "refs/heads/revert/CR-42")
	assert.NotContains(t, revertTree, "feature.txt", "feature.txt should be removed by revert")
	assert.Contains(t, revertTree, "main-only.txt", "main-only.txt should still exist")
	assert.Contains(t, revertTree, "README.md", "README.md should still exist")
}

func TestPerformRevert_DefaultBranchName(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	writeFile(t, workDir, "file.txt", "content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add file")
	commitSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	resp, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA: commitSHA,
		TargetRef: "refs/heads/main",
		// BranchName is empty — should default to "revert/<short-sha>"
	})
	require.NoError(t, err)

	assert.True(t, resp.Success)
	assert.Equal(t, "revert/"+commitSHA[:7], resp.RevertBranch)
}

func TestPerformRevert_InvalidCommitSHA(t *testing.T) {
	t.Parallel()
	gitDir, _ := initWorkRepo(t)

	ctx := context.Background()
	_, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		TargetRef:  "refs/heads/main",
		BranchName: "revert/bad",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read commit")
}

func TestPerformRevert_InvalidParentNumber(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	// Create a merge commit (2 parents) to test invalid parent_number
	run(t, workDir, "git", "checkout", "-b", "feature")
	writeFile(t, workDir, "feature.txt", "feature")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feature commit")

	run(t, workDir, "git", "checkout", "main")
	writeFile(t, workDir, "main.txt", "main")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "main commit")

	run(t, workDir, "git", "merge", "feature", "--no-edit")
	mergeCommitSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	_, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:    mergeCommitSHA,
		TargetRef:    "refs/heads/main",
		BranchName:   "revert/bad-parent",
		ParentNumber: 5, // Only 2 parents, so 5 is invalid
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid parent_number")
}

func TestPerformRevert_CustomMessage(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	writeFile(t, workDir, "file.txt", "content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add file")
	commitSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	customMsg := "Rollback: undo feature deployment"
	resp, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/custom",
		Message:    customMsg,
	})
	require.NoError(t, err)
	assert.Equal(t, customMsg, resp.RevertCommit.Message)
}

func TestPerformRevert_CustomAuthor(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	writeFile(t, workDir, "file.txt", "content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add file")
	commitSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	resp, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/author",
		Author: &AuthorInfo{
			Name:  "Custom Author",
			Email: "custom@example.com",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Custom Author", resp.RevertCommit.Author.Name)
	assert.Equal(t, "custom@example.com", resp.RevertCommit.Author.Email)
}

func TestPerformRevert_DuplicateBranchName(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	writeFile(t, workDir, "file.txt", "content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add file")
	commitSHA := getHeadSHA(t, workDir)

	ctx := context.Background()

	// First revert succeeds
	_, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/dup-test",
	})
	require.NoError(t, err)

	// Second revert with same branch name should fail
	_, err = performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/dup-test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestPerformRevert_SparseCheckout_MultipleFiles(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	// Create many files so sparse-checkout is meaningful
	for i := 0; i < 20; i++ {
		writeFile(t, workDir, fmt.Sprintf("keep/file%d.txt", i), fmt.Sprintf("keep %d", i))
	}
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add 20 keep files")

	// Commit that touches only 2 files — sparse-checkout should kick in
	writeFile(t, workDir, "feature/a.txt", "a")
	writeFile(t, workDir, "feature/b.txt", "b")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add feature a+b")
	commitSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	resp, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/sparse",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// feature/a.txt and feature/b.txt should be gone, 20 keep files intact
	tree := run(t, workDir, "git", "ls-tree", "-r", "--name-only", "refs/heads/revert/sparse")
	assert.NotContains(t, tree, "feature/a.txt")
	assert.NotContains(t, tree, "feature/b.txt")
	assert.Contains(t, tree, "keep/file0.txt")
	assert.Contains(t, tree, "keep/file19.txt")
	assert.Contains(t, tree, "README.md")
}

func TestPerformRevert_RenamedFile(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	// Create a file, then rename it in a new commit
	writeFile(t, workDir, "old_name.txt", "content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add old_name.txt")

	run(t, workDir, "git", "mv", "old_name.txt", "new_name.txt")
	run(t, workDir, "git", "commit", "-m", "rename to new_name.txt")
	renameSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	resp, err := performRevert(ctx, gitDir, &RevertRequest{
		CommitSHA:  renameSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/rename",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	tree := run(t, workDir, "git", "ls-tree", "-r", "--name-only", "refs/heads/revert/rename")
	assert.Contains(t, tree, "old_name.txt", "old name should be restored")
	assert.NotContains(t, tree, "new_name.txt", "new name should be gone")
}

func TestGetAffectedFiles_RegularCommit(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	writeFile(t, workDir, "a.txt", "a")
	writeFile(t, workDir, "b.txt", "b")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add a+b")
	commitSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	files := getAffectedFiles(ctx, gitDir, commitSHA, 0)

	assert.Len(t, files, 2)
	assert.Contains(t, files, "a.txt")
	assert.Contains(t, files, "b.txt")
}

func TestGetAffectedFiles_MergeCommit(t *testing.T) {
	t.Parallel()
	gitDir, workDir := initWorkRepo(t)

	run(t, workDir, "git", "checkout", "-b", "feat")
	writeFile(t, workDir, "feat.txt", "feat")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat")

	run(t, workDir, "git", "checkout", "main")
	writeFile(t, workDir, "main2.txt", "main2")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "main2")

	run(t, workDir, "git", "merge", "feat", "--no-edit")
	mergeSHA := getHeadSHA(t, workDir)

	ctx := context.Background()
	// parent 1 = main before merge → diff shows feature branch changes
	files := getAffectedFiles(ctx, gitDir, mergeSHA, 1)
	assert.Contains(t, files, "feat.txt")
	assert.NotContains(t, files, "main2.txt", "main2 is on mainline parent, not in diff")
}
