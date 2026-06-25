package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearConfigEnv 는 Load() 및 parse 헬퍼들이 읽는 모든 env 키를 비워
// 테스트 격리를 보장한다. t.Setenv 는 빈 값으로 set 후 자동 복원하므로
// 부모 프로세스 환경 오염 없이 깨끗한 baseline 을 만든다.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"APP_ENV",
		"PORT", "SERVER_PORT",
		"TCP_GIT_PORT", "SERVER_TCP_GIT_PORT",
		"BASE_PATH", "SERVER_BASE_PATH",
		"GIT_PROTOCOL_IDLE_TIMEOUT",
		"GIT_REPO_ROOT",
		"LOG_LEVEL",
		"EVENT_BUS_REDIS_NODE_ADDRESS", "EVENT_BUS_REDIS_MODE",
		"EVENT_BUS_REDIS_SOCKET_TIMEOUT_S", "EVENT_BUS_REDIS_USERNAME",
		"EVENT_BUS_REDIS_PASSWORD", "REDIS_URL",
		"BASE_URL_ORCHESTRATOR", "ORCHESTRATOR_BASE_URL",
		"ORCHESTRATOR_SSH_KEY_VALIDATE_PATH", "ORCHESTRATOR_TIMEOUT_MS",
		"ORCHESTRATOR_SSH_FINGERPRINT_HEADER", "ORCHESTRATOR_FORWARD_AUTH_HEADER",
		"MERGE_PROTECTION_ENABLED", "MERGE_PROTECTION_SAFETY_MODE",
		"MERGE_PROTECTION_FALLBACK_STRATEGY", "MERGE_PROTECTION_ALLOWED_DELETES",
		"MERGE_PROTECTION_CRITICAL_PATHS",
		"LFS_EXTERNAL_URL",
		"GIT_MAINTENANCE_ENABLED",
		"SECURITY_DECISION_JWT_SECRET", "DECISION_JWT_SECRET",
		"SECURITY_DECISION_JWT_AUDIENCE", "SECURITY_DECISION_JWT_ISSUER",
		"SECURITY_DECISION_JWT_REQUIRED",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

// setRequiredEnv 는 Load() 가 성공하는 데 필요한 최소 필수 env 를 설정한다.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PORT", "8080")
	t.Setenv("TCP_GIT_PORT", "9418")
	t.Setenv("BASE_PATH", "/api")
	t.Setenv("GIT_REPO_ROOT", "/repos")
}

// ------------------------------------------------------------
// AppEnv 메서드
// ------------------------------------------------------------

func TestAppEnvIsOnprem(t *testing.T) {
	assert.True(t, AppEnvOnprem.IsOnprem())
	assert.False(t, AppEnvProd.IsOnprem())
	assert.False(t, AppEnvLocal.IsOnprem())
	assert.False(t, AppEnvStage.IsOnprem())
}

func TestAppEnvIsProduction(t *testing.T) {
	assert.True(t, AppEnvProd.IsProduction())
	assert.True(t, AppEnvOnprem.IsProduction())
	assert.False(t, AppEnvLocal.IsProduction())
	assert.False(t, AppEnvStage.IsProduction())
}

// ------------------------------------------------------------
// parseAppEnv
// ------------------------------------------------------------

func TestParseAppEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want AppEnv
	}{
		{"prod", AppEnvProd},
		{"stage", AppEnvStage},
		{"onprem", AppEnvOnprem},
		{"local", AppEnvLocal},
		{"", AppEnvLocal},        // default
		{"unknown", AppEnvLocal}, // default
		{"  PROD ", AppEnvProd},  // trim + lowercase
		{"OnPrem", AppEnvOnprem}, // case-insensitive
	}
	for _, c := range cases {
		clearConfigEnv(t)
		t.Setenv("APP_ENV", c.raw)
		assert.Equalf(t, c.want, parseAppEnv(), "APP_ENV=%q", c.raw)
	}
}

// ------------------------------------------------------------
// parseServerConfig
// ------------------------------------------------------------

