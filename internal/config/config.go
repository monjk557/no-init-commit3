package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// AppEnv represents the application environment
type AppEnv string

const (
	AppEnvLocal  AppEnv = "local"
	AppEnvStage  AppEnv = "stage"
	AppEnvProd   AppEnv = "prod"
	AppEnvOnprem AppEnv = "onprem"
)

// IsOnprem returns true if the environment is on-premises (air-gap)
func (e AppEnv) IsOnprem() bool {
	return e == AppEnvOnprem
}

// IsProduction returns true if the environment is production-grade (prod or onprem)
func (e AppEnv) IsProduction() bool {
	return e == AppEnvProd || e == AppEnvOnprem
}

// Config represents the complete application configuration
type Config struct {
	Env             AppEnv
	Server          ServerConfig
	Git             GitConfig
	Logging         LoggingConfig
	Observability   ObservabilityConfig
	Security        SecurityConfig
	Permissions     PermissionsConfig
	Performance     PerformanceConfig
	Events          EventsConfig
	Orchestrator    OrchestratorConfig
	MergeProtection MergeProtectionConfig
	LFS             LFSConfig
	Maintenance     MaintenanceConfig
}

// LFSConfig는 git-executor 가 LFS 와 관련해서 알아야 할 단 하나의 정보
// — 외부에서 LFS HTTP API 에 도달할 수 있는 URL — 를 보관합니다.
//
// LFS HTTP API (Batch / Verify / Locks / Usage) 자체는 orchestrator 가
// 소유하며 (Issue-3052, 옵션 B), git-executor 는 그 URL 을 직접 호출하지
// 않습니다. 이 필드는 오로지 SSH 경유 `git-lfs-authenticate` 응답에서
// 클라이언트에게 알려줄 LFS HTTP endpoint 를 만들기 위해 존재합니다.
//
// 비어 있으면 (`LFS_EXTERNAL_URL` env 미설정) SSH 경유 LFS 인증은 거절되며
// 클라이언트는 `git config lfs.url` 로 endpoint 를 명시해야 합니다.
type LFSConfig struct {
	// ExternalURL 은 클라이언트가 LFS HTTP API 에 도달하는 base URL.
	// 예: https://api.collabops.ai/api/v2 (orchestrator 가 LFS 라우트를
	// 노출하는 prefix). 빈 값이면 SSH 경유 lfs-authenticate 비활성.
	ExternalURL string `json:"external_url"`
}

// MaintenanceConfig controls Git repository maintenance scheduling.
// See internal/maintenance package for the full implementation.
type MaintenanceConfig struct {
	Enabled bool `json:"enabled"`
}

// ServerConfig stores HTTP server configuration
type ServerConfig struct {
	Port            int
	TCPGitPort      int
	BasePath        string
	Host            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	HealthCheckPath string
	EnableDocs      bool
	DocsPath        string

	// GitProtocolIdleTimeout governs Smart HTTP streaming routes
	// (POST /git-upload-pack, POST /git-receive-pack). The global
	// http.Server WriteTimeout / ReadTimeout are absolute and would
	// truncate long-running pack streams, so those routes opt out of
	// fixed deadlines and instead enforce this per-stream idle window:
	// the deadline slides forward by GitProtocolIdleTimeout on every
	// successful read/write. Streams remain alive while data is
	// flowing and are reaped only when truly idle. Zero disables the
	// idle deadline (uncapped).
	GitProtocolIdleTimeout time.Duration
}

// GitConfig stores Git repository configuration
type GitConfig struct {
	RepoRoot       string
	EnablePackfile bool
	MaxCacheSize   int
	CacheTTL       time.Duration
}

// LoggingConfig stores logging configuration
type LoggingConfig struct {
	Level       string
	Format      string
	Output      string
	Development bool
}

// ObservabilityConfig holds observability configuration
type ObservabilityConfig struct {
	Tracing TracingConfig
	Metrics MetricsConfig
	Health  HealthConfig
}

// TracingConfig holds tracing configuration
type TracingConfig struct {
	Enabled        bool
	Endpoint       string
	ServiceName    string
	ServiceVersion string
}

// MetricsConfig holds metrics configuration
type MetricsConfig struct {
	Enabled  bool
	Endpoint string
	Port     int
}

