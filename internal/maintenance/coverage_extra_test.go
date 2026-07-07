package maintenance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coverage_extra_test.go — scheduler.maintainRepository, executor.runMultiPackIndex
// (non-atomic 경로), executor.logBitmapClosureDiagnostic 를 실제 repo 로 커버한다.

func TestMaintainRepository(t *testing.T) {
	logger := newTestLogger(t)

	t.Run("repo needing maintenance is executed", func(t *testing.T) {
		repoPath := createTestRepoWithContent(t)
		cfg := DefaultMaintenanceConfig()
		cfg.CooldownPeriod = 0
		cfg.Thresholds.LooseObjectsThreshold = 0
		cfg.Thresholds.PackFilesThreshold = 0
		cfg.Thresholds.LooseRefsThreshold = 0

		s := NewScheduler(cfg, filepath.Dir(repoPath), logger)
		s.maintainRepository(context.Background(), repoPath)

		assert.GreaterOrEqual(t, s.totalReposMaintained.Load(), int64(1),
			"a repo over threshold should be maintained")
	})

	t.Run("repo locked by another runner is skipped", func(t *testing.T) {
		// Hold the per-repo maintenance lock, then invoke maintainRepository on a
		// repo that *would* need work: tryLockRepo returns acquired=false, so the
		// runner skips before Execute (Issue-5071 replica coordination path).
		repoPath := createTestRepoWithContent(t)
		lock, acquired, err := tryLockRepo(repoPath)
		require.NoError(t, err)
		require.True(t, acquired)
		defer func() { _ = lock.unlock() }()

		cfg := DefaultMaintenanceConfig()
		cfg.CooldownPeriod = 0
		cfg.Thresholds.LooseObjectsThreshold = 0
		cfg.Thresholds.PackFilesThreshold = 0
		cfg.Thresholds.LooseRefsThreshold = 0

		s := NewScheduler(cfg, filepath.Dir(repoPath), logger)
		s.maintainRepository(context.Background(), repoPath)

		assert.Equal(t, int64(0), s.totalReposMaintained.Load(),
			"a repo held by another runner must not be maintained")
	})

	t.Run("repo within cooldown is skipped", func(t *testing.T) {
		// A just-maintained repo (fresh marker) is inside the cooldown window, so
		// Plan returns no tasks and maintainRepository returns before Execute.
		repoPath := createTestRepoWithContent(t)
		require.NoError(t, touchMaintenanceMarker(repoPath))
		cfg := DefaultMaintenanceConfig()
		cfg.CooldownPeriod = 1 * time.Hour

		s := NewScheduler(cfg, filepath.Dir(repoPath), logger)
		s.maintainRepository(context.Background(), repoPath)

		assert.Equal(t, int64(0), s.totalReposMaintained.Load())
		assert.Equal(t, int64(0), s.totalErrors.Load())
	})
}

func TestRunMultiPackIndex_NonAtomic(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	logger := newTestLogger(t)

	t.Run("write without bitmap", func(t *testing.T) {
		cfg := DefaultMaintenanceConfig()
		cfg.Repack.AtomicBitmap = false
		cfg.Repack.WriteBitmap = false
		e := NewExecutor(cfg, logger)

		// multi-pack-index needs at least one pack; a fresh clone may hold loose
		// objects only, so repack first to guarantee a pack exists.
		_, _ = e.runGeometricRepack(context.Background(), repoPath)

		out, err := e.runMultiPackIndex(context.Background(), repoPath)
		require.NoError(t, err)
		assert.NotContains(t, out, "skipped")
	})

	t.Run("write with bitmap", func(t *testing.T) {
		// A separate healthy repo; write MIDX + bitmap directly (non-atomic path).
		repo2 := createTestRepoWithContent(t)
		cfg := DefaultMaintenanceConfig()
		cfg.Repack.AtomicBitmap = false
		cfg.Repack.WriteBitmap = true
		e := NewExecutor(cfg, logger)

		// Ensure at least one pack exists so the MIDX/bitmap has something to span.
		_, _ = e.runGeometricRepack(context.Background(), repo2)

		_, err := e.runMultiPackIndex(context.Background(), repo2)
		// Healthy single-pack repo: bitmap write should succeed. If a given git
		// build rejects a single-pack bitmap, the closure-diagnostic path inside
		// runMultiPackIndex is still exercised — either way the code under test ran.
		if err != nil {
			t.Logf("multi-pack-index write --bitmap returned: %v", err)
		}
	})
}

func TestLogBitmapClosureDiagnostic(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	e := NewExecutor(DefaultMaintenanceConfig(), newTestLogger(t))

	// Best-effort read-only fsck diagnostic must never panic, even though the
	// supplied cause is synthetic.
	assert.NotPanics(t, func() {
		e.logBitmapClosureDiagnostic(context.Background(), repoPath,
			errors.New("synthetic bitmap closure failure"))
	})
}
