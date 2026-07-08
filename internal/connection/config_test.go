package connection

import (
	"os"
	"testing"
	"time"
)

func TestConfigManager_Defaults(t *testing.T) {
	config := NewConfigManager()

	if err := config.Validate(); err != nil {
		t.Errorf("Default configuration should be valid: %v", err)
	}

	// Check default timeout values
	if config.Timeouts.Handshake != 5*time.Second {
		t.Errorf("Expected handshake timeout 5s, got %v", config.Timeouts.Handshake)
	}

	if config.Timeouts.OperationMax != 30*time.Minute {
		t.Errorf("Expected operation max timeout 30m, got %v", config.Timeouts.OperationMax)
	}

	// Check default concurrency values (4Gi pod with bounded repacks; see
	// Issue-5071/5414).
	if config.Concurrency.MaxConcurrentClones != 12 {
		t.Errorf("Expected max concurrent clones 12, got %d", config.Concurrency.MaxConcurrentClones)
	}

	// Check default I/O values
	if config.IO.ReadBufferSize != 4096 {
		t.Errorf("Expected read buffer size 4096, got %d", config.IO.ReadBufferSize)
	}

	// Check monitoring defaults
	if !config.Monitoring.MetricsEnabled {
		t.Error("Metrics should be enabled by default")
	}

	if config.Monitoring.MetricsNamespace != "git_executor" {
		t.Errorf("Expected metrics namespace 'git_executor', got %s", config.Monitoring.MetricsNamespace)
	}
}

func TestConfigManager_Testing(t *testing.T) {
	config := NewConfigManagerForTesting()

	if !config.Testing.FastTimeouts {
		t.Error("Fast timeouts should be enabled for testing config")
	}

	// Test timeouts should be much shorter
	if config.Timeouts.Handshake >= 5*time.Second {
		t.Errorf("Testing handshake timeout should be < 5s, got %v", config.Timeouts.Handshake)
	}

	if config.Timeouts.StallThreshold >= 30*time.Second {
		t.Errorf("Testing stall threshold should be < 30s, got %v", config.Timeouts.StallThreshold)
	}

	// I/O buffers should be smaller for testing
	if config.IO.ReadBufferSize > 2048 {
		t.Errorf("Testing read buffer should be <= 2048, got %d", config.IO.ReadBufferSize)
	}
}

func TestConfigManager_EnvironmentOverrides(t *testing.T) {
	// Set environment variables
	originalValues := map[string]string{
		EnvHandshakeTimeout:    os.Getenv(EnvHandshakeTimeout),
		EnvMaxConcurrentClones: os.Getenv(EnvMaxConcurrentClones),
		EnvReadBufferSize:      os.Getenv(EnvReadBufferSize),
		EnvMetricsEnabled:      os.Getenv(EnvMetricsEnabled),
		EnvTestingFastTimeouts: os.Getenv(EnvTestingFastTimeouts),
	}

	// Set test values
	os.Setenv(EnvHandshakeTimeout, "2s")
	os.Setenv(EnvMaxConcurrentClones, "25")
	os.Setenv(EnvReadBufferSize, "8192")
	os.Setenv(EnvMetricsEnabled, "false")
	os.Setenv(EnvTestingFastTimeouts, "true")

	defer func() {
		// Restore original values
		for key, value := range originalValues {
			if value == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, value)
			}
		}
	}()

	config := NewConfigManagerFromEnv()

	// Check that environment overrides worked
	if config.Timeouts.Handshake != 2*time.Second {
		t.Errorf("Expected handshake timeout 2s from env, got %v", config.Timeouts.Handshake)
	}

	if config.Concurrency.MaxConcurrentClones != 25 {
		t.Errorf("Expected max concurrent clones 25 from env, got %d", config.Concurrency.MaxConcurrentClones)
	}

	if config.IO.ReadBufferSize != 8192 {
		t.Errorf("Expected read buffer size 8192 from env, got %d", config.IO.ReadBufferSize)
	}

	if config.Monitoring.MetricsEnabled {
		t.Error("Metrics should be disabled from env override")
	}

	if !config.Testing.FastTimeouts {
		t.Error("Fast timeouts should be enabled from env override")
	}
}

func TestConfigManager_Validation(t *testing.T) {
	config := NewConfigManager()

	// Test valid configuration
	if err := config.Validate(); err != nil {
		t.Errorf("Valid configuration should pass validation: %v", err)
	}

	// Test invalid handshake timeout
	config.Timeouts.Handshake = 0
	if err := config.Validate(); err == nil {
		t.Error("Zero handshake timeout should fail validation")
	}
	config.Timeouts.Handshake = 5 * time.Second

	// Test invalid operation timeout relationship
	config.Timeouts.OperationMax = config.Timeouts.OperationIdle
	if err := config.Validate(); err == nil {
		t.Error("OperationMax <= OperationIdle should fail validation")
	}
	config.Timeouts.OperationMax = 30 * time.Minute

	// Test invalid buffer sizes
	config.IO.ReadBufferSize = -1
	if err := config.Validate(); err == nil {
		t.Error("Negative read buffer size should fail validation")
	}
	config.IO.ReadBufferSize = 4096

	// Test invalid concurrency settings
	config.Concurrency.MaxConcurrentClones = 0
	if err := config.Validate(); err == nil {
		t.Error("Zero max concurrent clones should fail validation")
	}
}

