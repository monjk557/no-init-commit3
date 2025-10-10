package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
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
	Enabled         bool
	Endpoint        string
	Port            int
	CollectInterval time.Duration
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

// PerformanceConfig holds performance-related configuration
type PerformanceConfig struct {
	MemoryCheckInterval time.Duration `json:"memory_check_interval"`
	ConnectionTimeout   time.Duration `json:"connection_timeout"`
}

// HealthConfig holds health check configuration
type HealthConfig struct {
	LivenessTimeout  time.Duration `json:"liveness_timeout"`
	ReadinessTimeout time.Duration `json:"readiness_timeout"`
}

// Load는 환경변수에서 설정을 로드합니다
func Load(_ string) (*Config, error) {
	// 깨끗한 상태를 보장하기 위해 Viper 리셋
	viper.Reset()

	// 필수 환경변수들만 바인딩
	bindRequiredEnvVars()

	// 필수 환경변수 체크 - 없으면 바로 실패
	if err := validateRequiredEnvVars(); err != nil {
		return nil, err
	}

	// 선택적 환경변수 바인딩
	bindOptionalEnvVars()

	// 설정 구조체 생성 및 하드코딩된 기본값들 설정
	config := &Config{}
	setHardcodedDefaults(config)

	// 필수 환경변수 값들 설정
	config.Server.Port = viper.GetInt("server.port")
	config.Server.TCPGitPort = viper.GetInt("server.tcp_git_port")
	config.Server.Host = viper.GetString("server.host")
	config.Git.RepoRoot = viper.GetString("git.repo_root")

	// 선택적 환경변수 처리
	if endpoint := viper.GetString("observability.tracing.endpoint"); endpoint != "" {
		config.Observability.Tracing.Endpoint = endpoint
		config.Observability.Tracing.Enabled = true
	}
	if endpoint := viper.GetString("observability.metrics.endpoint"); endpoint != "" {
		config.Observability.Metrics.Endpoint = endpoint
		config.Observability.Metrics.Enabled = true
	}
	if port := viper.GetInt("observability.metrics.port"); port != 0 {
		config.Observability.Metrics.Port = port
	}

	// 설정 유효성 검사
	if err := validate(config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return config, nil
}

// setHardcodedDefaults는 하드코딩된 기본값들을 설정합니다
func setHardcodedDefaults(config *Config) {
	// 서버 하드코딩 기본값
	config.Server.BasePath = "/repos"
	config.Server.ReadTimeout = 30 * time.Second
	config.Server.WriteTimeout = 30 * time.Second
	config.Server.IdleTimeout = 120 * time.Second
	config.Server.ShutdownTimeout = 30 * time.Second
	config.Server.HealthCheckPath = "/health"
	config.Server.EnableDocs = true
	config.Server.DocsPath = "/api-docs"

	// Git 하드코딩 기본값
	config.Git.EnablePackfile = true
	config.Git.MaxCacheSize = 100
	config.Git.CacheTTL = 5 * time.Minute

	// 로깅 하드코딩 기본값
	config.Logging.Level = "info"
	config.Logging.Format = "json"
	config.Logging.Output = "stdout"
	config.Logging.Development = false

	// Observability 하드코딩 기본값
	config.Observability.Tracing.Enabled = false
	config.Observability.Tracing.ServiceName = "collabops-git-executor"
	config.Observability.Tracing.ServiceVersion = "1.0.0"
	config.Observability.Metrics.Enabled = false
	config.Observability.Metrics.Port = 9090
	config.Observability.Metrics.CollectInterval = 15 * time.Second
	config.Observability.Health.LivenessTimeout = 5 * time.Second
	config.Observability.Health.ReadinessTimeout = 10 * time.Second

	// Security 하드코딩 기본값
	config.Security.TLS.ReadTimeout = 15 * time.Second
	config.Security.TLS.WriteTimeout = 15 * time.Second
	config.Security.TLS.IdleTimeout = 60 * time.Second
	config.Security.TLS.ReadHeaderTimeout = 5 * time.Second
	config.Security.RateLimit.RequestTimeout = 30 * time.Second

	// Permissions 하드코딩 기본값
	config.Permissions.Timeout = 10 * time.Second
	config.Permissions.MaxRetryDelay = 5 * time.Second
	config.Permissions.RecoveryTimeout = 30 * time.Second

	// Performance 하드코딩 기본값
	config.Performance.MemoryCheckInterval = 30 * time.Second
	config.Performance.ConnectionTimeout = 30 * time.Second
}

// bindRequiredEnvVars는 필수 환경변수들만 바인딩합니다
func bindRequiredEnvVars() {
	viper.BindEnv("server.port", "PORT")
	viper.BindEnv("server.tcp_git_port", "TCP_PORT")
	viper.BindEnv("server.host", "HOST_NAME")
	viper.BindEnv("git.repo_root", "GIT_ROOT")
}

// bindOptionalEnvVars는 선택적 환경변수들을 바인딩합니다
func bindOptionalEnvVars() {
	viper.BindEnv("observability.tracing.endpoint", "TRACING_ENDPOINT")
	viper.BindEnv("observability.metrics.endpoint", "METRICS_ENDPOINT")
	viper.BindEnv("observability.metrics.port", "METRICS_PORT")
}

// validateRequiredEnvVars는 필수 환경변수들이 설정되었는지 체크합니다
func validateRequiredEnvVars() error {
	// Config key to environment key mapping
	requiredVarsMap := map[string]string{
		"server.port":         "PORT",
		"server.tcp_git_port": "TCP_PORT",
		"server.host":         "HOST_NAME",
		"git.repo_root":       "GIT_ROOT",
	}

	var missing []string
	for configKey, envKey := range requiredVarsMap {
		if !viper.IsSet(configKey) {
			missing = append(missing, envKey)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("required environment variables are missing: %s", strings.Join(missing, ", "))
	}

	return nil
}

// validate는 설정 유효성 검사를 수행합니다
func validate(config *Config) error {
	var errors []string

	// 서버 설정 유효성 검사
	if config.Server.Port < 1 || config.Server.Port > 65535 {
		errors = append(errors, "server.port must be between 1 and 65535")
	}

	if config.Server.TCPGitPort < 1 || config.Server.TCPGitPort > 65535 {
		errors = append(errors, "server.tcp_git_port must be between 1 and 65535")
	}

	if config.Server.BasePath == "" {
		errors = append(errors, "server.base_path is required")
	}

	if !strings.HasPrefix(config.Server.BasePath, "/") {
		errors = append(errors, "server.base_path must start with '/'")
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

// SetTestEnvVars는 테스트에 필요한 환경변수들을 설정합니다
func SetTestEnvVars(repoRoot string) {
	os.Setenv("PORT", "8080")
	os.Setenv("TCP_PORT", "5222")
	os.Setenv("HOST_NAME", "localhost")
	os.Setenv("GIT_ROOT", repoRoot)
}

// ClearTestEnvVars는 테스트용 환경변수들을 제거합니다
func ClearTestEnvVars() {
	os.Unsetenv("PORT")
	os.Unsetenv("TCP_PORT")
	os.Unsetenv("HOST_NAME")
	os.Unsetenv("GIT_ROOT")
}

// NewTestConfigFromLoad는 실제 config.Load()를 사용하여 테스트용 설정을 생성합니다
// 환경변수가 설정되어 있어야 성공하며, 없으면 fail-fast로 실패합니다
func NewTestConfigFromLoad(repoRoot string) (*Config, error) {
	// 테스트용 환경변수 설정
	SetTestEnvVars(repoRoot)

	// 실제 config.Load() 호출
	return Load("")
}

// NewTestConfig는 테스트용 설정을 직접 생성합니다 (환경변수 체크 우회)
// 중앙의 하드코딩된 기본값들을 사용하고, 테스트에 필요한 최소 필수값들만 파라미터로 받습니다
func NewTestConfig(repoRoot string) *Config {
	cfg := &Config{}

	// 중앙에서 관리하는 하드코딩된 기본값들 사용
	setHardcodedDefaults(cfg)

	// 테스트용 필수 환경변수 값들 (실제 환경변수 체크 우회)
	cfg.Server.Port = 8080        // 테스트용 포트
	cfg.Server.TCPGitPort = 5222  // 테스트용 TCP Git 포트
	cfg.Server.Host = "localhost" // 테스트용 호스트
	cfg.Git.RepoRoot = repoRoot   // 테스트마다 다른 임시 디렉토리

	return cfg
}

// OverrideForTest는 테스트용 설정 오버라이드 옵션입니다
type OverrideForTest func(*Config)

// WithPort는 서버 포트를 오버라이드합니다
func WithPort(port int) OverrideForTest {
	return func(c *Config) {
		c.Server.Port = port
	}
}

// WithRepoRoot는 Git 저장소 루트를 오버라이드합니다
func WithRepoRoot(repoRoot string) OverrideForTest {
	return func(c *Config) {
		c.Git.RepoRoot = repoRoot
	}
}

// WithLogging은 로깅 설정을 오버라이드합니다
func WithLogging(level, format string) OverrideForTest {
	return func(c *Config) {
		c.Logging.Level = level
		c.Logging.Format = format
	}
}

// NewTestConfigWithOverrides는 테스트용 설정을 생성하고 선택적으로 오버라이드합니다
func NewTestConfigWithOverrides(repoRoot string, overrides ...OverrideForTest) *Config {
	cfg := NewTestConfig(repoRoot)

	// 오버라이드 적용
	for _, override := range overrides {
		override(cfg)
	}

	return cfg
}
