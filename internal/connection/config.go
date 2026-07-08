package connection

import (
	"os"
	"strconv"
	"time"
)

// ConfigManager manages centralized configuration for connection management
type ConfigManager struct {
	Timeouts    TimeoutConfig
	IO          IOConfig
	Concurrency ConcurrencyConfigSettings
	Monitoring  MonitoringConfig
	Testing     TestingConfig
}

// TimeoutConfig contains all timeout-related configuration
type TimeoutConfig struct {
	Handshake             time.Duration `json:"handshake" yaml:"handshake"`
	Auth                  time.Duration `json:"auth" yaml:"auth"`
	ProtocolNegotiation   time.Duration `json:"protocol_negotiation" yaml:"protocol_negotiation"`
	OperationIdle         time.Duration `json:"operation_idle" yaml:"operation_idle"`
	OperationMax          time.Duration `json:"operation_max" yaml:"operation_max"`
	StallThreshold        time.Duration `json:"stall_threshold" yaml:"stall_threshold"`
	ProgressCheckInterval time.Duration `json:"progress_check_interval" yaml:"progress_check_interval"`
}

// IOConfig contains I/O related configuration
type IOConfig struct {
	ReadBufferSize  int `json:"read_buffer_size" yaml:"read_buffer_size"`
	WriteBufferSize int `json:"write_buffer_size" yaml:"write_buffer_size"`
	FlushThreshold  int `json:"flush_threshold" yaml:"flush_threshold"`
}

// ConcurrencyConfigSettings contains concurrency control configuration
type ConcurrencyConfigSettings struct {
	MaxConcurrentClones int `json:"max_concurrent_clones" yaml:"max_concurrent_clones"`
	MaxConcurrentPushes int `json:"max_concurrent_pushes" yaml:"max_concurrent_pushes"`
	PerTenantLimit      int `json:"per_tenant_limit" yaml:"per_tenant_limit"`
	PerIPLimit          int `json:"per_ip_limit" yaml:"per_ip_limit"`
	QueueSize           int `json:"queue_size" yaml:"queue_size"`
}

// MonitoringConfig contains monitoring and metrics configuration
type MonitoringConfig struct {
	MetricsEnabled      bool          `json:"metrics_enabled" yaml:"metrics_enabled"`
	HealthCheckInterval time.Duration `json:"health_check_interval" yaml:"health_check_interval"`
	CleanupInterval     time.Duration `json:"cleanup_interval" yaml:"cleanup_interval"`
	MaxIdleDuration     time.Duration `json:"max_idle_duration" yaml:"max_idle_duration"`
	MetricsNamespace    string        `json:"metrics_namespace" yaml:"metrics_namespace"`
}

// TestingConfig contains configuration specific to testing environments
type TestingConfig struct {
	FastTimeouts       bool           `json:"fast_timeouts" yaml:"fast_timeouts"`
	MockDataSizes      map[string]int `json:"mock_data_sizes" yaml:"mock_data_sizes"`
	SimulateNetworkLag bool           `json:"simulate_network_lag" yaml:"simulate_network_lag"`
	NetworkLagDuration time.Duration  `json:"network_lag_duration" yaml:"network_lag_duration"`
}

// Environment variable keys
const (
	EnvHandshakeTimeout      = "GIT_EXECUTOR_HANDSHAKE_TIMEOUT"
	EnvAuthTimeout           = "GIT_EXECUTOR_AUTH_TIMEOUT"
	EnvProtocolNegTimeout    = "GIT_EXECUTOR_PROTOCOL_NEG_TIMEOUT"
	EnvOperationIdleTimeout  = "GIT_EXECUTOR_OPERATION_IDLE_TIMEOUT"
	EnvOperationMaxTimeout   = "GIT_EXECUTOR_OPERATION_MAX_TIMEOUT"
	EnvStallThreshold        = "GIT_EXECUTOR_STALL_THRESHOLD"
	EnvProgressCheckInterval = "GIT_EXECUTOR_PROGRESS_CHECK_INTERVAL"
	EnvMaxConcurrentClones   = "GIT_EXECUTOR_MAX_CONCURRENT_CLONES"
	EnvMaxConcurrentPushes   = "GIT_EXECUTOR_MAX_CONCURRENT_PUSHES"
	EnvPerTenantLimit        = "GIT_EXECUTOR_PER_TENANT_LIMIT"
	EnvPerIPLimit            = "GIT_EXECUTOR_PER_IP_LIMIT"
	EnvReadBufferSize        = "GIT_EXECUTOR_READ_BUFFER_SIZE"
	EnvWriteBufferSize       = "GIT_EXECUTOR_WRITE_BUFFER_SIZE"
	EnvMetricsEnabled        = "GIT_EXECUTOR_METRICS_ENABLED"
	EnvMetricsNamespace      = "GIT_EXECUTOR_METRICS_NAMESPACE"
	EnvHealthCheckInterval   = "GIT_EXECUTOR_HEALTH_CHECK_INTERVAL"
	EnvMaxIdleDuration       = "GIT_EXECUTOR_MAX_IDLE_DURATION"
	EnvTestingFastTimeouts   = "GIT_EXECUTOR_TESTING_FAST_TIMEOUTS"
)

