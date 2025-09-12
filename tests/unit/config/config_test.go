package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ValidConfig(t *testing.T) {
	// Create a temporary config file
	configContent := `
server:
  port: 5001
  base_path: "/api/repos"
  host: "localhost"
  read_timeout: "45s"
  write_timeout: "45s"
  idle_timeout: "300s"

git:
  repo_root: "/tmp/test-repos"
  enable_packfile: false
  max_cache_size: 50
  cache_ttl: "10m"

jwt:
  algorithm: "HS256"
  secret_key: "test-secret-key"

logging:
  level: "debug"
  format: "text"
  output: "stderr"
  development: true
`

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0644))

	// Load configuration
	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify server configuration
	assert.Equal(t, 5001, cfg.Server.Port)
	assert.Equal(t, "/api/repos", cfg.Server.BasePath)
	assert.Equal(t, "localhost", cfg.Server.Host)
	assert.Equal(t, 45*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 45*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 300*time.Second, cfg.Server.IdleTimeout)

	// Verify git configuration
	assert.Equal(t, "/tmp/test-repos", cfg.Git.RepoRoot)
	assert.False(t, cfg.Git.EnablePackfile)
	assert.Equal(t, 50, cfg.Git.MaxCacheSize)
	assert.Equal(t, 10*time.Minute, cfg.Git.CacheTTL)

	// Verify JWT configuration
	assert.Equal(t, "HS256", cfg.JWT.Algorithm)
	assert.Equal(t, "test-secret-key", cfg.JWT.SecretKey)

	// Verify logging configuration
	assert.Equal(t, "debug", cfg.Logging.Level)
	assert.Equal(t, "text", cfg.Logging.Format)
	assert.Equal(t, "stderr", cfg.Logging.Output)
	assert.True(t, cfg.Logging.Development)
}

func TestLoad_Defaults(t *testing.T) {
	// Test loading with default values when no config file exists
	cfg, err := config.Load("/nonexistent/config.yaml")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify default values
	assert.Equal(t, 5001, cfg.Server.Port)
	assert.Equal(t, "/repos", cfg.Server.BasePath)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)

	assert.Equal(t, "/var/lib/git/repos", cfg.Git.RepoRoot)
	assert.True(t, cfg.Git.EnablePackfile)
	assert.Equal(t, 100, cfg.Git.MaxCacheSize)
	assert.Equal(t, 5*time.Minute, cfg.Git.CacheTTL)

	assert.Equal(t, "RS256", cfg.JWT.Algorithm)
	assert.Equal(t, "/etc/collabops/public.pem", cfg.JWT.PublicKeyPath)

	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, "json", cfg.Logging.Format)
	assert.Equal(t, "stdout", cfg.Logging.Output)
	assert.False(t, cfg.Logging.Development)
}

func TestLoad_InvalidYAML(t *testing.T) {
	// Create invalid YAML content
	invalidYAML := `
server:
  port: invalid_port
  base_path: /repos
    invalid_indent
`

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(invalidYAML), 0644))

	// Loading should fail with YAML parsing error
	cfg, err := config.Load(configPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "failed to unmarshal config")
}

func TestLoad_EnvironmentVariables(t *testing.T) {
	// Set environment variables
	require.NoError(t, os.Setenv("COLLABOPS_SERVER_PORT", "9999"))
	require.NoError(t, os.Setenv("COLLABOPS_GIT_REPO_ROOT", "/tmp/env-repos"))
	defer func() {
		_ = os.Unsetenv("COLLABOPS_SERVER_PORT")
		_ = os.Unsetenv("COLLABOPS_GIT_REPO_ROOT")
	}()

	// Load configuration (no file, should use env vars and defaults)
	cfg, err := config.Load("/nonexistent/config.yaml")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify environment variables were used
	assert.Equal(t, 9999, cfg.Server.Port)
	assert.Equal(t, "/tmp/env-repos", cfg.Git.RepoRoot)
}

func TestGetConfigPath(t *testing.T) {
	// Create a temporary config file
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  port: 8080"), 0644))

	// Load configuration
	_, err := config.Load(configPath)
	require.NoError(t, err)

	// Verify config path is returned
	usedPath := config.GetConfigPath()
	assert.Equal(t, configPath, usedPath)
}