func TestParseServerConfigSuccess(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)

	sc, err := parseServerConfig()
	require.NoError(t, err)
	assert.Equal(t, 8080, sc.Port)
	assert.Equal(t, 9418, sc.TCPGitPort)
	assert.Equal(t, "/api", sc.BasePath)
	assert.Equal(t, "0.0.0.0", sc.Host)
	assert.Equal(t, 30*time.Second, sc.ReadTimeout)
	assert.Equal(t, "/health", sc.HealthCheckPath)
	assert.True(t, sc.EnableDocs)
	assert.Equal(t, 5*time.Minute, sc.GitProtocolIdleTimeout)
}

func TestParseServerConfigLegacyAlias(t *testing.T) {
	clearConfigEnv(t)
	// 새 키 비우고 legacy SERVER_* 만 설정 → alias fallback.
	t.Setenv("SERVER_PORT", "7000")
	t.Setenv("SERVER_TCP_GIT_PORT", "7001")
	t.Setenv("SERVER_BASE_PATH", "/legacy")

	sc, err := parseServerConfig()
	require.NoError(t, err)
	assert.Equal(t, 7000, sc.Port)
	assert.Equal(t, 7001, sc.TCPGitPort)
	assert.Equal(t, "/legacy", sc.BasePath)
}

func TestParseServerConfigErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			name:  "missing PORT",
			setup: func(t *testing.T) {}, // 아무것도 안 set
		},
		{
			name: "invalid PORT",
			setup: func(t *testing.T) {
				t.Setenv("PORT", "notnum")
			},
		},
		{
			name: "missing TCP_GIT_PORT",
			setup: func(t *testing.T) {
				t.Setenv("PORT", "8080")
			},
		},
		{
			name: "invalid TCP_GIT_PORT",
			setup: func(t *testing.T) {
				t.Setenv("PORT", "8080")
				t.Setenv("TCP_GIT_PORT", "bad")
			},
		},
		{
			name: "missing BASE_PATH",
			setup: func(t *testing.T) {
				t.Setenv("PORT", "8080")
				t.Setenv("TCP_GIT_PORT", "9418")
			},
		},
		{
			name: "invalid GIT_PROTOCOL_IDLE_TIMEOUT",
			setup: func(t *testing.T) {
				t.Setenv("PORT", "8080")
				t.Setenv("TCP_GIT_PORT", "9418")
				t.Setenv("BASE_PATH", "/api")
				t.Setenv("GIT_PROTOCOL_IDLE_TIMEOUT", "not-a-duration")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearConfigEnv(t)
			c.setup(t)
			_, err := parseServerConfig()
			assert.Error(t, err)
		})
	}
}

func TestParseServerConfigCustomIdleTimeout(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("GIT_PROTOCOL_IDLE_TIMEOUT", "90s")

	sc, err := parseServerConfig()
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, sc.GitProtocolIdleTimeout)
}

// ------------------------------------------------------------
// parseGitConfig
// ------------------------------------------------------------

func TestParseGitConfigSuccess(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GIT_REPO_ROOT", "/srv/repos")

	gc, err := parseGitConfig()
	require.NoError(t, err)
	assert.Equal(t, "/srv/repos", gc.RepoRoot)
	assert.True(t, gc.EnablePackfile)
	assert.Equal(t, 100, gc.MaxCacheSize)
	assert.Equal(t, 5*time.Minute, gc.CacheTTL)
}

func TestParseGitConfigMissingRepoRoot(t *testing.T) {
	clearConfigEnv(t)
	_, err := parseGitConfig()
	assert.Error(t, err)
}

// ------------------------------------------------------------
// parseLoggingConfig
// ------------------------------------------------------------

func TestParseLoggingConfigFields(t *testing.T) {
	lc := parseLoggingConfig(AppEnvLocal)
	assert.Equal(t, "debug", lc.Level)
	assert.Equal(t, "json", lc.Format)
	assert.Equal(t, "stdout", lc.Output)
	assert.False(t, lc.Development)

	lc = parseLoggingConfig(AppEnvProd)
	assert.Equal(t, "info", lc.Level)
}

