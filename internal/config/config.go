package config

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// Config는 전체 애플리케이션 설정을 나타냅니다
type Config struct {
	Server        ServerConfig        `mapstructure:"server" validate:"required"`
	Git           GitConfig           `mapstructure:"git" validate:"required"`
	JWT           JWTConfig           `mapstructure:"jwt" validate:"required"`
	Logging       LoggingConfig       `mapstructure:"logging"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	mu            sync.RWMutex
}

// ServerConfig는 HTTP 서버 설정을 저장합니다
type ServerConfig struct {
	Port            int           `mapstructure:"port" validate:"required,min=1,max=65535"`
	TCPGitPort      int           `mapstructure:"tcp_git_port" validate:"min=1,max=65535"`
	BasePath        string        `mapstructure:"base_path" validate:"required"`
	Host            string        `mapstructure:"host"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	HealthCheckPath string        `mapstructure:"health_check_path"`
	EnableDocs      bool          `mapstructure:"enable_docs"`
	DocsPath        string        `mapstructure:"docs_path"`
}

// GitConfig는 Git 저장소 설정을 저장합니다
type GitConfig struct {
	RepoRoot       string        `mapstructure:"repo_root" validate:"required"`
	EnablePackfile bool          `mapstructure:"enable_packfile"`
	MaxCacheSize   int           `mapstructure:"max_cache_size"`
	CacheTTL       time.Duration `mapstructure:"cache_ttl"`
}

// JWTConfig는 JWT 인증 설정을 저장합니다
type JWTConfig struct {
	Algorithm     string `mapstructure:"algorithm" validate:"required,oneof=RS256 HS256 ES256"`
	PublicKeyPath string `mapstructure:"public_key_path"`
	SecretKey     string `mapstructure:"secret_key"`
}

// LoggingConfig는 로깅 설정을 저장합니다
type LoggingConfig struct {
	Level       string `mapstructure:"level"`
	Format      string `mapstructure:"format"`
	Output      string `mapstructure:"output"`
	Development bool   `mapstructure:"development"`
}

// ObservabilityConfig holds observability configuration
type ObservabilityConfig struct {
	Tracing TracingConfig `mapstructure:"tracing"`
	Metrics MetricsConfig `mapstructure:"metrics"`
}

// TracingConfig holds tracing configuration
type TracingConfig struct {
	Enabled        bool   `mapstructure:"enabled"`
	Endpoint       string `mapstructure:"endpoint"`
	ServiceName    string `mapstructure:"service_name"`
	ServiceVersion string `mapstructure:"service_version"`
}

// MetricsConfig holds metrics configuration
type MetricsConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Endpoint string `mapstructure:"endpoint"`
	Port     int    `mapstructure:"port"`
}

// PermissionsConfig holds permission service configuration
type PermissionsConfig struct {
	APIServiceURL string `mapstructure:"api_service_url"`
}