// HealthConfig holds health check configuration
type HealthConfig struct {
	LivenessTimeout  time.Duration
	ReadinessTimeout time.Duration
}

// SecurityConfig holds security-related configuration
type SecurityConfig struct {
	TLS         TLSConfig         `json:"tls"`
	RateLimit   RateLimitConfig   `json:"rate_limit"`
	DecisionJWT DecisionJWTConfig `json:"decision_jwt"`
}

// DecisionJWTConfig holds configuration for verifying authorization "decision" JWTs
// minted by the orchestrator (collabops-o2). A request that arrives through a trusted
// gateway (git-http / git-ssh) carries a short-lived, HS256-signed decision token in
// the X-Decision-JWT header. The executor verifies it to prove the request was
// already authorized upstream and was not delivered by a direct-bypass caller.
//
// Trust model: the JWT is symmetric (HS256), so Secret MUST match the orchestrator's
// DECISION_JWT_SECRET exactly (injected with the same value via Helm). The executor
// never mints tokens — it only verifies.
//
// Rollout is dual-accept by design:
//   - Secret == "" → verification is fully disabled (no-op). Nothing can be verified
//     before the shared secret is provisioned, so we must not reject. This is the
//     safe default that keeps existing traffic flowing during migration.
//   - Secret set, Required == false → verify-and-log only. Tokens are verified, but
//     NOTHING is ever rejected: missing, invalid, or non-attested all fall back to
//     legacy auth and emit a log. This must stay non-rejecting because upstream
//     legitimately relays tokens this verifier does not yet accept (e.g. git-http's
//     Layer-1 resolve path mints aud="api-service", not "git-executor"). The logs
//     measure rollout readiness — when they reach zero, strict mode is safe.
//   - Secret set, Required == true → strict. A missing, invalid, or non-attested
//     token is rejected. Flip this only after the fallback logs are clean AND every
//     relay path emits an accepted, attested token.
type DecisionJWTConfig struct {
	// Secret is the shared HS256 signing secret. Empty disables verification entirely.
	Secret string `json:"-"` // never serialize the secret
	// Required enables strict mode: reject requests lacking a valid, attested token.
	Required bool `json:"required"`
	// Audience is the expected `aud` claim (orchestrator ALLOWED_AUDIENCES entry).
	Audience string `json:"audience"`
	// Issuer is the expected `iss` claim.
	Issuer string `json:"issuer"`
	// LeewaySeconds tolerates clock skew between orchestrator and executor (exp/iat).
	LeewaySeconds int `json:"leeway_seconds"`
}

// TLSConfig holds TLS configuration
type TLSConfig struct {
	ReadTimeout       time.Duration `json:"read_timeout"`
	WriteTimeout      time.Duration `json:"write_timeout"`
	IdleTimeout       time.Duration `json:"idle_timeout"`
	ReadHeaderTimeout time.Duration `json:"read_header_timeout"`
}

// RateLimitConfig holds rate limiting configuration
type RateLimitConfig struct {
	RequestTimeout time.Duration `json:"request_timeout"`
}

// PermissionsConfig holds permission service configuration
type PermissionsConfig struct {
	APIServiceURL   string        `json:"api_service_url"`
	Timeout         time.Duration `json:"timeout"`
	MaxRetryDelay   time.Duration `json:"max_retry_delay"`
	RecoveryTimeout time.Duration `json:"recovery_timeout"`
}

type PerformanceConfig struct {
	MemoryCheckInterval time.Duration `json:"memory_check_interval"`
	ConnectionTimeout   time.Duration `json:"connection_timeout"`
}

// EventsConfig holds event publishing (event bus Redis) configuration.
//
// Canonical schema (EVENT_BUS_REDIS_*) — Python 서비스(orchestrator 등)와 동일.
// Redis ACL Phase B: 인프라가 EVENT_BUS_REDIS_USERNAME=app + PASSWORD=<secret> 주입.
type EventsConfig struct {
	Enabled       bool          `json:"enabled"`        // Whether to enable event publishing
	Mode          string        `json:"mode"`           // "standalone" | "cluster"
	NodeAddress   string        `json:"node_address"`   // host:port (cluster 면 csv)
	Username      string        `json:"-"`              // Redis ACL username (빈값=무인증)
	Password      string        `json:"-"`              // Redis ACL password
	SocketTimeout time.Duration `json:"socket_timeout"` // 소켓 read/write timeout

	// RedisURL 은 DEPRECATED 하위호환 경로 (EVENT_BUS_REDIS_NODE_ADDRESS 부재 시에만 사용).
	// helm/env 가 EVENT_BUS_REDIS_* 로 온보딩 완료되면 별도 PR 로 제거 예정.
	RedisURL string `json:"redis_url,omitempty"`
}