// ------------------------------------------------------------
// parseObservabilityConfig
// ------------------------------------------------------------

func TestParseObservabilityConfig(t *testing.T) {
	oc := parseObservabilityConfig()
	assert.True(t, oc.Tracing.Enabled)
	assert.Equal(t, "", oc.Tracing.Endpoint)
	assert.Equal(t, "collabops-git-executor", oc.Tracing.ServiceName)
	assert.Equal(t, "1.0.0", oc.Tracing.ServiceVersion)
	assert.True(t, oc.Metrics.Enabled)
	assert.Equal(t, 9090, oc.Metrics.Port)
}

// ------------------------------------------------------------
// parseMergeProtectionConfig
// ------------------------------------------------------------

func TestParseMergeProtectionConfigDefaults(t *testing.T) {
	clearConfigEnv(t)
	mp := parseMergeProtectionConfig()
	assert.True(t, mp.Enabled)
	assert.True(t, mp.IntelligentMerging)
	assert.True(t, mp.AdaptiveFallback)
	assert.True(t, mp.PredictiveConflictDetect)
	assert.True(t, mp.EnableMetrics)
	assert.True(t, mp.EnableAuditLogs)
	assert.Equal(t, "strict", mp.SafetyMode)
	assert.Equal(t, "preserve_target", mp.FallbackStrategy)

	ep := mp.EnvironmentProtection
	assert.True(t, ep.Enabled)
	assert.True(t, ep.ProtectConfigFiles)
	assert.True(t, ep.ProtectEnvFiles)
	assert.True(t, ep.StrictMode)
	// 기본 slice 값
	assert.Contains(t, ep.AllowedDeletePaths, "tmp/**")
	assert.Contains(t, ep.CriticalPaths, "*.env")
}

func TestParseMergeProtectionConfigOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MERGE_PROTECTION_ENABLED", "false")
	t.Setenv("MERGE_PROTECTION_SAFETY_MODE", "permissive")
	t.Setenv("MERGE_PROTECTION_FALLBACK_STRATEGY", "manual")
	t.Setenv("MERGE_PROTECTION_ALLOWED_DELETES", "a/**, b/**")
	t.Setenv("MERGE_PROTECTION_CRITICAL_PATHS", "secret/**")

	mp := parseMergeProtectionConfig()
	assert.False(t, mp.Enabled)
	assert.Equal(t, "permissive", mp.SafetyMode)
	assert.Equal(t, "manual", mp.FallbackStrategy)
	assert.Equal(t, []string{"a/**", "b/**"}, mp.EnvironmentProtection.AllowedDeletePaths)
	assert.Equal(t, []string{"secret/**"}, mp.EnvironmentProtection.CriticalPaths)
}

// ------------------------------------------------------------
// parseSecurityConfig
// ------------------------------------------------------------

func TestParseSecurityConfigDefaults(t *testing.T) {
	clearConfigEnv(t)
	sc := parseSecurityConfig()
	assert.Equal(t, "", sc.DecisionJWT.Secret) // 미설정 → 검증 비활성
	assert.False(t, sc.DecisionJWT.Required)
	assert.Equal(t, defaultDecisionJWTAudience, sc.DecisionJWT.Audience)
	assert.Equal(t, defaultDecisionJWTIssuer, sc.DecisionJWT.Issuer)
	assert.Equal(t, defaultDecisionJWTLeewaySeconds, sc.DecisionJWT.LeewaySeconds)
}

func TestParseSecurityConfigPrimarySecret(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("SECURITY_DECISION_JWT_SECRET", "primary")
	t.Setenv("DECISION_JWT_SECRET", "fallback")
	sc := parseSecurityConfig()
	assert.Equal(t, "primary", sc.DecisionJWT.Secret)
}

func TestParseSecurityConfigFallbackSecret(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DECISION_JWT_SECRET", "fallback")
	sc := parseSecurityConfig()
	assert.Equal(t, "fallback", sc.DecisionJWT.Secret)
}

func TestParseSecurityConfigOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("SECURITY_DECISION_JWT_AUDIENCE", "custom-aud")
	t.Setenv("SECURITY_DECISION_JWT_ISSUER", "custom-iss")
	t.Setenv("SECURITY_DECISION_JWT_REQUIRED", "true")
	sc := parseSecurityConfig()
	assert.Equal(t, "custom-aud", sc.DecisionJWT.Audience)
	assert.Equal(t, "custom-iss", sc.DecisionJWT.Issuer)
	assert.True(t, sc.DecisionJWT.Required)
}

// ------------------------------------------------------------
// parseBoolFromEnv
// ------------------------------------------------------------

func TestParseBoolFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		def  bool
		want bool
	}{
		{"true", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"TRUE", false, true},   // case-insensitive
		{" true ", false, true}, // trimmed
		{"false", true, false},
		{"0", true, false},
		{"no", true, false},
		{"garbage", true, false}, // non-truthy → false
		{"", true, true},         // empty → default
		{"", false, false},       // empty → default
	}
	for _, c := range cases {
		clearConfigEnv(t)
		t.Setenv("TEST_BOOL_KEY", c.raw)
		got := parseBoolFromEnv("TEST_BOOL_KEY", c.def)
		assert.Equalf(t, c.want, got, "raw=%q def=%v", c.raw, c.def)
	}
}

// ------------------------------------------------------------
// parseStringSliceFromEnv
// ------------------------------------------------------------

func TestParseStringSliceFromEnv(t *testing.T) {
	def := []string{"d1", "d2"}

	clearConfigEnv(t)
	t.Setenv("TEST_SLICE_KEY", "")
	assert.Equal(t, def, parseStringSliceFromEnv("TEST_SLICE_KEY", def))

	t.Setenv("TEST_SLICE_KEY", "a, b ,c")
	assert.Equal(t, []string{"a", "b", "c"}, parseStringSliceFromEnv("TEST_SLICE_KEY", def))

	// 공백/빈 요소는 걸러진다
	t.Setenv("TEST_SLICE_KEY", "x,, ,y")
	assert.Equal(t, []string{"x", "y"}, parseStringSliceFromEnv("TEST_SLICE_KEY", def))
}

// ------------------------------------------------------------
// parseLFSConfig
// ------------------------------------------------------------

func TestParseLFSConfig(t *testing.T) {
	clearConfigEnv(t)
	// 빈 값 → 빈 값 (SSH LFS 비활성)
	assert.Equal(t, "", parseLFSConfig().ExternalURL)

	// host base → /api/v2 보강
	t.Setenv("LFS_EXTERNAL_URL", "https://api.stage.collabops.ai")
	assert.Equal(t, "https://api.stage.collabops.ai/api/v2", parseLFSConfig().ExternalURL)

	// 이미 /api/vN 포함 → 그대로
	t.Setenv("LFS_EXTERNAL_URL", "https://api.collabops.ai/api/v3/")
	assert.Equal(t, "https://api.collabops.ai/api/v3", parseLFSConfig().ExternalURL)
}

// ------------------------------------------------------------
// parseMaintenanceConfig
// ------------------------------------------------------------

func TestParseMaintenanceConfig(t *testing.T) {
	clearConfigEnv(t)
	assert.True(t, parseMaintenanceConfig().Enabled) // default true

	t.Setenv("GIT_MAINTENANCE_ENABLED", "false")
	assert.False(t, parseMaintenanceConfig().Enabled)
}

// ------------------------------------------------------------
// applyOnpremOverrides
// ------------------------------------------------------------

func TestApplyOnpremOverrides(t *testing.T) {
	cfg := &Config{}
	cfg.Server.EnableDocs = true
	cfg.Observability.Tracing.Endpoint = "http://jaeger:4318"

	applyOnpremOverrides(cfg)

	assert.False(t, cfg.Server.EnableDocs)
	assert.Equal(t, "", cfg.Observability.Tracing.Endpoint)
}