func TestConfigManager_Conversion(t *testing.T) {
	config := NewConfigManager()

	// Test TimeoutPolicy conversion
	timeoutPolicy := config.ToTimeoutPolicy()
	if timeoutPolicy.HandshakeTimeout != config.Timeouts.Handshake {
		t.Error("TimeoutPolicy conversion mismatch for HandshakeTimeout")
	}

	// Test IOWrapperConfig conversion
	ioConfig := config.ToIOWrapperConfig()
	if ioConfig.ReadBufferSize != config.IO.ReadBufferSize {
		t.Error("IOWrapperConfig conversion mismatch for ReadBufferSize")
	}

	// Test ConcurrencyConfig conversion
	concurrencyConfig := config.ToConcurrencyConfig()
	if concurrencyConfig.MaxConcurrentClones != config.Concurrency.MaxConcurrentClones {
		t.Error("ConcurrencyConfig conversion mismatch for MaxConcurrentClones")
	}
}

func TestConfigManager_Summary(t *testing.T) {
	config := NewConfigManager()
	summary := config.GetSummary()

	// Check that summary contains expected keys
	expectedKeys := []string{"timeouts", "concurrency", "io", "monitoring", "testing"}
	for _, key := range expectedKeys {
		if _, exists := summary[key]; !exists {
			t.Errorf("Summary missing key: %s", key)
		}
	}

	// Check timeout values in summary
	timeouts, ok := summary["timeouts"].(map[string]interface{})
	if !ok {
		t.Fatal("Timeouts section should be a map")
	}

	if handshake, exists := timeouts["handshake"]; exists {
		if handshakeStr, ok := handshake.(string); ok {
			if handshakeStr != "5s" {
				t.Errorf("Expected handshake '5s' in summary, got '%s'", handshakeStr)
			}
		} else {
			t.Error("Handshake timeout should be a string in summary")
		}
	} else {
		t.Error("Summary should contain handshake timeout")
	}
}

func TestDefaultFunctionIntegration(t *testing.T) {
	// Test that the updated default functions work correctly
	timeoutPolicy := DefaultTimeoutPolicy()
	if timeoutPolicy.HandshakeTimeout != 5*time.Second {
		t.Errorf("DefaultTimeoutPolicy should use config manager, got %v", timeoutPolicy.HandshakeTimeout)
	}

	ioConfig := DefaultIOWrapperConfig()
	if ioConfig.ReadBufferSize != 4096 {
		t.Errorf("DefaultIOWrapperConfig should use config manager, got %d", ioConfig.ReadBufferSize)
	}

	concurrencyConfig := DefaultConcurrencyConfig()
	if concurrencyConfig.MaxConcurrentClones != 12 {
		t.Errorf("DefaultConcurrencyConfig should use config manager, got %d", concurrencyConfig.MaxConcurrentClones)
	}
}

func TestConfigFromFunction_WithNil(t *testing.T) {
	// Test that *FromConfig functions handle nil gracefully
	timeoutPolicy := TimeoutPolicyFromConfig(nil)
	if timeoutPolicy.HandshakeTimeout != 5*time.Second {
		t.Error("TimeoutPolicyFromConfig(nil) should return default")
	}

	ioConfig := IOWrapperConfigFromConfig(nil)
	if ioConfig.ReadBufferSize != 4096 {
		t.Error("IOWrapperConfigFromConfig(nil) should return default")
	}

	concurrencyConfig := ConcurrencyConfigFromConfig(nil)
	if concurrencyConfig.MaxConcurrentClones != 12 {
		t.Error("ConcurrencyConfigFromConfig(nil) should return default")
	}
}

func TestConfigFromFunction_WithCustomConfig(t *testing.T) {
	customConfig := NewConfigManager()
	customConfig.Timeouts.Handshake = 3 * time.Second
	customConfig.IO.ReadBufferSize = 8192
	customConfig.Concurrency.MaxConcurrentClones = 25

	// Test custom config usage
	timeoutPolicy := TimeoutPolicyFromConfig(customConfig)
	if timeoutPolicy.HandshakeTimeout != 3*time.Second {
		t.Error("TimeoutPolicyFromConfig should use custom config")
	}

	ioConfig := IOWrapperConfigFromConfig(customConfig)
	if ioConfig.ReadBufferSize != 8192 {
		t.Error("IOWrapperConfigFromConfig should use custom config")
	}

	concurrencyConfig := ConcurrencyConfigFromConfig(customConfig)
	if concurrencyConfig.MaxConcurrentClones != 25 {
		t.Error("ConcurrencyConfigFromConfig should use custom config")
	}
}

func BenchmarkConfigManager_Creation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		config := NewConfigManager()
		_ = config.ToTimeoutPolicy()
	}
}

func BenchmarkConfigManager_EnvironmentOverrides(b *testing.B) {
	// Set some environment variables
	os.Setenv(EnvHandshakeTimeout, "3s")
	os.Setenv(EnvMaxConcurrentClones, "30")
	defer func() {
		os.Unsetenv(EnvHandshakeTimeout)
		os.Unsetenv(EnvMaxConcurrentClones)
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		config := NewConfigManagerFromEnv()
		_ = config.Validate()
	}
}
