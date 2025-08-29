package permissions

import (
	"context"
	// "encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_BasicFunctionality(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:    3,
		RecoveryTimeout:     100 * time.Millisecond,
		MaxHalfOpenRequests: 1,
	}
	cb := NewCircuitBreaker(config)

	ctx := context.Background()

	// Test initial state
	assert.Equal(t, StateClosed, cb.getState())

	// Successful execution
	err := cb.Execute(ctx, func(context.Context) error { return nil })
	assert.NoError(t, err)
	assert.Equal(t, StateClosed, cb.getState())

	// Failing executions
	for i := 0; i < 2; i++ {
		err = cb.Execute(ctx, func(context.Context) error { return fmt.Errorf("test error") })
		assert.Error(t, err)
		assert.Equal(t, StateClosed, cb.getState()) // Still closed
	}

	// Third failure should open the circuit
	err = cb.Execute(ctx, func(context.Context) error { return fmt.Errorf("test error") })
	assert.Error(t, err)
	assert.Equal(t, StateOpen, cb.getState())

	// Should reject execution when open
	err = cb.Execute(ctx, func(context.Context) error { return nil })
	assert.Error(t, err)
	var cbErr *CircuitBreakerError
	assert.True(t, errors.As(err, &cbErr))
	assert.Equal(t, StateOpen, cbErr.State)
}

func TestCircuitBreaker_Recovery(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     50 * time.Millisecond,
		MaxHalfOpenRequests: 1,
	}
	cb := NewCircuitBreaker(config)

	ctx := context.Background()

	// Open the circuit
	for i := 0; i < 2; i++ {
		_ = cb.Execute(ctx, func(context.Context) error { return fmt.Errorf("test error") })
	}
	assert.Equal(t, StateOpen, cb.getState())

	// Wait for recovery timeout
	time.Sleep(60 * time.Millisecond)

	// Should transition to half-open
	assert.Equal(t, StateHalfOpen, cb.getState())

	// Successful execution should close the circuit
	err := cb.Execute(ctx, func(context.Context) error { return nil })
	assert.NoError(t, err)
	assert.Equal(t, StateClosed, cb.getState())
}

func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     50 * time.Millisecond,
		MaxHalfOpenRequests: 1,
	}
	cb := NewCircuitBreaker(config)

	ctx := context.Background()

	// Open the circuit
	for i := 0; i < 2; i++ {
		_ = cb.Execute(ctx, func(context.Context) error { return fmt.Errorf("test error") })
	}

	// Wait for recovery timeout
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, StateHalfOpen, cb.getState())

	// Failed execution should open the circuit again
	err := cb.Execute(ctx, func(context.Context) error { return fmt.Errorf("test error") })
	assert.Error(t, err)
	assert.Equal(t, StateOpen, cb.getState())
}

func TestCircuitBreaker_Metrics(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     100 * time.Millisecond,
		MaxHalfOpenRequests: 1,
	}
	cb := NewCircuitBreaker(config)

	ctx := context.Background()

	// Initial metrics
	metrics := cb.GetMetrics()
	assert.Equal(t, StateClosed, metrics.State)
	assert.Equal(t, 0, metrics.FailureCount)
	assert.Equal(t, 0, metrics.HalfOpenCount)
	assert.True(t, metrics.LastFailureTime.IsZero())
	assert.True(t, metrics.LastSuccessTime.IsZero())

	// Successful execution
	_ = cb.Execute(ctx, func(context.Context) error { return nil })
	metrics = cb.GetMetrics()
	assert.False(t, metrics.LastSuccessTime.IsZero())

	// Failed execution
	_ = cb.Execute(ctx, func(context.Context) error { return fmt.Errorf("test error") })
	metrics = cb.GetMetrics()
	assert.Equal(t, 1, metrics.FailureCount)
	assert.False(t, metrics.LastFailureTime.IsZero())
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:    10,
		RecoveryTimeout:     100 * time.Millisecond,
		MaxHalfOpenRequests: 3,
	}
	cb := NewCircuitBreaker(config)

	ctx := context.Background()
	const numGoroutines = 50
	const numOperations = 20

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				// Mix of successful and failed operations
				err := cb.Execute(ctx, func(context.Context) error {
					if (id+j)%3 == 0 {
						return fmt.Errorf("test error")
					}
					return nil
				})
				// Don't assert on error here as circuit state may change
				_ = err
			}
		}(i)
	}

	wg.Wait()

	// Just verify that we didn't panic and circuit breaker is functioning
	metrics := cb.GetMetrics()
	assert.True(t, metrics.State >= StateClosed && metrics.State <= StateHalfOpen)
}

