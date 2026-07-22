package maintenance

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

// createTestBareRepo creates a bare git repository with some objects for testing.
func createTestBareRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.git")

	cmd := exec.Command("git", "init", "--bare", repoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init failed: %s", string(out))

	return repoPath
}

func TestAnalyzer_AnalyzeRepository(t *testing.T) {
	repoPath := createTestBareRepo(t)

	analyzer := NewAnalyzer(DefaultMaintenanceConfig())
	state, err := analyzer.AnalyzeRepository(context.Background(), repoPath)

	require.NoError(t, err)
	assert.Equal(t, repoPath, state.RepoPath)
	assert.GreaterOrEqual(t, state.EstimatedLooseObjects, 0)
	assert.GreaterOrEqual(t, state.PackFileCount, 0)
}

func TestAnalyzer_Plan_NothingNeeded(t *testing.T) {
	repoPath := createTestBareRepo(t)

	analyzer := NewAnalyzer(DefaultMaintenanceConfig())
	state, err := analyzer.AnalyzeRepository(context.Background(), repoPath)
	require.NoError(t, err)

	plan := analyzer.Plan(state)
	assert.False(t, plan.NeedsMaintenance)
	assert.Empty(t, plan.Tasks)
}

func TestAnalyzer_Plan_LooseObjectsExceedThreshold(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Thresholds.LooseObjectsThreshold = 10

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:              "/tmp/test.git",
		EstimatedLooseObjects: 50,
		PackFileCount:         1,
	}

	plan := analyzer.Plan(state)
	assert.True(t, plan.NeedsMaintenance)
	assert.Contains(t, plan.Tasks, TaskGeometricRepack)
}

func TestAnalyzer_Plan_TooManyPackFiles(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Thresholds.PackFilesThreshold = 5

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:      "/tmp/test.git",
		PackFileCount: 10,
	}

	plan := analyzer.Plan(state)
	assert.True(t, plan.NeedsMaintenance)
	assert.Contains(t, plan.Tasks, TaskGeometricRepack)
}

func TestAnalyzer_Plan_LooseRefsExceedThreshold(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Thresholds.LooseRefsThreshold = 100

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:      "/tmp/test.git",
		LooseRefCount: 200,
	}

	plan := analyzer.Plan(state)
	assert.True(t, plan.NeedsMaintenance)
	assert.Contains(t, plan.Tasks, TaskPackRefs)
}

func TestAnalyzer_Plan_MissingCommitGraph(t *testing.T) {
	config := DefaultMaintenanceConfig()

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:       "/tmp/test.git",
		HasCommitGraph: false,
		HasRefs:        true, // Only trigger commit-graph for non-empty repos
	}

	plan := analyzer.Plan(state)
	assert.True(t, plan.NeedsMaintenance)
	assert.Contains(t, plan.Tasks, TaskCommitGraph)
}

func TestAnalyzer_Plan_StaleCommitGraph(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Thresholds.CommitGraphStaleness = 1 * time.Hour

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:       "/tmp/test.git",
		HasCommitGraph: true,
		CommitGraphAge: 2 * time.Hour,
		HasRefs:        true,
	}

	plan := analyzer.Plan(state)
	assert.True(t, plan.NeedsMaintenance)
	assert.Contains(t, plan.Tasks, TaskCommitGraph)
}

func TestAnalyzer_Plan_CooldownPeriod(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.CooldownPeriod = 6 * time.Hour
	config.Thresholds.LooseObjectsThreshold = 10

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:              "/tmp/test.git",
		EstimatedLooseObjects: 100,
		LastMaintenanceAt:     time.Now().Add(-1 * time.Hour), // 1 hour ago, within cooldown
	}

	plan := analyzer.Plan(state)
	assert.False(t, plan.NeedsMaintenance, "should be blocked by cooldown period")
}

func TestAnalyzer_Plan_CooldownExpired(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.CooldownPeriod = 6 * time.Hour
	config.Thresholds.LooseObjectsThreshold = 10

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:              "/tmp/test.git",
		EstimatedLooseObjects: 100,
		LastMaintenanceAt:     time.Now().Add(-7 * time.Hour), // 7 hours ago, past cooldown
	}

	plan := analyzer.Plan(state)
	assert.True(t, plan.NeedsMaintenance, "cooldown should have expired")
}

