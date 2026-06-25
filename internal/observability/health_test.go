package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHealthTestConfig 는 RepoRoot 가 유효한 임시 디렉터리를 가리키는
// 최소 config 를 만든다. 헬스 체크의 filesystem/repository 경로가 통과하도록.
func newHealthTestConfig(t *testing.T) *config.Config {
	t.Helper()
	repoRoot := t.TempDir()
	return &config.Config{
		Git: config.GitConfig{RepoRoot: repoRoot},
		Observability: config.ObservabilityConfig{
			Health: config.HealthConfig{
				LivenessTimeout:  2 * time.Second,
				ReadinessTimeout: 2 * time.Second,
			},
		},
	}
}

func TestNewHealthChecker(t *testing.T) {
	cfg := newHealthTestConfig(t)
	hc := NewHealthChecker(cfg, "v1.2.3")

	require.NotNil(t, hc)
	assert.Equal(t, "v1.2.3", hc.version)
	assert.Equal(t, cfg, hc.config)
	assert.False(t, hc.startTime.IsZero())
}

func TestLivenessHandler(t *testing.T) {
	cfg := newHealthTestConfig(t)
	hc := NewHealthChecker(cfg, "v1.0.0")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/liveness", nil)

	hc.LivenessHandler()(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var resp HealthResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, StatusHealthy, resp.Status)
	assert.Equal(t, "v1.0.0", resp.Version)
	assert.Contains(t, resp.Checks, "service")
	assert.Contains(t, resp.Checks, "memory")
	assert.Equal(t, "liveness", resp.Details["type"])
}

func TestReadinessHandler_Healthy(t *testing.T) {
	cfg := newHealthTestConfig(t)
	hc := NewHealthChecker(cfg, "v1.0.0")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/readiness", nil)

	hc.ReadinessHandler()(rr, req)

	// permission_service 가 degraded 를 반환하므로 전체 상태는 degraded 지만
	// readiness 는 unhealthy 일 때만 503 → 200 OK.
	assert.Equal(t, http.StatusOK, rr.Code)

	var resp HealthResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, StatusDegraded, resp.Status)
	assert.Contains(t, resp.Checks, "filesystem")
	assert.Contains(t, resp.Checks, "permission_service")
	assert.Contains(t, resp.Checks, "repository_access")
	assert.Equal(t, "readiness", resp.Details["type"])
}

func TestReadinessHandler_Unhealthy_MissingRepoRoot(t *testing.T) {
	// RepoRoot 가 빈 문자열이면 filesystem/repository 체크가 unhealthy →
	// 전체 unhealthy → 503.
	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: ""},
		Observability: config.ObservabilityConfig{
			Health: config.HealthConfig{
				LivenessTimeout:  time.Second,
				ReadinessTimeout: time.Second,
			},
		},
	}
	hc := NewHealthChecker(cfg, "v1.0.0")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/readiness", nil)

	hc.ReadinessHandler()(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var resp HealthResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, StatusUnhealthy, resp.Status)
}

func TestPerformLivenessChecks(t *testing.T) {
	hc := NewHealthChecker(newHealthTestConfig(t), "v1.0.0")
	resp := hc.performLivenessChecks(context.Background())

	assert.Equal(t, StatusHealthy, resp.Status)
	assert.Equal(t, "v1.0.0", resp.Version)
	assert.NotEmpty(t, resp.Uptime)
	assert.Equal(t, StatusHealthy, resp.Checks["service"].Status)
	assert.Equal(t, StatusHealthy, resp.Checks["memory"].Status)
}

func TestPerformReadinessChecks_Degraded(t *testing.T) {
	hc := NewHealthChecker(newHealthTestConfig(t), "v1.0.0")
	resp := hc.performReadinessChecks(context.Background())

	// permission service 미설정 → degraded.
	assert.Equal(t, StatusDegraded, resp.Status)
	assert.Equal(t, StatusHealthy, resp.Checks["filesystem"].Status)
	assert.Equal(t, StatusDegraded, resp.Checks["permission_service"].Status)
	assert.Equal(t, StatusHealthy, resp.Checks["repository_access"].Status)
}

func TestCheckServiceHealth(t *testing.T) {
	hc := NewHealthChecker(newHealthTestConfig(t), "v1.0.0")
	result := hc.checkServiceHealth(context.Background())

	assert.Equal(t, StatusHealthy, result.Status)
	assert.NotEmpty(t, result.Message)
	assert.NotEmpty(t, result.Duration)
}