// RedisMode constants for EventsConfig.Mode.
const (
	RedisModeStandalone = "standalone"
	RedisModeCluster    = "cluster"
)

// OrchestratorConfig holds API orchestrator integration configuration
type OrchestratorConfig struct {
	BaseURL            string        `json:"base_url"`
	SSHKeyValidatePath string        `json:"ssh_key_validate_path"`
	Timeout            time.Duration `json:"timeout"`
	FingerprintHeader  string        `json:"fingerprint_header"`
	ForwardAuthHeader  bool          `json:"forward_auth_header"`
}

// MergeProtectionConfig holds merge protection feature flags and configuration
type MergeProtectionConfig struct {
	// Global protection enable/disable
	Enabled bool `json:"enabled"`

	// Environment file protection features
	EnvironmentProtection EnvironmentProtectionConfig `json:"environment_protection"`

	// Advanced protection features
	IntelligentMerging       bool `json:"intelligent_merging"`
	AdaptiveFallback         bool `json:"adaptive_fallback"`
	PredictiveConflictDetect bool `json:"predictive_conflict_detect"`

	// Observability and safety
	EnableMetrics    bool   `json:"enable_metrics"`
	EnableAuditLogs  bool   `json:"enable_audit_logs"`
	SafetyMode       string `json:"safety_mode"`       // "strict", "permissive", "disabled"
	FallbackStrategy string `json:"fallback_strategy"` // "preserve_target", "preserve_source", "manual"
}

// EnvironmentProtectionConfig holds environment-specific protection settings
type EnvironmentProtectionConfig struct {
	Enabled bool `json:"enabled"`

	// File pattern protection
	ProtectConfigFiles   bool `json:"protect_config_files"`
	ProtectEnvFiles      bool `json:"protect_env_files"`
	ProtectDockerFiles   bool `json:"protect_docker_files"`
	ProtectDatabaseFiles bool `json:"protect_database_files"`

	// Merge method specific protection
	EnableForSquashMerge  bool `json:"enable_for_squash_merge"`
	EnableForRegularMerge bool `json:"enable_for_regular_merge"`
	EnableForRebaseMerge  bool `json:"enable_for_rebase_merge"`

	// Protection strength
	StrictMode         bool     `json:"strict_mode"`          // Fail merge if protection can't be applied
	AllowedDeletePaths []string `json:"allowed_delete_paths"` // Paths that are safe to delete
	CriticalPaths      []string `json:"critical_paths"`       // Paths that must never be deleted
}

// parseAppEnv parses APP_ENV from the environment variable
func parseAppEnv() AppEnv {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	switch env {
	case "prod":
		return AppEnvProd
	case "stage":
		return AppEnvStage
	case "onprem":
		return AppEnvOnprem
	default:
		return AppEnvLocal
	}
}

// defaultLogLevelForEnv returns the default log level based on APP_ENV.
// local/stage 는 debug, onprem/prod 는 info.
func defaultLogLevelForEnv(env AppEnv) string {
	switch env {
	case AppEnvLocal, AppEnvStage:
		return "debug"
	default:
		return "info"
	}
}

