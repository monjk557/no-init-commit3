package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCacheConfig(t *testing.T) {
	config := NewCacheConfig()

	assert.Equal(t, DefaultMaxRepositories, config.MaxRepositories)
	assert.Equal(t, DefaultTTL, config.TTL)
	assert.Equal(t, DefaultMaxMemoryMB, config.MaxMemoryMB)
	assert.Equal(t, DefaultCleanupInterval, config.CleanupInterval)
	assert.Equal(t, DefaultEnabled, config.Enabled)
}

func TestCacheConfig_Validate(t *testing.T) {
	tests := []struct {
		name      string
		config    *CacheConfig
		wantError bool
		errorMsg  string
	}{
		{
			name:      "Valid default configuration",
			config:    NewCacheConfig(),
			wantError: false,
		},
		{
			name: "Valid custom configuration",
			config: &CacheConfig{
				MaxRepositories: 50,
				TTL:             15 * time.Minute,
				MaxMemoryMB:     512,
				CleanupInterval: 2 * time.Minute,
				Enabled:         true,
			},
			wantError: false,
		},
		{
			name: "MaxRepositories too low",
			config: &CacheConfig{
				MaxRepositories: 0,
				TTL:             DefaultTTL,
				MaxMemoryMB:     DefaultMaxMemoryMB,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "max_repositories must be at least 1",
		},
		{
			name: "MaxRepositories too high",
			config: &CacheConfig{
				MaxRepositories: 20000,
				TTL:             DefaultTTL,
				MaxMemoryMB:     DefaultMaxMemoryMB,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "max_repositories must be at most 10000",
		},
		{
			name: "TTL too short",
			config: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             30 * time.Second,
				MaxMemoryMB:     DefaultMaxMemoryMB,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "ttl must be at least 1m0s",
		},
		{
			name: "TTL too long",
			config: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             25 * time.Hour,
				MaxMemoryMB:     DefaultMaxMemoryMB,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "ttl must be at most 24h0m0s",
		},
		{
			name: "MaxMemoryMB negative",
			config: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             DefaultTTL,
				MaxMemoryMB:     -100,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "max_memory_mb cannot be negative",
		},
		{
			name: "MaxMemoryMB too high",
			config: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             DefaultTTL,
				MaxMemoryMB:     8192,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "max_memory_mb must be at most 4096",
		},
		{
			name: "CleanupInterval too short",
			config: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             DefaultTTL,
				MaxMemoryMB:     DefaultMaxMemoryMB,
				CleanupInterval: 30 * time.Second,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "cleanup_interval must be at least 1m0s",
		},
		{
			name: "CleanupInterval too long",
			config: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             DefaultTTL,
				MaxMemoryMB:     DefaultMaxMemoryMB,
				CleanupInterval: 2 * time.Hour,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "cleanup_interval must be at most 1h0m0s",
		},
		{
			name: "Multiple validation errors",
			config: &CacheConfig{
				MaxRepositories: 0,
				TTL:             30 * time.Second,
				MaxMemoryMB:     -100,
				CleanupInterval: 30 * time.Second,
				Enabled:         true,
			},
			wantError: true,
			errorMsg:  "cache configuration validation failed",
		},
		{
			name: "Zero MaxMemoryMB is valid (unlimited)",
			config: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             DefaultTTL,
				MaxMemoryMB:     0,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCacheConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   *CacheConfig
		expected *CacheConfig
	}{
		{
			name: "All fields zero - apply all defaults",
			config: &CacheConfig{
				MaxRepositories: 0,
				TTL:             0,
				MaxMemoryMB:     0,
				CleanupInterval: 0,
				Enabled:         false,
			},
			expected: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             DefaultTTL,
				MaxMemoryMB:     0, // 0 is valid for MaxMemoryMB (unlimited)
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         false, // false is a valid value for Enabled
			},
		},
		{
			name: "Partial fields set - apply defaults for unset fields",
			config: &CacheConfig{
				MaxRepositories: 50,
				TTL:             0,
				MaxMemoryMB:     256,
				CleanupInterval: 3 * time.Minute,
				Enabled:         true,
			},
			expected: &CacheConfig{
				MaxRepositories: 50,
				TTL:             DefaultTTL,
				MaxMemoryMB:     256,
				CleanupInterval: 3 * time.Minute,
				Enabled:         true,
			},
		},
		{
			name: "All fields set - no defaults applied",
			config: &CacheConfig{
				MaxRepositories: 200,
				TTL:             45 * time.Minute,
				MaxMemoryMB:     1024,
				CleanupInterval: 10 * time.Minute,
				Enabled:         false,
			},
			expected: &CacheConfig{
				MaxRepositories: 200,
				TTL:             45 * time.Minute,
				MaxMemoryMB:     1024,
				CleanupInterval: 10 * time.Minute,
				Enabled:         false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.ApplyDefaults()
			assert.Equal(t, tt.expected, tt.config)
		})
	}
}

func TestCacheConfig_ValidateAndApplyDefaults(t *testing.T) {
	tests := []struct {
		name      string
		config    *CacheConfig
		wantError bool
		expected  *CacheConfig
	}{
		{
			name: "Valid config with defaults needed",
			config: &CacheConfig{
				MaxRepositories: 0, // Will be set to default
				TTL:             0, // Will be set to default
				MaxMemoryMB:     0, // 0 is valid (unlimited)
				CleanupInterval: 0, // Will be set to default
				Enabled:         true,
			},
			wantError: false,
			expected: &CacheConfig{
				MaxRepositories: DefaultMaxRepositories,
				TTL:             DefaultTTL,
				MaxMemoryMB:     0,
				CleanupInterval: DefaultCleanupInterval,
				Enabled:         true,
			},
		},
		{
			name: "Invalid config even after defaults",
			config: &CacheConfig{
				MaxRepositories: -5, // Invalid even after defaults
				TTL:             0,  // Will be set to valid default
				MaxMemoryMB:     0,
				CleanupInterval: 0,
				Enabled:         true,
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateAndApplyDefaults()

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, tt.config)
			}
		})
	}
}

