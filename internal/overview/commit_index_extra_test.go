package overview

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// MergeStore
// ---------------------------------------------------------------------------

func Test_CommitIndex_MergeStore_NoExisting_Stores(t *testing.T) {
	t.Parallel()
	idx := newTestIndex(t)

	commits := map[string]*GitCommit{"a.go": {SHA: "1"}}
	idx.MergeStore("repo", "ref1", "", commits, LatestCommitsMeta{ScannedCommits: 5})

	got, meta, hit := idx.Lookup("repo", "ref1", "")
	require.True(t, hit)
	assert.Equal(t, "1", got["a.go"].SHA)
	assert.Equal(t, 5, meta.ScannedCommits)
	assert.Equal(t, int64(1), idx.TotalCost())
}

func Test_CommitIndex_MergeStore_ExistingComplete_NewIncomplete_NoDowngrade(t *testing.T) {
	t.Parallel()
	idx := newTestIndex(t)

	// 기존 complete 엔트리.
	idx.Store("repo", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "complete-a"},
	}, LatestCommitsMeta{Incomplete: false, ScannedCommits: 100})

	// incomplete 새 데이터 (새 path b.go 포함).
	idx.MergeStore("repo", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "stale-a"}, // 기존에 이미 있으므로 덮어쓰지 않음
		"b.go": {SHA: "new-b"},   // 누락이므로 추가
	}, LatestCommitsMeta{Incomplete: true, ScannedCommits: 10})

	got, meta, hit := idx.Lookup("repo", "ref1", "")
	require.True(t, hit)
	assert.False(t, meta.Incomplete, "complete 상태가 유지되어야 함")
	assert.Equal(t, "complete-a", got["a.go"].SHA, "기존 완전 엔트리는 유지")
	assert.Equal(t, "new-b", got["b.go"].SHA, "누락 path 는 추가")
}

func Test_CommitIndex_MergeStore_BothIncomplete_MergesAndKeepsMoreScanned(t *testing.T) {
	t.Parallel()
	idx := newTestIndex(t)

	idx.Store("repo", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "a1"},
	}, LatestCommitsMeta{Incomplete: true, ScannedCommits: 10})

	idx.MergeStore("repo", "ref1", "", map[string]*GitCommit{
		"b.go": {SHA: "b1"},
	}, LatestCommitsMeta{Incomplete: true, ScannedCommits: 50})

	got, meta, hit := idx.Lookup("repo", "ref1", "")
	require.True(t, hit)
	assert.True(t, meta.Incomplete)
	assert.Equal(t, 50, meta.ScannedCommits, "더 많이 스캔한 meta 가 유지")
	assert.Equal(t, "a1", got["a.go"].SHA)
	assert.Equal(t, "b1", got["b.go"].SHA)
}

func Test_CommitIndex_MergeStore_ExistingIncomplete_NewComplete_Upgrades(t *testing.T) {
	t.Parallel()
	idx := newTestIndex(t)

	idx.Store("repo", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "a-old"},
	}, LatestCommitsMeta{Incomplete: true, ScannedCommits: 10})

	idx.MergeStore("repo", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "a-new"},
		"b.go": {SHA: "b-new"},
	}, LatestCommitsMeta{Incomplete: false, ScannedCommits: 200})

	got, meta, hit := idx.Lookup("repo", "ref1", "")
	require.True(t, hit)
	assert.False(t, meta.Incomplete, "complete 로 업그레이드")
	assert.Equal(t, "a-new", got["a.go"].SHA, "complete 데이터로 덮어씀")
	assert.Equal(t, "b-new", got["b.go"].SHA)
}

// ---------------------------------------------------------------------------
// ForceScheduleBackgroundPopulation
// ---------------------------------------------------------------------------

func Test_CommitIndex_ForceSchedule_ColdKey_StillPopulates(t *testing.T) {
	t.Parallel()

	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	refHash, err := gitRepo.ResolveRevision("HEAD")
	require.NoError(t, err)

	idx := NewCommitIndex(100, service, nil)

	repoPath := tempDir + "/test.git"
	pathInfos := []CommitPathInfo{{Path: "README.md", IsTree: false}}

	// hotness gate 없이 즉시 스케줄 (cold key 라도).
	idx.ForceScheduleBackgroundPopulation(repoPath, refHash.String(), "", gitRepo, pathInfos)

	// background goroutine 완료 대기.
	require.Eventually(t, func() bool {
		_, _, hit := idx.Lookup(repoPath, refHash.String(), "")
		return hit
	}, 5*time.Second, 50*time.Millisecond, "force populate 가 인덱스를 채워야 함")
}

func Test_CommitIndex_ScheduleBackgroundPopulation_HotKey_Populates(t *testing.T) {
	t.Parallel()

	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	refHash, err := gitRepo.ResolveRevision("HEAD")
	require.NoError(t, err)

	idx := NewCommitIndex(100, service, nil)
	repoPath := tempDir + "/test.git"
	pathInfos := []CommitPathInfo{{Path: "README.md", IsTree: false}}

	key := indexKey(repoPath, refHash.String(), "")
	// hotness threshold (3) 만큼 bump → hot 상태로 만듦.
	idx.bumpHotness(key)
	idx.bumpHotness(key)
	idx.bumpHotness(key)
	require.True(t, idx.isHot(key))

	idx.ScheduleBackgroundPopulation(repoPath, refHash.String(), "", gitRepo, pathInfos)

	require.Eventually(t, func() bool {
		_, _, hit := idx.Lookup(repoPath, refHash.String(), "")
		return hit
	}, 5*time.Second, 50*time.Millisecond, "hot key 는 background populate 되어야 함")
}

func Test_CommitIndex_ForceSchedule_Dedup_SecondCallSkips(t *testing.T) {
	t.Parallel()

	service, tempDir, cleanup := setupTestService(t)
	t.Cleanup(cleanup)

	gitRepo := createTestRepositoryWithCommits(t, tempDir)
	refHash, err := gitRepo.ResolveRevision("HEAD")
	require.NoError(t, err)

	idx := NewCommitIndex(100, service, nil)
	repoPath := tempDir + "/test.git"
	pathInfos := []CommitPathInfo{{Path: "README.md", IsTree: false}}

	idx.ForceScheduleBackgroundPopulation(repoPath, refHash.String(), "", gitRepo, pathInfos)
	// 두 번째 호출은 dedup/repo-lock 으로 인해 panic 없이 처리되어야 함.
	idx.ForceScheduleBackgroundPopulation(repoPath, refHash.String(), "", gitRepo, pathInfos)

	require.Eventually(t, func() bool {
		_, _, hit := idx.Lookup(repoPath, refHash.String(), "")
		return hit
	}, 5*time.Second, 50*time.Millisecond)
}