// Load는 환경변수에서 설정을 로드합니다
func Load() (*Config, error) {

	appEnv := parseAppEnv()

	// ServerConfig, GitConfig는 환경변수에서 파싱합니다.
	serverConfig, err := parseServerConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to parse server config: %w", err)
	}

	gitConfig, err := parseGitConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to parse git config: %w", err)
	}

	// LoggingConfig: LOG_LEVEL env 우선, 미설정 시 APP_ENV 기반 default.
	// ObservabilityConfig 는 환경변수에서 파싱하지 않고 기본값 사용.
	loggingConfig := parseLoggingConfig(appEnv)
	observabilityConfig := parseObservabilityConfig()
	eventsConfig, err := parseEventsConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to parse events config: %w", err)
	}
	orchestratorConfig := parseOrchestratorConfig()
	mergeProtectionConfig := parseMergeProtectionConfig()
	lfsConfig := parseLFSConfig()
	maintenanceConfig := parseMaintenanceConfig()
	securityConfig := parseSecurityConfig()

	config := &Config{
		Env:             appEnv,
		Server:          serverConfig,
		Git:             gitConfig,
		Logging:         loggingConfig,
		Observability:   observabilityConfig,
		Events:          eventsConfig,
		Orchestrator:    orchestratorConfig,
		MergeProtection: mergeProtectionConfig,
		LFS:             lfsConfig,
		Maintenance:     maintenanceConfig,
		Security:        securityConfig,
	}

	// onprem 환경 오버라이드: 운영 + 내부망 제약 적용
	if appEnv.IsOnprem() {
		applyOnpremOverrides(config)
	}

	// 설정 유효성 검사
	if err := validate(config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return config, nil
}

// applyOnpremOverrides는 onprem 환경에서 필요한 설정을 강제 적용합니다.
// onprem = prod 수준의 보안/안정성 + 내부망 제약 (HTTPS 없음, 외부 서비스 접근 불가, 자체 인프라).
//
// 적용 조건: APP_ENV=onprem 일 때만 호출됨 (parseAppEnv().IsOnprem() 가 true).
// stage/prod 환경에서는 이 override 자체가 실행되지 않으므로 본 함수의 변경은
// onprem 환경에만 영향. validate() 에서는 backend 별 필수 필드 검증을 그대로 수행.
func applyOnpremOverrides(config *Config) {
	// API 문서 비활성화 (운영 환경 — 보안)
	config.Server.EnableDocs = false

	// LFS storage backend / metadata 는 git-executor 가 소유하지 않으므로
	// onprem override 도 필요 없음 (api-service + MinIO 가 처리, Issue-3052).

	// 외부 Observability exporter 비활성화 (내부망에서 외부 서비스 접근 불가)
	config.Observability.Tracing.Endpoint = ""
}

// ------------------------------------------------------------
// ServerConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseServerConfig() (ServerConfig, error) {

	// 필수 환경변수 파싱 — doc-360 §4.7 self-prefix 제거. legacy SERVER_*
	// 키는 helm-charts/values 가 Phase 2 에서 새 키로 갱신될 때까지 alias
	// fallback 으로 받는다 (deprecation warning emit). Phase 3 에서 제거.
	port := envWithAlias("PORT", "SERVER_PORT")
	if port == "" {
		return ServerConfig{}, fmt.Errorf("PORT environment variable is required but not set")
	}
	portInt, err := strconv.Atoi(port)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("PORT environment variable is not a valid integer: %w", err)
	}

	TCPGitPort := envWithAlias("TCP_GIT_PORT", "SERVER_TCP_GIT_PORT")
	if TCPGitPort == "" {
		return ServerConfig{}, fmt.Errorf("TCP_GIT_PORT environment variable is required but not set")
	}
	TCPGitPortInt, err := strconv.Atoi(TCPGitPort)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("TCP_GIT_PORT environment variable is not a valid integer: %w", err)
	}
	basePath := envWithAlias("BASE_PATH", "SERVER_BASE_PATH")
	if basePath == "" {
		return ServerConfig{}, fmt.Errorf("BASE_PATH environment variable is required but not set")
	}

	// 기본값 하드코딩
	host := "0.0.0.0"
	readTimeout := 30 * time.Second
	writeTimeout := 30 * time.Second
	idleTimeout := 120 * time.Second
	shutdownTimeout := 30 * time.Second
	healthCheckPath := "/health"
	enableDocs := true
	docsPath := "/api-docs"

	// Smart HTTP streaming idle deadline. Override via env for ops
	// tuning without code change. Default 5 minutes balances long
	// fetches against zombie-conn risk under saturation.
	gitProtocolIdleTimeout := 5 * time.Minute
	if raw := os.Getenv("GIT_PROTOCOL_IDLE_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return ServerConfig{}, fmt.Errorf("GIT_PROTOCOL_IDLE_TIMEOUT is not a valid duration: %w", err)
		}
		gitProtocolIdleTimeout = parsed
	}

	return ServerConfig{
		Port:                   portInt,
		TCPGitPort:             TCPGitPortInt,
		BasePath:               basePath,
		Host:                   host,
		ReadTimeout:            readTimeout,
		WriteTimeout:           writeTimeout,
		IdleTimeout:            idleTimeout,
		ShutdownTimeout:        shutdownTimeout,
		HealthCheckPath:        healthCheckPath,
		EnableDocs:             enableDocs,
		DocsPath:               docsPath,
		GitProtocolIdleTimeout: gitProtocolIdleTimeout,
	}, nil
}