func TestCacheConfig_String(t *testing.T) {
	config := &CacheConfig{
		MaxRepositories: 150,
		TTL:             20 * time.Minute,
		MaxMemoryMB:     512,
		CleanupInterval: 3 * time.Minute,
		Enabled:         true,
	}

	result := config.String()
	expected := "CacheConfig{MaxRepositories: 150, TTL: 20m0s, MaxMemoryMB: 512, CleanupInterval: 3m0s, Enabled: true}"
	assert.Equal(t, expected, result)
}

func TestCacheConfig_IsMemoryLimited(t *testing.T) {
	tests := []struct {
		name        string
		maxMemoryMB int
		expected    bool
	}{
		{
			name:        "Unlimited memory (0)",
			maxMemoryMB: 0,
			expected:    false,
		},
		{
			name:        "Limited memory (512MB)",
			maxMemoryMB: 512,
			expected:    true,
		},
		{
			name:        "Limited memory (1MB)",
			maxMemoryMB: 1,
			expected:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &CacheConfig{MaxMemoryMB: tt.maxMemoryMB}
			assert.Equal(t, tt.expected, config.IsMemoryLimited())
		})
	}
}

func TestCacheConfig_MemoryLimitBytes(t *testing.T) {
	tests := []struct {
		name        string
		maxMemoryMB int
		expected    int64
	}{
		{
			name:        "Unlimited memory (0)",
			maxMemoryMB: 0,
			expected:    0,
		},
		{
			name:        "512 MB limit",
			maxMemoryMB: 512,
			expected:    512 * 1024 * 1024,
		},
		{
			name:        "1 MB limit",
			maxMemoryMB: 1,
			expected:    1024 * 1024,
		},
		{
			name:        "1024 MB (1GB) limit",
			maxMemoryMB: 1024,
			expected:    1024 * 1024 * 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &CacheConfig{MaxMemoryMB: tt.maxMemoryMB}
			assert.Equal(t, tt.expected, config.MemoryLimitBytes())
		})
	}
}

func TestCacheConfig_EdgeCases(t *testing.T) {
	t.Run("Boundary values validation", func(t *testing.T) {
		// Test minimum valid values
		config := &CacheConfig{
			MaxRepositories: MinMaxRepositories,
			TTL:             MinTTL,
			MaxMemoryMB:     0, // 0 is valid (unlimited)
			CleanupInterval: MinCleanupInterval,
			Enabled:         false,
		}
		assert.NoError(t, config.Validate())

		// Test maximum valid values
		config = &CacheConfig{
			MaxRepositories: MaxMaxRepositories,
			TTL:             MaxTTL,
			MaxMemoryMB:     MaxMaxMemoryMB,
			CleanupInterval: MaxCleanupInterval,
			Enabled:         true,
		}
		assert.NoError(t, config.Validate())
	})

	t.Run("Zero configuration", func(t *testing.T) {
		config := &CacheConfig{}
		err := config.ValidateAndApplyDefaults()
		require.NoError(t, err)

		// All fields should have sensible defaults
		assert.Equal(t, DefaultMaxRepositories, config.MaxRepositories)
		assert.Equal(t, DefaultTTL, config.TTL)
		assert.Equal(t, DefaultMaxMemoryMB, config.MaxMemoryMB)
		assert.Equal(t, DefaultCleanupInterval, config.CleanupInterval)
		assert.Equal(t, false, config.Enabled) // Zero value for bool is false, which is valid
	})

	t.Run("Disabled cache configuration", func(t *testing.T) {
		config := &CacheConfig{
			MaxRepositories: DefaultMaxRepositories,
			TTL:             DefaultTTL,
			MaxMemoryMB:     DefaultMaxMemoryMB,
			CleanupInterval: DefaultCleanupInterval,
			Enabled:         false,
		}
		assert.NoError(t, config.Validate())
		assert.False(t, config.Enabled)
	})
}
