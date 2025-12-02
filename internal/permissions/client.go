// Package permissions provides secure repository access permission verification
// by integrating with an external API-Service. It includes caching, retry logic,
// and graceful handling of service unavailability.
//
// The package is designed to be thread-safe and production-ready, with
// comprehensive error handling and observability features.
package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// PermissionClient는 저장소 권한 검증을 위한 인터페이스입니다.
// 특정 저장소에 대한 사용자의 읽기 및 쓰기 권한을 검사하는 메소드를 제공합니다.
type PermissionClient interface {
	// CheckReadAccess verifies if a user has read access to a repository.
	// Returns true if access is allowed, false if denied.
	// Returns an error if the permission check cannot be completed.
	CheckReadAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error)

	// CheckWriteAccess verifies if a user has write access to a repository.
	// Returns true if access is allowed, false if denied.
	// Returns an error if the permission check cannot be completed.
	CheckWriteAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error)

	// InvalidateCache removes cached permission results for a specific user and repository.
	// This is useful when permissions are known to have changed.
	InvalidateCache(userID, repoOwner, repoName string)

	// ClearCache removes all cached permission results.
	ClearCache()

	// GetCircuitBreakerMetrics returns metrics about the circuit breaker state.
	// Returns nil if circuit breaker is disabled.
	GetCircuitBreakerMetrics() *CircuitBreakerMetrics

	// Close gracefully shuts down the client, cleaning up resources.
	Close() error
}

// Config는 권한 클라이언트의 설정 옵션을 담는 구조체입니다.
type Config struct {
	// APIServiceURL is the base URL of the API-Service.
	// Example: "http://api-service.local"
	APIServiceURL string

	// Timeout is the maximum duration for HTTP requests.
	// Default: 10 seconds
	Timeout time.Duration

	// MaxRetries is the maximum number of retry attempts for failed requests.
	// Default: 3
	MaxRetries int

	// InitialRetryDelay is the initial delay before the first retry.
	// Default: 100ms
	InitialRetryDelay time.Duration

	// MaxRetryDelay is the maximum delay between retries.
	// Default: 5 seconds
	MaxRetryDelay time.Duration

	// CacheTTL is the time-to-live for cached permission results.
	// Default: 5 minutes
	CacheTTL time.Duration

	// HTTPClient allows injection of a custom HTTP client for testing.
	// If nil, a default client will be created.
	HTTPClient HTTPClientInterface

	// CircuitBreakerConfig configures the circuit breaker behavior.
	// If nil, a default configuration will be used.
	CircuitBreakerConfig *CircuitBreakerConfig

	// FallbackPolicy defines the behavior when the API-Service is unavailable.
	// Default: FallbackUseCache
	FallbackPolicy FallbackPolicy

	// EnableCircuitBreaker enables or disables circuit breaker functionality.
	// Default: true
	EnableCircuitBreaker bool
}

// HTTPClientInterface는 테스트를 위해 HTTP 클라이언트의 인터페이스를 정의합니다.
type HTTPClientInterface interface {
	Do(req *http.Request) (*http.Response, error)
}

// DefaultConfig returns a configuration with sensible default values.
func DefaultConfig() *Config {
	return &Config{
		APIServiceURL:        "http://api-service.local",
		Timeout:              10 * time.Second,
		MaxRetries:           3,
		InitialRetryDelay:    100 * time.Millisecond,
		MaxRetryDelay:        5 * time.Second,
		CacheTTL:             5 * time.Minute,
		HTTPClient:           nil, // Will be created during client initialization
		CircuitBreakerConfig: nil, // Will use default circuit breaker config
		FallbackPolicy:       FallbackUseCache,
		EnableCircuitBreaker: true,
	}
}

// Validate checks the configuration for required fields and valid values.
func (c *Config) Validate() error {
	if c.APIServiceURL == "" {
		return errors.New("APIServiceURL is required")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if c.MaxRetries < 0 {
		return errors.New("MaxRetries must be non-negative")
	}
	if c.InitialRetryDelay <= 0 {
		return errors.New("InitialRetryDelay must be positive")
	}
	if c.MaxRetryDelay <= 0 {
		return errors.New("MaxRetryDelay must be positive")
	}
	if c.CacheTTL <= 0 {
		return errors.New("CacheTTL must be positive")
	}
	return nil
}

// Operation은 요청되는 작업의 유형을 나타냅니다.
type Operation string

const (
	// OperationRead represents a read operation.
	OperationRead Operation = "read"
	// OperationWrite represents a write operation.
	OperationWrite Operation = "write"
)

// PermissionResult는 권한 검사 결과를 나타내는 구조체입니다.
type PermissionResult struct {
	UserID    string
	RepoOwner string
	RepoName  string
	Operation Operation
	HasAccess bool
	CachedAt  time.Time
	ExpiresAt time.Time
}

// IsExpired checks if the permission result has expired based on TTL.
func (pr *PermissionResult) IsExpired() bool {
	return time.Now().After(pr.ExpiresAt)
}

// CacheKey generates a unique cache key for the permission result.
func (pr *PermissionResult) CacheKey() string {
	return pr.UserID + ":" + pr.RepoOwner + ":" + pr.RepoName + ":" + string(pr.Operation)
}

// Client는 PermissionClient 인터페이스를 구현하는 구조체입니다.
// 외부 API 서비스와 통신하여 권한을 검증하고 결과를 캐싱합니다.
type Client struct {
	config         *Config
	httpClient     HTTPClientInterface
	cache          *PermissionCache
	circuitBreaker *CircuitBreaker
}

// CheckReadAccess implements PermissionClient.CheckReadAccess.
func (c *Client) CheckReadAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	return c.checkAccess(ctx, userID, repoOwner, repoName, OperationRead)
}