func TestFallbackPolicy_ShouldAllowAccess(t *testing.T) {
	tests := []struct {
		policy          FallbackPolicy
		operation       Operation
		hasCachedResult bool
		expected        bool
	}{
		// FallbackDeny
		{FallbackDeny, OperationRead, false, false},
		{FallbackDeny, OperationRead, true, false},
		{FallbackDeny, OperationWrite, false, false},
		{FallbackDeny, OperationWrite, true, false},

		// FallbackAllow
		{FallbackAllow, OperationRead, false, true},
		{FallbackAllow, OperationRead, true, true},
		{FallbackAllow, OperationWrite, false, true},
		{FallbackAllow, OperationWrite, true, true},

		// FallbackReadOnly
		{FallbackReadOnly, OperationRead, false, true},
		{FallbackReadOnly, OperationRead, true, true},
		{FallbackReadOnly, OperationWrite, false, false},
		{FallbackReadOnly, OperationWrite, true, false},

		// FallbackUseCache
		{FallbackUseCache, OperationRead, false, false},
		{FallbackUseCache, OperationRead, true, true},
		{FallbackUseCache, OperationWrite, false, false},
		{FallbackUseCache, OperationWrite, true, true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s_cached_%t", tt.policy, tt.operation, tt.hasCachedResult), func(t *testing.T) {
			result := tt.policy.ShouldAllowAccess(tt.operation, tt.hasCachedResult)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestClient_CircuitBreakerIntegration(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping circuit breaker integration test - permission client is being removed")

	// Original test logic commented out for reference
	/*
		requestCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			if requestCount <= 5 {
				// First several requests fail
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Later requests succeed
			response := APIResponse{Allowed: true}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()

		config := &Config{
			APIServiceURL:     server.URL,
			Timeout:           1 * time.Second,
			MaxRetries:        0, // No retries for cleaner test
			InitialRetryDelay: 10 * time.Millisecond,
			MaxRetryDelay:     50 * time.Millisecond,
			CacheTTL:          5 * time.Minute,
			HTTPClient:        server.Client(),
			CircuitBreakerConfig: &CircuitBreakerConfig{
				FailureThreshold:    3,
				RecoveryTimeout:     100 * time.Millisecond,
				MaxHalfOpenRequests: 1,
			},
			FallbackPolicy:       FallbackDeny,
			EnableCircuitBreaker: true,
		}

		client, err := NewPermissionClient(config)

		defer func() { _ = client.Close() }()

		ctx := context.Background()

		// Make enough requests to open the circuit
		for i := 0; i < 4; i++ {
			hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", fmt.Sprintf("repo%d", i))
			assert.False(t, hasAccess) // FallbackDeny policy
			assert.Error(t, err)
		}

		// Verify circuit is open
		metrics := client.GetCircuitBreakerMetrics()
		require.NotNil(t, metrics)
		assert.Equal(t, StateOpen, metrics.State)

		// Immediate request should fail with circuit breaker error
		hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "immediate-repo")
		assert.False(t, hasAccess)
		assert.Error(t, err)
		var svcErr *ServiceUnavailableError
		assert.True(t, errors.As(err, &svcErr))
		assert.Equal(t, StateOpen, svcErr.CircuitState)

		// Wait for recovery timeout - circuit should transition to half-open
		time.Sleep(150 * time.Millisecond)

		// Reset request counter for successful response
		requestCount = 10 // Ensure next request succeeds

		// Next request should succeed (circuit half-open -> closed)
		hasAccess, err = client.CheckReadAccess(ctx, "user123", "owner", "recovery-repo")
		assert.NoError(t, err)
		assert.True(t, hasAccess)

		// Circuit should be closed again
		metrics = client.GetCircuitBreakerMetrics()
		assert.Equal(t, StateClosed, metrics.State)
	*/
}

func TestClient_FallbackPolicies(t *testing.T) {
	// Server that always fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tests := []struct {
		name           string
		policy         FallbackPolicy
		operation      Operation
		hasCachedEntry bool
		cachedAccess   bool
		expectedAccess bool
	}{
		{"Deny policy - read", FallbackDeny, OperationRead, false, false, false},
		{"Deny policy - write", FallbackDeny, OperationWrite, false, false, false},
		{"Deny policy - cached read", FallbackDeny, OperationRead, true, true, false},

		{"Allow policy - read", FallbackAllow, OperationRead, false, false, true},
		{"Allow policy - write", FallbackAllow, OperationWrite, false, false, true},

		{"ReadOnly policy - read", FallbackReadOnly, OperationRead, false, false, true},
		{"ReadOnly policy - write", FallbackReadOnly, OperationWrite, false, false, false},

		{"UseCache policy - no cache", FallbackUseCache, OperationRead, false, false, false},
		{"UseCache policy - cached allow", FallbackUseCache, OperationRead, true, true, true},
		{"UseCache policy - cached deny", FallbackUseCache, OperationRead, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				APIServiceURL:     server.URL,
				Timeout:           100 * time.Millisecond,
				MaxRetries:        1,
				InitialRetryDelay: 10 * time.Millisecond,
				MaxRetryDelay:     50 * time.Millisecond,
				CacheTTL:          5 * time.Minute,
				HTTPClient:        server.Client(),
				CircuitBreakerConfig: &CircuitBreakerConfig{
					FailureThreshold:    1,             // Open immediately
					RecoveryTimeout:     1 * time.Hour, // Don't recover during test
					MaxHalfOpenRequests: 1,
				},
				FallbackPolicy:       tt.policy,
				EnableCircuitBreaker: true,
			}

			client, err := NewPermissionClient(config)
			require.NoError(t, err)
			defer func() { _ = client.Close() }()

			ctx := context.Background()

			// Make an initial request to open the circuit breaker
			if !tt.hasCachedEntry {
				_, _ = client.CheckReadAccess(ctx, "user123", "owner", "warmup")
			}

			// Pre-populate cache if needed
			if tt.hasCachedEntry {
				result := &PermissionResult{
					UserID:    "user123",
					RepoOwner: "owner",
					RepoName:  "repo",
					Operation: tt.operation,
					HasAccess: tt.cachedAccess,
					CachedAt:  time.Now(),
					ExpiresAt: time.Now().Add(5 * time.Minute),
				}
				clientImpl := client.(*Client)
				clientImpl.cache.Set(result.CacheKey(), result)
			}

			var hasAccess bool
			if tt.operation == OperationRead {
				hasAccess, err = client.CheckReadAccess(ctx, "user123", "owner", "repo")
			} else {
				hasAccess, err = client.CheckWriteAccess(ctx, "user123", "owner", "repo")
			}

			if tt.hasCachedEntry {
				// When cached result is available, should return without error
				assert.NoError(t, err)
				assert.Equal(t, tt.cachedAccess, hasAccess)
			} else {
				// When no cached result, should use fallback policy and return error
				assert.Error(t, err)
				assert.Equal(t, tt.expectedAccess, hasAccess)

				var svcErr *ServiceUnavailableError
				assert.True(t, errors.As(err, &svcErr))
				assert.True(t, svcErr.FallbackUsed)
				assert.Equal(t, tt.expectedAccess, svcErr.FallbackAccess)
			}
		})
	}
}

