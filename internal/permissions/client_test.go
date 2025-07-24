package permissions

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockHTTPClient implements HTTPClientInterface for testing.
type MockHTTPClient struct {
	DoFunc func(req *http.Request) (*http.Response, error)
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if m.DoFunc != nil {
		return m.DoFunc(req)
	}
	return nil, nil
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.Equal(t, "http://api-service.local", config.APIServiceURL)
	assert.Equal(t, 10*time.Second, config.Timeout)
	assert.Equal(t, 3, config.MaxRetries)
	assert.Equal(t, 100*time.Millisecond, config.InitialRetryDelay)
	assert.Equal(t, 5*time.Second, config.MaxRetryDelay)
	assert.Equal(t, 5*time.Minute, config.CacheTTL)
	assert.Nil(t, config.HTTPClient)
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid config",
			config:      DefaultConfig(),
			expectError: false,
		},
		{
			name: "empty API service URL",
			config: &Config{
				APIServiceURL:     "",
				Timeout:           10 * time.Second,
				MaxRetries:        3,
				InitialRetryDelay: 100 * time.Millisecond,
				MaxRetryDelay:     5 * time.Second,
				CacheTTL:          5 * time.Minute,
			},
			expectError: true,
			errorMsg:    "APIServiceURL is required",
		},
		{
			name: "zero timeout",
			config: &Config{
				APIServiceURL:     "http://api-service.local",
				Timeout:           0,
				MaxRetries:        3,
				InitialRetryDelay: 100 * time.Millisecond,
				MaxRetryDelay:     5 * time.Second,
				CacheTTL:          5 * time.Minute,
			},
			expectError: true,
			errorMsg:    "timeout must be positive",
		},
		{
			name: "negative max retries",
			config: &Config{
				APIServiceURL:     "http://api-service.local",
				Timeout:           10 * time.Second,
				MaxRetries:        -1,
				InitialRetryDelay: 100 * time.Millisecond,
				MaxRetryDelay:     5 * time.Second,
				CacheTTL:          5 * time.Minute,
			},
			expectError: true,
			errorMsg:    "MaxRetries must be non-negative",
		},
		{
			name: "zero initial retry delay",
			config: &Config{
				APIServiceURL:     "http://api-service.local",
				Timeout:           10 * time.Second,
				MaxRetries:        3,
				InitialRetryDelay: 0,
				MaxRetryDelay:     5 * time.Second,
				CacheTTL:          5 * time.Minute,
			},
			expectError: true,
			errorMsg:    "InitialRetryDelay must be positive",
		},
		{
			name: "zero max retry delay",
			config: &Config{
				APIServiceURL:     "http://api-service.local",
				Timeout:           10 * time.Second,
				MaxRetries:        3,
				InitialRetryDelay: 100 * time.Millisecond,
				MaxRetryDelay:     0,
				CacheTTL:          5 * time.Minute,
			},
			expectError: true,
			errorMsg:    "MaxRetryDelay must be positive",
		},
		{
			name: "zero cache TTL",
			config: &Config{
				APIServiceURL:     "http://api-service.local",
				Timeout:           10 * time.Second,
				MaxRetries:        3,
				InitialRetryDelay: 100 * time.Millisecond,
				MaxRetryDelay:     5 * time.Second,
				CacheTTL:          0,
			},
			expectError: true,
			errorMsg:    "CacheTTL must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewPermissionClient(t *testing.T) {
	t.Run("with default config", func(t *testing.T) {
		client, err := NewPermissionClient(nil)
		require.NoError(t, err)
		require.NotNil(t, client)

		// Verify that the client satisfies the interface
		var _ = client

		// Clean up
		err = client.Close()
		assert.NoError(t, err)
	})

	t.Run("with custom config", func(t *testing.T) {
		mockHTTPClient := &MockHTTPClient{}
		config := &Config{
			APIServiceURL:     "http://custom-api.local",
			Timeout:           5 * time.Second,
			MaxRetries:        2,
			InitialRetryDelay: 50 * time.Millisecond,
			MaxRetryDelay:     3 * time.Second,
			CacheTTL:          2 * time.Minute,
			HTTPClient:        mockHTTPClient,
		}

		client, err := NewPermissionClient(config)
		require.NoError(t, err)
		require.NotNil(t, client)

		// Verify that the client uses the provided config
		clientImpl, ok := client.(*Client)
		require.True(t, ok)
		assert.Equal(t, config.APIServiceURL, clientImpl.config.APIServiceURL)
		assert.Equal(t, config.Timeout, clientImpl.config.Timeout)
		assert.Equal(t, mockHTTPClient, clientImpl.httpClient)

		// Clean up
		err = client.Close()
		assert.NoError(t, err)
	})

	t.Run("with invalid config", func(t *testing.T) {
		config := &Config{
			APIServiceURL: "", // Invalid: empty URL
			Timeout:       10 * time.Second,
		}

		client, err := NewPermissionClient(config)
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "invalid configuration")
	})
}

func TestPermissionResult(t *testing.T) {
	now := time.Now()

	t.Run("cache key generation", func(t *testing.T) {
		result := &PermissionResult{
			UserID:    "user123",
			RepoOwner: "owner",
			RepoName:  "repo",
			Operation: OperationRead,
		}

		expected := "user123:owner:repo:read"
		assert.Equal(t, expected, result.CacheKey())
	})

	t.Run("expiration check", func(t *testing.T) {
		// Not expired
		result := &PermissionResult{
			ExpiresAt: now.Add(1 * time.Hour),
		}
		assert.False(t, result.IsExpired())

		// Expired
		result.ExpiresAt = now.Add(-1 * time.Hour)
		assert.True(t, result.IsExpired())

		// Exactly at expiration time (should be expired)
		result.ExpiresAt = now
		time.Sleep(1 * time.Millisecond) // Ensure some time has passed
		assert.True(t, result.IsExpired())
	})
}

func TestClientInputValidation(t *testing.T) {
	client, err := NewPermissionClient(DefaultConfig())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	ctx := context.Background()

	tests := []struct {
		name      string
		userID    string
		repoOwner string
		repoName  string
		expectErr string
	}{
		{
			name:      "empty userID",
			userID:    "",
			repoOwner: "owner",
			repoName:  "repo",
			expectErr: "userID cannot be empty",
		},
		{
			name:      "empty repoOwner",
			userID:    "user123",
			repoOwner: "",
			repoName:  "repo",
			expectErr: "repoOwner cannot be empty",
		},
		{
			name:      "empty repoName",
			userID:    "user123",
			repoOwner: "owner",
			repoName:  "",
			expectErr: "repoName cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test both read and write access methods
			_, err := client.CheckReadAccess(ctx, tt.userID, tt.repoOwner, tt.repoName)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)

			_, err = client.CheckWriteAccess(ctx, tt.userID, tt.repoOwner, tt.repoName)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)
		})
	}
}

func TestClientCacheOperations(t *testing.T) {
	client, err := NewPermissionClient(DefaultConfig())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Test cache invalidation (should not panic)
	client.InvalidateCache("user123", "owner", "repo")

	// Test cache clearing (should not panic)
	client.ClearCache()
}

func TestOperationConstants(t *testing.T) {
	assert.Equal(t, Operation("read"), OperationRead)
	assert.Equal(t, Operation("write"), OperationWrite)
}
