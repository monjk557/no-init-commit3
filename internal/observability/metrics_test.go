package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultMetricsConfig(t *testing.T) {
	cfg := DefaultMetricsConfig()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "git_executor", cfg.Namespace)
	assert.Equal(t, 15*time.Second, cfg.CollectInterval)
}

func TestNewApplicationMetrics_Singleton(t *testing.T) {
	m1 := NewApplicationMetrics()
	m2 := NewApplicationMetrics()
	require.NotNil(t, m1)
	// 싱글턴이므로 동일 인스턴스를 반환해야 한다.
	assert.Same(t, m1, m2)
}

func TestGetApplicationMetrics(t *testing.T) {
	m := GetApplicationMetrics()
	require.NotNil(t, m)
	assert.Same(t, NewApplicationMetrics(), m)
}

func TestMust(t *testing.T) {
	assert.Equal(t, 42, must(42, nil))
	assert.Panics(t, func() { must(0, errors.New("boom")) })
}

func TestCtx(t *testing.T) {
	assert.NotNil(t, ctx())
	assert.Equal(t, context.Background(), ctx())
}

func TestCollectResourceMetrics(t *testing.T) {
	m := NewApplicationMetrics()
	// 패닉 없이 실행되면 충분 (OTel 게이지 record).
	assert.NotPanics(t, func() { m.collectResourceMetrics() })
}

func TestStartResourceCollector(t *testing.T) {
	m := NewApplicationMetrics()
	// 짧은 인터벌로 시작 → 최소 한 번 tick 이 돌고 패닉이 없는지 확인.
	assert.NotPanics(t, func() { m.StartResourceCollector(5 * time.Millisecond) })
	time.Sleep(20 * time.Millisecond)
}

// TestRecordMethods 는 모든 Record*/Set* 메서드가 패닉 없이 OTel 미터를
// 업데이트하는지 확인한다. 이들은 부수효과만 있고 반환값이 없다.
func TestRecordMethods(t *testing.T) {
	m := NewApplicationMetrics()

	assert.NotPanics(t, func() {
		m.RecordGitOperation("clone", 100*time.Millisecond, true)
		m.RecordGitOperation("push", 50*time.Millisecond, false)
		m.RecordError("io_error", "clone")
		m.RecordPermissionDenied("repo", "read")
		m.RecordValidationError("branch", "invalid_name")
		m.RecordRepositoryAccess()
		m.RecordGitObjectProcessed("blob")
		m.RecordPermissionCheckDuration("success", 10*time.Millisecond)
		m.RecordAPIServiceCall("/api/v2/repos", "success")
		m.SetCacheHitRate(0.85)

		m.RecordOverviewCacheOp("branch", "hit")
		m.SetOverviewCacheEntries("branch", 12)
		m.RecordOverviewSingleflightShared("branch")

		m.RecordLatestCommitsBatch("ok", 30*time.Millisecond, 5, 100)
		m.RecordLatestCommitsBatchCutReason("budget")

		m.RecordTreeWithCommit("ok", 20*time.Millisecond, 42)

		m.RecordCommitIndexOp("hit")
		m.SetCommitIndexEntries(99)
	})
}

// TestRecordMethods_NilReceiver 는 nil receiver 가드(if am == nil)를 커버한다.
func TestRecordMethods_NilReceiver(t *testing.T) {
	var m *ApplicationMetrics

	assert.NotPanics(t, func() {
		m.RecordOverviewCacheOp("branch", "hit")
		m.SetOverviewCacheEntries("branch", 1)
		m.RecordOverviewSingleflightShared("branch")
		m.RecordLatestCommitsBatch("ok", time.Millisecond, 1, 1)
		m.RecordLatestCommitsBatchCutReason("budget")
		m.RecordTreeWithCommit("ok", time.Millisecond, 1)
		m.RecordCommitIndexOp("hit")
		m.SetCommitIndexEntries(1)
	})
}

func TestMetricsHandler(t *testing.T) {
	h := MetricsHandler()
	require.NotNil(t, h)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestHealthMetricsHandler(t *testing.T) {
	h := HealthMetricsHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics/health", nil)
	h(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "text/plain", rr.Header().Get("Content-Type"))
	assert.Contains(t, rr.Body.String(), "healthy")
}
