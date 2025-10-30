package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config는 전체 애플리케이션 설정을 나타냅니다
type Config struct {
	Server        ServerConfig
	Git           GitConfig
	Logging       LoggingConfig
	Observability ObservabilityConfig
	Security      SecurityConfig
	Permissions   PermissionsConfig
	Performance   PerformanceConfig
}

// ServerConfig는 HTTP 서버 설정을 저장합니다
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

// GitConfig는 Git 저장소 설정을 저장합니다
type GitConfig struct {
	RepoRoot       string
	EnablePackfile bool
	MaxCacheSize   int
	CacheTTL       time.Duration
}

// LoggingConfig는 로깅 설정을 저장합니다
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

	config := &Config{
		Server:        serverConfig,
		Git:           gitConfig,
		Logging:       loggingConfig,
		Observability: observabilityConfig,
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

	if len(errors) > 0 {
		return fmt.Errorf("validation errors: %s", strings.Join(errors, "; "))
	}

	return nil
}