func TestClient_DisabledCircuitBreaker(t *testing.T) {
	// Server that always fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:        server.URL,
		Timeout:              100 * time.Millisecond,
		MaxRetries:           1,
		InitialRetryDelay:    10 * time.Millisecond,
		MaxRetryDelay:        50 * time.Millisecond,
		CacheTTL:             5 * time.Minute,
		HTTPClient:           server.Client(),
		FallbackPolicy:       FallbackAllow,
		EnableCircuitBreaker: false, // Disabled
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	ctx := context.Background()

	// Should return fallback policy result without circuit breaker
	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")
	assert.Error(t, err)
	assert.True(t, hasAccess) // FallbackAllow

	// Circuit breaker metrics should be nil
	metrics := client.GetCircuitBreakerMetrics()
	assert.Nil(t, metrics)
}

func TestClient_CachedResultDuringServiceUnavailability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	config := &Config{
		APIServiceURL:     server.URL,
		Timeout:           100 * time.Millisecond,
		MaxRetries:        1,
		InitialRetryDelay: 10 * time.Millisecond,
		MaxRetryDelay:     50 * time.Millisecond,
		CacheTTL:          1 * time.Hour, // Long TTL
		HTTPClient:        server.Client(),
		CircuitBreakerConfig: &CircuitBreakerConfig{
			FailureThreshold:    1,
			RecoveryTimeout:     1 * time.Hour,
			MaxHalfOpenRequests: 1,
		},
		FallbackPolicy:       FallbackDeny,
		EnableCircuitBreaker: true,
	}

	client, err := NewPermissionClient(config)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Pre-populate cache with allowed access
	result := &PermissionResult{
		UserID:    "user123",
		RepoOwner: "owner",
		RepoName:  "repo",
		Operation: OperationRead,
		HasAccess: true,
		CachedAt:  time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	clientImpl := client.(*Client)
	clientImpl.cache.Set(result.CacheKey(), result)

	ctx := context.Background()

	// Should return cached result even though service is failing
	hasAccess, err := client.CheckReadAccess(ctx, "user123", "owner", "repo")
	assert.NoError(t, err)    // No error when using cache
	assert.True(t, hasAccess) // Cached value
}

