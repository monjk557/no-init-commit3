package core_test

import (
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCacheConfig(t *testing.T) {
	config := cache.NewCacheConfig()

	assert.Equal(t, cache.DefaultMaxRepositories, config.MaxRepositories)
	assert.Equal(t, cache.DefaultTTL, config.TTL)
	assert.Equal(t, cache.DefaultMaxMemoryMB, config.MaxMemoryMB)
	assert.Equal(t, cache.DefaultCleanupInterval, config.CleanupInterval)
	assert.Equal(t, cache.DefaultEnabled, config.Enabled)
}

func TestCacheConfig_Validate(t *testing.T) {
	tests := []struct {
		name      string
		config    *cache.CacheConfig
		wantError bool
		errorMsg  string
	}{
		{
			name:      "Valid default configuration",
			config:    cache.NewCacheConfig(),
			wantError: false,
		},
		{
			name: "Valid custom configuration",
			config: &cache.CacheConfig{
				MaxRepositories: 50,
				TTL:             15 * time.Minute,
				MaxMemoryMB:     512,
				CleanupInterval: 2 * time.Minute,
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
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCacheConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   *cache.CacheConfig
		expected *cache.CacheConfig
	}{
		{
			name: "All fields zero - apply all defaults",
			config: &cache.CacheConfig{
				MaxRepositories: 0,
				TTL:             0,
				MaxMemoryMB:     0,
				CleanupInterval: 0,
				Enabled:         false,
			},
			expected: &cache.CacheConfig{
				MaxRepositories: cache.DefaultMaxRepositories,
				TTL:             cache.DefaultTTL,
				MaxMemoryMB:     0, // 0 is valid for MaxMemoryMB (unlimited)
				CleanupInterval: cache.DefaultCleanupInterval,
				Enabled:         false, // false is a valid value for Enabled
			},
		},
		{
			name: "Partial fields set - apply defaults for unset fields",
			config: &cache.CacheConfig{
				MaxRepositories: 50,
				TTL:             0,
				MaxMemoryMB:     256,
				CleanupInterval: 3 * time.Minute,
				Enabled:         true,
			},
			expected: &cache.CacheConfig{
				MaxRepositories: 50,
				TTL:             cache.DefaultTTL,
				MaxMemoryMB:     256,
				CleanupInterval: 3 * time.Minute,
				Enabled:         true,
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
		config    *cache.CacheConfig
		wantError bool
		expected  *cache.CacheConfig
	}{
		{
			name: "Valid config with defaults needed",
			config: &cache.CacheConfig{
				MaxRepositories: 0, // Will be set to default
				TTL:             0, // Will be set to default
				MaxMemoryMB:     0, // 0 is valid (unlimited)
				CleanupInterval: 0, // Will be set to default
				Enabled:         true,
			},
			wantError: false,
			expected: &cache.CacheConfig{
				MaxRepositories: cache.DefaultMaxRepositories,
				TTL:             cache.DefaultTTL,
				MaxMemoryMB:     0,
				CleanupInterval: cache.DefaultCleanupInterval,
				Enabled:         true,
			},
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
	config := &cache.CacheConfig{
		MaxRepositories: 150,
		TTL:             20 * time.Minute,
		MaxMemoryMB:     512,
		CleanupInterval: 3 * time.Minute,
		Enabled:         true,
	}

	result := config.String()
	// Just verify it's not empty and contains some expected parts
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "150")
	assert.Contains(t, result, "20m")
	assert.Contains(t, result, "512")
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
			config := &cache.CacheConfig{MaxMemoryMB: tt.maxMemoryMB}
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
			config := &cache.CacheConfig{MaxMemoryMB: tt.maxMemoryMB}
			assert.Equal(t, tt.expected, config.MemoryLimitBytes())
		})
	}
}

func TestCacheConfig_EdgeCases(t *testing.T) {
	t.Run("Zero configuration", func(t *testing.T) {
		config := &cache.CacheConfig{}
		err := config.ValidateAndApplyDefaults()
		require.NoError(t, err)

		// All fields should have sensible defaults
		assert.Equal(t, cache.DefaultMaxRepositories, config.MaxRepositories)
		assert.Equal(t, cache.DefaultTTL, config.TTL)
		assert.Equal(t, cache.DefaultMaxMemoryMB, config.MaxMemoryMB)
		assert.Equal(t, cache.DefaultCleanupInterval, config.CleanupInterval)
		assert.Equal(t, false, config.Enabled) // Zero value for bool is false, which is valid
	})

	t.Run("Disabled cache configuration", func(t *testing.T) {
		config := &cache.CacheConfig{
			MaxRepositories: cache.DefaultMaxRepositories,
			TTL:             cache.DefaultTTL,
			MaxMemoryMB:     cache.DefaultMaxMemoryMB,
			CleanupInterval: cache.DefaultCleanupInterval,
			Enabled:         false,
		}
		assert.NoError(t, config.Validate())
		assert.False(t, config.Enabled)
	})
}