func TestCheckMemoryUsage(t *testing.T) {
	hc := NewHealthChecker(newHealthTestConfig(t), "v1.0.0")
	result := hc.checkMemoryUsage(context.Background())

	assert.Equal(t, StatusHealthy, result.Status)
	assert.NotEmpty(t, result.Message)
}

func TestCheckFilesystem_Healthy(t *testing.T) {
	hc := NewHealthChecker(newHealthTestConfig(t), "v1.0.0")
	result := hc.checkFilesystem(context.Background())

	assert.Equal(t, StatusHealthy, result.Status)
	assert.Contains(t, result.Message, "writable")
}

func TestCheckFilesystem_MissingConfig(t *testing.T) {
	hc := NewHealthChecker(&config.Config{Git: config.GitConfig{RepoRoot: ""}}, "v1.0.0")
	result := hc.checkFilesystem(context.Background())

	assert.Equal(t, StatusUnhealthy, result.Status)
	assert.Equal(t, "configuration error", result.Error)
}

func TestCheckFilesystem_NotADirectory(t *testing.T) {
	// RepoRoot 가 디렉터리가 아닌 파일을 가리키면 unhealthy.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "regular_file")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))

	hc := NewHealthChecker(&config.Config{Git: config.GitConfig{RepoRoot: filePath}}, "v1.0.0")
	result := hc.checkFilesystem(context.Background())

	assert.Equal(t, StatusUnhealthy, result.Status)
	assert.Equal(t, "path is not a directory", result.Error)
}

func TestCheckFilesystem_NonExistentPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does_not_exist")
	hc := NewHealthChecker(&config.Config{Git: config.GitConfig{RepoRoot: missing}}, "v1.0.0")
	result := hc.checkFilesystem(context.Background())

	assert.Equal(t, StatusUnhealthy, result.Status)
	assert.NotEmpty(t, result.Error)
}

func TestCheckPermissionService(t *testing.T) {
	hc := NewHealthChecker(newHealthTestConfig(t), "v1.0.0")
	result := hc.checkPermissionService(context.Background())

	// 권한 서비스는 항상 degraded (제거됨).
	assert.Equal(t, StatusDegraded, result.Status)
}

func TestCheckRepositoryAccess_Healthy(t *testing.T) {
	// .git 디렉터리를 가진 repo 하나를 만들어 카운트되는지 확인.
	repoRoot := t.TempDir()
	repoDir := filepath.Join(repoRoot, "myrepo")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755))
	// .git 이 없는 일반 디렉터리도 하나 추가 (카운트 제외 경로 커버).
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "notarepo"), 0o755))

	hc := NewHealthChecker(&config.Config{Git: config.GitConfig{RepoRoot: repoRoot}}, "v1.0.0")
	result := hc.checkRepositoryAccess(context.Background())

	assert.Equal(t, StatusHealthy, result.Status)
	assert.Contains(t, result.Message, "1 repositories found")
}

func TestCheckRepositoryAccess_MissingConfig(t *testing.T) {
	hc := NewHealthChecker(&config.Config{Git: config.GitConfig{RepoRoot: ""}}, "v1.0.0")
	result := hc.checkRepositoryAccess(context.Background())

	assert.Equal(t, StatusUnhealthy, result.Status)
	assert.Equal(t, "configuration error", result.Error)
}

func TestCheckRepositoryAccess_UnreadableDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	hc := NewHealthChecker(&config.Config{Git: config.GitConfig{RepoRoot: missing}}, "v1.0.0")
	result := hc.checkRepositoryAccess(context.Background())

	assert.Equal(t, StatusUnhealthy, result.Status)
	assert.NotEmpty(t, result.Error)
}

func TestSendHealthResponse(t *testing.T) {
	hc := NewHealthChecker(newHealthTestConfig(t), "v1.0.0")
	rr := httptest.NewRecorder()
	resp := HealthResponse{Status: StatusHealthy, Timestamp: time.Now().UTC()}

	hc.sendHealthResponse(rr, resp, http.StatusOK)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache, no-store, must-revalidate", rr.Header().Get("Cache-Control"))

	var decoded HealthResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &decoded))
	assert.Equal(t, StatusHealthy, decoded.Status)
}