// Load는 Viper로 파일에서 설정을 로드합니다
func Load(configPath string) (*Config, error) {
	// 깨끗한 상태를 보장하기 위해 Viper 리셋
	viper.Reset()

	// 먼저 기본값 설정
	setDefaults()

	// Viper 설정
	viper.SetConfigType("yaml")
	viper.SetConfigName("config")

	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		// 설정 검색 경로 추가
		viper.AddConfigPath("./config")
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/collabops")
	}

	// 환경 변수 지원 활성화
	viper.SetEnvPrefix("COLLABOPS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// 설정 파일 읽기
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// 설정 파일을 찾을 수 없음; 기본값 사용
			// 참고: 아직 초기화되지 않았기 때문에 여기서 로거를 사용할 수 없음
		} else if strings.Contains(err.Error(), "no such file or directory") {
			// 특정 파일 경로가 존재하지 않음; 기본값 사용
			// 참고: 아직 초기화되지 않았기 때문에 여기서 로거를 사용할 수 없음
		} else {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// 설정 언마샬
	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// 설정 유효성 검사
	if err := validate(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

// setDefaults는 기본 설정 값을 설정합니다
func setDefaults() {
	// 서버 기본값
	viper.SetDefault("server.port", 5001)
	viper.SetDefault("server.tcp_git_port", 5222)
	viper.SetDefault("server.base_path", "/repos")
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("server.read_timeout", "30s")
	viper.SetDefault("server.write_timeout", "30s")
	viper.SetDefault("server.idle_timeout", "120s")

	// Git 기본값
	viper.SetDefault("git.repo_root", "/var/lib/git/repos")
	viper.SetDefault("git.enable_packfile", true)
	viper.SetDefault("git.max_cache_size", 100)
	viper.SetDefault("git.cache_ttl", "5m")

	// JWT 기본값
	viper.SetDefault("jwt.algorithm", "RS256")
	viper.SetDefault("jwt.public_key_path", "/etc/collabops/public.pem")

	// 서버 추가 기본값
	viper.SetDefault("server.shutdown_timeout", "30s")
	viper.SetDefault("server.health_check_path", "/health")
	viper.SetDefault("server.enable_docs", true)
	viper.SetDefault("server.docs_path", "/api-docs")

	// 로깅 기본값
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")
	viper.SetDefault("logging.output", "stdout")
	viper.SetDefault("logging.development", false)

	// Observability defaults
	viper.SetDefault("observability.tracing.enabled", false)
	viper.SetDefault("observability.tracing.endpoint", "")
	viper.SetDefault("observability.tracing.service_name", "collabops-git-executor")
	viper.SetDefault("observability.tracing.service_version", "1.0.0")
	viper.SetDefault("observability.metrics.enabled", false)
	viper.SetDefault("observability.metrics.endpoint", "")
	viper.SetDefault("observability.metrics.port", 9090)
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

	// Git 설정 유효성 검사
	if config.Git.RepoRoot == "" {
		errors = append(errors, "git.repo_root is required")
	}

	if config.Git.MaxCacheSize < 0 {
		errors = append(errors, "git.max_cache_size must be >= 0")
	}

	// JWT 설정 유효성 검사
	validAlgorithms := []string{"RS256", "HS256", "ES256"}
	algorithmValid := false
	for _, alg := range validAlgorithms {
		if config.JWT.Algorithm == alg {
			algorithmValid = true
			break
		}
	}
	if !algorithmValid {
		errors = append(errors, fmt.Sprintf("jwt.algorithm must be one of: %v", validAlgorithms))
	}

	// RS256과 ES256의 경우, 공개 키 경로 필요
	if (config.JWT.Algorithm == "RS256" || config.JWT.Algorithm == "ES256") && config.JWT.PublicKeyPath == "" {
		errors = append(errors, fmt.Sprintf("jwt.public_key_path is required for algorithm %s", config.JWT.Algorithm))
	}

	// HS256의 경우, 비밀 키 필요
	if config.JWT.Algorithm == "HS256" && config.JWT.SecretKey == "" {
		errors = append(errors, "jwt.secret_key is required for algorithm HS256")
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

// GetConfigPath는 사용 중인 설정 파일의 경로를 반환합니다
func GetConfigPath() string {
	return viper.ConfigFileUsed()
}

// GetServer는 서버 설정의 복사본을 반환합니다 (스레드 안전)
func (c *Config) GetServer() ServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Server
}

// GetGit은 git 설정의 복사본을 반환합니다 (스레드 안전)
func (c *Config) GetGit() GitConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Git
}

// GetJWT는 JWT 설정의 복사본을 반환합니다 (스레드 안전)
func (c *Config) GetJWT() JWTConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.JWT
}

// GetLogging은 로깅 설정의 복사본을 반환합니다 (스레드 안전)
func (c *Config) GetLogging() LoggingConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Logging
}

// GetObservability returns a copy of the observability configuration (thread-safe)
func (c *Config) GetObservability() ObservabilityConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Observability
}

// Reload reloads the configuration from file (thread-safe)
func (c *Config) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 설정 다시 읽기
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	// 유효성 검사를 위한 임시 설정 생성
	var newConfig Config
	if err := viper.Unmarshal(&newConfig); err != nil {
		return fmt.Errorf("failed to unmarshal reloaded config: %w", err)
	}

	// 새 설정 유효성 검사
	if err := validate(&newConfig); err != nil {
		return fmt.Errorf("reloaded config validation failed: %w", err)
	}

	// 필드 업데이트
	c.Server = newConfig.Server
	c.Git = newConfig.Git
	c.JWT = newConfig.JWT
	c.Logging = newConfig.Logging
	c.Observability = newConfig.Observability

	return nil
}
