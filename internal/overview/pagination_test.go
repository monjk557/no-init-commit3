package overview

import (
	"context"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPagination 은 테스트용 SmartPagination 을 생성한다.
func newTestPagination(t *testing.T) *SmartPagination {
	t.Helper()
	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)
	return NewSmartPagination(logger)
}

// ---------------------------------------------------------------------------
// DeterminePageSize
// ---------------------------------------------------------------------------

func Test_SmartPagination_DeterminePageSize_RequestedWithinBounds(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	// 명시적 요청이 제한값 내면 그대로 반환.
	got := p.DeterminePageSize(context.Background(), "repo", "path", 30)
	assert.Equal(t, 30, got)
}

func Test_SmartPagination_DeterminePageSize_RequestedAboveMax_Capped(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	got := p.DeterminePageSize(context.Background(), "repo", "path", 1000)
	assert.Equal(t, p.config.MaxPageSize, got)
}

func Test_SmartPagination_DeterminePageSize_RequestedBelowMin_Adjusted(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	got := p.DeterminePageSize(context.Background(), "repo", "path", 1)
	assert.Equal(t, p.config.MinPageSize, got)
}

func Test_SmartPagination_DeterminePageSize_AutoEstimate_SmallDefault(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	// requestedSize=0 → estimateDirectorySize 는 50 을 반환 (<= SmallRepoThreshold 100)
	// → SmallRepoPageSize.
	got := p.DeterminePageSize(context.Background(), "repo", "path", 0)
	assert.Equal(t, p.config.SmallRepoPageSize, got)
}

func Test_SmartPagination_DeterminePageSize_PerfDegradation_ReducesSize(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	// 평균 응답시간을 MaxResponseTime 초과로 만들어 페이지 크기 감소 경로 트리거.
	p.monitor.RecordResponseTime("slowrepo", 5*time.Second)

	got := p.DeterminePageSize(context.Background(), "slowrepo", "path", 0)
	// estimate(50)→small bucket(100); perf degradation → 100 * 2/3 = 66.
	assert.Equal(t, 66, got)
}

// ---------------------------------------------------------------------------
// ApplyPagination
// ---------------------------------------------------------------------------

func makeEntries(n int) []GitTreeEntryWithCommit {
	entries := make([]GitTreeEntryWithCommit, n)
	for i := 0; i < n; i++ {
		entries[i] = GitTreeEntryWithCommit{Type: "blob", Name: "f", Path: "f"}
	}
	return entries
}

func Test_SmartPagination_ApplyPagination_FirstPage(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	resp, info := p.ApplyPagination(makeEntries(25), 1, 10, "https://x/tree")

	require.NotNil(t, resp)
	require.NotNil(t, info)
	assert.Len(t, resp.Tree, 10)
	assert.Equal(t, 25, info.TotalFiles)
	assert.Equal(t, 3, info.TotalPages)
	assert.True(t, info.HasNextPage)
	assert.False(t, info.HasPrevPage)
	assert.NotEmpty(t, info.NextPageURL)
	assert.Empty(t, info.PrevPageURL)
}

func Test_SmartPagination_ApplyPagination_MiddlePage_HasBothURLs(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	_, info := p.ApplyPagination(makeEntries(25), 2, 10, "https://x/tree")

	assert.True(t, info.HasNextPage)
	assert.True(t, info.HasPrevPage)
	assert.NotEmpty(t, info.NextPageURL)
	assert.NotEmpty(t, info.PrevPageURL)
}

func Test_SmartPagination_ApplyPagination_LastPage_Partial(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	resp, info := p.ApplyPagination(makeEntries(25), 3, 10, "")

	assert.Len(t, resp.Tree, 5)
	assert.False(t, info.HasNextPage)
	assert.True(t, info.HasPrevPage)
	// baseURL 이 빈 문자열이면 URL 미생성.
	assert.Empty(t, info.NextPageURL)
	assert.Empty(t, info.PrevPageURL)
}

func Test_SmartPagination_ApplyPagination_OutOfRangePage_Empty(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	resp, info := p.ApplyPagination(makeEntries(5), 99, 10, "")

	assert.Empty(t, resp.Tree)
	assert.Equal(t, 5, info.TotalFiles)
}