// ------------------------------------------------------------
// GitConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseGitConfig() (GitConfig, error) {

	repoRoot := os.Getenv("GIT_REPO_ROOT")
	if repoRoot == "" {
		return GitConfig{}, fmt.Errorf("GIT_REPO_ROOT environment variable is required but not set")
	}

	// 기본값 하드코딩
	enablePackfile := true
	maxCacheSize := 100
	cacheTTL := 5 * time.Minute

	return GitConfig{
		RepoRoot:       repoRoot,
		EnablePackfile: enablePackfile,
		MaxCacheSize:   maxCacheSize,
		CacheTTL:       cacheTTL,
	}, nil
}

// ------------------------------------------------------------
// LoggingConfig 파싱 (하드코딩)
// ------------------------------------------------------------
func parseLoggingConfig(appEnv AppEnv) LoggingConfig {
	// Log level 은 APP_ENV 에서 직접 결정 (local/stage=debug, onprem/prod=info)
	level := defaultLogLevelForEnv(appEnv)
	format := "json"
	output := "stdout"
	development := false

	return LoggingConfig{
		Level:       level,
		Format:      format,
		Output:      output,
		Development: development,
	}
}

// ------------------------------------------------------------
// ObservabilityConfig 파싱 (하드코딩)
// ------------------------------------------------------------
func parseObservabilityConfig() ObservabilityConfig {

	// 기본값 하드코딩
	tracingEnabled := true
	tracingEndpoint := ""
	tracingServiceName := "collabops-git-executor"
	tracingServiceVersion := "1.0.0"
	metricsEnabled := true
	metricsEndpoint := ""
	metricsPort := 9090

	return ObservabilityConfig{
		Tracing: TracingConfig{
			Enabled:        tracingEnabled,
			Endpoint:       tracingEndpoint,
			ServiceName:    tracingServiceName,
			ServiceVersion: tracingServiceVersion,
		},
		Metrics: MetricsConfig{
			Enabled:  metricsEnabled,
			Endpoint: metricsEndpoint,
			Port:     metricsPort,
		},
	}
}

// defaultRedisSocketTimeout is used when EVENT_BUS_REDIS_SOCKET_TIMEOUT_S is unset
// or invalid. Mirrors the previously hardcoded 5s dial/ping budget.
const defaultRedisSocketTimeout = 5 * time.Second

