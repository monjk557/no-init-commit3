package overview

import (
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
)

// commit_index_schedule_coverage_test.go — ScheduleBackgroundPopulation 의 방어
// branch(hotness gate / queue cap / per-repo lock / worker 포화)를 커버한다.
// happy path(hot key populate)는 commit_index_extra_test.go 에 이미 존재.

type schedFixture struct {
	idx      *CommitIndex
	repoPath string
	refHash  string
	repo     *gogit.Repository
}

func newSchedFixture(t *testing.T) schedFixture {
	t.Helper()
	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	refHash, err := gitRepo.ResolveRevision("HEAD")
	require.NoError(t, err)

	return schedFixture{
		idx:      NewCommitIndex(100, service, nil),
		repoPath: tempDir + "/test.git",
		refHash:  refHash.String(),
		repo:     gitRepo,
	}
}

func (f schedFixture) markHot(t *testing.T) {
	t.Helper()
	key := indexKey(f.repoPath, f.refHash, "")
	f.idx.bumpHotness(key)
	f.idx.bumpHotness(key)
	f.idx.bumpHotness(key)
	require.True(t, f.idx.isHot(key))
}

func Test_ScheduleBackgroundPopulation_ColdKey_Skips(t *testing.T) {
	t.Parallel()
	f := newSchedFixture(t)
	pathInfos := []CommitPathInfo{{Path: "README.md"}}

	// hotness bump 없음 → isHot(key) == false → 조기 반환, populate 안 됨.
	f.idx.ScheduleBackgroundPopulation(f.repoPath, f.refHash, "", f.repo, pathInfos)

	_, _, hit := f.idx.Lookup(f.repoPath, f.refHash, "")
	require.False(t, hit, "cold key 는 populate 되지 않아야 함")
}

func Test_ScheduleBackgroundPopulation_QueueFull_Drops(t *testing.T) {
	t.Parallel()
	f := newSchedFixture(t)
	pathInfos := []CommitPathInfo{{Path: "README.md"}}
	f.markHot(t)

	// 큐를 가득 채워 queue cap drop branch 를 강제.
	f.idx.queueLen.Store(int32(f.idx.maxQueueLen))

	f.idx.ScheduleBackgroundPopulation(f.repoPath, f.refHash, "", f.repo, pathInfos)

	_, _, hit := f.idx.Lookup(f.repoPath, f.refHash, "")
	require.False(t, hit, "queue 포화 시 drop 되어야 함")
}

func Test_ScheduleBackgroundPopulation_RepoInflight_Skips(t *testing.T) {
	t.Parallel()
	f := newSchedFixture(t)
	pathInfos := []CommitPathInfo{{Path: "README.md"}}
	f.markHot(t)

	// 같은 repo 에 대해 이미 populate 진행 중인 것처럼 표시 → per-repo lock skip.
	f.idx.repoInflight.Store(f.repoPath, struct{}{})

	f.idx.ScheduleBackgroundPopulation(f.repoPath, f.refHash, "", f.repo, pathInfos)

	_, _, hit := f.idx.Lookup(f.repoPath, f.refHash, "")
	require.False(t, hit, "per-repo lock 보유 시 skip")
}

func Test_ScheduleBackgroundPopulation_WorkersBusy_Drops(t *testing.T) {
	t.Parallel()
	f := newSchedFixture(t)
	pathInfos := []CommitPathInfo{{Path: "README.md"}}
	f.markHot(t)

	// 모든 worker slot 을 점유 → tryAcquire 실패 → drop branch.
	for i := 0; i < f.idx.maxWorkers; i++ {
		f.idx.workerSem <- struct{}{}
	}

	f.idx.ScheduleBackgroundPopulation(f.repoPath, f.refHash, "", f.repo, pathInfos)

	_, _, hit := f.idx.Lookup(f.repoPath, f.refHash, "")
	require.False(t, hit, "worker 포화 시 drop")

	// slot 반환.
	for i := 0; i < f.idx.maxWorkers; i++ {
		<-f.idx.workerSem
	}
}