func Test_SmartPagination_ApplyPagination_NegativePage_Normalized(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	// page<1 → 1, perPage<1 → MediumRepoPageSize.
	resp, info := p.ApplyPagination(makeEntries(5), -1, 0, "")

	assert.Equal(t, 1, info.Page)
	assert.Equal(t, p.config.MediumRepoPageSize, info.PerPage)
	assert.Len(t, resp.Tree, 5)
}

func Test_SmartPagination_ApplyPagination_EmptyList(t *testing.T) {
	t.Parallel()
	p := newTestPagination(t)

	resp, info := p.ApplyPagination(nil, 1, 10, "")

	assert.Empty(t, resp.Tree)
	assert.Equal(t, 0, info.TotalFiles)
	assert.Equal(t, 1, info.TotalPages)
	assert.False(t, info.HasNextPage)
	assert.False(t, info.HasPrevPage)
}

// ---------------------------------------------------------------------------
// PerformanceMonitor
// ---------------------------------------------------------------------------

func Test_PerformanceMonitor_GetAverageResponseTime_Default(t *testing.T) {
	t.Parallel()
	pm := &PerformanceMonitor{responseTimeCache: make(map[string]time.Duration)}

	// 기록이 없는 repo 는 기본값 100ms.
	assert.Equal(t, 100*time.Millisecond, pm.GetAverageResponseTime("unknown"))
}

func Test_PerformanceMonitor_GetAverageResponseTime_NilCache(t *testing.T) {
	t.Parallel()
	pm := &PerformanceMonitor{}

	assert.Equal(t, time.Duration(0), pm.GetAverageResponseTime("any"))
}

func Test_PerformanceMonitor_RecordResponseTime_MovingAverage(t *testing.T) {
	t.Parallel()
	pm := &PerformanceMonitor{responseTimeCache: make(map[string]time.Duration)}

	pm.RecordResponseTime("repo", 200*time.Millisecond)
	assert.Equal(t, 200*time.Millisecond, pm.GetAverageResponseTime("repo"))

	// 두 번째 기록은 이동 평균: (200+400)/2 = 300.
	pm.RecordResponseTime("repo", 400*time.Millisecond)
	assert.Equal(t, 300*time.Millisecond, pm.GetAverageResponseTime("repo"))
}

func Test_PerformanceMonitor_RecordResponseTime_NilCache_Initializes(t *testing.T) {
	t.Parallel()
	pm := &PerformanceMonitor{}

	pm.RecordResponseTime("repo", 150*time.Millisecond)
	assert.Equal(t, 150*time.Millisecond, pm.GetAverageResponseTime("repo"))
}

// ---------------------------------------------------------------------------
// perf_config: blobSizeParallelism + env 분기
// ---------------------------------------------------------------------------

func Test_BlobSizeParallelism_Default(t *testing.T) {
	assert.Equal(t, 1, blobSizeParallelism())
}

func Test_BlobSizeParallelism_EnvOverride(t *testing.T) {
	t.Setenv("OVERVIEW_BLOB_SIZE_PARALLELISM", "4")
	assert.Equal(t, 4, blobSizeParallelism())
}

func Test_BlobSizeParallelism_InvalidEnv_FallsBackToDefault(t *testing.T) {
	t.Setenv("OVERVIEW_BLOB_SIZE_PARALLELISM", "notanumber")
	assert.Equal(t, 1, blobSizeParallelism())
}

func Test_LatestCommitsTimeBudget_Default(t *testing.T) {
	assert.Equal(t, 300*time.Millisecond, latestCommitsTimeBudget())
}

func Test_LatestCommitsTimeBudget_EnvOverride(t *testing.T) {
	t.Setenv("OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS", "750")
	assert.Equal(t, 750*time.Millisecond, latestCommitsTimeBudget())
}

func Test_LatestCommitsMaxCommits_Default(t *testing.T) {
	assert.Equal(t, 10_000, latestCommitsMaxCommits())
}

func Test_LatestCommitsMaxCommits_EnvOverride(t *testing.T) {
	t.Setenv("OVERVIEW_LATEST_COMMITS_MAX_COMMITS", "555")
	assert.Equal(t, 555, latestCommitsMaxCommits())
}

func Test_MaxCommitsForEntryCount_MediumBucket(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 15_000, maxCommitsForEntryCount(150))
}

func Test_MaxCommitsForEntryCount_LargeBucket(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 10_000, maxCommitsForEntryCount(400))
}

func Test_TimeBudgetForEntryCount_500Boundary(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 3*time.Second, timeBudgetForEntryCount(500))
}
