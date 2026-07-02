package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsBitmapClosureError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("permission denied"), false},
		{"full closure", errors.New("git multi-pack-index write --bitmap failed: warning: Failed to write bitmap index. Packfile doesn't have full closure (object abc is missing)"), true},
		{"could not write", errors.New("error: could not write multi-pack bitmap: exit status 1"), true},
		{"case-insensitive", errors.New("FAILED TO WRITE BITMAP index"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, isBitmapClosureError(c.err))
		})
	}
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "abc", truncate("abc", 3))
	assert.Equal(t, "ab…(truncated)", truncate("abcdef", 2))
}

func TestDefaultConfig_BitmapFlags(t *testing.T) {
	// Defaults: atomic ON (the separate step is broken), fsck diagnostic on.
	cfg := DefaultMaintenanceConfig()
	assert.True(t, cfg.Repack.AtomicBitmap, "AtomicBitmap should default on")
	assert.True(t, cfg.Repack.BitmapFsckDiagnostic, "BitmapFsckDiagnostic should default on")

	// Env is an escape hatch: it can disable the atomic path.
	t.Setenv("GIT_MAINTENANCE_ATOMIC_BITMAP", "false")
	t.Setenv("GIT_MAINTENANCE_BITMAP_FSCK_DIAGNOSTIC", "false")
	cfg = DefaultMaintenanceConfig()
	assert.False(t, cfg.Repack.AtomicBitmap, "env should disable AtomicBitmap")
	assert.False(t, cfg.Repack.BitmapFsckDiagnostic, "env should disable diagnostic")
}

func TestRunMultiPackIndex_SkippedUnderAtomicBitmap(t *testing.T) {
	cfg := DefaultMaintenanceConfig()
	cfg.Repack.AtomicBitmap = true
	cfg.Repack.WriteBitmap = true

	e := NewExecutor(cfg, newTestLogger(t))
	// No git exec happens on the skip path, so an empty repo path is fine.
	out, err := e.runMultiPackIndex(context.Background(), t.TempDir())
	require.NoError(t, err)
	assert.Contains(t, out, "skipped")
}

func TestAtomicBitmapRepack_WritesMultiPackIndex(t *testing.T) {
	repoPath := createTestRepoWithContent(t)

	cfg := DefaultMaintenanceConfig()
	cfg.Repack.AtomicBitmap = true
	cfg.Repack.WriteBitmap = true
	cfg.Repack.WriteMultiPackIndex = true

	e := NewExecutor(cfg, newTestLogger(t))
	plan := &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks:            []MaintenanceTask{TaskGeometricRepack, TaskMultiPackIndex},
		Reasons:          []string{"test"},
	}

	result := e.Execute(context.Background(), repoPath, plan)
	assert.True(t, result.Success, "atomic-bitmap maintenance should succeed on a healthy repo")

	// repack --write-midx must have produced a multi-pack-index.
	midx := filepath.Join(repoPath, "objects", "pack", "multi-pack-index")
	_, statErr := os.Stat(midx)
	assert.NoError(t, statErr, "multi-pack-index should exist after atomic-bitmap repack")

	// The separate MIDX task must have been skipped (not re-run without bitmap).
	for _, tr := range result.Tasks {
		if tr.Task == TaskMultiPackIndex {
			assert.NoError(t, tr.Err)
			assert.True(t, strings.Contains(tr.Output, "skipped"),
				"MIDX task should be skipped under atomic bitmap, got %q", tr.Output)
		}
	}
}
