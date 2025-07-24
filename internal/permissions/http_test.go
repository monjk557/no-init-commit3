package permissions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"errors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_makeHTTPRequest_Success(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request format
		expectedPath := "/permissions/user123/owner/repo"
		assert.Equal(t, expectedPath, r.URL.Path)
		assert.Equal(t, "op=read", r.URL.RawQuery)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "CollabOps-Git-Executor/1.0", r.Header.Get("User-Agent"))

		// Send success response
		response := APIResponse{Allowed: true}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           5 * time.Second,
		MaxRetries:        3,
		InitialRetryDelay: 100 * time.Millisecond,
		MaxRetryDelay:     1 * time.Second,
		CacheTTL:          5 * time.Minute,
		HTTPClient:        server.Client(),
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()
	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")
	assert.NoError(t, err)
	assert.True(t, hasAccess)
}

func TestClient_makeHTTPRequest_PermissionDenied(t *testing.T) {
	// Create test server that denies permission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := APIResponse{Allowed: false, Reason: "Insufficient permissions"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           5 * time.Second,
		MaxRetries:        3,
		InitialRetryDelay: 100 * time.Millisecond,
		MaxRetryDelay:     1 * time.Second,
		CacheTTL:          5 * time.Minute,
		HTTPClient:        server.Client(),
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()
	hasAccess, err := client.CheckWriteAccess(ctx, "user123", "owner", "repo")
	assert.NoError(t, err)
	assert.False(t, hasAccess)
}

func TestClient_buildAPIURL(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		userID      string
		repoOwner   string
		repoName    string
		operation   Operation
		expectedURL string
		expectError bool
	}{
		{
			name:        "valid URL construction",
			baseURL:     "http://api-service.local",
			userID:      "user123",
			repoOwner:   "owner",
			repoName:    "repo",
			operation:   OperationRead,
			expectedURL: "http://api-service.local/permissions/user123/owner/repo?op=read",
		},
		{
			name:        "URL with special characters",
			baseURL:     "http://api-service.local",
			userID:      "user@domain.com",
			repoOwner:   "org/team",
			repoName:    "repo-name_v2",
			operation:   OperationWrite,
			expectedURL: "http://api-service.local/permissions/user@domain.com/org%252Fteam/repo-name_v2?op=write",
		},
		{
			name:        "base URL with path",
			baseURL:     "http://api-service.local/api/v1",
			userID:      "user",
			repoOwner:   "owner",
			repoName:    "repo",
			operation:   OperationRead,
			expectedURL: "http://api-service.local/permissions/user/owner/repo?op=read",
		},
		{
			name:        "invalid base URL",
			baseURL:     "://invalid-url",
			userID:      "user",
			repoOwner:   "owner",
			repoName:    "repo",
			operation:   OperationRead,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{APIServiceURL: tt.baseURL}
			client := &Client{config: config}

			url, err := client.buildAPIURL(tt.userID, tt.repoOwner, tt.repoName, tt.operation)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedURL, url)
			}
		})
	}
}

func TestClient_HTTPRequestErrors(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError string
		expectRetry   bool
	}{
		{
			name:          "400 Bad Request - non-retryable",
			statusCode:    http.StatusBadRequest,
			responseBody:  "Bad request",
			expectedError: "permission check failed",
			expectRetry:   false,
		},
		{
			name:          "401 Unauthorized - non-retryable",
			statusCode:    http.StatusUnauthorized,
			responseBody:  "Unauthorized",
			expectedError: "permission check failed",
			expectRetry:   false,
		},
		{
			name:          "403 Forbidden - non-retryable",
			statusCode:    http.StatusForbidden,
			responseBody:  "Forbidden",
			expectedError: "permission check failed",
			expectRetry:   false,
		},
		{
			name:          "404 Not Found - non-retryable",
			statusCode:    http.StatusNotFound,
			responseBody:  "Not found",
			expectedError: "permission check failed",
			expectRetry:   false,
		},
		{
			name:          "429 Too Many Requests - retryable",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  "Rate limited",
			expectedError: "permission check failed",
			expectRetry:   true,
		},
		{
			name:          "500 Internal Server Error - retryable",
			statusCode:    http.StatusInternalServerError,
			responseBody:  "Internal error",
			expectedError: "permission check failed",
			expectRetry:   true,
		},
		{
			name:          "502 Bad Gateway - retryable",
			statusCode:    http.StatusBadGateway,
			responseBody:  "Bad gateway",
			expectedError: "permission check failed",
			expectRetry:   true,
		},
		{
			name:          "503 Service Unavailable - retryable",
			statusCode:    http.StatusServiceUnavailable,
			responseBody:  "Service unavailable",
			expectedError: "permission check failed",
			expectRetry:   true,
		},
		{
			name:          "504 Gateway Timeout - retryable",
			statusCode:    http.StatusGatewayTimeout,
			responseBody:  "Gateway timeout",
			expectedError: "permission check failed",
			expectRetry:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			config := &Config{
				APIServiceURL:     server.URL,
				Timeout:           1 * time.Second,
				MaxRetries:        2, // Small number for faster tests
				InitialRetryDelay: 10 * time.Millisecond,
				MaxRetryDelay:     50 * time.Millisecond,
				CacheTTL:          5 * time.Minute,
				HTTPClient:        server.Client(),
			}

			client, err := NewPermissionClient(config)
			require.NoError(t, err)
			defer client.Close()

			ctx := context.Background()
			hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")

			// Should always return an error and false for access
			assert.Error(t, err)
			assert.False(t, hasAccess)
			assert.Contains(t, err.Error(), tt.expectedError)

			// Check retry behavior
			if tt.expectRetry {
				// Should have made multiple requests
				assert.Greater(t, requestCount, 1, "Expected retries for status %d", tt.statusCode)
			} else {
				// Should have made only one request (no retries)
				assert.Equal(t, 1, requestCount, "Expected no retries for status %d", tt.statusCode)
			}

			// Verify error type
			var permErr *PermissionError
			assert.True(t, errors.As(err, &permErr))
			assert.Equal(t, tt.expectRetry, permErr.IsRetryable())
		})
	}
}

