package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForceFullPlan_TasksAndOmitsPrune(t *testing.T) {
	plan := ForceFullPlan()
	require.True(t, plan.NeedsMaintenance)

	got := map[MaintenanceTask]bool{}
	for _, task := range plan.Tasks {
		got[task] = true
	}
	require.True(t, got[TaskPackRefs])
	require.True(t, got[TaskGeometricRepack])
	require.True(t, got[TaskCommitGraph])
	require.True(t, got[TaskMultiPackIndex])
	// Prune must never run right after an ingest.
	require.False(t, got[TaskPrune])
}

func TestMaintainNow_ForcesMaintenanceOnFreshRepo(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	od := NewOnDemand(DefaultMaintenanceConfig(), newTestLogger(t))

	result, err := od.MaintainNow(context.Background(), repoPath)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success, "tasks: %+v", result.Tasks)
	require.NotEmpty(t, result.Tasks)
}

func TestMaintainNow_ConflictsWhenRepoLocked(t *testing.T) {
	repoPath := createTestRepoWithContent(t)
	od := NewOnDemand(DefaultMaintenanceConfig(), newTestLogger(t))

	// Hold the per-repo maintenance lock, simulating the periodic scheduler (or a
	// concurrent on-demand run) working this repo.
	lock, acquired, err := tryLockRepo(repoPath)
	require.NoError(t, err)
	require.True(t, acquired)

	_, err = od.MaintainNow(context.Background(), repoPath)
	require.ErrorIs(t, err, ErrMaintenanceInProgress)

	// Once released, the same call succeeds.
	require.NoError(t, lock.unlock())
	result, err := od.MaintainNow(context.Background(), repoPath)
	require.NoError(t, err)
	require.True(t, result.Success, "tasks: %+v", result.Tasks)
}
