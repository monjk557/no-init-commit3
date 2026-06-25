package overview

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestIndex(t *testing.T) *CommitIndex {
	t.Helper()
	service, _, cleanup := setupTestService(t)
	t.Cleanup(cleanup)
	return NewCommitIndex(100, service, nil)
}

// ---------------------------------------------------------------------------
// Lookup / Store
// ---------------------------------------------------------------------------

func Test_CommitIndex_Lookup_Miss_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)

	commits, meta, hit := idx.Lookup("repo", "abc123", "")
	assert.False(t, hit)
	assert.Nil(t, commits)
	assert.Empty(t, meta.CutReason)
}

func Test_CommitIndex_StoreAndLookup_Hit_ReturnsStoredData(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)

	storedCommits := map[string]*GitCommit{
		"src/main.go": {SHA: "abc123", Message: "initial"},
	}
	storedMeta := LatestCommitsMeta{
		ResolvedRefHash: "ref123",
		CutReason:       "complete",
		ScannedCommits:  42,
	}

	idx.Store("repo", "ref123", "", storedCommits, storedMeta)

	commits, meta, hit := idx.Lookup("repo", "ref123", "")
	require.True(t, hit)
	require.NotNil(t, commits)
	assert.Equal(t, "abc123", commits["src/main.go"].SHA)
	assert.Equal(t, "complete", meta.CutReason)
	assert.Equal(t, 42, meta.ScannedCommits)
}

func Test_CommitIndex_DifferentRefHash_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)

	storedCommits := map[string]*GitCommit{
		"src/main.go": {SHA: "abc123", Message: "initial"},
	}
	storedMeta := LatestCommitsMeta{CutReason: "complete"}

	idx.Store("repo", "ref123", "", storedCommits, storedMeta)

	// Different refHash → miss
	_, _, hit := idx.Lookup("repo", "ref456", "")
	assert.False(t, hit)
}

func Test_CommitIndex_DifferentDirPath_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)

	storedCommits := map[string]*GitCommit{
		"src/main.go": {SHA: "abc123", Message: "initial"},
	}
	storedMeta := LatestCommitsMeta{CutReason: "complete"}

	idx.Store("repo", "ref123", "src", storedCommits, storedMeta)

	// Different dirPath → miss
	_, _, hit := idx.Lookup("repo", "ref123", "docs")
	assert.False(t, hit)
}

func Test_CommitIndex_Len_ReturnsCorrectCount(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)
	assert.Equal(t, 0, idx.Len())

	idx.Store("repo1", "ref1", "", map[string]*GitCommit{}, LatestCommitsMeta{})
	assert.Equal(t, 1, idx.Len())

	idx.Store("repo2", "ref2", "src", map[string]*GitCommit{}, LatestCommitsMeta{})
	assert.Equal(t, 2, idx.Len())
}

// ---------------------------------------------------------------------------
// Cost tracking
// ---------------------------------------------------------------------------

func Test_CommitIndex_TotalCost_TracksPathCount(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)
	assert.Equal(t, int64(0), idx.TotalCost())

	idx.Store("repo", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "1"},
		"b.go": {SHA: "2"},
		"c.go": {SHA: "3"},
	}, LatestCommitsMeta{})

	assert.Equal(t, int64(3), idx.TotalCost())

	// Overwrite with fewer entries → cost should decrease
	idx.Store("repo", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "1"},
	}, LatestCommitsMeta{})

	assert.Equal(t, int64(1), idx.TotalCost())
}

func Test_CommitIndex_TotalCost_MultipleEntries(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)

	idx.Store("repo1", "ref1", "", map[string]*GitCommit{
		"a.go": {SHA: "1"},
		"b.go": {SHA: "2"},
	}, LatestCommitsMeta{})

	idx.Store("repo2", "ref2", "src", map[string]*GitCommit{
		"c.go": {SHA: "3"},
	}, LatestCommitsMeta{})

	assert.Equal(t, int64(3), idx.TotalCost())
}

// ---------------------------------------------------------------------------
// Hotness + Background populate
// ---------------------------------------------------------------------------

func Test_CommitIndex_ScheduleBackgroundPopulation_NotHot_DoesNotPopulate(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)

	// Without any lookups (not hot), schedule should be a no-op.
	idx.ScheduleBackgroundPopulation("repo", "ref123", "", nil, nil)

	// Give a moment for goroutine (if any)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int32(0), idx.QueueLen(), "no background job should be queued for cold key")
}

func Test_CommitIndex_Hotness_BecomesHotAfterThreshold(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)
	key := indexKey("repo", "ref123", "")

	// Not hot initially
	assert.False(t, idx.isHot(key))

	// Bump 3 times (default threshold)
	idx.bumpHotness(key)
	idx.bumpHotness(key)
	idx.bumpHotness(key)

	assert.True(t, idx.isHot(key))
}