// ------------------------------------------------------------
// EventsConfig 파싱 (환경변수 + 하드코딩)
//
// Canonical: EVENT_BUS_REDIS_* (Python 서비스와 동일 스키마, Redis ACL Phase B).
// 하위호환: EVENT_BUS_REDIS_NODE_ADDRESS 부재 시 기존 REDIS_URL 로 폴백하고
// stderr 에 deprecation 경고를 1회 출력한다.
// 둘 다 없으면 events 비활성 (테스트/로컬 환경).
// ------------------------------------------------------------
func parseEventsConfig() (EventsConfig, error) {
	// 1) Canonical EVENT_BUS_REDIS_* 스키마
	if nodeAddress := strings.TrimSpace(os.Getenv("EVENT_BUS_REDIS_NODE_ADDRESS")); nodeAddress != "" {
		mode := strings.ToLower(strings.TrimSpace(os.Getenv("EVENT_BUS_REDIS_MODE")))
		if mode == "" {
			mode = RedisModeStandalone
		}
		if mode != RedisModeStandalone && mode != RedisModeCluster {
			return EventsConfig{}, fmt.Errorf("EVENT_BUS_REDIS_MODE must be %q or %q, got %q",
				RedisModeStandalone, RedisModeCluster, mode)
		}

		socketTimeout := defaultRedisSocketTimeout
		if raw := strings.TrimSpace(os.Getenv("EVENT_BUS_REDIS_SOCKET_TIMEOUT_S")); raw != "" {
			seconds, err := strconv.ParseFloat(raw, 64)
			if err != nil || seconds <= 0 {
				return EventsConfig{}, fmt.Errorf("EVENT_BUS_REDIS_SOCKET_TIMEOUT_S must be a positive number (seconds), got %q", raw)
			}
			socketTimeout = time.Duration(seconds * float64(time.Second))
		}

		return EventsConfig{
			Enabled:       true,
			Mode:          mode,
			NodeAddress:   nodeAddress,
			Username:      os.Getenv("EVENT_BUS_REDIS_USERNAME"),
			Password:      os.Getenv("EVENT_BUS_REDIS_PASSWORD"),
			SocketTimeout: socketTimeout,
		}, nil
	}

	// 2) DEPRECATED 하위호환: REDIS_URL
	if redisURL := strings.TrimSpace(os.Getenv("REDIS_URL")); redisURL != "" {
		fmt.Fprintln(os.Stderr,
			`{"level":"warn","event":"env_var_deprecated","deprecated_key":"REDIS_URL","use_instead":"EVENT_BUS_REDIS_NODE_ADDRESS","note":"event bus Redis 설정은 EVENT_BUS_REDIS_* 스키마로 이전하세요 (Issue-3658)"}`)
		return EventsConfig{
			Enabled:       true,
			RedisURL:      redisURL,
			SocketTimeout: defaultRedisSocketTimeout,
		}, nil
	}

	// 3) 미설정 → events 비활성
	return EventsConfig{Enabled: false}, nil
}

// ------------------------------------------------------------
// OrchestratorConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseOrchestratorConfig() OrchestratorConfig {
	// Optional: if not set, orchestrator fallback is disabled
	baseURL := os.Getenv("ORCHESTRATOR_BASE_URL")
	sshKeyValidatePath := os.Getenv("ORCHESTRATOR_SSH_KEY_VALIDATE_PATH")
	if sshKeyValidatePath == "" {
		// Keep empty by default to avoid guessing endpoint
		sshKeyValidatePath = ""
	}

	timeout := 1500 * time.Millisecond
	if v := os.Getenv("ORCHESTRATOR_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	// Which header carries SSH key fingerprint from ssh-gateway to executor (optional)
	fingerprintHeader := os.Getenv("ORCHESTRATOR_SSH_FINGERPRINT_HEADER")
	if fingerprintHeader == "" {
		fingerprintHeader = "X-SSH-Key-Fingerprint"
	}

	forwardAuthHeader := true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("ORCHESTRATOR_FORWARD_AUTH_HEADER"))); v != "" {
		if v == "false" || v == "0" || v == "no" {
			forwardAuthHeader = false
		}
	}

	return OrchestratorConfig{
		BaseURL:            baseURL,
		SSHKeyValidatePath: sshKeyValidatePath,
		Timeout:            timeout,
		FingerprintHeader:  fingerprintHeader,
		ForwardAuthHeader:  forwardAuthHeader,
	}
}