// CheckWriteAccess implements PermissionClient.CheckWriteAccess.
func (c *Client) CheckWriteAccess(ctx context.Context, userID, repoOwner, repoName string) (bool, error) {
	return c.checkAccess(ctx, userID, repoOwner, repoName, OperationWrite)
}

// checkAccess is the internal method that performs the actual permission check.
func (c *Client) checkAccess(ctx context.Context, userID, repoOwner, repoName string, operation Operation) (bool, error) {
	// Input validation
	if userID == "" {
		return false, errors.New("userID cannot be empty")
	}
	if repoOwner == "" {
		return false, errors.New("repoOwner cannot be empty")
	}
	if repoName == "" {
		return false, errors.New("repoName cannot be empty")
	}

	// Check cache first
	result := &PermissionResult{
		UserID:    userID,
		RepoOwner: repoOwner,
		RepoName:  repoName,
		Operation: operation,
	}

	cacheKey := result.CacheKey()
	cachedResult := c.cache.Get(cacheKey)

	// Return cached result if valid
	if cachedResult != nil && !cachedResult.IsExpired() {
		return cachedResult.HasAccess, nil
	}

	// Attempt API request with circuit breaker protection
	hasAccess, err := c.requestPermissionWithFallback(ctx, userID, repoOwner, repoName, operation, cachedResult)
	if err != nil {
		// Check if this is a ServiceUnavailableError with fallback information
		var svcErr *ServiceUnavailableError
		if errors.As(err, &svcErr) && svcErr.FallbackUsed {
			// Return the fallback access decision with the error
			return hasAccess, err
		}
		return false, err
	}

	// Cache the successful result
	result.HasAccess = hasAccess
	result.CachedAt = time.Now()
	result.ExpiresAt = result.CachedAt.Add(c.config.CacheTTL)
	c.cache.Set(cacheKey, result)

	return hasAccess, nil
}

// requestPermissionWithFallback handles the API request with circuit breaker and fallback logic.
func (c *Client) requestPermissionWithFallback(ctx context.Context, userID, repoOwner, repoName string, operation Operation, cachedResult *PermissionResult) (bool, error) {
	// Use circuit breaker for API requests
	var requestErr error
	var hasAccess bool

	err := c.circuitBreaker.Execute(ctx, func(ctx context.Context) error {
		var err error
		hasAccess, err = c.requestPermission(ctx, userID, repoOwner, repoName, operation)
		requestErr = err
		return err
	})

	// Handle circuit breaker states
	if err != nil {
		var cbErr *CircuitBreakerError
		if errors.As(err, &cbErr) {
			// Circuit breaker is open - apply fallback policy
			hasCachedResult := cachedResult != nil
			if hasCachedResult {
				return cachedResult.HasAccess, nil
			}

			fallbackAccess := c.config.FallbackPolicy.ShouldAllowAccess(operation, hasCachedResult)
			return fallbackAccess, &ServiceUnavailableError{
				Operation:      operation,
				UserID:         userID,
				RepoOwner:      repoOwner,
				RepoName:       repoName,
				CircuitState:   cbErr.State,
				FallbackUsed:   true,
				FallbackAccess: fallbackAccess,
				Err:            cbErr,
			}
		}

		// Other errors from the request - apply fallback logic
		if cachedResult != nil {
			return cachedResult.HasAccess, nil
		}

		fallbackAccess := c.config.FallbackPolicy.ShouldAllowAccess(operation, false)
		return fallbackAccess, &ServiceUnavailableError{
			Operation:      operation,
			UserID:         userID,
			RepoOwner:      repoOwner,
			RepoName:       repoName,
			FallbackUsed:   true,
			FallbackAccess: fallbackAccess,
			Err:            requestErr,
		}
	}

	return hasAccess, nil
}