func TestAnalyzer_Plan_PruneOnlyWithoutCruftPacks(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Repack.UseCruftPacks = false
	config.Thresholds.LooseObjectsThreshold = 10

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:              "/tmp/test.git",
		EstimatedLooseObjects: 50,
	}

	plan := analyzer.Plan(state)
	assert.Contains(t, plan.Tasks, TaskPrune)
}

func TestAnalyzer_Plan_NoPruneWithCruftPacks(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Repack.UseCruftPacks = true
	config.Thresholds.LooseObjectsThreshold = 10

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:              "/tmp/test.git",
		EstimatedLooseObjects: 50,
	}

	plan := analyzer.Plan(state)
	assert.NotContains(t, plan.Tasks, TaskPrune, "cruft packs handle pruning internally")
}

func TestAnalyzer_Plan_MultiPackIndex(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Repack.WriteMultiPackIndex = true
	config.Thresholds.PackFilesThreshold = 5

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:      "/tmp/test.git",
		PackFileCount: 10,
	}

	plan := analyzer.Plan(state)
	assert.Contains(t, plan.Tasks, TaskMultiPackIndex)
}

func TestAnalyzer_Plan_TaskOrdering(t *testing.T) {
	config := DefaultMaintenanceConfig()
	config.Thresholds.LooseObjectsThreshold = 10
	config.Thresholds.LooseRefsThreshold = 10
	config.Repack.WriteMultiPackIndex = true
	config.Repack.UseCruftPacks = true

	analyzer := NewAnalyzer(config)

	state := &RepoState{
		RepoPath:              "/tmp/test.git",
		EstimatedLooseObjects: 50,
		LooseRefCount:         20,
		PackFileCount:         15,
		HasCommitGraph:        false,
		HasRefs:               true,
	}

	plan := analyzer.Plan(state)
	require.True(t, plan.NeedsMaintenance)

	// Verify task ordering: pack-refs → repack → commit-graph → midx
	taskOrder := make([]MaintenanceTask, 0, len(plan.Tasks))
	taskOrder = append(taskOrder, plan.Tasks...)

	packRefsIdx := indexOf(taskOrder, TaskPackRefs)
	repackIdx := indexOf(taskOrder, TaskGeometricRepack)
	commitGraphIdx := indexOf(taskOrder, TaskCommitGraph)
	midxIdx := indexOf(taskOrder, TaskMultiPackIndex)

	assert.Greater(t, repackIdx, packRefsIdx, "repack should come after pack-refs")
	assert.Greater(t, commitGraphIdx, repackIdx, "commit-graph should come after repack")
	assert.Greater(t, midxIdx, commitGraphIdx, "midx should come after commit-graph")
}

func TestCountLooseRefs(t *testing.T) {
	dir := t.TempDir()

	// Create some "ref" files
	refsDir := filepath.Join(dir, "refs", "heads")
	require.NoError(t, os.MkdirAll(refsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(refsDir, "main"), []byte("abc123"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(refsDir, "feature"), []byte("def456"), 0o644))

	tagsDir := filepath.Join(dir, "refs", "tags")
	require.NoError(t, os.MkdirAll(tagsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tagsDir, "v1.0"), []byte("ghi789"), 0o644))

	count := countLooseRefs(filepath.Join(dir, "refs"))
	assert.Equal(t, 3, count)
}

func TestMaintenanceMarkerPath(t *testing.T) {
	path := maintenanceMarkerPath("/data/repos/owner/repo.git")
	assert.Equal(t, "/data/repos/owner/repo.git/.maintenance-last-run", path)
}

func TestMaintenanceTaskString(t *testing.T) {
	assert.Equal(t, "pack-refs", TaskPackRefs.String())
	assert.Equal(t, "geometric-repack", TaskGeometricRepack.String())
	assert.Equal(t, "commit-graph", TaskCommitGraph.String())
	assert.Equal(t, "multi-pack-index", TaskMultiPackIndex.String())
	assert.Equal(t, "prune", TaskPrune.String())
}

func TestIsBareRepo(t *testing.T) {
	repoPath := createTestBareRepo(t)
	assert.True(t, isBareRepo(repoPath))
	assert.False(t, isBareRepo(t.TempDir()))
}

func indexOf(tasks []MaintenanceTask, task MaintenanceTask) int {
	for i, t := range tasks {
		if t == task {
			return i
		}
	}
	return -1
}
