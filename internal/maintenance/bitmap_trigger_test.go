package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hasTask(tasks []MaintenanceTask, want MaintenanceTask) bool {
	for _, t := range tasks {
		if t == want {
			return true
		}
	}
	return false
}

// TestPlan_BitmapMissingTriggersRepack: a repo with packs but no bitmap must be
// scheduled for repack even when pack/loose counts are below threshold, so the
// (atomic) bitmap gets (re)written. See Issue-5071.
func TestPlan_BitmapMissingTriggersRepack(t *testing.T) {
	a := NewAnalyzer(DefaultMaintenanceConfig())

	// Below all count thresholds, but has packs and NO bitmap.
	state := &RepoState{
		PackFileCount:         2, // < PackFilesThreshold(30)
		EstimatedLooseObjects: 0,
		HasRefs:               true,
		HasCommitGraph:        true,
		HasBitmap:             false,
	}
	plan := a.Plan(state)

	assert.True(t, plan.NeedsMaintenance, "missing bitmap should need maintenance")
	assert.True(t, hasTask(plan.Tasks, TaskGeometricRepack), "should schedule repack to write bitmap")
	assert.Contains(t, plan.Reasons, "no reachability bitmap")
}

func TestPlan_BitmapPresent_NoRepackWhenBelowThreshold(t *testing.T) {
	a := NewAnalyzer(DefaultMaintenanceConfig())
	state := &RepoState{
		PackFileCount:  2,
		HasRefs:        true,
		HasCommitGraph: true,
		HasBitmap:      true, // already has a bitmap
	}
	plan := a.Plan(state)
	assert.False(t, hasTask(plan.Tasks, TaskGeometricRepack),
		"present bitmap + below thresholds should not trigger repack")
}

func TestPlan_NoPacks_NoBitmapTrigger(t *testing.T) {
	a := NewAnalyzer(DefaultMaintenanceConfig())
	// Empty repo: no packs → nothing to bitmap.
	state := &RepoState{PackFileCount: 0, HasBitmap: false, HasRefs: false, HasCommitGraph: true}
	plan := a.Plan(state)
	assert.False(t, hasTask(plan.Tasks, TaskGeometricRepack),
		"no packs → bitmap trigger must not fire")
}

func TestPlan_WriteBitmapDisabled_NoBitmapTrigger(t *testing.T) {
	cfg := DefaultMaintenanceConfig()
	cfg.Repack.WriteBitmap = false
	a := NewAnalyzer(cfg)
	state := &RepoState{PackFileCount: 3, HasBitmap: false, HasRefs: true, HasCommitGraph: true}
	plan := a.Plan(state)
	assert.False(t, hasTask(plan.Tasks, TaskGeometricRepack),
		"bitmap disabled → missing bitmap must not trigger repack")
}

// TestAnalyzeRepository_DetectsBitmap checks the filesystem probe for *.bitmap.
func TestAnalyzeRepository_DetectsBitmap(t *testing.T) {
	packDir := filepath.Join(t.TempDir(), "objects", "pack")
	require.NoError(t, os.MkdirAll(packDir, 0o755))
	repoPath := filepath.Dir(filepath.Dir(packDir))

	a := NewAnalyzer(DefaultMaintenanceConfig())

	// No bitmap yet.
	st, err := a.AnalyzeRepository(context.Background(), repoPath)
	require.NoError(t, err)
	assert.False(t, st.HasBitmap)

	// Multi-pack-index bitmap present → detected.
	require.NoError(t, os.WriteFile(filepath.Join(packDir, "multi-pack-index-abc.bitmap"), []byte("x"), 0o644))
	st, err = a.AnalyzeRepository(context.Background(), repoPath)
	require.NoError(t, err)
	assert.True(t, st.HasBitmap)
}
