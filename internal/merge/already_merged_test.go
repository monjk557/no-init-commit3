package merge

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/stretchr/testify/require"
)

// newBareRepoForMerge creates a bare repo with a single commit on "main".
func newBareRepoForMerge(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo.git")

	cmd := exec.Command("git", "init", "--bare", repoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	blobHash := testHashObject(t, repoPath, "init content\n")
	treeHash := testMkTree(t, repoPath, "100644 blob "+blobHash+"\tinit.txt\n")
	commitHash := testCommitTree(t, repoPath, treeHash, nil, "Initial commit")

	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", commitHash)
	run(t, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main")

	return repoPath, commitHash
}

// addCommit creates a new commit on top of parentHash with a unique file.
func addCommit(t *testing.T, repoPath, parentHash, filename, content, message string) string {
	t.Helper()

	// Read parent tree
	parentTree := trimGitOutput(t, "git", "--git-dir", repoPath, "rev-parse", parentHash+"^{tree}")

	// Create new blob
	blobHash := testHashObject(t, repoPath, content)

	// Read existing tree entries and add new one
	treeInput := "100644 blob " + blobHash + "\t" + filename + "\n"

	// Use git read-tree + mktree approach: read parent tree then add entry
	cmd := exec.Command("git", "--git-dir", repoPath, "ls-tree", parentTree)
	out, err := cmd.Output()
	require.NoError(t, err)

	existingEntries := string(out)
	fullTreeInput := existingEntries + treeInput

	newTreeHash := testMkTree(t, repoPath, fullTreeInput)
	return testCommitTree(t, repoPath, newTreeHash, []string{parentHash}, message)
}

func testHashObject(t *testing.T, repoPath, content string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func testMkTree(t *testing.T, repoPath, input string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "mktree")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func testCommitTree(t *testing.T, repoPath, treeHash string, parents []string, message string) string {
	t.Helper()
	args := []string{"--git-dir", repoPath, "commit-tree", treeHash, "-m", message}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_AUTHOR_DATE=2024-01-01T00:00:00+00:00",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_COMMITTER_DATE=2024-01-01T00:00:00+00:00",
	)
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func trimGitOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func newConflictDetectorForTest(t *testing.T, repoPath string) *ConflictDetector {
	t.Helper()
	repoRoot := filepath.Dir(repoPath)
	pathConfig, err := repositories.NewPathConfig(repoRoot)
	require.NoError(t, err)
	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "text",
	})
	require.NoError(t, err)
	return NewConflictDetector(pathConfig, logger)
}

// Test_AlreadyMerged_SourceIsAncestorOfTarget tests that when source is already
// merged into target (source is an ancestor of target), the analysis returns
// IsAlreadyMerged=true and CanMerge=false.
func Test_AlreadyMerged_SourceIsAncestorOfTarget(t *testing.T) {
	t.Parallel()

	// Setup: A -> B -> C (linear history)
	// source=A, target=C => A is ancestor of C => already merged
	repoPath, commitA := newBareRepoForMerge(t)
	commitB := addCommit(t, repoPath, commitA, "b.txt", "b content", "Commit B")
	commitC := addCommit(t, repoPath, commitB, "c.txt", "c content", "Commit C")

	run(t, "git", "--git-dir", repoPath, "update-ref", "refs/heads/main", commitC)

	cd := newConflictDetectorForTest(t, repoPath)

	analysis, err := cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:  repoPath,
		SourceRef: commitA,
		TargetRef: commitC,
		Strategy:  MergeStrategyRecursive,
	})

	require.NoError(t, err)
	require.NotNil(t, analysis)
	require.True(t, analysis.IsAlreadyMerged, "source is ancestor of target => already merged")
	require.False(t, analysis.CanMerge, "already merged branch should not be mergeable")
	require.False(t, analysis.HasConflicts, "already merged should have no conflicts")
	require.False(t, analysis.IsFastForward, "already merged is not fast-forward")
	require.Equal(t, 0, analysis.ConflictCount)
}

// Test_AlreadyMerged_SameCommit tests that when source and target point to the
// same commit, fast-forward is detected (not already merged).
func Test_AlreadyMerged_SameCommit(t *testing.T) {
	t.Parallel()

	repoPath, commitA := newBareRepoForMerge(t)

	cd := newConflictDetectorForTest(t, repoPath)

	analysis, err := cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:  repoPath,
		SourceRef: commitA,
		TargetRef: commitA,
		Strategy:  MergeStrategyRecursive,
	})

	require.NoError(t, err)
	require.NotNil(t, analysis)
	// Same commit: target is ancestor of source => fast-forward
	require.True(t, analysis.IsFastForward, "same commit should be fast-forward")
	require.False(t, analysis.IsAlreadyMerged, "same commit should not be already merged")
}

// Test_AlreadyMerged_NormalMerge_NotFlagged tests that for diverged branches,
// neither source nor target is an ancestor of the other.
func Test_AlreadyMerged_NormalMerge_NotFlagged(t *testing.T) {
	t.Parallel()

	// Setup: A -> B (main), A -> C (feature) - diverged branches
	repoPath, commitA := newBareRepoForMerge(t)
	commitB := addCommit(t, repoPath, commitA, "b.txt", "b content", "Commit B")
	commitC := addCommit(t, repoPath, commitA, "c.txt", "c content", "Commit C")

	ctx := context.Background()

	// C is not ancestor of B (source not already merged into target)
	isAlreadyMerged, err := gitMergeBaseIsAncestor(ctx, repoPath, commitC, commitB)
	require.NoError(t, err)
	require.False(t, isAlreadyMerged, "diverged: source should not be ancestor of target")

	// B is not ancestor of C (not fast-forward either)
	isFF, err := gitMergeBaseIsAncestor(ctx, repoPath, commitB, commitC)
	require.NoError(t, err)
	require.False(t, isFF, "diverged: target should not be ancestor of source")
}

// Test_AlreadyMerged_FastForward_NotFlagged tests that a true fast-forward
// (target is ancestor of source) is not flagged as already merged.
func Test_AlreadyMerged_FastForward_NotFlagged(t *testing.T) {
	t.Parallel()

	// Setup: A -> B -> C, source=C, target=A => fast-forward
	repoPath, commitA := newBareRepoForMerge(t)
	commitB := addCommit(t, repoPath, commitA, "b.txt", "b content", "Commit B")
	commitC := addCommit(t, repoPath, commitB, "c.txt", "c content", "Commit C")

	cd := newConflictDetectorForTest(t, repoPath)

	analysis, err := cd.AnalyzeMerge(context.Background(), &MergeAnalysisRequest{
		RepoPath:  repoPath,
		SourceRef: commitC,
		TargetRef: commitA,
		Strategy:  MergeStrategyRecursive,
	})

	require.NoError(t, err)
	require.NotNil(t, analysis)
	require.True(t, analysis.IsFastForward, "target ancestor of source => fast-forward")
	require.False(t, analysis.IsAlreadyMerged, "fast-forward should not be already merged")
}
