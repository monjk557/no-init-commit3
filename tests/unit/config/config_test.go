package config_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_WithRequiredEnvVars(t *testing.T) {
	// Set required environment variables
	setRequiredEnvVars(t)
	defer cleanupEnvVars(t)

	// Load configuration
	cfg, err := config.Load("")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify required environment variables were used
	assert.Equal(t, 5001, cfg.Server.Port)
	assert.Equal(t, 5222, cfg.Server.TCPGitPort)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, "/tmp/test-repos", cfg.Git.RepoRoot)

	// Verify hardcoded defaults
	assert.Equal(t, "/repos", cfg.Server.BasePath)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 120*time.Second, cfg.Server.IdleTimeout)
	assert.Equal(t, "/health", cfg.Server.HealthCheckPath)
	assert.True(t, cfg.Server.EnableDocs)

	// Verify Git hardcoded defaults
	assert.True(t, cfg.Git.EnablePackfile)
	assert.Equal(t, 100, cfg.Git.MaxCacheSize)
	assert.Equal(t, 5*time.Minute, cfg.Git.CacheTTL)

	// Verify logging hardcoded defaults
	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, "json", cfg.Logging.Format)
	assert.Equal(t, "stdout", cfg.Logging.Output)
	assert.False(t, cfg.Logging.Development)

	// Verify observability hardcoded defaults
	assert.False(t, cfg.Observability.Tracing.Enabled)
	assert.Equal(t, "collabops-git-executor", cfg.Observability.Tracing.ServiceName)
	assert.Equal(t, "1.0.0", cfg.Observability.Tracing.ServiceVersion)
	assert.False(t, cfg.Observability.Metrics.Enabled)
	assert.Equal(t, 9090, cfg.Observability.Metrics.Port)
}

func TestLoad_MissingRequiredEnvVars(t *testing.T) {
	// Clear all environment variables
	cleanupEnvVars(t)

	// Loading should fail when required environment variables are missing
	cfg, err := config.Load("")
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "required environment variables are missing")

	// Should contain all required environment variable names
	assert.Contains(t, err.Error(), "PORT")
	assert.Contains(t, err.Error(), "TCP_PORT")
	assert.Contains(t, err.Error(), "HOST_NAME")
	assert.Contains(t, err.Error(), "GIT_ROOT")
}

func TestLoad_PartialEnvVars(t *testing.T) {
	// Set only some required environment variables
	require.NoError(t, os.Setenv("PORT", "8080"))
	require.NoError(t, os.Setenv("HOST_NAME", "localhost"))
	defer cleanupEnvVars(t)

	// Should fail because not all required environment variables are set
	cfg, err := config.Load("")
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "required environment variables are missing")

	// Should contain missing environment variable names
	assert.Contains(t, err.Error(), "TCP_PORT")
	assert.Contains(t, err.Error(), "GIT_ROOT")
}

func TestLoad_OptionalEnvVars(t *testing.T) {
	// Set required environment variables
	setRequiredEnvVars(t)

	// Set optional observability environment variables
	require.NoError(t, os.Setenv("TRACING_ENDPOINT", "http://jaeger:14268"))
	require.NoError(t, os.Setenv("METRICS_ENDPOINT", "http://prometheus:9090"))
	require.NoError(t, os.Setenv("METRICS_PORT", "9091"))

	defer cleanupEnvVars(t)

	// Load configuration
	cfg, err := config.Load("")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify optional environment variables were used
	assert.True(t, cfg.Observability.Tracing.Enabled)
	assert.Equal(t, "http://jaeger:14268", cfg.Observability.Tracing.Endpoint)
	assert.True(t, cfg.Observability.Metrics.Enabled)
	assert.Equal(t, "http://prometheus:9090", cfg.Observability.Metrics.Endpoint)
	assert.Equal(t, 9091, cfg.Observability.Metrics.Port)
}

func TestLoad_InvalidPortValues(t *testing.T) {
	// Set invalid port values - viper.GetInt() returns 0 for invalid strings
	// which will then fail validation as being out of port range
	require.NoError(t, os.Setenv("PORT", "invalid"))
	require.NoError(t, os.Setenv("TCP_PORT", "5222"))
	require.NoError(t, os.Setenv("HOST_NAME", "0.0.0.0"))
	require.NoError(t, os.Setenv("GIT_ROOT", "/tmp/repos"))
	defer cleanupEnvVars(t)

	// Should fail with validation error (viper.GetInt() returns 0 for invalid strings)
	cfg, err := config.Load("")
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "config validation failed")
	assert.Contains(t, err.Error(), "server.port must be between 1 and 65535")
}

func TestLoad_ValidationErrors(t *testing.T) {
	// Set environment variables with values that will fail validation
	require.NoError(t, os.Setenv("PORT", "99999")) // Invalid port range
	require.NoError(t, os.Setenv("TCP_PORT", "5222"))
	require.NoError(t, os.Setenv("HOST_NAME", "0.0.0.0"))
	require.NoError(t, os.Setenv("GIT_ROOT", "/tmp/repos"))
	defer cleanupEnvVars(t)

	// Should fail with validation error
	cfg, err := config.Load("")
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "config validation failed")
	assert.Contains(t, err.Error(), "server.port must be between 1 and 65535")
}

// Helper functions

func setRequiredEnvVars(t *testing.T) {
	require.NoError(t, os.Setenv("PORT", "5001"))
	require.NoError(t, os.Setenv("TCP_PORT", "5222"))
	require.NoError(t, os.Setenv("HOST_NAME", "0.0.0.0"))
	require.NoError(t, os.Setenv("GIT_ROOT", "/tmp/test-repos"))
}

func cleanupEnvVars(t *testing.T) {
	// Required environment variables
	_ = os.Unsetenv("PORT")
	_ = os.Unsetenv("TCP_PORT")
	_ = os.Unsetenv("HOST_NAME")
	_ = os.Unsetenv("GIT_ROOT")

	// Optional environment variables
	_ = os.Unsetenv("TRACING_ENDPOINT")
	_ = os.Unsetenv("METRICS_ENDPOINT")
	_ = os.Unsetenv("METRICS_PORT")
}

// Helper function to test validation directly (not exported from config package)
func validateConfig(cfg *config.Config) error {
	// This simulates the validation that happens inside config.Load()
	// In a real scenario, you'd either export the validate function or test it through Load()

	// Basic validation checks (since JWT is removed, we just check basic config)
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535")
	}
	return nil
}

// TestNewTestConfigFromLoad_Success tests successful config loading with environment variables
func TestNewTestConfigFromLoad_Success(t *testing.T) {
	tempDir := t.TempDir()

	// 환경변수 정리
	config.ClearTestEnvVars()
	defer config.ClearTestEnvVars()

	// NewTestConfigFromLoad 호출 (내부에서 환경변수 설정)
	cfg, err := config.NewTestConfigFromLoad(tempDir)

	// 성공해야 함
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, tempDir, cfg.Git.RepoRoot)
}

// TestNewTestConfigFromLoad_FailFast tests fail-fast when environment variables are missing
func TestNewTestConfigFromLoad_FailFast(t *testing.T) {
	// 모든 환경변수 정리 (없는 상태로 만듦)
	config.ClearTestEnvVars()
	defer config.ClearTestEnvVars()

	// 실제 config.Load() 직접 호출 (환경변수 없이)
	cfg, err := config.Load("")

	// fail-fast로 실패해야 함
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "PORT")
}
