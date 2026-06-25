package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newManagerTestConfig 는 NewManager 가 성공하기 위한 최소 config 를 만든다.
// tracing 은 비활성(기본)으로 두어 외부 exporter 의존성을 피한다.
func newManagerTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Git: config.GitConfig{RepoRoot: t.TempDir()},
		Observability: config.ObservabilityConfig{
			Health: config.HealthConfig{
				LivenessTimeout:  2 * time.Second,
				ReadinessTimeout: 2 * time.Second,
			},
			Metrics: config.MetricsConfig{Enabled: true},
			Tracing: config.TracingConfig{Enabled: false},
		},
	}
}

func TestDefaultObservabilityConfig(t *testing.T) {
	cfg := DefaultObservabilityConfig()
	assert.True(t, cfg.Health.Enabled)
	assert.Equal(t, "/health/liveness", cfg.Health.LivenessPath)
	assert.Equal(t, "/health/readiness", cfg.Health.ReadinessPath)
	assert.Equal(t, "/metrics", cfg.Health.MetricsPath)
	assert.Equal(t, "info", cfg.Logging.Level)
}

func TestNewManager_NilConfig(t *testing.T) {
	m, err := NewManager(nil, "v1.0.0")
	assert.Error(t, err)
	assert.Nil(t, m)
}

func TestNewManager_Success(t *testing.T) {
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)
	require.NotNil(t, m)

	// 모든 컴포넌트 접근자가 non-nil.
	assert.NotNil(t, m.GetLogger())
	assert.NotNil(t, m.GetMetrics())
	assert.NotNil(t, m.GetGitTracer())
	assert.NotNil(t, m.GetAPITracer())
	assert.NotNil(t, m.GetPermissionTracer())
	assert.NotNil(t, m.GetCacheIntegration())

	// Shutdown 은 logger.Sync() 가 stdout 에서 "bad file descriptor" 를
	// 반환할 수 있어(zap 의 알려진 동작) 에러를 단언하지 않고 안전 실행만 확인.
	assert.NotPanics(t, func() { _ = m.Shutdown(context.Background()) })
}

func TestNewManager_TracingEnabled(t *testing.T) {
	cfg := newManagerTestConfig(t)
	cfg.Observability.Tracing = config.TracingConfig{
		Enabled:        true,
		ServiceVersion: "1.2.3",
	}

	m, err := NewManager(cfg, "v1.0.0")
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.NotNil(t, m.tracingManager)

	assert.NotPanics(t, func() { _ = m.Shutdown(context.Background()) })
}

func TestManager_SetupMiddleware(t *testing.T) {
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	router := gin.New()
	assert.NotPanics(t, func() { m.SetupMiddleware(router) })
}

func TestManager_SetupMiddleware_WithTracing(t *testing.T) {
	cfg := newManagerTestConfig(t)
	cfg.Observability.Tracing = config.TracingConfig{Enabled: true}
	m, err := NewManager(cfg, "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	// tracing 활성 시 ginTracingMiddleware 가 등록된다.
	router := gin.New()
	m.config.Tracing.Enabled = true
	assert.NotPanics(t, func() { m.SetupMiddleware(router) })
}

func TestManager_SetupRoutes(t *testing.T) {
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	router := gin.New()
	m.SetupRoutes(router)

	// liveness route 가 실제로 동작하는지 확인.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/liveness", nil)
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	// metrics route.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	router.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusOK, rr2.Code)
}

func TestManager_GinMetricsMiddleware(t *testing.T) {
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	router := gin.New()
	router.Use(m.ginMetricsMiddleware())
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "pong", rr.Body.String())
}

func TestManager_GinTracingMiddleware_NilTracer(t *testing.T) {
	// tracing 비활성 매니저 → tracingManager 가 nil → pass-through 미들웨어.
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	router := gin.New()
	router.Use(m.ginTracingMiddleware())
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestManager_GinTracingMiddleware_WithTracer(t *testing.T) {
	cfg := newManagerTestConfig(t)
	cfg.Observability.Tracing = config.TracingConfig{Enabled: true}
	m, err := NewManager(cfg, "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	router := gin.New()
	router.Use(m.ginTracingMiddleware())
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestManager_RecordGitOperationAndError(t *testing.T) {
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	assert.NotPanics(t, func() {
		m.RecordGitOperation("clone", 10*time.Millisecond, true)
		m.RecordError("io_error", "clone")
	})
}

func TestManager_GetHealthStatus(t *testing.T) {
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	status := m.GetHealthStatus(context.Background())
	assert.Equal(t, "healthy", status.Overall)
	assert.Contains(t, status.Components, "metrics")
	assert.Contains(t, status.Components, "tracing")
	assert.Contains(t, status.Components, "logging")
	assert.Equal(t, ServiceVersion, status.Version)
}

func TestManager_GetHealthStatus_TracingEnabled(t *testing.T) {
	cfg := newManagerTestConfig(t)
	cfg.Observability.Tracing = config.TracingConfig{Enabled: true}
	m, err := NewManager(cfg, "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	status := m.GetHealthStatus(context.Background())
	tracing := status.Components["tracing"].(map[string]interface{})
	assert.Equal(t, "healthy", tracing["status"])
}

func TestNewInstrumentedServer_AndShutdown(t *testing.T) {
	m, err := NewManager(newManagerTestConfig(t), "v1.0.0")
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := NewInstrumentedServer(":0", handler, m)
	require.NotNil(t, srv)

	// 서버를 시작하지 않았어도 Shutdown 은 안전하게 동작해야 한다.
	// (observability Shutdown 의 logger.Sync 가 stdout 에서 실패할 수 있어
	// 에러는 단언하지 않고 패닉 없이 완료되는지만 확인한다.)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	assert.NotPanics(t, func() { _ = srv.Shutdown(ctx) })
}