// ------------------------------------------------------------
// validate
// ------------------------------------------------------------

func validConfig() *Config {
	return &Config{
		Env: AppEnvLocal,
		Server: ServerConfig{
			Port:            8080,
			BasePath:        "/api",
			HealthCheckPath: "/health",
		},
		Git: GitConfig{
			RepoRoot:     "/repos",
			MaxCacheSize: 100,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		MergeProtection: MergeProtectionConfig{
			SafetyMode:       "strict",
			FallbackStrategy: "preserve_target",
		},
	}
}

func TestValidateSuccess(t *testing.T) {
	assert.NoError(t, validate(validConfig()))
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
	}{
		{"port too low", func(c *Config) { c.Server.Port = 0 }},
		{"port too high", func(c *Config) { c.Server.Port = 70000 }},
		{"empty base path", func(c *Config) { c.Server.BasePath = "" }},
		{"base path no slash", func(c *Config) { c.Server.BasePath = "api" }},
		{"empty health path", func(c *Config) { c.Server.HealthCheckPath = "" }},
		{"empty repo root", func(c *Config) { c.Git.RepoRoot = "" }},
		{"negative cache size", func(c *Config) { c.Git.MaxCacheSize = -1 }},
		{"bad log level", func(c *Config) { c.Logging.Level = "trace" }},
		{"bad log format", func(c *Config) { c.Logging.Format = "xml" }},
		{"bad safety mode", func(c *Config) { c.MergeProtection.SafetyMode = "nope" }},
		{"bad fallback strategy", func(c *Config) { c.MergeProtection.FallbackStrategy = "nope" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := validConfig()
			c.mutate(cfg)
			assert.Error(t, validate(cfg))
		})
	}
}

// ------------------------------------------------------------
// Load — 통합 경로
// ------------------------------------------------------------

func TestLoadSuccessLocal(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "local")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, AppEnvLocal, cfg.Env)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, "/repos", cfg.Git.RepoRoot)
	assert.Equal(t, "debug", cfg.Logging.Level)
	assert.True(t, cfg.Server.EnableDocs)
}

func TestLoadSuccessProd(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "prod")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, AppEnvProd, cfg.Env)
	assert.Equal(t, "info", cfg.Logging.Level)
	// prod 는 onprem override 미적용 → docs 유지
	assert.True(t, cfg.Server.EnableDocs)
}

func TestLoadOnpremAppliesOverrides(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "onprem")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, AppEnvOnprem, cfg.Env)
	assert.False(t, cfg.Server.EnableDocs) // onprem override
	assert.Equal(t, "", cfg.Observability.Tracing.Endpoint)
	assert.Equal(t, "info", cfg.Logging.Level)
}

func TestLoadMissingRequiredVar(t *testing.T) {
	clearConfigEnv(t)
	// PORT 누락 → parseServerConfig 실패 → Load 에러
	t.Setenv("TCP_GIT_PORT", "9418")
	t.Setenv("BASE_PATH", "/api")
	t.Setenv("GIT_REPO_ROOT", "/repos")

	_, err := Load()
	assert.Error(t, err)
}

func TestLoadMissingRepoRoot(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PORT", "8080")
	t.Setenv("TCP_GIT_PORT", "9418")
	t.Setenv("BASE_PATH", "/api")
	// GIT_REPO_ROOT 누락 → parseGitConfig 실패

	_, err := Load()
	assert.Error(t, err)
}

func TestLoadWithEventsConfig(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("EVENT_BUS_REDIS_NODE_ADDRESS", "redis:6379")
	t.Setenv("EVENT_BUS_REDIS_MODE", "standalone")

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.Events.Enabled)
	assert.Equal(t, "redis:6379", cfg.Events.NodeAddress)
}

func TestLoadInvalidEventsConfigFails(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("EVENT_BUS_REDIS_NODE_ADDRESS", "redis:6379")
	t.Setenv("EVENT_BUS_REDIS_MODE", "bogus") // 잘못된 mode → parseEventsConfig 에러

	_, err := Load()
	assert.Error(t, err)
}
