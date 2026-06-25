package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRepositoryCache(t *testing.T) {
	tests := []struct {
		name   string
		config *CacheConfig
	}{
		{name: "nil config falls back to defaults", config: nil},
		{name: "explicit config", config: NewCacheConfig()},
		{
			name: "custom valid config",
			config: &CacheConfig{
				MaxRepositories: 10,
				TTL:             2 * time.Minute,
				CleanupInterval: time.Minute,
				Enabled:         true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewRepositoryCache(tt.config)
			require.NoError(t, err)
			require.NotNil(t, c)
			t.Cleanup(func() { _ = c.Close() })

			// Returned value satisfies the interface and is usable.
			var _ RepositoryCache = c
			m := c.GetMetrics()
			assert.Equal(t, int64(0), m.Hits)
		})
	}
}

func TestNewRepositoryCache_InvalidConfig(t *testing.T) {
	bad := &CacheConfig{
		MaxRepositories: -5, // invalid, fails validation
		TTL:             DefaultTTL,
		CleanupInterval: DefaultCleanupInterval,
	}
	c, err := NewRepositoryCache(bad)
	require.Error(t, err)
	assert.Nil(t, c)
	assert.Contains(t, err.Error(), "failed to create LRU cache")
}

func TestNewDefaultRepositoryCache(t *testing.T) {
	c, err := NewDefaultRepositoryCache()
	require.NoError(t, err)
	require.NotNil(t, c)
	t.Cleanup(func() { _ = c.Close() })

	var _ RepositoryCache = c
}
