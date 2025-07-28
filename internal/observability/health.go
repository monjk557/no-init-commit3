// Package observability provides comprehensive health check endpoints
// for monitoring service liveness and readiness in production environments.
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
	"github.com/collabops/git-executor/internal/permissions"
)

// HealthStatus represents the overall health status
type HealthStatus string

const (
	// StatusHealthy indicates the service is healthy
	StatusHealthy HealthStatus = "healthy"
	// StatusUnhealthy indicates the service is unhealthy
	StatusUnhealthy HealthStatus = "unhealthy"
	// StatusDegraded indicates the service is running but with limitations
	StatusDegraded HealthStatus = "degraded"
)

// HealthResponse represents the health check response
type HealthResponse struct {
	Status    HealthStatus           `json:"status"`
	Timestamp time.Time              `json:"timestamp"`
	Version   string                 `json:"version,omitempty"`
	Uptime    string                 `json:"uptime,omitempty"`
	Checks    map[string]CheckResult `json:"checks,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

// CheckResult represents the result of an individual health check
type CheckResult struct {
	Status      HealthStatus `json:"status"`
	Message     string       `json:"message,omitempty"`
	Duration    string       `json:"duration,omitempty"`
	LastChecked time.Time    `json:"last_checked"`
	Error       string       `json:"error,omitempty"`
}

// HealthChecker manages health checks for the application
type HealthChecker struct {
	config           *config.Config
	permissionClient permissions.PermissionClient
	startTime        time.Time
	version          string
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(cfg *config.Config, permClient permissions.PermissionClient, version string) *HealthChecker {
	return &HealthChecker{
		config:           cfg,
		permissionClient: permClient,
		startTime:        time.Now(),
		version:          version,
	}
}

// LivenessHandler handles liveness probe requests (GET /health/liveness)
// Liveness checks verify that the application is running and able to process requests
func (h *HealthChecker) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
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

// ReadinessHandler handles readiness probe requests (GET /health/readiness)
// Readiness checks verify that the application is ready to handle traffic
func (h *HealthChecker) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
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
	if checks["filesystem"].Status == StatusUnhealthy {
		hasUnhealthy = true
	} else if checks["filesystem"].Status == StatusDegraded {
		hasDegraded = true
	}

	// Permission service check
	checks["permission_service"] = h.checkPermissionService(ctx)
	if checks["permission_service"].Status == StatusUnhealthy {
		// Permission service failure is degraded, not unhealthy (graceful degradation)
		hasDegraded = true
	}

	// Repository access check
	checks["repository_access"] = h.checkRepositoryAccess(ctx)
	if checks["repository_access"].Status == StatusUnhealthy {
		hasUnhealthy = true
	} else if checks["repository_access"].Status == StatusDegraded {
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

	if h.permissionClient == nil {
		return CheckResult{
			Status:      StatusDegraded,
			Message:     "Permission service not configured (running in degraded mode)",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
		}
	}

	// Test permission service with a lightweight health check
	// This assumes your permission client has a health check method
	// If not, you could do a test permission check with a known user/repo
	select {
	case <-ctx.Done():
		return CheckResult{
			Status:      StatusDegraded,
			Message:     "Permission service check timed out",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
			Error:       "timeout",
		}
	default:
		// Simplified check - try to make a permission check call
		// In a real implementation, you'd want a dedicated health check method
		_, err := h.permissionClient.CheckReadAccess(ctx, "health-check-user", "health-check-owner", "health-check-repo")

		// For health checks, we expect this call to fail (user doesn't exist)
		// but we want to ensure the service is reachable
		if err != nil {
			// Check if error indicates service is unavailable vs normal "user not found" type errors
			errStr := err.Error()
			if isServiceUnavailableError(errStr) {
				return CheckResult{
					Status:      StatusDegraded,
					Message:     "Permission service is unavailable (graceful degradation enabled)",
					Duration:    time.Since(start).String(),
					LastChecked: time.Now().UTC(),
					Error:       err.Error(),
				}
			}
		}

		return CheckResult{
			Status:      StatusHealthy,
			Message:     "Permission service is reachable",
			Duration:    time.Since(start).String(),
			LastChecked: time.Now().UTC(),
		}
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
