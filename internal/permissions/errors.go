package permissions

import (
	"fmt"
	"net/http"
)

// PermissionError는 권한 검사 중 발생하는 에러를 나타냅니다.
type PermissionError struct {
	Operation  Operation
	UserID     string
	RepoOwner  string
	RepoName   string
	Err        error
	StatusCode int
	Retryable  bool
}

func (e *PermissionError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("permission check failed for %s access to %s/%s by user %s: HTTP %d - %v",
			e.Operation, e.RepoOwner, e.RepoName, e.UserID, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("permission check failed for %s access to %s/%s by user %s: %v",
		e.Operation, e.RepoOwner, e.RepoName, e.UserID, e.Err)
}

func (e *PermissionError) Unwrap() error {
	return e.Err
}

func (e *PermissionError) IsRetryable() bool {
	return e.Retryable
}

// NewPermissionError는 새로운 PermissionError를 생성합니다.
func NewPermissionError(operation Operation, userID, repoOwner, repoName string, err error) *PermissionError {
	return &PermissionError{
		Operation: operation,
		UserID:    userID,
		RepoOwner: repoOwner,
		RepoName:  repoName,
		Err:       err,
		Retryable: true,
	}
}

// NewPermissionErrorWithStatus creates a new PermissionError with HTTP status code.
func NewPermissionErrorWithStatus(operation Operation, userID, repoOwner, repoName string, statusCode int, err error) *PermissionError {
	retryable := isRetryableStatusCode(statusCode)
	return &PermissionError{
		Operation:  operation,
		UserID:     userID,
		RepoOwner:  repoOwner,
		RepoName:   repoName,
		StatusCode: statusCode,
		Err:        err,
		Retryable:  retryable,
	}
}

// isRetryableStatusCode determines if an HTTP status code indicates a retryable error.
func isRetryableStatusCode(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	case http.StatusUnauthorized, // 401
		http.StatusForbidden,  // 403
		http.StatusNotFound,   // 404
		http.StatusBadRequest: // 400
		return false
	default:
		// For unknown status codes >= 500, assume retryable
		return statusCode >= 500
	}
}

// APIResponse는 API 서비스로부터의 응답을 나타냅니다.
type APIResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// ServiceUnavailableError는 API 서비스를 사용할 수 없고
// 폴백 정책이 적용되는 상황의 에러를 나타냅니다.
type ServiceUnavailableError struct {
	Operation      Operation
	UserID         string
	RepoOwner      string
	RepoName       string
	CircuitState   CircuitBreakerState
	FallbackUsed   bool
	FallbackAccess bool
	Err            error
}

func (e *ServiceUnavailableError) Error() string {
	if e.CircuitState != 0 {
		return fmt.Sprintf("API-Service unavailable (circuit breaker %s) for %s access to %s/%s by user %s: fallback %s access, underlying error: %v",
			e.CircuitState, e.Operation, e.RepoOwner, e.RepoName, e.UserID,
			map[bool]string{true: "granted", false: "denied"}[e.FallbackAccess], e.Err)
	}
	return fmt.Sprintf("API-Service unavailable for %s access to %s/%s by user %s: fallback %s access, underlying error: %v",
		e.Operation, e.RepoOwner, e.RepoName, e.UserID,
		map[bool]string{true: "granted", false: "denied"}[e.FallbackAccess], e.Err)
}

func (e *ServiceUnavailableError) Unwrap() error {
	return e.Err
}

func (e *ServiceUnavailableError) IsFallbackUsed() bool {
	return e.FallbackUsed
}
