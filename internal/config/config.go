package config

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// Config represents the complete application configuration
type Config struct {
	Server  ServerConfig  `mapstructure:"server" validate:"required"`
	Git     GitConfig     `mapstructure:"git" validate:"required"`
	JWT     JWTConfig     `mapstructure:"jwt" validate:"required"`
	Logging LoggingConfig `mapstructure:"logging"`
	mu      sync.RWMutex
}

// ServerConfig holds HTTP server configuration
type ServerConfig struct {
	Port            int           `mapstructure:"port" validate:"required,min=1,max=65535"`
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

// GitConfig holds Git repository configuration
type GitConfig struct {
	RepoRoot       string        `mapstructure:"repo_root" validate:"required"`
	EnablePackfile bool          `mapstructure:"enable_packfile"`
	MaxCacheSize   int           `mapstructure:"max_cache_size"`
	CacheTTL       time.Duration `mapstructure:"cache_ttl"`
}

// JWTConfig holds JWT authentication configuration
type JWTConfig struct {
	Algorithm     string `mapstructure:"algorithm" validate:"required,oneof=RS256 HS256 ES256"`
	PublicKeyPath string `mapstructure:"public_key_path"`
	SecretKey     string `mapstructure:"secret_key"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level       string `mapstructure:"level"`
	Format      string `mapstructure:"format"`
	Output      string `mapstructure:"output"`
	Development bool   `mapstructure:"development"`
}

// Load loads configuration from file with Viper
func Load(configPath string) (*Config, error) {
	// Reset Viper to ensure clean state
	viper.Reset()

	// Set defaults first
	setDefaults()

	// Configure Viper
	viper.SetConfigType("yaml")
	viper.SetConfigName("config")

	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		// Add config search paths
		viper.AddConfigPath("./config")
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/collabops")
	}

	// Enable environment variable support
	viper.SetEnvPrefix("COLLABOPS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Read configuration file
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found; use defaults
			// Note: Can't use logger here as it's not initialized yet
		} else if strings.Contains(err.Error(), "no such file or directory") {
			// Specific file path doesn't exist; use defaults
			// Note: Can't use logger here as it's not initialized yet
		} else {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// Unmarshal configuration
	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate configuration
	if err := validate(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

// setDefaults sets default configuration values
func setDefaults() {
	// Server defaults
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.base_path", "/repos")
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("server.read_timeout", "30s")
	viper.SetDefault("server.write_timeout", "30s")
	viper.SetDefault("server.idle_timeout", "120s")

	// Git defaults
	viper.SetDefault("git.repo_root", "/var/lib/git/repos")
	viper.SetDefault("git.enable_packfile", true)
	viper.SetDefault("git.max_cache_size", 100)
	viper.SetDefault("git.cache_ttl", "5m")

	// JWT defaults
	viper.SetDefault("jwt.algorithm", "RS256")
	viper.SetDefault("jwt.public_key_path", "/etc/collabops/public.pem")

	// Server additional defaults
	viper.SetDefault("server.shutdown_timeout", "30s")
	viper.SetDefault("server.health_check_path", "/health")
	viper.SetDefault("server.enable_docs", true)
	viper.SetDefault("server.docs_path", "/api-docs")

	// Logging defaults
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")
	viper.SetDefault("logging.output", "stdout")
	viper.SetDefault("logging.development", false)
}

// validate performs configuration validation
func validate(config *Config) error {
	var errors []string

	// Validate server configuration
	if config.Server.Port < 1 || config.Server.Port > 65535 {
		errors = append(errors, "server.port must be between 1 and 65535")
	}

	if config.Server.BasePath == "" {
		errors = append(errors, "server.base_path is required")
	}

	if !strings.HasPrefix(config.Server.BasePath, "/") {
		errors = append(errors, "server.base_path must start with '/'")
	}

	// Validate Git configuration
	if config.Git.RepoRoot == "" {
		errors = append(errors, "git.repo_root is required")
	}

	if config.Git.MaxCacheSize < 0 {
		errors = append(errors, "git.max_cache_size must be >= 0")
	}

	// Validate JWT configuration
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

	// For RS256 and ES256, public key path is required
	if (config.JWT.Algorithm == "RS256" || config.JWT.Algorithm == "ES256") && config.JWT.PublicKeyPath == "" {
		errors = append(errors, fmt.Sprintf("jwt.public_key_path is required for algorithm %s", config.JWT.Algorithm))
	}

	// For HS256, secret key is required
	if config.JWT.Algorithm == "HS256" && config.JWT.SecretKey == "" {
		errors = append(errors, "jwt.secret_key is required for algorithm HS256")
	}

	// Validate logging configuration
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

// GetConfigPath returns the path to the configuration file being used
func GetConfigPath() string {
	return viper.ConfigFileUsed()
}

// GetServer returns a copy of the server configuration (thread-safe)
func (c *Config) GetServer() ServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Server
}

// GetGit returns a copy of the git configuration (thread-safe)
func (c *Config) GetGit() GitConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Git
}

// GetJWT returns a copy of the JWT configuration (thread-safe)
func (c *Config) GetJWT() JWTConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.JWT
}

// GetLogging returns a copy of the logging configuration (thread-safe)
func (c *Config) GetLogging() LoggingConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Logging
}

// Reload reloads the configuration from file (thread-safe)
func (c *Config) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-read configuration
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	// Create temporary config to validate
	var newConfig Config
	if err := viper.Unmarshal(&newConfig); err != nil {
		return fmt.Errorf("failed to unmarshal reloaded config: %w", err)
	}

	// Validate new configuration
	if err := validate(&newConfig); err != nil {
		return fmt.Errorf("reloaded config validation failed: %w", err)
	}

	// Update fields
	c.Server = newConfig.Server
	c.Git = newConfig.Git
	c.JWT = newConfig.JWT
	c.Logging = newConfig.Logging

	return nil
}
