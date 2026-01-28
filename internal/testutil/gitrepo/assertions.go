package gitrepo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// HTTP Response Assertions
// ⚠️ Error Mapper 없음: 핸들러가 직접 분기한 결과를 검증
// =============================================================================

// APIError represents the standard error response structure
type APIError struct {
	Success bool `json:"success"`
	Error   struct {
		Code      string                 `json:"code"`
		Message   string                 `json:"message"`
		RequestID string                 `json:"request_id,omitempty"`
		Details   map[string]interface{} `json:"details,omitempty"`
	} `json:"error"`
}

// RequireStatus asserts the HTTP status code
// 회귀: "status code가 예상과 다르면 body를 포함해서 실패 메시지를 보여줌"
func RequireStatus(t *testing.T, resp *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if resp.Code != expected {
		t.Fatalf("expected status %d, got %d\nBody: %s", expected, resp.Code, resp.Body.String())
	}
}

// AssertStatus asserts status code (non-fatal)
func AssertStatus(t *testing.T, resp *httptest.ResponseRecorder, expected int) bool {
	t.Helper()
	if resp.Code != expected {
		t.Errorf("expected status %d, got %d\nBody: %s", expected, resp.Code, resp.Body.String())
		return false
	}
	return true
}

// RequireErrCode asserts the error.code field in response
// 회귀: "machine-readable error.code가 예상대로 반환되는지 검증"
func RequireErrCode(t *testing.T, resp *httptest.ResponseRecorder, expectedCode string) {
	t.Helper()

	var apiErr APIError
	err := json.Unmarshal(resp.Body.Bytes(), &apiErr)
	require.NoError(t, err, "response body should be valid JSON: %s", resp.Body.String())

	require.False(t, apiErr.Success, "success should be false for error response")
	require.Equal(t, expectedCode, apiErr.Error.Code,
		"expected error.code=%s, got=%s\nFull response: %s",
		expectedCode, apiErr.Error.Code, resp.Body.String())
}

// AssertErrCode asserts error code (non-fatal)
func AssertErrCode(t *testing.T, resp *httptest.ResponseRecorder, expectedCode string) bool {
	t.Helper()

	var apiErr APIError
	err := json.Unmarshal(resp.Body.Bytes(), &apiErr)
	if err != nil {
		t.Errorf("response body should be valid JSON: %s", resp.Body.String())
		return false
	}

	if apiErr.Success {
		t.Errorf("success should be false for error response")
		return false
	}

	if apiErr.Error.Code != expectedCode {
		t.Errorf("expected error.code=%s, got=%s\nFull response: %s",
			expectedCode, apiErr.Error.Code, resp.Body.String())
		return false
	}
	return true
}

// RequireRetryAfter asserts that Retry-After header is present
// 회귀: "423/429/503 응답에 Retry-After 헤더가 포함되는지 검증"
func RequireRetryAfter(t *testing.T, resp *httptest.ResponseRecorder) string {
	t.Helper()

	retryAfter := resp.Header().Get("Retry-After")
	require.NotEmpty(t, retryAfter,
		"Retry-After header should be present for status %d\nHeaders: %v",
		resp.Code, resp.Header())
	return retryAfter
}

// AssertRetryAfter asserts Retry-After header (non-fatal)
func AssertRetryAfter(t *testing.T, resp *httptest.ResponseRecorder) bool {
	t.Helper()

	retryAfter := resp.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("Retry-After header should be present for status %d", resp.Code)
		return false
	}
	return true
}

// RequireNo500 asserts that response is NOT a 500 Internal Server Error
// 회귀: "어떤 경우에도 500 INTERNAL_ERROR가 발생하지 않음"
func RequireNo500(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()

	if resp.Code == http.StatusInternalServerError {
		t.Fatalf("Unexpected 500 Internal Server Error!\nBody: %s", resp.Body.String())
	}
}

// AssertNo500 asserts no 500 error (non-fatal)
func AssertNo500(t *testing.T, resp *httptest.ResponseRecorder) bool {
	t.Helper()

	if resp.Code == http.StatusInternalServerError {
		t.Errorf("Unexpected 500 Internal Server Error!\nBody: %s", resp.Body.String())
		return false
	}
	return true
}

// RequireSuccess asserts a successful response (2xx)
func RequireSuccess(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()

	if resp.Code < 200 || resp.Code >= 300 {
		t.Fatalf("expected success (2xx), got %d\nBody: %s", resp.Code, resp.Body.String())
	}
}

// =============================================================================
// Status Code Specific Assertions
// =============================================================================

// Require400InvalidRef asserts 400 with INVALID_REF code
func Require400InvalidRef(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusBadRequest)
	RequireErrCode(t, resp, "INVALID_REF")
}

// Require400UnsupportedRef asserts 400 with UNSUPPORTED_REF code
func Require400UnsupportedRef(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusBadRequest)
	RequireErrCode(t, resp, "UNSUPPORTED_REF")
}

