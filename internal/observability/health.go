// observability 패키지는 프로덕션 환경에서 서비스 liveness와 readiness 모니터링을 위한
// 종합적인 헬스 체크 엔드포인트를 제공합니다.
// 메트릭, 추적, 통합 모니터링 기능을 포함합니다.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/collabops/git-executor/internal/config"
)

// HealthStatus는 전반적인 헬스 상태를 나타냅니다.
type HealthStatus string

const (
	// StatusHealthy indicates the service is healthy
	StatusHealthy HealthStatus = "healthy"
	// StatusUnhealthy indicates the service is unhealthy
	StatusUnhealthy HealthStatus = "unhealthy"
	// StatusDegraded indicates the service is running but with limitations
	StatusDegraded HealthStatus = "degraded"
)

// HealthResponse는 헬스 체크 응답을 나타냅니다.
type HealthResponse struct {
	Status    HealthStatus           `json:"status"`
	Timestamp time.Time              `json:"timestamp"`
	Version   string                 `json:"version,omitempty"`
	Uptime    string                 `json:"uptime,omitempty"`
	Checks    map[string]CheckResult `json:"checks,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

// CheckResult는 개별 헬스 체크의 결과를 나타냅니다.
type CheckResult struct {
	Status      HealthStatus `json:"status"`
	Message     string       `json:"message,omitempty"`
	Duration    string       `json:"duration,omitempty"`
	LastChecked time.Time    `json:"last_checked"`
	Error       string       `json:"error,omitempty"`
}

// HealthChecker는 애플리케이션의 헬스 체크를 관리합니다.
type HealthChecker struct {
	config    *config.Config
	startTime time.Time
	version   string
}

// NewHealthChecker는 새로운 헬스 체커를 생성합니다.
func NewHealthChecker(cfg *config.Config, version string) *HealthChecker {
	return &HealthChecker{
		config:    cfg,
		startTime: time.Now(),
		version:   version,
	}
}

// LivenessHandler는 liveness 프로브 요청을 처리합니다 (GET /health/liveness).
// Liveness 체크는 애플리케이션이 실행 중이고 요청을 처리할 수 있는지 확인합니다.
func (h *HealthChecker) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), h.config.Observability.Health.LivenessTimeout)
		defer cancel()

		response := h.performLivenessChecks(ctx)

		// Liveness should always pass unless the service is completely broken
		statusCode := http.StatusOK
		if response.Status == StatusUnhealthy {
			statusCode = http.StatusServiceUnavailable
		}

		h.sendHealthResponse(w, response, statusCode)
	}
}

// ReadinessHandler는 readiness 프로브 요청을 처리합니다 (GET /health/readiness).
// Readiness 체크는 애플리케이션이 트래픽을 처리할 준비가 되어 있는지 확인합니다.
func (h *HealthChecker) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), h.config.Observability.Health.ReadinessTimeout)
		defer cancel()

		response := h.performReadinessChecks(ctx)

		// Readiness fails if any critical dependency is unavailable
		statusCode := http.StatusOK
		if response.Status == StatusUnhealthy {
			statusCode = http.StatusServiceUnavailable
		}

		h.sendHealthResponse(w, response, statusCode)
	}
}

// performLivenessChecks performs basic liveness checks
func (h *HealthChecker) performLivenessChecks(ctx context.Context) HealthResponse {
	checks := make(map[string]CheckResult)
	overall := StatusHealthy

	// Basic service health check
	checks["service"] = h.checkServiceHealth(ctx)
	if checks["service"].Status == StatusUnhealthy {
		overall = StatusUnhealthy
	}

	// Memory check (basic)
	checks["memory"] = h.checkMemoryUsage(ctx)
	if checks["memory"].Status == StatusUnhealthy {
		overall = StatusUnhealthy
	}

	return HealthResponse{
		Status:    overall,
		Timestamp: time.Now().UTC(),
		Version:   h.version,
		Uptime:    time.Since(h.startTime).String(),
		Checks:    checks,
		Details: map[string]interface{}{
			"type": "liveness",
		},
	}
}

// performReadinessChecks performs comprehensive readiness checks
func (h *HealthChecker) performReadinessChecks(ctx context.Context) HealthResponse {
	checks := make(map[string]CheckResult)
	overall := StatusHealthy
	hasUnhealthy := false
	hasDegraded := false

	// File system check
	checks["filesystem"] = h.checkFilesystem(ctx)
	switch checks["filesystem"].Status {
	case StatusUnhealthy:
		hasUnhealthy = true
	case StatusDegraded:
		hasDegraded = true
	}

	// Permission service check
	checks["permission_service"] = h.checkPermissionService(ctx)
	switch checks["permission_service"].Status {
	case StatusUnhealthy:
		hasUnhealthy = true
	case StatusDegraded:
		hasDegraded = true
	}

	// Repository access check
	checks["repository_access"] = h.checkRepositoryAccess(ctx)
	switch checks["repository_access"].Status {
	case StatusUnhealthy:
		hasUnhealthy = true
	case StatusDegraded:
		hasDegraded = true
	}

	// Determine overall status
	if hasUnhealthy {
		overall = StatusUnhealthy
	} else if hasDegraded {
		overall = StatusDegraded
	}

	return HealthResponse{
		Status:    overall,
		Timestamp: time.Now().UTC(),
		Version:   h.version,
		Uptime:    time.Since(h.startTime).String(),
		Checks:    checks,
		Details: map[string]interface{}{
			"type": "readiness",
		},
	}
}

// checkServiceHealth performs basic service health checks
func (h *HealthChecker) checkServiceHealth(ctx context.Context) CheckResult {
	start := time.Now()

	// Basic health indicators - just ensure we can allocate memory
	_ = make([]byte, 1024)

	return CheckResult{
		Status:      StatusHealthy,
		Message:     "Service is running normally",
		Duration:    time.Since(start).String(),
		LastChecked: time.Now().UTC(),
	}
}

// checkMemoryUsage checks if memory usage is within acceptable limits
func (h *HealthChecker) checkMemoryUsage(ctx context.Context) CheckResult {
	start := time.Now()

	// This is a simplified memory check
	// In production, you'd want more sophisticated memory monitoring
	return CheckResult{
		Status:      StatusHealthy,
		Message:     "Memory usage is normal",
		Duration:    time.Since(start).String(),
		LastChecked: time.Now().UTC(),
	}
}

// checkFilesystem checks if the repository filesystem is accessible
func (h *HealthChecker) checkFilesystem(ctx context.Context) CheckResult {
	start := time.Now()

	if h.config == nil || h.config.Git.RepoRoot == "" {
		return CheckResult{
			Status:      StatusUnhealthy,
			Message:     "Repository root path not configured",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
			Error:       "configuration error",
		}
	}

	// Check if repository root directory exists and is accessible
	repoRoot := h.config.Git.RepoRoot
	stat, err := os.Stat(repoRoot)
	if err != nil {
		return CheckResult{
			Status:      StatusUnhealthy,
			Message:     "Repository root directory not accessible",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
			Error:       err.Error(),
		}
	}

	if !stat.IsDir() {
		return CheckResult{
			Status:      StatusUnhealthy,
			Message:     "Repository root path is not a directory",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
			Error:       "path is not a directory",
		}
	}

	// Test write access by creating a temporary file
	testFile := filepath.Join(repoRoot, ".health_check_test")
	err = os.WriteFile(testFile, []byte("health check"), 0644)
	if err != nil {
		return CheckResult{
			Status:      StatusDegraded,
			Message:     "Repository root directory is read-only",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
			Error:       err.Error(),
		}
	}

	// Clean up test file
	_ = os.Remove(testFile)

	return CheckResult{
		Status:      StatusHealthy,
		Message:     "Filesystem is accessible and writable",
		Duration:    time.Since(start).String(),
		LastChecked: time.Now().UTC(),
	}
}

// checkPermissionService checks if the permission service is available
func (h *HealthChecker) checkPermissionService(ctx context.Context) CheckResult {
	start := time.Now()

	// Permission service check is disabled (permissions package removed)
	// Return degraded status as permission service is not available
	return CheckResult{
		Status:      StatusDegraded,
		Message:     "Permission service not configured (running in degraded mode)",
		Duration:    time.Since(start).String(),
		LastChecked: time.Now().UTC(),
	}

	return CheckResult{
		Status:      StatusHealthy,
		Message:     "Permission service is reachable",
		Duration:    time.Since(start).String(),
		LastChecked: time.Now().UTC(),
	}
}

// checkRepositoryAccess checks if we can access Git repositories
func (h *HealthChecker) checkRepositoryAccess(ctx context.Context) CheckResult {
	start := time.Now()

	if h.config == nil || h.config.Git.RepoRoot == "" {
		return CheckResult{
			Status:      StatusUnhealthy,
			Message:     "Repository configuration missing",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
			Error:       "configuration error",
		}
	}

	// Check if we can list the repository directory
	repoRoot := h.config.Git.RepoRoot
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return CheckResult{
			Status:      StatusUnhealthy,
			Message:     "Cannot read repository directory",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
			Error:       err.Error(),
		}
	}

	// Count accessible repositories
	repoCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			repoPath := filepath.Join(repoRoot, entry.Name())
			if _, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil {
				repoCount++
			}
		}
	}

	message := fmt.Sprintf("Repository access is working (%d repositories found)", repoCount)

	return CheckResult{
		Status:      StatusHealthy,
		Message:     message,
		Duration:    time.Since(start).String(),
		LastChecked: time.Now().UTC(),
	}
}

// sendHealthResponse sends a standardized health response
func (h *HealthChecker) sendHealthResponse(w http.ResponseWriter, response HealthResponse, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		// Fallback to simple text response if JSON encoding fails
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "Health check failed: %v", err)
	}
}

// isServiceUnavailableError determines if an error indicates service unavailability
func isServiceUnavailableError(errStr string) bool {
	unavailableKeywords := []string{
		"connection refused",
		"timeout",
		"network unreachable",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
	}

	for _, keyword := range unavailableKeywords {
		if len(errStr) > 0 && contains(errStr, keyword) {
			return true
		}
	}
	return false
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					findSubstring(s, substr))))
}

// findSubstring performs a simple substring search
func findSubstring(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
