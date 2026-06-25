package cache

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCacheConfig_Defaults(t *testing.T) {
	c := NewCacheConfig()
	require.NotNil(t, c)
	assert.Equal(t, DefaultMaxRepositories, c.MaxRepositories)
	assert.Equal(t, DefaultTTL, c.TTL)
	assert.Equal(t, DefaultMaxMemoryMB, c.MaxMemoryMB)
	assert.Equal(t, DefaultCleanupInterval, c.CleanupInterval)
	assert.Equal(t, DefaultEnabled, c.Enabled)
}

func TestCacheConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*CacheConfig)
		wantErr bool
		errPart string
	}{
		{
			name:    "valid defaults",
			mutate:  func(c *CacheConfig) {},
			wantErr: false,
		},
		{
			name:    "max repositories too low",
			mutate:  func(c *CacheConfig) { c.MaxRepositories = 0 },
			wantErr: true,
			errPart: "max_repositories must be at least",
		},
		{
			name:    "max repositories too high",
			mutate:  func(c *CacheConfig) { c.MaxRepositories = MaxMaxRepositories + 1 },
			wantErr: true,
			errPart: "max_repositories must be at most",
		},
		{
			name:    "ttl too low",
			mutate:  func(c *CacheConfig) { c.TTL = time.Second },
			wantErr: true,
			errPart: "ttl must be at least",
		},
		{
			name:    "ttl too high",
			mutate:  func(c *CacheConfig) { c.TTL = MaxTTL + time.Hour },
			wantErr: true,
			errPart: "ttl must be at most",
		},
		{
			name:    "negative memory",
			mutate:  func(c *CacheConfig) { c.MaxMemoryMB = -1 },
			wantErr: true,
			errPart: "max_memory_mb cannot be negative",
		},
		{
			name:    "memory too high",
			mutate:  func(c *CacheConfig) { c.MaxMemoryMB = MaxMaxMemoryMB + 1 },
			wantErr: true,
			errPart: "max_memory_mb must be at most",
		},
		{
			name:    "cleanup interval too low",
			mutate:  func(c *CacheConfig) { c.CleanupInterval = time.Second },
			wantErr: true,
			errPart: "cleanup_interval must be at least",
		},
		{
			name:    "cleanup interval too high",
			mutate:  func(c *CacheConfig) { c.CleanupInterval = MaxCleanupInterval + time.Hour },
			wantErr: true,
			errPart: "cleanup_interval must be at most",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCacheConfig()
			tt.mutate(c)
			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errPart)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCacheConfig_ApplyDefaults(t *testing.T) {
	c := &CacheConfig{}
	c.ApplyDefaults()
	assert.Equal(t, DefaultMaxRepositories, c.MaxRepositories)
	assert.Equal(t, DefaultTTL, c.TTL)
	assert.Equal(t, DefaultCleanupInterval, c.CleanupInterval)
	// MaxMemoryMB stays at 0 (unlimited).
	assert.Equal(t, 0, c.MaxMemoryMB)
}

func TestCacheConfig_ApplyDefaults_PreservesNonZero(t *testing.T) {
	c := &CacheConfig{
		MaxRepositories: 5,
		TTL:             2 * time.Minute,
		CleanupInterval: 2 * time.Minute,
		MaxMemoryMB:     128,
	}
	c.ApplyDefaults()
	assert.Equal(t, 5, c.MaxRepositories)
	assert.Equal(t, 2*time.Minute, c.TTL)
	assert.Equal(t, 2*time.Minute, c.CleanupInterval)
	assert.Equal(t, 128, c.MaxMemoryMB)
}

func TestCacheConfig_ValidateAndApplyDefaults(t *testing.T) {
	c := &CacheConfig{}
	require.NoError(t, c.ValidateAndApplyDefaults())
	assert.Equal(t, DefaultMaxRepositories, c.MaxRepositories)

	// Out-of-range value survives defaults and fails validation.
	bad := &CacheConfig{MaxRepositories: MaxMaxRepositories + 1, TTL: DefaultTTL, CleanupInterval: DefaultCleanupInterval}
	require.Error(t, bad.ValidateAndApplyDefaults())
}

func TestCacheConfig_String(t *testing.T) {
	c := NewCacheConfig()
	s := c.String()
	assert.True(t, strings.HasPrefix(s, "CacheConfig{"))
	assert.Contains(t, s, "MaxRepositories: 100")
	assert.Contains(t, s, "Enabled: true")
}

func TestCacheConfig_MemoryLimits(t *testing.T) {
	unlimited := &CacheConfig{MaxMemoryMB: 0}
	assert.False(t, unlimited.IsMemoryLimited())
	assert.Equal(t, int64(0), unlimited.MemoryLimitBytes())

	limited := &CacheConfig{MaxMemoryMB: 2}
	assert.True(t, limited.IsMemoryLimited())
	assert.Equal(t, int64(2*1024*1024), limited.MemoryLimitBytes())
}
