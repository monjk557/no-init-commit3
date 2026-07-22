package maintenance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduler_DiscoverRepositories(t *testing.T) {
	dir := t.TempDir()

	// Create repo structure: {root}/{owner}/{repo}.git/
	owners := []string{"alice", "bob"}
	repos := []string{"project1.git", "project2.git"}

	for _, owner := range owners {
		for _, repo := range repos {
			repoPath := filepath.Join(dir, owner, repo)
			cmd := exec.Command("git", "init", "--bare", repoPath)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git init failed: %s", string(out))
		}
	}

	// Also create a non-repo directory that should be skipped.
	nonRepoDir := filepath.Join(dir, "alice", "not-a-repo.git")
	require.NoError(t, os.MkdirAll(nonRepoDir, 0o755))
	// No HEAD file, so it should be skipped.

	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	scheduler := NewScheduler(cfg, dir, logger)

	discovered, err := scheduler.discoverRepositories()
	require.NoError(t, err)

	// Should find 4 repos (2 owners x 2 repos).
	assert.Len(t, discovered, 4)

	// All discovered paths should end with .git.
	for _, path := range discovered {
		assert.True(t, filepath.Ext(path) == ".git" || filepath.Base(path) == ".git",
			"expected .git suffix: %s", path)
	}
}

func TestScheduler_DiscoverRepositories_EmptyRoot(t *testing.T) {
	dir := t.TempDir()

	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	scheduler := NewScheduler(cfg, dir, logger)

	discovered, err := scheduler.discoverRepositories()
	require.NoError(t, err)
	assert.Empty(t, discovered)
}

func TestScheduler_StartStop(t *testing.T) {
	dir := t.TempDir()
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	cfg.ScanInterval = 100 * time.Millisecond
	cfg.InitialDelay = 10 * time.Millisecond

	scheduler := NewScheduler(cfg, dir, logger)
	scheduler.Start()

	// Give it time to run at least one scan.
	time.Sleep(300 * time.Millisecond)

	scheduler.Stop()

	stats := scheduler.Stats()
	assert.GreaterOrEqual(t, stats["total_runs"].(int64), int64(1))
}

// stop 신호가 이미 떨어진 상태의 scan 은 저장소를 하나도 fan-out 하지 않아야 한다.
// 기존 코드는 select 안의 bare break 가 select 만 탈출해 stop 이후에도 전체
// 저장소를 계속 dispatch 했다 (Issue-6360 / staticcheck SA4011).
func TestScheduler_RunScan_StopAbortsDispatch(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.git", "b.git", "c.git"} {
		repoPath := filepath.Join(dir, "owner", name)
		out, err := exec.Command("git", "init", "--bare", repoPath).CombinedOutput()
		require.NoError(t, err, "git init failed: %s", string(out))
	}

	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	scheduler := NewScheduler(cfg, dir, logger)

	// 시작 전에 stop — runScan 진입 시점부터 stopChan 이 닫혀 있다.
	scheduler.Stop()
	scheduler.runScan(cfg.MaxConcurrentJobs)

	stats := scheduler.Stats()
	assert.Equal(t, int64(0), stats["total_repos_dispatched"].(int64),
		"stop 이후에는 저장소를 dispatch 하지 않아야 한다")
}

func TestScheduler_Disabled(t *testing.T) {
	dir := t.TempDir()
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	cfg.Enabled = false

	scheduler := NewScheduler(cfg, dir, logger)
	scheduler.Start()

	time.Sleep(100 * time.Millisecond)
	scheduler.Stop()

	stats := scheduler.Stats()
	assert.Equal(t, int64(0), stats["total_runs"].(int64))
}

func TestScheduler_Stats(t *testing.T) {
	dir := t.TempDir()
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	cfg.Enabled = false

	scheduler := NewScheduler(cfg, dir, logger)
	stats := scheduler.Stats()

	assert.Contains(t, stats, "total_runs")
	assert.Contains(t, stats, "total_repos_maintained")
	assert.Contains(t, stats, "total_errors")
	assert.Contains(t, stats, "last_run_at")
}

func TestScheduler_IntegrationWithRealRepos(t *testing.T) {
	dir := t.TempDir()

	// Create a repo with some content.
	repoPath := filepath.Join(dir, "testowner", "testrepo.git")
	workDir := filepath.Join(dir, "workdir")

	cmds := [][]string{
		{"git", "init", workDir},
		{"git", "-C", workDir, "config", "user.email", "test@test.com"},
		{"git", "-C", workDir, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", args, string(out))
	}

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("content"), 0o644))
	cmd := exec.Command("git", "-C", workDir, "add", ".")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", workDir, "commit", "-m", "test")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "clone", "--bare", workDir, repoPath)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git clone --bare failed: %s", string(out))

	// Use very low thresholds to trigger maintenance.
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	cfg.ScanInterval = 100 * time.Millisecond
	cfg.InitialDelay = 10 * time.Millisecond
	cfg.CooldownPeriod = 0 // No cooldown for test
	cfg.Thresholds.LooseObjectsThreshold = 0
	cfg.Thresholds.PackFilesThreshold = 0
	cfg.Thresholds.LooseRefsThreshold = 0

	scheduler := NewScheduler(cfg, dir, logger)
	scheduler.Start()

	// Wait for at least one scan to complete.
	time.Sleep(1 * time.Second)
	scheduler.Stop()

	stats := scheduler.Stats()
	assert.GreaterOrEqual(t, stats["total_repos_maintained"].(int64), int64(1),
		"should have maintained at least one repo")
}