// NewConfigManager creates a new configuration manager with defaults
func NewConfigManager() *ConfigManager {
	config := &ConfigManager{
		Timeouts:    getDefaultTimeouts(),
		IO:          getDefaultIOConfig(),
		Concurrency: getDefaultConcurrencyConfig(),
		Monitoring:  getDefaultMonitoringConfig(),
		Testing:     getDefaultTestingConfig(),
	}

	// Apply environment variable overrides
	config.applyEnvironmentOverrides()

	return config
}

// NewConfigManagerFromEnv creates configuration manager with environment overrides
func NewConfigManagerFromEnv() *ConfigManager {
	return NewConfigManager()
}

// NewConfigManagerForTesting creates configuration manager optimized for testing
func NewConfigManagerForTesting() *ConfigManager {
	config := NewConfigManager()

	// Override with fast testing values
	config.Testing.FastTimeouts = true
	config.Timeouts = getTestingTimeouts()
	config.IO.ReadBufferSize = 1024 // Smaller buffers for testing
	config.IO.WriteBufferSize = 1024
	config.Monitoring.HealthCheckInterval = 100 * time.Millisecond
	config.Monitoring.CleanupInterval = 500 * time.Millisecond

	return config
}

// getDefaultTimeouts returns production timeout configuration
func getDefaultTimeouts() TimeoutConfig {
	return TimeoutConfig{
		Handshake:             5 * time.Second,
		Auth:                  10 * time.Second,
		ProtocolNegotiation:   15 * time.Second,
		OperationIdle:         60 * time.Second,
		OperationMax:          30 * time.Minute,
		StallThreshold:        30 * time.Second,
		ProgressCheckInterval: 5 * time.Second,
	}
}

// getTestingTimeouts returns faster timeouts for testing
func getTestingTimeouts() TimeoutConfig {
	return TimeoutConfig{
		Handshake:             500 * time.Millisecond,
		Auth:                  1 * time.Second,
		ProtocolNegotiation:   2 * time.Second,
		OperationIdle:         5 * time.Second,
		OperationMax:          30 * time.Second,
		StallThreshold:        2 * time.Second,
		ProgressCheckInterval: 500 * time.Millisecond,
	}
}

// getDefaultIOConfig returns default I/O configuration
func getDefaultIOConfig() IOConfig {
	return IOConfig{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		FlushThreshold:  1024,
	}
}

// getDefaultConcurrencyConfig returns default concurrency configuration.
//
// Clone/push globals are sized against the container memory limit: each granted
// pack operation has a bounded RSS (see pack_limits.go, per-op ~200MB) and, as
// of Issue-5414, maintenance repacks are bounded the same way (executor.go), so
// the only unbounded consumers left are gone. The global cap × per-op ceiling
// must stay under the container limit with headroom for the server itself.
//
// 12 concurrent clones ≈ 2.4GB fits a 4Gi pod alongside bounded maintenance;
// the value was raised from 6 (Issue-5071's 2Gi-era guard) once bitmap pack
// reuse landed reliably (Issue-5273/5414) — reused clones cost far less than
// the ~200MB delta-search ceiling, and the pod limit moved 2Gi→4Gi. The prior
// default of 50 let unbounded pack-objects sum past the limit and OOM-kill the
// whole pod. Override per environment via GIT_EXECUTOR_MAX_CONCURRENT_CLONES /
// _PUSHES; raise alongside GIT_EXECUTOR_PACK_THREADS on higher-memory pods.
//
// PerIPLimit tracks the global clone cap rather than sitting below it: all Git
// traffic reaches the executor through the single git-http proxy IP, so a low
// per-IP cap would bind before the global one and 429 legitimate CI bursts
// (Issue-5414). Pushes stay at 6 — receive-pack's index-pack RSS is not bounded
// by the pack.* clone knobs, so it keeps the conservative ceiling.
func getDefaultConcurrencyConfig() ConcurrencyConfigSettings {
	return ConcurrencyConfigSettings{
		MaxConcurrentClones: 12,
		MaxConcurrentPushes: 6,
		PerTenantLimit:      12,
		PerIPLimit:          12,
		QueueSize:           100,
	}
}