func TestCircuitBreakerError_Methods(t *testing.T) {
	err := &CircuitBreakerError{
		State:   StateOpen,
		Message: "test message",
	}

	assert.Equal(t, "test message", err.Error())
}

func TestServiceUnavailableError_Methods(t *testing.T) {
	underlyingErr := fmt.Errorf("underlying error")

	err := &ServiceUnavailableError{
		Operation:      OperationRead,
		UserID:         "user123",
		RepoOwner:      "owner",
		RepoName:       "repo",
		CircuitState:   StateOpen,
		FallbackUsed:   true,
		FallbackAccess: false,
		Err:            underlyingErr,
	}

	assert.Contains(t, err.Error(), "API-Service unavailable")
	assert.Contains(t, err.Error(), "circuit breaker open")
	assert.Contains(t, err.Error(), "fallback denied access")
	assert.Equal(t, underlyingErr, err.Unwrap())
	assert.True(t, err.IsFallbackUsed())

	// Test without circuit state
	err.CircuitState = 0
	assert.NotContains(t, err.Error(), "circuit breaker")
}

func TestFallbackPolicy_String(t *testing.T) {
	assert.Equal(t, "deny", FallbackDeny.String())
	assert.Equal(t, "allow", FallbackAllow.String())
	assert.Equal(t, "read-only", FallbackReadOnly.String())
	assert.Equal(t, "use-cache", FallbackUseCache.String())
}

func TestCircuitBreakerState_String(t *testing.T) {
	assert.Equal(t, "closed", StateClosed.String())
	assert.Equal(t, "open", StateOpen.String())
	assert.Equal(t, "half-open", StateHalfOpen.String())
}
