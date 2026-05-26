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
	Blob            BlobConfig
}

// BlobConfig 는 blob preview 응답 가드 임계치입니다.
//
// /overview/blob 핸들러가 size / line-count / max-line-length 가드를 적용해
// 임계치 초과 시 Content 필드를 비우고 PreviewState sentinel 만 반환하도록
// 한다. 클라이언트는 sentinel 을 받으면 raw /blob streaming 으로 폴백한다.
// GitHub / GitLab 의 "서버가 미리보기 비용을 계산해 클라이언트에 contract 로
// 제공한다" 패턴을 따른다 (Issue-3144).
type BlobConfig struct {
	// PreviewMaxBytes 는 인라인 미리보기를 허용하는 최대 파일 바이트 수.
	// 초과 시 PreviewState=too_large.
	PreviewMaxBytes int64 `json:"preview_max_bytes"`

	// PreviewMaxLines 는 인라인 미리보기를 허용하는 최대 라인 수.
	// 초과 시 PreviewState=too_large.
	PreviewMaxLines int `json:"preview_max_lines"`

	// PreviewMaxLineLength 는 단일 라인의 최대 바이트 수.
	// 초과 시 PreviewState=long_lines. minified / inline sourcemap data URI
	// 같은 케이스를 잡는다.
	PreviewMaxLineLength int `json:"preview_max_line_length"`
}

// LFSConfig는 Git LFS 기능 설정입니다.
type LFSConfig struct {
	// 기능 활성화 여부
	Enabled bool `json:"enabled"`

	// 스토리지 백엔드 종류 ("gcs" 또는 "local")
	StorageBackend string `json:"storage_backend"`

	// GCS 버킷 이름 (StorageBackend=gcs일 때 필수)
	GCSBucket string `json:"gcs_bucket"`

	// GCP 프로젝트 ID (ADC 자동 감지 시 생략 가능)
	GCSProject string `json:"gcs_project"`

	// 로컬 스토리지 경로 (StorageBackend=local일 때 필수)
	LocalPath string `json:"local_path"`

	// git-executor 외부 접근 URL (StorageBackend=local일 때 필수)
	// 로컬 백엔드는 클라이언트가 이 URL을 통해 업로드/다운로드합니다.
	BaseURL string `json:"base_url"`

	// Signed URL / 로컬 URL 유효시간
	URLExpiry time.Duration `json:"url_expiry"`

	// 단일 오브젝트 최대 크기
	MaxObjectSize int64 `json:"max_object_size"`

	// 메타데이터 SQLite DB 경로
	MetadataDBPath string `json:"metadata_db_path"`

	// 워크스페이스별 스토리지 한도 (bytes, 0 = 무제한)
	StorageLimitPerOwner int64 `json:"storage_limit_per_owner"`

	// 워크스페이스별 월간 대역폭 한도 (bytes, 0 = 무제한)
	BandwidthLimitPerMonth int64 `json:"bandwidth_limit_per_month"`
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
	TLS       TLSConfig       `json:"tls"`
	RateLimit RateLimitConfig `json:"rate_limit"`
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

// EventsConfig holds event publishing configuration
type EventsConfig struct {
	RedisURL string `json:"redis_url"` // Redis connection URL (e.g., redis://localhost:6379/0)
	Enabled  bool   `json:"enabled"`   // Whether to enable event publishing
}

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
	lfsConfig := parseLFSConfig(gitConfig.RepoRoot)
	maintenanceConfig := parseMaintenanceConfig()
	blobConfig := parseBlobConfig()

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
		Blob:            blobConfig,
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

	// LFS 스토리지: onprem (air-gap) 환경에서는 GCS API endpoint (storage.googleapis.com)
	// 도달 불가능 → local backend 로 강제 변경.
	//
	// 주의:
	//   - 이 override 는 **onprem 환경 전용**. stage/prod 에서는 GCS backend 정상 동작.
	//   - MinIO (collabops-service-objstore) backend 는 onprem 에 deploy 되어 있어
	//     cluster-internal 도달 가능 → override 대상 아님. 향후 LFS_STORAGE_BACKEND=minio
	//     케이스가 추가되어도 이 분기에서 건드리지 않음.
	if config.LFS.Enabled && config.LFS.StorageBackend == "gcs" {
		config.LFS.StorageBackend = "local"
		config.LFS.GCSBucket = ""
		config.LFS.GCSProject = ""
	}

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

// ------------------------------------------------------------
// EventsConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseEventsConfig() (EventsConfig, error) {
	// Redis URL from environment variable (optional for testing)
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		// If REDIS_URL is not set, disable events (useful for testing environments)
		return EventsConfig{
			RedisURL: "",
			Enabled:  false,
		}, nil
	}
	// Enable events by default if REDIS_URL is set
	enabled := true

	return EventsConfig{
		RedisURL: redisURL,
		Enabled:  enabled,
	}, nil
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
// LFSConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseLFSConfig(repoRoot string) LFSConfig {
	enabled := parseBoolFromEnv("LFS_ENABLED", false)

	// 스토리지 백엔드 (기본: "local", air-gap 환경 우선)
	storageBackend := os.Getenv("LFS_STORAGE_BACKEND")
	if storageBackend == "" {
		storageBackend = "local"
	}

	gcsBucket := os.Getenv("LFS_GCS_BUCKET")
	gcsProject := os.Getenv("LFS_GCS_PROJECT")

	// 로컬 스토리지 경로 (기본: {repoRoot}/.lfs-objects)
	localPath := os.Getenv("LFS_LOCAL_PATH")
	if localPath == "" {
		localPath = repoRoot + "/.lfs-objects"
	}

	// 로컬 백엔드용 git-executor 외부 접근 URL
	baseURL := os.Getenv("LFS_BASE_URL")

	urlExpiry := 15 * time.Minute
	if v := os.Getenv("LFS_URL_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			urlExpiry = d
		}
	}

	// 단일 오브젝트 최대 5GB
	maxObjectSize := int64(5 * 1024 * 1024 * 1024)
	if v := os.Getenv("LFS_MAX_OBJECT_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxObjectSize = n
		}
	}

	metadataDBPath := os.Getenv("LFS_METADATA_DB_PATH")
	if metadataDBPath == "" {
		metadataDBPath = repoRoot + "/.lfs-meta.db"
	}

	// 워크스페이스당 스토리지 한도 (기본 10GB, 0 = 무제한)
	storageLimitPerOwner := int64(10 * 1024 * 1024 * 1024)
	if v := os.Getenv("LFS_STORAGE_LIMIT_PER_OWNER"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			storageLimitPerOwner = n
		}
	}

	// 월간 대역폭 한도 (기본 50GB, 0 = 무제한)
	bandwidthLimitPerMonth := int64(50 * 1024 * 1024 * 1024)
	if v := os.Getenv("LFS_BANDWIDTH_LIMIT_PER_MONTH"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			bandwidthLimitPerMonth = n
		}
	}

	return LFSConfig{
		Enabled:                enabled,
		StorageBackend:         storageBackend,
		GCSBucket:              gcsBucket,
		GCSProject:             gcsProject,
		LocalPath:              localPath,
		BaseURL:                baseURL,
		URLExpiry:              urlExpiry,
		MaxObjectSize:          maxObjectSize,
		MetadataDBPath:         metadataDBPath,
		StorageLimitPerOwner:   storageLimitPerOwner,
		BandwidthLimitPerMonth: bandwidthLimitPerMonth,
	}
}

// MaintenanceConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseMaintenanceConfig() MaintenanceConfig {
	return MaintenanceConfig{
		Enabled: parseBoolFromEnv("GIT_MAINTENANCE_ENABLED", true),
	}
}

// ------------------------------------------------------------
// BlobConfig 파싱 (환경변수 + 하드코딩 기본값)
// ------------------------------------------------------------
//
// 기본값 산정 근거 (Issue-3144):
//   - PreviewMaxBytes (256KB): syntax highlighter 가 안정적으로 처리하는
//     상한. GitHub UI 가 1MB, GitLab rendered 가 1MB 이지만 우리는 더
//     보수적으로 시작해 메트릭 기반으로 조정.
//   - PreviewMaxLines (10,000): TSX/JSX 토큰 span DOM 노드 폭증을 피하는
//     선. 그 이상은 viewport-only 렌더가 필요.
//   - PreviewMaxLineLength (4KB): minified / inline sourcemap data URI
//     케이스 (라인당 100KB~수 MB) 를 잡는 1차 방어선. 정상 source 의 한 줄은
//     보통 200 chars 미만이므로 4KB 도 매우 관대한 임계치.
func parseBlobConfig() BlobConfig {
	maxBytes := int64(256 * 1024)
	if v := os.Getenv("BLOB_PREVIEW_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxBytes = n
		}
	}

	maxLines := 10000
	if v := os.Getenv("BLOB_PREVIEW_MAX_LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxLines = n
		}
	}

	maxLineLen := 4096
	if v := os.Getenv("BLOB_PREVIEW_MAX_LINE_LENGTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxLineLen = n
		}
	}

	return BlobConfig{
		PreviewMaxBytes:      maxBytes,
		PreviewMaxLines:      maxLines,
		PreviewMaxLineLength: maxLineLen,
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

	// LFS 설정 유효성 검사
	if config.LFS.Enabled {
		switch config.LFS.StorageBackend {
		case "gcs":
			if config.LFS.GCSBucket == "" {
				errors = append(errors, "lfs.gcs_bucket is required when LFS storage backend is 'gcs'")
			}
		case "local":
			if config.LFS.LocalPath == "" {
				errors = append(errors, "lfs.local_path is required when LFS storage backend is 'local'")
			}
			if config.LFS.BaseURL == "" {
				errors = append(errors, "lfs.base_url is required when LFS storage backend is 'local'")
			}
		default:
			errors = append(errors, fmt.Sprintf("lfs.storage_backend must be 'gcs' or 'local', got '%s'", config.LFS.StorageBackend))
		}
		if config.LFS.MaxObjectSize <= 0 {
			errors = append(errors, "lfs.max_object_size must be > 0")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors: %s", strings.Join(errors, "; "))
	}

	return nil
}
