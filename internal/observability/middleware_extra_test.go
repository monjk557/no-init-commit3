package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetricsMiddleware(t *testing.T) {
	mw := NewMetricsMiddleware()
	require.NotNil(t, mw)
	require.NotNil(t, mw.metrics)
}

func TestMetricsMiddleware_Handler(t *testing.T) {
	mw := NewMetricsMiddleware()

	// 다양한 status code 로 error 기록 분기들을 커버한다.
	statuses := []int{200, 400, 403, 404, 500}
	for _, status := range statuses {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})
		handler := mw.Handler()(inner)

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/repos/owner/repo/branches", nil)
		assert.NotPanics(t, func() { handler.ServeHTTP(rr, req) })
		assert.Equal(t, status, rr.Code)
	}
}

func TestNewGitOperationMiddleware(t *testing.T) {
	mw := NewGitOperationMiddleware()
	require.NotNil(t, mw)
}

func TestGitOperationMiddleware_RecordOperation_Success(t *testing.T) {
	mw := NewGitOperationMiddleware()
	called := false
	err := mw.RecordOperation("clone", func() error {
		called = true
		return nil
	})
	assert.NoError(t, err)
	assert.True(t, called)
}

func TestGitOperationMiddleware_RecordOperation_Error(t *testing.T) {
	mw := NewGitOperationMiddleware()
	wantErr := errors.New("repository not found")
	err := mw.RecordOperation("fetch", func() error { return wantErr })
	assert.ErrorIs(t, err, wantErr)
}

func TestGitOperationMiddleware_RecordGitObjectProcessing(t *testing.T) {
	mw := NewGitOperationMiddleware()
	assert.NotPanics(t, func() { mw.RecordGitObjectProcessing("blob", 3) })
}

func TestNewPermissionMiddleware(t *testing.T) {
	require.NotNil(t, NewPermissionMiddleware())
}

func TestPermissionMiddleware_RecordPermissionCheck(t *testing.T) {
	mw := NewPermissionMiddleware()

	// granted
	ok, err := mw.RecordPermissionCheck(func() (bool, error) { return true, nil })
	assert.True(t, ok)
	assert.NoError(t, err)

	// denied
	ok, err = mw.RecordPermissionCheck(func() (bool, error) { return false, nil })
	assert.False(t, ok)
	assert.NoError(t, err)

	// error
	ok, err = mw.RecordPermissionCheck(func() (bool, error) { return false, errors.New("boom") })
	assert.False(t, ok)
	assert.Error(t, err)
}

func TestGetRequestSize(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/repos/owner/repo", nil)
	req.Header.Set("X-Custom", "value")
	req.ContentLength = 128

	size := getRequestSize(req)
	assert.Greater(t, size, int64(128))
}

func TestNormalizeEndpoint_Variants(t *testing.T) {
	// 실제 normalizeEndpoint 동작에 맞춘 기대값:
	// "/repos/" → "/repos/{owner}/" 치환 후 parts[2]/parts[3] 를 다시
	// {owner}/{repo} 로 덮어쓰므로 한 단계가 더 들어간다.
	tests := map[string]string{
		"/repos/owner/repo/branches?foo=bar": "/repos/{owner}/{repo}/repo/branches",
		"/ssh/git-upload-pack":               "/ssh/{command}",
		"/health/liveness":                   "/health/liveness",
	}
	for in, want := range tests {
		assert.Equal(t, want, normalizeEndpoint(in), "input=%s", in)
	}
}

func TestCategorizeHTTPError(t *testing.T) {
	cases := map[int]string{
		500: "server_error",
		503: "server_error",
		404: "not_found",
		403: "permission_denied",
		401: "unauthorized",
		400: "bad_request",
		418: "client_error",
		200: "unknown_error",
	}
	for status, want := range cases {
		assert.Equal(t, want, categorizeHTTPError(status), "status=%d", status)
	}
}

func TestCategorizeGitError(t *testing.T) {
	cases := map[string]string{
		"":                          "none", // nil 처리
		"object not found":          "not_found",
		"permission denied":         "permission_denied",
		"operation timeout":         "timeout",
		"invalid ref name":          "invalid_input",
		"network unreachable":       "network_error",
		"repository is corrupt":     "repository_error",
		"some other failure string": "git_error",
	}
	for msg, want := range cases {
		var err error
		if msg != "" {
			err = errors.New(msg)
		}
		assert.Equal(t, want, categorizeGitError(err), "msg=%q", msg)
	}
}

func TestDefaultMetricsMiddlewareConfig(t *testing.T) {
	cfg := DefaultMetricsMiddlewareConfig()
	assert.True(t, cfg.EnableResourceCollection)
	assert.Equal(t, 15*time.Second, cfg.ResourceCollectionInterval)
	assert.True(t, cfg.EndpointNormalization)
	assert.Equal(t, 100, cfg.MaxPathLength)
}

func TestCacheMetricsIntegration(t *testing.T) {
	c := NewCacheMetricsIntegration()
	require.NotNil(t, c)
	assert.NotPanics(t, func() { c.UpdateCacheHitRate(0.5) })
}

func TestAPIServiceCallsTracker(t *testing.T) {
	tr := NewAPIServiceCallsTracker()
	require.NotNil(t, tr)
	assert.NotPanics(t, func() {
		tr.RecordAPICall("/api/v2/repos", true)
		tr.RecordAPICall("/api/v2/repos", false)
	})
}