// getDefaultMonitoringConfig returns default monitoring configuration
func getDefaultMonitoringConfig() MonitoringConfig {
	return MonitoringConfig{
		MetricsEnabled:      true,
		HealthCheckInterval: 30 * time.Second,
		CleanupInterval:     5 * time.Minute,
		MaxIdleDuration:     5 * time.Minute,
		MetricsNamespace:    "git_executor",
	}
}

// getDefaultTestingConfig returns default testing configuration
func getDefaultTestingConfig() TestingConfig {
	return TestingConfig{
		FastTimeouts: false,
		MockDataSizes: map[string]int{
			"small":  1024,
			"medium": 64 * 1024,
			"large":  1024 * 1024,
		},
		SimulateNetworkLag: false,
		NetworkLagDuration: 10 * time.Millisecond,
	}
}

// applyEnvironmentOverrides applies configuration overrides from environment variables
func (cm *ConfigManager) applyEnvironmentOverrides() {
	// Testing overrides first (so individual overrides can still take precedence)
	if val := os.Getenv(EnvTestingFastTimeouts); val != "" {
		if boolVal, err := strconv.ParseBool(val); err == nil {
			cm.Testing.FastTimeouts = boolVal
			if boolVal {
				cm.Timeouts = getTestingTimeouts()
			}
		}
	}

	// Timeout overrides (these take precedence over testing defaults)
	if val := os.Getenv(EnvHandshakeTimeout); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Timeouts.Handshake = duration
		}
	}

	if val := os.Getenv(EnvAuthTimeout); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Timeouts.Auth = duration
		}
	}

	if val := os.Getenv(EnvProtocolNegTimeout); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Timeouts.ProtocolNegotiation = duration
		}
	}

	if val := os.Getenv(EnvOperationIdleTimeout); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Timeouts.OperationIdle = duration
		}
	}

	if val := os.Getenv(EnvOperationMaxTimeout); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Timeouts.OperationMax = duration
		}
	}

	if val := os.Getenv(EnvStallThreshold); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Timeouts.StallThreshold = duration
		}
	}

	if val := os.Getenv(EnvProgressCheckInterval); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Timeouts.ProgressCheckInterval = duration
		}
	}

	// Concurrency overrides
	if val := os.Getenv(EnvMaxConcurrentClones); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil {
			cm.Concurrency.MaxConcurrentClones = intVal
		}
	}

	if val := os.Getenv(EnvMaxConcurrentPushes); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil {
			cm.Concurrency.MaxConcurrentPushes = intVal
		}
	}

	if val := os.Getenv(EnvPerTenantLimit); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil {
			cm.Concurrency.PerTenantLimit = intVal
		}
	}

	if val := os.Getenv(EnvPerIPLimit); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil {
			cm.Concurrency.PerIPLimit = intVal
		}
	}

	// I/O overrides
	if val := os.Getenv(EnvReadBufferSize); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil {
			cm.IO.ReadBufferSize = intVal
		}
	}

	if val := os.Getenv(EnvWriteBufferSize); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil {
			cm.IO.WriteBufferSize = intVal
		}
	}

	// Monitoring overrides
	if val := os.Getenv(EnvMetricsEnabled); val != "" {
		if boolVal, err := strconv.ParseBool(val); err == nil {
			cm.Monitoring.MetricsEnabled = boolVal
		}
	}

	if val := os.Getenv(EnvMetricsNamespace); val != "" {
		cm.Monitoring.MetricsNamespace = val
	}

	if val := os.Getenv(EnvHealthCheckInterval); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Monitoring.HealthCheckInterval = duration
		}
	}

	if val := os.Getenv(EnvMaxIdleDuration); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			cm.Monitoring.MaxIdleDuration = duration
		}
	}

}

// ToTimeoutPolicy converts config to TimeoutPolicy struct
func (cm *ConfigManager) ToTimeoutPolicy() *TimeoutPolicy {
	return &TimeoutPolicy{
		HandshakeTimeout:      cm.Timeouts.Handshake,
		AuthTimeout:           cm.Timeouts.Auth,
		ProtocolNegTimeout:    cm.Timeouts.ProtocolNegotiation,
		OperationIdleTimeout:  cm.Timeouts.OperationIdle,
		OperationMaxTimeout:   cm.Timeouts.OperationMax,
		StallThreshold:        cm.Timeouts.StallThreshold,
		ProgressCheckInterval: cm.Timeouts.ProgressCheckInterval,
	}
}

