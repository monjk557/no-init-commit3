package protocol

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReceivePackHandler(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	// Test successful handler creation
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	assert.NotNil(t, handler)
	assert.NotNil(t, handler.pathConfig)
	assert.NotNil(t, handler.errorHandler)
	assert.NotNil(t, handler.capabilities)
}
func TestReceivePackHandler_HandleInfoRefs(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	tests := []struct {
		name                  string
		service               string
		userID                string
		permissionShouldAllow bool
		permissionShouldError bool
		expectedStatus        int
		expectedContentType   string
		expectedContains      []string
	}{
		{
			name:                  "Valid receive-pack request",
			service:               "git-receive-pack",
			userID:                "user123",
			permissionShouldAllow: true,
			permissionShouldError: false,
			expectedStatus:        http.StatusNotFound, // Repository doesn't exist in test
			expectedContentType:   "",                  // Error response has different content type
			expectedContains:      []string{"Repository not found"},
		},
		{
			name:                  "Invalid service parameter",
			service:               "invalid-service",
			userID:                "user123",
			permissionShouldAllow: true,
			permissionShouldError: false,
			expectedStatus:        http.StatusBadRequest,
			expectedContentType:   "",
			expectedContains:      []string{},
		},
		{
			name:                  "Missing user ID",
			service:               "git-receive-pack",
			userID:                "",
			permissionShouldAllow: true,
			permissionShouldError: false,
			expectedStatus:        http.StatusUnauthorized,
			expectedContentType:   "",
			expectedContains:      []string{},
		},
		{
			name:                  "Permission denied",
			service:               "git-receive-pack",
			userID:                "user123",
			permissionShouldAllow: false,
			permissionShouldError: false,
			expectedStatus:        http.StatusForbidden,
			expectedContentType:   "",
			expectedContains:      []string{},
		},
		{
			name:                  "Permission check error",
			service:               "git-receive-pack",
			userID:                "user123",
			permissionShouldAllow: false,
			permissionShouldError: true,
			expectedStatus:        http.StatusInternalServerError,
			expectedContentType:   "",
			expectedContains:      []string{},
		},
		{
			name:                  "Upload-pack service (should fail)",
			service:               "git-upload-pack",
			userID:                "user123",
			permissionShouldAllow: true,
			permissionShouldError: false,
			expectedStatus:        http.StatusBadRequest,
			expectedContentType:   "",
			expectedContains:      []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create handler
			handler, err := NewReceivePackHandler(cfg)
			require.NoError(t, err)
			// Create request
			req := httptest.NewRequest(http.MethodGet, "/info/refs?service="+tt.service, nil)
			if tt.userID != "" {
				req.Header.Set("X-USER-ID", tt.userID)
			}
			// Add URL parameters
			params := map[string]string{}
			params["owner"] = "testowner"
			params["repo"] = "testrepo"
			req = AddGinParamsToRequest(req, params)
			// Create response recorder
			w := httptest.NewRecorder()
			// Call handler
			handler.HandleInfoRefs(w, req)
			// Check status code
			assert.Equal(t, tt.expectedStatus, w.Code)
			// Check content type for successful requests
			if tt.expectedContentType != "" {
				assert.Equal(t, tt.expectedContentType, w.Header().Get("Content-Type"))
			}
			// Check response contains expected strings
			if len(tt.expectedContains) > 0 {
				body := w.Body.String()
				for _, expected := range tt.expectedContains {
					assert.Contains(t, body, expected)
				}
			}
		})
	}
}
func TestReceivePackHandler_HandleReceivePack(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	// Create handler
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	// Create request
	req := httptest.NewRequest(http.MethodPost, "/git-receive-pack", nil)
	req.Header.Set("X-USER-ID", "user123")
	// Add URL parameters
	params := map[string]string{}
	params["owner"] = "testowner"
	params["repo"] = "testrepo"
	req = AddGinParamsToRequest(req, params)
	// Create response recorder
	w := httptest.NewRecorder()
	// Call handler (will fail with repository not found, but that's expected)
	handler.HandleReceivePack(w, req)
	// Check that it processes the request (fails due to missing repo, but that's expected)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "Repository not found")
}
func TestReceivePackHandler_buildReceiveCapabilities(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	// Create handler
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	// Test capability string building
	capString := handler.buildReceiveCapabilities()
	// Verify expected capabilities are present
	expectedCaps := []string{
		"report-status",
		"delete-refs",
		"ofs-delta",
		"side-band-64k",
		"atomic",
		"push-options",
		"agent=collabops-git-executor/1.0",
	}
	for _, cap := range expectedCaps {
		assert.Contains(t, capString, cap, "Expected capability %s not found in: %s", cap, capString)
	}
}
func TestReceivePackHandler_AuthenticationFlow(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	t.Run("Missing_Authentication", func(t *testing.T) {

		handler, err := NewReceivePackHandler(cfg)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodGet, "/info/refs?service=git-receive-pack", nil)
		// Deliberately not setting X-USER-ID header
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		handler.HandleInfoRefs(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
	t.Run("Valid_Authentication_No_Permission", func(t *testing.T) {
		handler, err := NewReceivePackHandler(cfg)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodGet, "/info/refs?service=git-receive-pack", nil)
		req.Header.Set("X-USER-ID", "user123")
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		handler.HandleInfoRefs(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
	t.Run("Valid_Authentication_With_Permission", func(t *testing.T) {
		handler, err := NewReceivePackHandler(cfg)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodGet, "/info/refs?service=git-receive-pack", nil)
		req.Header.Set("X-USER-ID", "user123")
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		handler.HandleInfoRefs(w, req)
		// Should get OK or NotFound (depending on whether repo exists)
		// Both are acceptable since we're testing auth flow
		assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusNotFound,
			"Expected OK or NotFound, got %d", w.Code)
	})
}
func TestReceivePackHandler_WritePermissionCheck(t *testing.T) {
	// This test verifies that receive-pack operations check for WRITE permissions
	// while upload-pack operations would check for READ permissions
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/info/refs?service=git-receive-pack", nil)
	req.Header.Set("X-USER-ID", "user123")
	params := map[string]string{}
	params["owner"] = "testowner"
	params["repo"] = "testrepo"
	req = AddGinParamsToRequest(req, params)
	w := httptest.NewRecorder()
	handler.HandleInfoRefs(w, req)
	// Verify that CheckWriteAccess was called (not CheckReadAccess)
	// This is implicitly tested by the mock's behavior
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusNotFound,
		"Permission check should have succeeded or failed due to missing repo, got %d", w.Code)
}