// ------------------------------------------------------------
// MergeProtectionConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseMergeProtectionConfig() MergeProtectionConfig {
	// Parse main protection flags
	enabled := parseBoolFromEnv("MERGE_PROTECTION_ENABLED", true) // Default enabled
	intelligentMerging := parseBoolFromEnv("MERGE_PROTECTION_INTELLIGENT", true)
	adaptiveFallback := parseBoolFromEnv("MERGE_PROTECTION_ADAPTIVE", true)
	predictiveConflict := parseBoolFromEnv("MERGE_PROTECTION_PREDICTIVE", true)

	// Parse observability flags
	enableMetrics := parseBoolFromEnv("MERGE_PROTECTION_METRICS", true)
	enableAuditLogs := parseBoolFromEnv("MERGE_PROTECTION_AUDIT_LOGS", true)

	// Parse safety configuration
	safetyMode := os.Getenv("MERGE_PROTECTION_SAFETY_MODE")
	if safetyMode == "" {
		safetyMode = "strict" // Default to strict for production safety
	}

	fallbackStrategy := os.Getenv("MERGE_PROTECTION_FALLBACK_STRATEGY")
	if fallbackStrategy == "" {
		fallbackStrategy = "preserve_target" // Default to preserve target
	}

	// Parse environment protection config
	envProtectionConfig := EnvironmentProtectionConfig{
		Enabled: parseBoolFromEnv("MERGE_PROTECTION_ENV_ENABLED", true),

		// File type protection
		ProtectConfigFiles:   parseBoolFromEnv("MERGE_PROTECTION_CONFIG_FILES", true),
		ProtectEnvFiles:      parseBoolFromEnv("MERGE_PROTECTION_ENV_FILES", true),
		ProtectDockerFiles:   parseBoolFromEnv("MERGE_PROTECTION_DOCKER_FILES", true),
		ProtectDatabaseFiles: parseBoolFromEnv("MERGE_PROTECTION_DB_FILES", true),

		// Merge method protection
		EnableForSquashMerge:  parseBoolFromEnv("MERGE_PROTECTION_SQUASH", true),
		EnableForRegularMerge: parseBoolFromEnv("MERGE_PROTECTION_REGULAR", true),
		EnableForRebaseMerge:  parseBoolFromEnv("MERGE_PROTECTION_REBASE", true),

		// Protection strength
		StrictMode: parseBoolFromEnv("MERGE_PROTECTION_STRICT", true),

		// Parse allowed delete paths from environment
		AllowedDeletePaths: parseStringSliceFromEnv("MERGE_PROTECTION_ALLOWED_DELETES", []string{
			"tmp/**",
			"*.tmp",
			"build/**",
			"dist/**",
		}),

		// Parse critical paths from environment
		CriticalPaths: parseStringSliceFromEnv("MERGE_PROTECTION_CRITICAL_PATHS", []string{
			"config/env/**",
			"config/**/*.yml",
			"config/**/*.yaml",
			"docker-compose*.yml",
			"*.env",
		}),
	}

	return MergeProtectionConfig{
		Enabled:                  enabled,
		EnvironmentProtection:    envProtectionConfig,
		IntelligentMerging:       intelligentMerging,
		AdaptiveFallback:         adaptiveFallback,
		PredictiveConflictDetect: predictiveConflict,
		EnableMetrics:            enableMetrics,
		EnableAuditLogs:          enableAuditLogs,
		SafetyMode:               safetyMode,
		FallbackStrategy:         fallbackStrategy,
	}
}

// Decision JWT defaults — MUST mirror the orchestrator issuer
// (collabops-api-orchestrator app/services/authz/decision_jwt.py).
const (
	// defaultDecisionJWTIssuer is the orchestrator's DECISION_JWT_ISSUER.
	defaultDecisionJWTIssuer = "collabops-o2"
	// defaultDecisionJWTAudience is this service's entry in the orchestrator's
	// ALLOWED_AUDIENCES set.
	defaultDecisionJWTAudience = "git-executor"
	// defaultDecisionJWTLeewaySeconds mirrors DECISION_JWT_CLOCK_SKEW_SECONDS.
	defaultDecisionJWTLeewaySeconds = 5
)

// parseSecurityConfig assembles SecurityConfig from the environment.
//
// TLS / RateLimit currently rely on zero-value timeouts (unchanged behavior);
// only the decision-JWT verification layer is environment-driven.
//
// Secret resolution prefers SECURITY_DECISION_JWT_SECRET (this service's own
// namespace) and falls back to DECISION_JWT_SECRET so a single shared Helm secret
// value can be injected under either name without divergence from the orchestrator.
func parseSecurityConfig() SecurityConfig {
	secret := strings.TrimSpace(os.Getenv("SECURITY_DECISION_JWT_SECRET"))
	if secret == "" {
		secret = strings.TrimSpace(os.Getenv("DECISION_JWT_SECRET"))
	}

	audience := strings.TrimSpace(os.Getenv("SECURITY_DECISION_JWT_AUDIENCE"))
	if audience == "" {
		audience = defaultDecisionJWTAudience
	}

	issuer := strings.TrimSpace(os.Getenv("SECURITY_DECISION_JWT_ISSUER"))
	if issuer == "" {
		issuer = defaultDecisionJWTIssuer
	}

	return SecurityConfig{
		DecisionJWT: DecisionJWTConfig{
			Secret:        secret,
			Required:      parseBoolFromEnv("SECURITY_DECISION_JWT_REQUIRED", false),
			Audience:      audience,
			Issuer:        issuer,
			LeewaySeconds: defaultDecisionJWTLeewaySeconds,
		},
	}
}

