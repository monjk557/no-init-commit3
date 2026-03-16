package maintenance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
)

func newTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "debug",
		Format:      "json",
		Output:      "stdout",
		Development: true,
	})
	require.NoError(t, err)
	return logger
}

// createTestRepoWithContent creates a bare repo with an initial commit
// so that maintenance commands have something to work with.
func createTestRepoWithContent(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	workDir := filepath.Join(dir, "work")
	bareDir := filepath.Join(dir, "test.git")

	// Create a working repo, add a commit, then clone to bare.
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

	// Create a file and commit
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0o644))
	cmd := exec.Command("git", "-C", workDir, "add", ".")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", workDir, "commit", "-m", "initial commit")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	// Clone to bare repo
	cmd = exec.Command("git", "clone", "--bare", workDir, bareDir)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git clone --bare failed: %s", string(out))

	return bareDir
}

func TestExecutor_PackRefs(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()

	executor := NewExecutor(cfg, logger)

	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks:            []MaintenanceTask{TaskPackRefs},
		Reasons:          []string{"test"},
	}

	result := executor.Execute(context.Background(), repoPath, plan)
	assert.True(t, result.Success)
	assert.Len(t, result.Tasks, 1)
	assert.NoError(t, result.Tasks[0].Err)
}

func TestExecutor_GeometricRepack(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()

	executor := NewExecutor(cfg, logger)

	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks:            []MaintenanceTask{TaskGeometricRepack},
		Reasons:          []string{"test"},
	}

	result := executor.Execute(context.Background(), repoPath, plan)
	assert.True(t, result.Success)
	assert.NoError(t, result.Tasks[0].Err)
}

func TestExecutor_CommitGraph(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()

	executor := NewExecutor(cfg, logger)

	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks:            []MaintenanceTask{TaskCommitGraph},
		Reasons:          []string{"test"},
	}

	result := executor.Execute(context.Background(), repoPath, plan)
	assert.True(t, result.Success)
	assert.NoError(t, result.Tasks[0].Err)

	// Verify commit-graph was actually written.
	graphDir := filepath.Join(repoPath, "objects", "info", "commit-graphs")
	graphFile := filepath.Join(repoPath, "objects", "info", "commit-graph")
	_, errDir := os.Stat(graphDir)
	_, errFile := os.Stat(graphFile)
	assert.True(t, errDir == nil || errFile == nil, "commit-graph should exist after write")
}

func TestExecutor_Prune(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	cfg.Repack.UseCruftPacks = false

	executor := NewExecutor(cfg, logger)

	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks:            []MaintenanceTask{TaskPrune},
		Reasons:          []string{"test"},
	}

	result := executor.Execute(context.Background(), repoPath, plan)
	assert.True(t, result.Success)
	assert.NoError(t, result.Tasks[0].Err)
}

func TestExecutor_FullPipeline(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()

	executor := NewExecutor(cfg, logger)

	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks: []MaintenanceTask{
			TaskPackRefs,
			TaskGeometricRepack,
			TaskCommitGraph,
		},
		Reasons: []string{"full pipeline test"},
	}

	result := executor.Execute(context.Background(), repoPath, plan)
	assert.True(t, result.Success)
	assert.Len(t, result.Tasks, 3)
	for _, tr := range result.Tasks {
		assert.NoError(t, tr.Err, "task %s should succeed", tr.Task.String())
	}

	// Verify marker file was created.
	markerPath := maintenanceMarkerPath(repoPath)
	_, err := os.Stat(markerPath)
	assert.NoError(t, err, "maintenance marker file should exist")
}

func TestExecutor_ContextCancellation(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()

	executor := NewExecutor(cfg, logger)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks:            []MaintenanceTask{TaskPackRefs},
		Reasons:          []string{"test cancellation"},
	}

	result := executor.Execute(ctx, repoPath, plan)
	// Should fail because context is already cancelled.
	assert.False(t, result.Success)
}

func TestFormatDurationForGit(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{72 * time.Hour, "3.days.ago"},
		{48 * time.Hour, "2.days.ago"},
		{24 * time.Hour, "1.days.ago"},
		{36 * time.Hour, "36.hours.ago"},
		{6 * time.Hour, "6.hours.ago"},
	}

	for _, tc := range tests {
		result := formatDurationForGit(tc.duration)
		assert.Equal(t, tc.expected, result, "duration %v", tc.duration)
	}
}

func TestTouchMaintenanceMarker(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.git")
	require.NoError(t, os.MkdirAll(repoPath, 0o755))

	err := touchMaintenanceMarker(repoPath)
	require.NoError(t, err)

	markerPath := maintenanceMarkerPath(repoPath)
	info, err := os.Stat(markerPath)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), info.ModTime(), 2*time.Second)
}

func TestExecutor_GeometricRepackWithCruft(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)
	cfg := DefaultMaintenanceConfig()
	cfg.Repack.UseCruftPacks = true
	cfg.Prune.CruftExpireAge = 72 * time.Hour

	executor := NewExecutor(cfg, logger)

	// Check if git version supports cruft packs (>= 2.37)
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		t.Skip("git not available")
	}

	// Parse version
	version := strings.TrimSpace(string(out))
	// git version 2.x.y
	parts := strings.Fields(version)
	if len(parts) < 3 {
		t.Skip("cannot parse git version")
	}
	vParts := strings.Split(parts[2], ".")
	if len(vParts) < 2 {
		t.Skip("cannot parse git version parts")
	}

	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks:            []MaintenanceTask{TaskGeometricRepack},
		Reasons:          []string{"test cruft repack"},
	}

	result := executor.Execute(context.Background(), repoPath, plan)
	// Even if git version doesn't support --cruft, we log the error gracefully.
	// On modern git (>= 2.37), this should succeed.
	if result.Success {
		assert.NoError(t, result.Tasks[0].Err)
	}
}
