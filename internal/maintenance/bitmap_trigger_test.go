package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestPlan_BitmapStaleTriggersRepack: an actively-pushed repo idles just below
// the pack/loose thresholds but keeps accumulating packs newer than its bitmap.
// The !HasBitmap trigger never fires (a bitmap exists), yet the bitmap no longer
// covers the new packs so every clone is expensive. The staleness trigger must
// schedule a repack to rewrite a full-coverage bitmap. See Issue-5273.
func TestPlan_BitmapStaleTriggersRepack(t *testing.T) {
	a := NewAnalyzer(DefaultMaintenanceConfig())

	bitmapAt := time.Now().Add(-2 * time.Hour)
	state := &RepoState{
		PackFileCount:         17,  // < PackFilesThreshold(30)
		EstimatedLooseObjects: 768, // < LooseObjectsThreshold(1024)
		HasRefs:               true,
		HasCommitGraph:        true,
		HasBitmap:             true,                           // bitmap exists, but...
		BitmapModTime:         bitmapAt,                       // ...it is older than
		NewestPackModTime:     bitmapAt.Add(30 * time.Minute), // newer packs
	}
	plan := a.Plan(state)

	assert.True(t, plan.NeedsMaintenance, "stale bitmap should need maintenance")
	assert.True(t, hasTask(plan.Tasks, TaskGeometricRepack), "should repack to refresh bitmap")
	assert.Contains(t, plan.Reasons, "reachability bitmap stale (packs newer than bitmap)")
}

// TestPlan_BitmapFresh_NoStaleTrigger: when the bitmap is newer than every pack
// (the state right after an atomic repack), the staleness trigger must stay off.
func TestPlan_BitmapFresh_NoStaleTrigger(t *testing.T) {
	a := NewAnalyzer(DefaultMaintenanceConfig())

	packAt := time.Now().Add(-1 * time.Hour)
	state := &RepoState{
		PackFileCount:     3,
		HasRefs:           true,
		HasCommitGraph:    true,
		HasBitmap:         true,
		NewestPackModTime: packAt,
		BitmapModTime:     packAt.Add(time.Second), // bitmap written last
	}
	plan := a.Plan(state)
	assert.False(t, hasTask(plan.Tasks, TaskGeometricRepack),
		"bitmap newer than packs must not trigger repack")
}

// TestPlan_BitmapStaleWithinMargin_NoTrigger: a pack newer than the bitmap by
// less than bitmapStaleMargin is the atomic repack write race, not real
// staleness, and must not self-trigger a repack.
func TestPlan_BitmapStaleWithinMargin_NoTrigger(t *testing.T) {
	a := NewAnalyzer(DefaultMaintenanceConfig())

	bitmapAt := time.Now().Add(-1 * time.Hour)
	state := &RepoState{
		PackFileCount:     2,
		HasRefs:           true,
		HasCommitGraph:    true,
		HasBitmap:         true,
		BitmapModTime:     bitmapAt,
		NewestPackModTime: bitmapAt.Add(bitmapStaleMargin - time.Second),
	}
	plan := a.Plan(state)
	assert.False(t, hasTask(plan.Tasks, TaskGeometricRepack),
		"sub-margin skew must not trigger repack (atomic write race)")
}

// TestPlan_SingleStalePack_NoTrigger: a single pack is the ideal bitmapped
// state; even if its mtime edges past the bitmap it must not repack (>1 pack
// gate), avoiding churn on healthy single-pack repos.
func TestPlan_SingleStalePack_NoTrigger(t *testing.T) {
	a := NewAnalyzer(DefaultMaintenanceConfig())

	bitmapAt := time.Now().Add(-1 * time.Hour)
	state := &RepoState{
		PackFileCount:     1,
		HasRefs:           true,
		HasCommitGraph:    true,
		HasBitmap:         true,
		BitmapModTime:     bitmapAt,
		NewestPackModTime: bitmapAt.Add(2 * time.Hour),
	}
	plan := a.Plan(state)
	assert.False(t, hasTask(plan.Tasks, TaskGeometricRepack),
		"single pack must not trigger the staleness repack")
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

// TestAnalyzeRepository_CapturesPackAndBitmapMTime verifies the scan records the
// newest pack and bitmap modification times so the staleness trigger can compare
// them. A pack written after the bitmap must make the analyzed state stale.
func TestAnalyzeRepository_CapturesPackAndBitmapMTime(t *testing.T) {
	packDir := filepath.Join(t.TempDir(), "objects", "pack")
	require.NoError(t, os.MkdirAll(packDir, 0o755))
	repoPath := filepath.Dir(filepath.Dir(packDir))

	bitmapPath := filepath.Join(packDir, "multi-pack-index-abc.bitmap")
	packPath := filepath.Join(packDir, "pack-newer.pack")
	require.NoError(t, os.WriteFile(bitmapPath, []byte("b"), 0o644))
	require.NoError(t, os.WriteFile(packPath, []byte("p"), 0o644))

	base := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(bitmapPath, base, base))
	require.NoError(t, os.Chtimes(packPath, base, base.Add(30*time.Minute)))

	a := NewAnalyzer(DefaultMaintenanceConfig())
	st, err := a.AnalyzeRepository(context.Background(), repoPath)
	require.NoError(t, err)

	assert.True(t, st.HasBitmap)
	assert.Equal(t, 1, st.PackFileCount)
	assert.True(t, st.NewestPackModTime.After(st.BitmapModTime),
		"pack written after bitmap must be recorded as newer")
}