func TestClient_RequestTimeout(t *testing.T) {
	// Create server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // Delay longer than client timeout
		response := APIResponse{Allowed: true}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           50 * time.Millisecond, // Short timeout
		MaxRetries:        1,
		InitialRetryDelay: 10 * time.Millisecond,
		MaxRetryDelay:     50 * time.Millisecond,
		CacheTTL:          5 * time.Minute,
		HTTPClient:        &http.Client{Timeout: 50 * time.Millisecond},
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()
	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")

	assert.Error(t, err)
	assert.False(t, hasAccess)
	assert.Contains(t, strings.ToLower(err.Error()), "timeout")
}

func TestClient_ContextCancellation(t *testing.T) {
	// Create server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		response := APIResponse{Allowed: true}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           5 * time.Second,
		MaxRetries:        3,
		InitialRetryDelay: 50 * time.Millisecond,
		MaxRetryDelay:     200 * time.Millisecond,
		CacheTTL:          5 * time.Minute,
		HTTPClient:        server.Client(),
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	// Create context that cancels quickly
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")

	assert.Error(t, err)
	assert.False(t, hasAccess)
	assert.Contains(t, strings.ToLower(err.Error()), "context")
}

func TestClient_MalformedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"invalid": json`)) // Malformed JSON
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           5 * time.Second,
		MaxRetries:        1,
		InitialRetryDelay: 10 * time.Millisecond,
		MaxRetryDelay:     50 * time.Millisecond,
		CacheTTL:          5 * time.Minute,
		HTTPClient:        server.Client(),
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()
	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")

	assert.Error(t, err)
	assert.False(t, hasAccess)
	assert.Contains(t, err.Error(), "failed to parse JSON response")
}

func TestClient_RetryLogic(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount < 3 {
			// First two requests fail with retryable error
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Internal server error"))
			return
		}
		// Third request succeeds
		response := APIResponse{Allowed: true}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           5 * time.Second,
		MaxRetries:        3,
		InitialRetryDelay: 10 * time.Millisecond,
		MaxRetryDelay:     50 * time.Millisecond,
		CacheTTL:          5 * time.Minute,
		HTTPClient:        server.Client(),
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()
	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")

	assert.NoError(t, err)
	assert.True(t, hasAccess)
	assert.Equal(t, 3, requestCount, "Expected exactly 3 requests (2 failures + 1 success)")
}

func TestClient_RetryExhaustion(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Always return retryable error
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal server error"))
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           5 * time.Second,
		MaxRetries:        2,
		InitialRetryDelay: 10 * time.Millisecond,
		MaxRetryDelay:     50 * time.Millisecond,
		CacheTTL:          5 * time.Minute,
		HTTPClient:        server.Client(),
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()
	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")

	assert.Error(t, err)
	assert.False(t, hasAccess)
	assert.Equal(t, 3, requestCount, "Expected 3 requests (1 initial + 2 retries)")
	assert.Contains(t, err.Error(), "permission check failed")
}

func TestClient_CacheIntegrationWithHTTP(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		response := APIResponse{Allowed: true}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           5 * time.Second,
		MaxRetries:        3,
		InitialRetryDelay: 10 * time.Millisecond,
		MaxRetryDelay:     50 * time.Millisecond,
		CacheTTL:          100 * time.Millisecond, // Short TTL for testing
		HTTPClient:        server.Client(),
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()

	// First request - should hit the API
	hasAccess1, err1 := client.CheckReadAccess(ctx, "user123", "owner", "repo")
	assert.NoError(t, err1)
	assert.True(t, hasAccess1)
	assert.Equal(t, 1, requestCount)

	// Second request - should hit cache
	hasAccess2, err2 := client.CheckReadAccess(ctx, "user123", "owner", "repo")
	assert.NoError(t, err2)
	assert.True(t, hasAccess2)
	assert.Equal(t, 1, requestCount, "Should not make additional request due to cache")

	// Wait for cache to expire
	time.Sleep(150 * time.Millisecond)

	// Third request - cache expired, should hit API again
	hasAccess3, err3 := client.CheckReadAccess(ctx, "user123", "owner", "repo")
	assert.NoError(t, err3)
	assert.True(t, hasAccess3)
	assert.Equal(t, 2, requestCount, "Should make new request after cache expiry")
}
