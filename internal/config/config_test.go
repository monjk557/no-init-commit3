package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ValidConfig(t *testing.T) {
	// Create a temporary config file
	configContent := `
server:
  port: 9090
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

log:
  level: "debug"
  format: "text"
  output: "stderr"
  development: true
`

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0644))

	// Load configuration
	config, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, config)

	// Verify server configuration
	assert.Equal(t, 9090, config.Server.Port)
	assert.Equal(t, "/api/repos", config.Server.BasePath)
	assert.Equal(t, "localhost", config.Server.Host)
	assert.Equal(t, 45*time.Second, config.Server.ReadTimeout)
	assert.Equal(t, 45*time.Second, config.Server.WriteTimeout)
	assert.Equal(t, 300*time.Second, config.Server.IdleTimeout)

	// Verify git configuration
	assert.Equal(t, "/tmp/test-repos", config.Git.RepoRoot)
	assert.False(t, config.Git.EnablePackfile)
	assert.Equal(t, 50, config.Git.MaxCacheSize)
	assert.Equal(t, 10*time.Minute, config.Git.CacheTTL)

	// Verify JWT configuration
	assert.Equal(t, "HS256", config.JWT.Algorithm)
	assert.Equal(t, "test-secret-key", config.JWT.SecretKey)

	// Verify log configuration
	assert.Equal(t, "debug", config.Log.Level)
	assert.Equal(t, "text", config.Log.Format)
	assert.Equal(t, "stderr", config.Log.Output)
	assert.True(t, config.Log.Development)
}

func TestLoad_Defaults(t *testing.T) {
	// Test loading with default values when no config file exists
	config, err := Load("/nonexistent/config.yaml")
	require.NoError(t, err)
	require.NotNil(t, config)

	// Verify default values
	assert.Equal(t, 8080, config.Server.Port)
	assert.Equal(t, "/repos", config.Server.BasePath)
	assert.Equal(t, "0.0.0.0", config.Server.Host)
	assert.Equal(t, 30*time.Second, config.Server.ReadTimeout)

	assert.Equal(t, "/var/lib/git/repos", config.Git.RepoRoot)
	assert.True(t, config.Git.EnablePackfile)
	assert.Equal(t, 100, config.Git.MaxCacheSize)
	assert.Equal(t, 5*time.Minute, config.Git.CacheTTL)

	assert.Equal(t, "RS256", config.JWT.Algorithm)
	assert.Equal(t, "/etc/collabops/public.pem", config.JWT.PublicKeyPath)

	assert.Equal(t, "info", config.Log.Level)
	assert.Equal(t, "json", config.Log.Format)
	assert.Equal(t, "stdout", config.Log.Output)
	assert.False(t, config.Log.Development)
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
	config, err := Load(configPath)
	assert.Error(t, err)
	assert.Nil(t, config)
	assert.Contains(t, err.Error(), "failed to unmarshal config")
}

func TestValidate_InvalidServerPort(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"negative port", -1},
		{"zero port", 0},
		{"port too high", 70000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Server: ServerConfig{
					Port:     tt.port,
					BasePath: "/repos",
				},
				Git: GitConfig{
					RepoRoot: "/var/lib/git/repos",
				},
				JWT: JWTConfig{
					Algorithm:     "RS256",
					PublicKeyPath: "/etc/collabops/public.pem",
				},
				Log: LogConfig{
					Level:  "info",
					Format: "json",
				},
			}

			err := validate(config)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "server.port must be between 1 and 65535")
		})
	}
}

func TestValidate_InvalidBasePath(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
	}{
		{"empty base path", ""},
		{"base path without leading slash", "repos"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Server: ServerConfig{
					Port:     8080,
					BasePath: tt.basePath,
				},
				Git: GitConfig{
					RepoRoot: "/var/lib/git/repos",
				},
				JWT: JWTConfig{
					Algorithm:     "RS256",
					PublicKeyPath: "/etc/collabops/public.pem",
				},
				Log: LogConfig{
					Level:  "info",
					Format: "json",
				},
			}

			err := validate(config)
			assert.Error(t, err)
		})
	}
}

func TestValidate_JWTAlgorithms(t *testing.T) {
	tests := []struct {
		name          string
		algorithm     string
		publicKeyPath string
		secretKey     string
		shouldError   bool
		errorContains string
	}{
		{
			name:          "valid RS256 with public key",
			algorithm:     "RS256",
			publicKeyPath: "/etc/collabops/public.pem",
			shouldError:   false,
		},
		{
			name:        "valid HS256 with secret key",
			algorithm:   "HS256",
			secretKey:   "secret",
			shouldError: false,
		},
		{
			name:          "invalid algorithm",
			algorithm:     "INVALID",
			shouldError:   true,
			errorContains: "jwt.algorithm must be one of",
		},
		{
			name:          "RS256 without public key",
			algorithm:     "RS256",
			shouldError:   true,
			errorContains: "jwt.public_key_path is required for algorithm RS256",
		},
		{
			name:          "HS256 without secret key",
			algorithm:     "HS256",
			shouldError:   true,
			errorContains: "jwt.secret_key is required for algorithm HS256",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Server: ServerConfig{
					Port:     8080,
					BasePath: "/repos",
				},
				Git: GitConfig{
					RepoRoot: "/var/lib/git/repos",
				},
				JWT: JWTConfig{
					Algorithm:     tt.algorithm,
					PublicKeyPath: tt.publicKeyPath,
					SecretKey:     tt.secretKey,
				},
				Log: LogConfig{
					Level:  "info",
					Format: "json",
				},
			}

			err := validate(config)
			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidate_LogConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		level       string
		format      string
		shouldError bool
	}{
		{"valid debug level", "debug", "json", false},
		{"valid info level", "info", "text", false},
		{"invalid log level", "invalid", "json", true},
		{"invalid log format", "info", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Server: ServerConfig{
					Port:     8080,
					BasePath: "/repos",
				},
				Git: GitConfig{
					RepoRoot: "/var/lib/git/repos",
				},
				JWT: JWTConfig{
					Algorithm:     "RS256",
					PublicKeyPath: "/etc/collabops/public.pem",
				},
				Log: LogConfig{
					Level:  tt.level,
					Format: tt.format,
				},
			}

			err := validate(config)
			if tt.shouldError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	// Test with missing git.repo_root
	config := &Config{
		Server: ServerConfig{
			Port:     8080,
			BasePath: "/repos",
		},
		Git: GitConfig{
			RepoRoot: "", // Missing required field
		},
		JWT: JWTConfig{
			Algorithm:     "RS256",
			PublicKeyPath: "/etc/collabops/public.pem",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
	}

	err := validate(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git.repo_root is required")
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
	config, err := Load("/nonexistent/config.yaml")
	require.NoError(t, err)
	require.NotNil(t, config)

	// Verify environment variables were used
	assert.Equal(t, 9999, config.Server.Port)
	assert.Equal(t, "/tmp/env-repos", config.Git.RepoRoot)
}

func TestGetConfigPath(t *testing.T) {
	// Create a temporary config file
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  port: 8080"), 0644))

	// Load configuration
	_, err := Load(configPath)
	require.NoError(t, err)

	// Verify config path is returned
	usedPath := GetConfigPath()
	assert.Equal(t, configPath, usedPath)
}
