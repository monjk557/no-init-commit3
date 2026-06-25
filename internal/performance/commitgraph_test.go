package performance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/testutil/gitrepo"
)

func TestCommitGraphOptimizer_DisabledNoop(t *testing.T) {
	cfg := &PerformanceConfig{EnableCommitGraphOptimization: false}
	cg := NewCommitGraphOptimizer(cfg, testLogger())

	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")

	// Disabled -> returns nil without doing anything.
	require.NoError(t, cg.EnsureCommitGraph(r.Dir))

	// OptimizeLogTraversal returns disabled flag.
	opts, optimized := cg.OptimizeLogTraversal(r.Repo, plumbing.ZeroHash)
	assert.NotNil(t, opts)
	assert.False(t, optimized)
}

func TestCommitGraphOptimizer_HasCommitGraph(t *testing.T) {
	cfg := &PerformanceConfig{EnableCommitGraphOptimization: true}
	cg := NewCommitGraphOptimizer(cfg, testLogger())

	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")

	// No commit-graph file yet.
	assert.False(t, cg.HasCommitGraph(r.Dir))

	// Manually create the commit-graph file to exercise the positive path.
	infoDir := filepath.Join(r.Dir, ".git", "objects", "info")
	require.NoError(t, os.MkdirAll(infoDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(infoDir, "commit-graph"), []byte("data"), 0644))

	assert.True(t, cg.HasCommitGraph(r.Dir))

	// EnsureCommitGraph short-circuits when the file already exists.
	require.NoError(t, cg.EnsureCommitGraph(r.Dir))
}

func TestCommitGraphOptimizer_EnsureGeneratesWhenGitAvailable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	cfg := &PerformanceConfig{EnableCommitGraphOptimization: true}
	cg := NewCommitGraphOptimizer(cfg, testLogger())

	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")

	err := cg.EnsureCommitGraph(r.Dir)
	require.NoError(t, err)
	assert.True(t, cg.HasCommitGraph(r.Dir))
}

func TestCommitGraphOptimizer_EnsureFailsOnBadRepo(t *testing.T) {
	cfg := &PerformanceConfig{EnableCommitGraphOptimization: true}
	cg := NewCommitGraphOptimizer(cfg, testLogger())

	// Non-git directory -> git command fails.
	dir := t.TempDir()
	err := cg.EnsureCommitGraph(dir)
	// Either git is missing (skip-able) or the command errors; both acceptable
	// but if git exists we expect an error.
	if _, lookErr := exec.LookPath("git"); lookErr == nil {
		assert.Error(t, err)
	}
}

func TestCommitGraphOptimizer_OptimizeLogTraversalEnabled(t *testing.T) {
	cfg := &PerformanceConfig{EnableCommitGraphOptimization: true}
	cg := NewCommitGraphOptimizer(cfg, testLogger())

	r := gitrepo.NewRepo(t)
	hash := r.Commit("f.txt", "x", "init")

	opts, optimized := cg.OptimizeLogTraversal(r.Repo, hash)
	require.NotNil(t, opts)
	assert.True(t, optimized)
	assert.Equal(t, hash, opts.From)
}

func TestCommitGraphOptimizer_GetOptimizationStats(t *testing.T) {
	cfg := &PerformanceConfig{EnableCommitGraphOptimization: true}
	cg := NewCommitGraphOptimizer(cfg, testLogger())

	r := gitrepo.NewRepo(t)
	r.Commit("f.txt", "x", "init")

	// Without commit-graph file.
	stats := cg.GetOptimizationStats(r.Dir)
	assert.Equal(t, true, stats["commit_graph_enabled"])
	assert.Equal(t, false, stats["commit_graph_exists"])
	assert.Equal(t, false, stats["optimization_active"])
	assert.NotContains(t, stats, "commit_graph_size")

	// With commit-graph file present -> size/modified included.
	infoDir := filepath.Join(r.Dir, ".git", "objects", "info")
	require.NoError(t, os.MkdirAll(infoDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(infoDir, "commit-graph"), []byte("graphdata"), 0644))

	stats2 := cg.GetOptimizationStats(r.Dir)
	assert.Equal(t, true, stats2["commit_graph_exists"])
	assert.Equal(t, true, stats2["optimization_active"])
	assert.Contains(t, stats2, "commit_graph_size")
	assert.Contains(t, stats2, "commit_graph_modified")
}