func Test_CommitIndex_Hotness_ExpiresAfterWindow(t *testing.T) {
	// Can't easily test time window without mocking, but verify fresh key is not hot.
	t.Parallel()

	idx := newTestIndex(t)
	key := indexKey("repo", "ref999", "")

	// Single bump → not hot
	idx.bumpHotness(key)
	assert.False(t, idx.isHot(key))
}

func Test_CommitIndex_QueueLen_StartsAtZero(t *testing.T) {
	t.Parallel()

	idx := newTestIndex(t)
	assert.Equal(t, int32(0), idx.QueueLen())
}

// ---------------------------------------------------------------------------
// perf_config bucket tests
// ---------------------------------------------------------------------------

// 회귀: time budget 버킷은 #217 (827c8bf) 에서 ms → s 로 의도적으로 상향됨
// ("최소 1회 DiffTree + 여러 commit scan 이 가능할 만큼 길어야 함").
func Test_PerfConfig_TimeBudgetForEntryCount_SmallDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 5*time.Second, timeBudgetForEntryCount(10))
}

func Test_PerfConfig_TimeBudgetForEntryCount_MediumDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 4*time.Second, timeBudgetForEntryCount(150))
}

func Test_PerfConfig_TimeBudgetForEntryCount_LargeDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 2*time.Second, timeBudgetForEntryCount(800))
}

func Test_PerfConfig_TimeBudgetForEntryCount_HugeDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1500*time.Millisecond, timeBudgetForEntryCount(5000))
}

func Test_PerfConfig_MaxCommitsForEntryCount_SmallDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 20_000, maxCommitsForEntryCount(10))
}

func Test_PerfConfig_MaxCommitsForEntryCount_HugeDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 5_000, maxCommitsForEntryCount(5000))
}

func Test_PerfConfig_TimeBudgetForEntryCount_EnvOverride(t *testing.T) {
	t.Setenv("OVERVIEW_TREE_COMMITS_TIME_BUDGET_MS", "999")
	assert.Equal(t, 999*time.Millisecond, timeBudgetForEntryCount(10))
}

func Test_PerfConfig_MaxCommitsForEntryCount_EnvOverride(t *testing.T) {
	t.Setenv("OVERVIEW_TREE_COMMITS_MAX_COMMITS", "42")
	assert.Equal(t, 42, maxCommitsForEntryCount(10))
}

// ---------------------------------------------------------------------------
// Partial cache
// ---------------------------------------------------------------------------

func Test_OverviewCache_PartialCache_SetAndGetAny(t *testing.T) {
	t.Parallel()

	oc := NewOverviewCache(10, 30*time.Second)

	partialCommits := map[string]*GitCommit{
		"a.go": {SHA: "partial1"},
	}

	oc.SetLatestCommitsBatchPartial("repo", "ref1", "key1", partialCommits)

	// GetLatestCommitsBatchAny should find partial.
	commits, isComplete, found := oc.GetLatestCommitsBatchAny("repo", "ref1", "key1")
	require.True(t, found)
	assert.False(t, isComplete, "should be partial")
	assert.Equal(t, "partial1", commits["a.go"].SHA)
}

func Test_OverviewCache_CompleteTakesPrecedenceOverPartial(t *testing.T) {
	t.Parallel()

	oc := NewOverviewCache(10, 30*time.Second)

	partialCommits := map[string]*GitCommit{
		"a.go": {SHA: "partial1"},
	}
	completeCommits := map[string]*GitCommit{
		"a.go": {SHA: "complete1"},
		"b.go": {SHA: "complete2"},
	}

	oc.SetLatestCommitsBatchPartial("repo", "ref1", "key1", partialCommits)
	oc.SetLatestCommitsBatch("repo", "ref1", "key1", completeCommits)

	// GetAny should return complete.
	commits, isComplete, found := oc.GetLatestCommitsBatchAny("repo", "ref1", "key1")
	require.True(t, found)
	assert.True(t, isComplete, "complete should take precedence")
	assert.Equal(t, "complete1", commits["a.go"].SHA)
	assert.Equal(t, "complete2", commits["b.go"].SHA)
}

func Test_OverviewCache_PartialCacheExpires(t *testing.T) {
	// Partial TTL is 2s. Verify expiration by using a custom ttlLRUCache.
	t.Parallel()

	oc := NewOverviewCache(10, 30*time.Second)

	// Override the `now` function to simulate time passing.
	baseTime := time.Now()
	oc.partialCommits.now = func() time.Time { return baseTime }

	partialCommits := map[string]*GitCommit{
		"a.go": {SHA: "partial1"},
	}
	oc.SetLatestCommitsBatchPartial("repo", "ref1", "key1", partialCommits)

	// Should find it now.
	_, _, found := oc.GetLatestCommitsBatchAny("repo", "ref1", "key1")
	require.True(t, found)

	// Advance time past TTL.
	oc.partialCommits.now = func() time.Time { return baseTime.Add(3 * time.Second) }

	_, _, found = oc.GetLatestCommitsBatchAny("repo", "ref1", "key1")
	assert.False(t, found, "partial should have expired")
}