// requestPermission makes an HTTP request to the API-Service to check permissions.
func (c *Client) requestPermission(ctx context.Context, userID, repoOwner, repoName string, operation Operation) (bool, error) {
	// Create backoff strategy
	backoffStrategy := backoff.NewExponentialBackOff()
	backoffStrategy.InitialInterval = c.config.InitialRetryDelay
	backoffStrategy.MaxInterval = c.config.MaxRetryDelay
	backoffStrategy.MaxElapsedTime = 0 // No overall timeout; rely on context

	// Limit the number of retries
	retryBackoff := backoff.WithMaxRetries(backoffStrategy, uint64(c.config.MaxRetries))

	// Add context cancellation to backoff
	retryBackoff = backoff.WithContext(retryBackoff, ctx)

	var lastErr error

	// Retry operation
	retryOperation := func() error {
		hasAccess, err := c.makeHTTPRequest(ctx, userID, repoOwner, repoName, operation)
		if err != nil {
			lastErr = err

			// Check if error is retryable
			var permErr *PermissionError
			if errors.As(err, &permErr) && !permErr.IsRetryable() {
				// Non-retryable error, stop retrying
				return backoff.Permanent(err)
			}

			// Retryable error, continue retrying
			return err
		}

		// Success - store result and return
		lastErr = nil

		// Cache the successful result
		result := &PermissionResult{
			UserID:    userID,
			RepoOwner: repoOwner,
			RepoName:  repoName,
			Operation: operation,
			HasAccess: hasAccess,
			CachedAt:  time.Now(),
			ExpiresAt: time.Now().Add(c.config.CacheTTL),
		}
		c.cache.Set(result.CacheKey(), result)

		return nil
	}

	err := backoff.Retry(retryOperation, retryBackoff)
	if err != nil {
		if lastErr != nil {
			return false, lastErr
		}
		return false, err
	}

	// If we reach here, the operation succeeded
	// Get the cached result (which was set in the retry operation)
	result := c.cache.Get((&PermissionResult{
		UserID:    userID,
		RepoOwner: repoOwner,
		RepoName:  repoName,
		Operation: operation,
	}).CacheKey())

	if result != nil {
		return result.HasAccess, nil
	}

	// This should not happen, but fallback to false
	return false, errors.New("permission check completed but result not found in cache")
}

// makeHTTPRequest performs a single HTTP request to the API-Service.
func (c *Client) makeHTTPRequest(ctx context.Context, userID, repoOwner, repoName string, operation Operation) (bool, error) {
	// Build URL: GET http://api-service.local/permissions/:user_id/:repo_owner/:repo_name?op=read|write
	apiURL, err := c.buildAPIURL(userID, repoOwner, repoName, operation)
	if err != nil {
		return false, NewPermissionError(operation, userID, repoOwner, repoName,
			fmt.Errorf("failed to build API URL: %w", err))
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return false, NewPermissionError(operation, userID, repoOwner, repoName,
			fmt.Errorf("failed to create HTTP request: %w", err))
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CollabOps-Git-Executor/1.0")

	// Make the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Network/connection errors are retryable
		return false, NewPermissionError(operation, userID, repoOwner, repoName,
			fmt.Errorf("HTTP request failed: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, NewPermissionErrorWithStatus(operation, userID, repoOwner, repoName, resp.StatusCode,
			fmt.Errorf("failed to read response body: %w", err))
	}

	// Handle HTTP status codes
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
		return false, NewPermissionErrorWithStatus(operation, userID, repoOwner, repoName, resp.StatusCode, err)
	}

	// Parse JSON response
	var apiResponse APIResponse
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		return false, NewPermissionErrorWithStatus(operation, userID, repoOwner, repoName, resp.StatusCode,
			fmt.Errorf("failed to parse JSON response: %w", err))
	}

	return apiResponse.Allowed, nil
}

// buildAPIURL constructs the API URL for permission checking.
func (c *Client) buildAPIURL(userID, repoOwner, repoName string, operation Operation) (string, error) {
	// Parse base URL
	baseURL, err := url.Parse(c.config.APIServiceURL)
	if err != nil {
		return "", fmt.Errorf("invalid API service URL: %w", err)
	}

	// URL escape the path components
	escapedUserID := url.PathEscape(userID)
	escapedRepoOwner := url.PathEscape(repoOwner)
	escapedRepoName := url.PathEscape(repoName)

	// Build the path
	path := fmt.Sprintf("/permissions/%s/%s/%s", escapedUserID, escapedRepoOwner, escapedRepoName)

	// Build full URL with proper relative resolution
	fullURL := baseURL.ResolveReference(&url.URL{
		Path:     path,
		RawQuery: "op=" + url.QueryEscape(string(operation)),
	})

	return fullURL.String(), nil
}

// InvalidateCache implements PermissionClient.InvalidateCache.
func (c *Client) InvalidateCache(userID, repoOwner, repoName string) {
	readKey := userID + ":" + repoOwner + ":" + repoName + ":" + string(OperationRead)
	writeKey := userID + ":" + repoOwner + ":" + repoName + ":" + string(OperationWrite)
	c.cache.Delete(readKey)
	c.cache.Delete(writeKey)
}

// ClearCache implements PermissionClient.ClearCache.
func (c *Client) ClearCache() {
	c.cache.Clear()
}

// GetCircuitBreakerMetrics implements PermissionClient.GetCircuitBreakerMetrics.
func (c *Client) GetCircuitBreakerMetrics() *CircuitBreakerMetrics {
	if c.circuitBreaker == nil {
		return nil
	}
	metrics := c.circuitBreaker.GetMetrics()
	return &metrics
}

// Close implements PermissionClient.Close.
func (c *Client) Close() error {
	c.cache.Close()
	return nil
}
