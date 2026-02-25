package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config represents the complete application configuration
type Config struct {
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

// Load는 환경변수에서 설정을 로드합니다
func Load() (*Config, error) {

	// ServerConfig, GitConfig는 환경변수에서 파싱합니다.
	serverConfig, err := parseServerConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to parse server config: %w", err)
	}

	gitConfig, err := parseGitConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to parse git config: %w", err)
	}

	// Loggint과 ObservabilityConfig는 환경변수에서 파싱하지 않고, 기본값을 사용합니다.
	loggingConfig := parseLoggingConfig()
	observabilityConfig := parseObservabilityConfig()
	eventsConfig, err := parseEventsConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to parse events config: %w", err)
	}
	orchestratorConfig := parseOrchestratorConfig()
	mergeProtectionConfig := parseMergeProtectionConfig()

	config := &Config{
		Server:          serverConfig,
		Git:             gitConfig,
		Logging:         loggingConfig,
		Observability:   observabilityConfig,
		Events:          eventsConfig,
		Orchestrator:    orchestratorConfig,
		MergeProtection: mergeProtectionConfig,
	}

	// 설정 유효성 검사
	if err := validate(config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return config, nil
}

// ------------------------------------------------------------
// ServerConfig 파싱 (환경변수 + 하드코딩)
// ------------------------------------------------------------
func parseServerConfig() (ServerConfig, error) {

	// 필수 환경변수 파싱
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		return ServerConfig{}, fmt.Errorf("SERVER_PORT environment variable is required but not set")
	}
	portInt, err := strconv.Atoi(port)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("SERVER_PORT environment variable is not a valid integer: %w", err)
	}

	TCPGitPort := os.Getenv("SERVER_TCP_GIT_PORT")
	if TCPGitPort == "" {
		return ServerConfig{}, fmt.Errorf("SERVER_TCP_GIT_PORT environment variable is required but not set")
	}
	TCPGitPortInt, err := strconv.Atoi(TCPGitPort)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("SERVER_TCP_GIT_PORT environment variable is not a valid integer: %w", err)
	}
	basePath := os.Getenv("SERVER_BASE_PATH")
	if basePath == "" {
		return ServerConfig{}, fmt.Errorf("SERVER_BASE_PATH environment variable is required but not set")
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

	return ServerConfig{
		Port:            portInt,
		TCPGitPort:      TCPGitPortInt,
		BasePath:        basePath,
		Host:            host,
		ReadTimeout:     readTimeout,
		WriteTimeout:    writeTimeout,
		IdleTimeout:     idleTimeout,
		ShutdownTimeout: shutdownTimeout,
		HealthCheckPath: healthCheckPath,
		EnableDocs:      enableDocs,
		DocsPath:        docsPath,
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
func parseLoggingConfig() LoggingConfig {

	// 기본값 하드코딩
	level := "info"
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

	if len(errors) > 0 {
		return fmt.Errorf("validation errors: %s", strings.Join(errors, "; "))
	}

	return nil
}