// Require400AmbiguousRef asserts 400 with AMBIGUOUS_REF code
func Require400AmbiguousRef(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusBadRequest)
	RequireErrCode(t, resp, "AMBIGUOUS_REF")
}

// Require404RefNotFound asserts 404 with REF_NOT_FOUND code
func Require404RefNotFound(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusNotFound)
	RequireErrCode(t, resp, "REF_NOT_FOUND")
}

// Require404ObjectNotFound asserts 404 with OBJECT_NOT_FOUND code
func Require404ObjectNotFound(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusNotFound)
	RequireErrCode(t, resp, "OBJECT_NOT_FOUND")
}

// Require403PolicyViolation asserts 403 with POLICY_VIOLATION code
func Require403PolicyViolation(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusForbidden)
	RequireErrCode(t, resp, "POLICY_VIOLATION")
}

// Require409ConcurrentUpdate asserts 409 with CONCURRENT_UPDATE code
func Require409ConcurrentUpdate(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusConflict)
	RequireErrCode(t, resp, "CONCURRENT_UPDATE")
}

// Require409AlreadyExists asserts 409 with ALREADY_EXISTS code
func Require409AlreadyExists(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusConflict)
	RequireErrCode(t, resp, "ALREADY_EXISTS")
}

// Require422MergeConflict asserts 422 with MERGE_CONFLICT code
func Require422MergeConflict(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusUnprocessableEntity)
	RequireErrCode(t, resp, "MERGE_CONFLICT")
}

// Require422NoCommonAncestor asserts 422 with NO_COMMON_ANCESTOR code
func Require422NoCommonAncestor(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusUnprocessableEntity)
	RequireErrCode(t, resp, "NO_COMMON_ANCESTOR")
}

// Require423Locked asserts 423 with LOCKED code and Retry-After header
func Require423Locked(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusLocked)
	RequireErrCode(t, resp, "LOCKED")
	RequireRetryAfter(t, resp)
}

// Require429RateLimited asserts 429 with RATE_LIMITED code and Retry-After header
func Require429RateLimited(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusTooManyRequests)
	RequireErrCode(t, resp, "RATE_LIMITED")
	RequireRetryAfter(t, resp)
}

// Require503Timeout asserts 503 with TIMEOUT code and Retry-After header
func Require503Timeout(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	RequireStatus(t, resp, http.StatusServiceUnavailable)
	RequireErrCode(t, resp, "TIMEOUT")
	RequireRetryAfter(t, resp)
}

// =============================================================================
// Schema Stability Assertions
// 회귀: "응답 스키마가 변경되면 클라이언트가 깨짐"
// =============================================================================

// RequireStableSchema validates that essential fields are present
func RequireStableSchema(t *testing.T, resp *httptest.ResponseRecorder, requiredFields ...string) {
	t.Helper()

	body := resp.Body.String()
	for _, field := range requiredFields {
		assert.Contains(t, body, field,
			"response should contain field '%s'\nBody: %s", field, body)
	}
}

// RequireJSONField validates specific JSON field exists and has expected value
func RequireJSONField(t *testing.T, resp *httptest.ResponseRecorder, fieldPath string, expected interface{}) {
	t.Helper()

	var data map[string]interface{}
	err := json.Unmarshal(resp.Body.Bytes(), &data)
	require.NoError(t, err)

	// Navigate path (e.g., "error.code")
	parts := strings.Split(fieldPath, ".")
	current := data
	for i, part := range parts {
		if i == len(parts)-1 {
			actual, exists := current[part]
			require.True(t, exists, "field '%s' should exist", fieldPath)
			require.Equal(t, expected, actual, "field '%s' mismatch", fieldPath)
		} else {
			next, ok := current[part].(map[string]interface{})
			require.True(t, ok, "field '%s' should be an object", strings.Join(parts[:i+1], "."))
			current = next
		}
	}
}

// =============================================================================
// Truncation Contract Assertions
// 회귀: "대용량 응답에서도 truncation 계약이 유지됨"
// =============================================================================

// TruncationInfo represents truncation metadata
type TruncationInfo struct {
	Truncated  bool   `json:"truncated"`
	TotalCount int    `json:"total_count,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// RequireTruncationContract validates truncation fields
func RequireTruncationContract(t *testing.T, resp *httptest.ResponseRecorder) TruncationInfo {
	t.Helper()

	var result struct {
		Truncated  bool   `json:"truncated"`
		TotalCount int    `json:"total_count"`
		NextCursor string `json:"next_cursor"`
	}
	err := json.Unmarshal(resp.Body.Bytes(), &result)
	require.NoError(t, err)

	return TruncationInfo{
		Truncated:  result.Truncated,
		TotalCount: result.TotalCount,
		NextCursor: result.NextCursor,
	}
}
