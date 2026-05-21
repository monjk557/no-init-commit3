package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/tests/unit" // conftest.go의 환경변수 로드
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain은 모든 테스트 실행 전에 환경변수를 설정합니다
func TestMain(m *testing.M) {
	// conftest.go의 환경변수 설정 함수 호출
	unit.SetupTestEnv()
	// 테스트 실행
	os.Exit(m.Run())
}

func TestLoad_Defaults(t *testing.T) {
	// Test loading with default values from environment
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify default values
	assert.Equal(t, 5001, cfg.Server.Port)
	assert.Equal(t, "/", cfg.Server.BasePath)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, 120*time.Second, cfg.Server.IdleTimeout)

	assert.Equal(t, "/var/lib/git/repos", cfg.Git.RepoRoot)
	assert.True(t, cfg.Git.EnablePackfile)
	assert.Equal(t, 100, cfg.Git.MaxCacheSize)
	assert.Equal(t, 5*time.Minute, cfg.Git.CacheTTL)

	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, "json", cfg.Logging.Format)
	assert.Equal(t, "stdout", cfg.Logging.Output)
	assert.False(t, cfg.Logging.Development)
}

func TestLoad_EnvironmentVariables(t *testing.T) {
	// conftest.go에서 이미 환경변수가 설정되어 있음을 검증 (doc-360 §4.7)
	assert.Equal(t, "5001", os.Getenv("PORT"))
	assert.Equal(t, "5222", os.Getenv("TCP_GIT_PORT"))
	assert.Equal(t, "/", os.Getenv("BASE_PATH"))
	assert.Equal(t, "/var/lib/git/repos", os.Getenv("GIT_REPO_ROOT"))

	// Load configuration (conftest.go의 환경변수 사용)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify environment variables were used
	assert.Equal(t, 5001, cfg.Server.Port)
	assert.Equal(t, 5222, cfg.Server.TCPGitPort)
	assert.Equal(t, "/", cfg.Server.BasePath)
	assert.Equal(t, "/var/lib/git/repos", cfg.Git.RepoRoot)
}

func TestServerConfig_GetMethods(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Test getter methods
	serverCfg := cfg.Server
	assert.Equal(t, cfg.Server.Port, serverCfg.Port)
	assert.Equal(t, cfg.Server.BasePath, serverCfg.BasePath)

	gitCfg := cfg.Git
	assert.Equal(t, cfg.Git.RepoRoot, gitCfg.RepoRoot)

	loggingCfg := cfg.Logging
	assert.Equal(t, cfg.Logging.Level, loggingCfg.Level)

	obsCfg := cfg.Observability
	assert.Equal(t, cfg.Observability.Tracing.Enabled, obsCfg.Tracing.Enabled)
}

func TestObservabilityConfig(t *testing.T) {

	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify observability configuration
	assert.True(t, cfg.Observability.Tracing.Enabled)
	assert.Equal(t, "collabops-git-executor", cfg.Observability.Tracing.ServiceName)
	assert.True(t, cfg.Observability.Metrics.Enabled)
	assert.Equal(t, 9090, cfg.Observability.Metrics.Port)
}

// TestLoad_LegacyServerPrefixFallback verifies that the legacy SERVER_*
// keys (doc-360 §4.7 self-prefix violation) are still honored while
// helm-charts/values catch up in Phase 2. Remove this test together with
// the SERVER_* aliases in Phase 3.
func TestLoad_LegacyServerPrefixFallback(t *testing.T) {
	// Unset new keys, set only legacy keys.
	_ = os.Unsetenv("PORT")
	_ = os.Unsetenv("TCP_GIT_PORT")
	_ = os.Unsetenv("BASE_PATH")
	if err := os.Setenv("SERVER_PORT", "6001"); err != nil {
		t.Fatalf("setenv SERVER_PORT: %v", err)
	}
	if err := os.Setenv("SERVER_TCP_GIT_PORT", "6222"); err != nil {
		t.Fatalf("setenv SERVER_TCP_GIT_PORT: %v", err)
	}
	if err := os.Setenv("SERVER_BASE_PATH", "/legacy"); err != nil {
		t.Fatalf("setenv SERVER_BASE_PATH: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("SERVER_PORT")
		_ = os.Unsetenv("SERVER_TCP_GIT_PORT")
		_ = os.Unsetenv("SERVER_BASE_PATH")
		// Restore the standard test env for subsequent tests.
		_ = os.Setenv("PORT", "5001")
		_ = os.Setenv("TCP_GIT_PORT", "5222")
		_ = os.Setenv("BASE_PATH", "/")
	})

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 6001, cfg.Server.Port, "legacy SERVER_PORT should be honored when PORT is unset")
	assert.Equal(t, 6222, cfg.Server.TCPGitPort, "legacy SERVER_TCP_GIT_PORT should be honored when TCP_GIT_PORT is unset")
	assert.Equal(t, "/legacy", cfg.Server.BasePath, "legacy SERVER_BASE_PATH should be honored when BASE_PATH is unset")
}