// Helper function to parse boolean from environment with default
func parseBoolFromEnv(key string, defaultValue bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return defaultValue
	}
	return value == "true" || value == "1" || value == "yes" || value == "on"
}

// Helper function to parse string slice from environment with default
func parseStringSliceFromEnv(key string, defaultValue []string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	// Split by comma and trim spaces
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// ------------------------------------------------------------
// parseLFSConfig 는 git-executor 가 LFS 와 관련해서 알아야 할 단 하나의 정보
// — 외부 LFS HTTP API base URL — 만 읽어옵니다 (Issue-3052, 옵션 B).
//
// Storage backend / metadata DB / quota 설정은 모두 api-service 로 이전됐기
// 때문에 더 이상 git-executor 가 알 필요가 없습니다.
func parseLFSConfig() LFSConfig {
	return LFSConfig{
		ExternalURL: strings.TrimRight(os.Getenv("LFS_EXTERNAL_URL"), "/"),
	}
}

// MaintenanceConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseMaintenanceConfig() MaintenanceConfig {
	return MaintenanceConfig{
		Enabled: parseBoolFromEnv("GIT_MAINTENANCE_ENABLED", true),
	}
}

// validate는 설정 유효성 검사를 수행합니다
func validate(config *Config) error {
	var errors []string

	// 서버 설정 유효성 검사
	if config.Server.Port < 1 || config.Server.Port > 65535 {
		errors = append(errors, "server.port must be between 1 and 65535")
	}

	if config.Server.BasePath == "" {
		errors = append(errors, "server.base_path is required")
	}

	if !strings.HasPrefix(config.Server.BasePath, "/") {
		errors = append(errors, "server.base_path must start with '/'")
	}

	// HealthCheckPath must be explicitly set and non-empty
	if strings.TrimSpace(config.Server.HealthCheckPath) == "" {
		errors = append(errors, "server.health_check_path is required")
	}

	// Git 설정 유효성 검사
	if config.Git.RepoRoot == "" {
		errors = append(errors, "git.repo_root is required")
	}

	if config.Git.MaxCacheSize < 0 {
		errors = append(errors, "git.max_cache_size must be >= 0")
	}

	// 로깅 설정 유효성 검사
	validLogLevels := []string{"debug", "info", "warn", "error", "fatal"}
	logLevelValid := false
	for _, level := range validLogLevels {
		if config.Logging.Level == level {
			logLevelValid = true
			break
		}
	}
	if !logLevelValid {
		errors = append(errors, fmt.Sprintf("logging.level must be one of: %v", validLogLevels))
	}

	validLogFormats := []string{"json", "text"}
	logFormatValid := false
	for _, format := range validLogFormats {
		if config.Logging.Format == format {
			logFormatValid = true
			break
		}
	}
	if !logFormatValid {
		errors = append(errors, fmt.Sprintf("logging.format must be one of: %v", validLogFormats))
	}

	// Merge protection configuration validation
	validSafetyModes := []string{"strict", "permissive", "disabled"}
	safetyModeValid := false
	for _, mode := range validSafetyModes {
		if config.MergeProtection.SafetyMode == mode {
			safetyModeValid = true
			break
		}
	}
	if !safetyModeValid {
		errors = append(errors, fmt.Sprintf("merge_protection.safety_mode must be one of: %v", validSafetyModes))
	}

	validFallbackStrategies := []string{"preserve_target", "preserve_source", "manual"}
	fallbackStrategyValid := false
	for _, strategy := range validFallbackStrategies {
		if config.MergeProtection.FallbackStrategy == strategy {
			fallbackStrategyValid = true
			break
		}
	}
	if !fallbackStrategyValid {
		errors = append(errors, fmt.Sprintf("merge_protection.fallback_strategy must be one of: %v", validFallbackStrategies))
	}

	// LFSConfig 는 ExternalURL 하나만 옵션으로 받으므로 필수 검사 없음.

	if len(errors) > 0 {
		return fmt.Errorf("validation errors: %s", strings.Join(errors, "; "))
	}

	return nil
}