// ToIOWrapperConfig converts config to IOWrapperConfig struct
func (cm *ConfigManager) ToIOWrapperConfig() *IOWrapperConfig {
	return &IOWrapperConfig{
		ReadBufferSize:  cm.IO.ReadBufferSize,
		WriteBufferSize: cm.IO.WriteBufferSize,
	}
}

// ToConcurrencyConfig converts config to ConcurrencyConfig struct
func (cm *ConfigManager) ToConcurrencyConfig() *ConcurrencyConfig {
	return &ConcurrencyConfig{
		MaxConcurrentClones: cm.Concurrency.MaxConcurrentClones,
		MaxConcurrentPushes: cm.Concurrency.MaxConcurrentPushes,
		PerTenantLimit:      cm.Concurrency.PerTenantLimit,
		PerIPLimit:          cm.Concurrency.PerIPLimit,
		QueueSize:           cm.Concurrency.QueueSize,
	}
}

// Validate validates the configuration
func (cm *ConfigManager) Validate() error {
	// Validate timeouts
	if cm.Timeouts.Handshake <= 0 {
		return NewConfigurationError("handshake_timeout", cm.Timeouts.Handshake, "handshake timeout must be positive")
	}
	if cm.Timeouts.OperationMax <= cm.Timeouts.OperationIdle {
		return NewConfigurationError("operation_max_timeout", cm.Timeouts.OperationMax, "operation max timeout must be greater than idle timeout")
	}
	if cm.Timeouts.StallThreshold <= 0 {
		return NewConfigurationError("stall_threshold", cm.Timeouts.StallThreshold, "stall threshold must be positive")
	}

	// Validate I/O settings
	if cm.IO.ReadBufferSize <= 0 {
		return NewConfigurationError("read_buffer_size", cm.IO.ReadBufferSize, "read buffer size must be positive")
	}
	if cm.IO.WriteBufferSize <= 0 {
		return NewConfigurationError("write_buffer_size", cm.IO.WriteBufferSize, "write buffer size must be positive")
	}

	// Validate concurrency settings
	if cm.Concurrency.MaxConcurrentClones <= 0 {
		return NewConfigurationError("max_concurrent_clones", cm.Concurrency.MaxConcurrentClones, "max concurrent clones must be positive")
	}
	if cm.Concurrency.MaxConcurrentPushes <= 0 {
		return NewConfigurationError("max_concurrent_pushes", cm.Concurrency.MaxConcurrentPushes, "max concurrent pushes must be positive")
	}

	return nil
}

// GetSummary returns a human-readable summary of the configuration
func (cm *ConfigManager) GetSummary() map[string]interface{} {
	return map[string]interface{}{
		"timeouts": map[string]interface{}{
			"handshake":               cm.Timeouts.Handshake.String(),
			"auth":                    cm.Timeouts.Auth.String(),
			"protocol_negotiation":    cm.Timeouts.ProtocolNegotiation.String(),
			"operation_idle":          cm.Timeouts.OperationIdle.String(),
			"operation_max":           cm.Timeouts.OperationMax.String(),
			"stall_threshold":         cm.Timeouts.StallThreshold.String(),
			"progress_check_interval": cm.Timeouts.ProgressCheckInterval.String(),
		},
		"concurrency": map[string]interface{}{
			"max_concurrent_clones": cm.Concurrency.MaxConcurrentClones,
			"max_concurrent_pushes": cm.Concurrency.MaxConcurrentPushes,
			"per_tenant_limit":      cm.Concurrency.PerTenantLimit,
			"per_ip_limit":          cm.Concurrency.PerIPLimit,
			"queue_size":            cm.Concurrency.QueueSize,
		},
		"io": map[string]interface{}{
			"read_buffer_size":  cm.IO.ReadBufferSize,
			"write_buffer_size": cm.IO.WriteBufferSize,
		},
		"monitoring": map[string]interface{}{
			"metrics_enabled":       cm.Monitoring.MetricsEnabled,
			"metrics_namespace":     cm.Monitoring.MetricsNamespace,
			"health_check_interval": cm.Monitoring.HealthCheckInterval.String(),
			"max_idle_duration":     cm.Monitoring.MaxIdleDuration.String(),
		},
		"testing": map[string]interface{}{
			"fast_timeouts": cm.Testing.FastTimeouts,
		},
	}
}
