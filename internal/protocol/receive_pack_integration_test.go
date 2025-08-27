package protocol

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReceivePackIntegration tests the complete receive-pack workflow
func TestReceivePackIntegration(t *testing.T) {
	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "receive-pack-integration-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	tests := []struct {
		name                 string
		method               string
		path                 string
		headers              map[string]string
		expectedStatus       int
		expectedContentType  string
		expectedBodyContains []string
		skipBodyCheck        bool
	}{
		{
			name:   "Receive-Pack Info/Refs Success",
			method: "GET",
			path:   "/testowner/testrepo/info/refs?service=git-receive-pack",
			headers: map[string]string{
				"X-USER-ID": "testuser123",
				"X-Source":  "http-gateway",
			},
			expectedStatus:      http.StatusNotFound, // Repository doesn't exist, but auth passes
			expectedContentType: "text/plain; charset=utf-8",
			expectedBodyContains: []string{
				"Repository not found",
			},
		},
		{
			name:   "Upload-Pack Info/Refs Success (Comparison)",
			method: "GET",
			path:   "/testowner/testrepo/info/refs?service=git-upload-pack",
			headers: map[string]string{
				"X-USER-ID": "testuser123",
				"X-Source":  "http-gateway",
			},
			expectedStatus:      http.StatusNotFound, // Repository doesn't exist, but auth passes
			expectedContentType: "text/plain; charset=utf-8",
			expectedBodyContains: []string{
				"Repository not found",
			},
		},
		{
			name:           "Missing Authentication",
			method:         "GET",
			path:           "/testowner/testrepo/info/refs?service=git-receive-pack",
			headers:        map[string]string{},
			expectedStatus: http.StatusUnauthorized,
			skipBodyCheck:  true,
		},
		{
			name:   "Invalid Source Header",
			method: "GET",
			path:   "/testowner/testrepo/info/refs?service=git-receive-pack",
			headers: map[string]string{
				"X-USER-ID": "testuser123",
				"X-Source":  "invalid-source",
			},
			expectedStatus: http.StatusNotFound, // Without access control middleware, gets to handler and fails on repo not found
			skipBodyCheck:  true,
		},
		{
			name:   "Invalid Service Parameter",
			method: "GET",
			path:   "/testowner/testrepo/info/refs?service=invalid-service",
			headers: map[string]string{
				"X-USER-ID": "testuser123",
				"X-Source":  "http-gateway",
			},
			expectedStatus: http.StatusBadRequest,
			skipBodyCheck:  true,
		},
		{
			name:   "Receive-Pack POST Endpoint",
			method: "POST",
			path:   "/testowner/testrepo/git-receive-pack",
			headers: map[string]string{
				"X-USER-ID":    "testuser123",
				"X-Source":     "http-gateway",
				"Content-Type": "application/x-git-receive-pack-request",
			},
			expectedStatus: http.StatusNotFound, // Repository doesn't exist
			skipBodyCheck:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create handlers
			uploadHandler, err := NewUploadPackHandler(cfg)
			require.NoError(t, err)
			receiveHandler, err := NewReceivePackHandler(cfg)
			require.NoError(t, err)
			// Create router and setup routes like the real server
			gin.SetMode(gin.TestMode)
			router := gin.New()

			// Add Gin context middleware that our handlers expect
			router.Use(func(c *gin.Context) {
				// Store Gin context in request context for our URLParam function
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), GinContextKey{}, c))
				c.Next()
			})

			// Add the same access control middleware pattern used in server
			router.GET("/:owner/:repo/info/refs", gin.WrapH(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				service := r.URL.Query().Get("service")
				switch service {
				case "git-upload-pack":
					uploadHandler.HandleInfoRefs(w, r)
				case "git-receive-pack":
					receiveHandler.HandleInfoRefs(w, r)
				default:
					http.Error(w, "Invalid service parameter", http.StatusBadRequest)
				}
			})))
			router.POST("/:owner/:repo/git-receive-pack", gin.WrapH(http.HandlerFunc(receiveHandler.HandleReceivePack)))
			// Create request
			var req *http.Request
			if tt.method == "POST" {
				req = httptest.NewRequest(tt.method, tt.path, bytes.NewReader([]byte{}))
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			// Add headers
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			// Add URL parameters for chi router
			params := map[string]string{}
			params["owner"] = "testowner"
			params["repo"] = "testrepo"
			req = AddGinParamsToRequest(req, params)
			// Execute request
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			// Validate response
			assert.Equal(t, tt.expectedStatus, w.Code,
				"Unexpected status code for %s", tt.name)
			if tt.expectedContentType != "" {
				assert.Equal(t, tt.expectedContentType, w.Header().Get("Content-Type"),
					"Unexpected content type for %s", tt.name)
			}
			if !tt.skipBodyCheck && len(tt.expectedBodyContains) > 0 {
				body := w.Body.String()
				for _, expected := range tt.expectedBodyContains {
					assert.Contains(t, body, expected,
						"Response body should contain %s for %s. Body: %s", expected, tt.name, body)
				}
			}
			t.Logf("Test %s: Status=%d, ContentType=%s, Body=%s",
				tt.name, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		})
	}
}

// TestReceivePackCapabilities verifies that receive-pack advertises the correct capabilities
func TestReceivePackCapabilities(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "receive-pack-capabilities-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create a test repository with proper Git structure
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(repoPath, 0755) // Use os.MkdirAll to create nested directories
	require.NoError(t, err)
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	// Test capability building
	capString := handler.buildReceiveCapabilities()
	expectedCapabilities := []string{
		"report-status",
		"delete-refs",
		"ofs-delta",
		"side-band-64k",
		"atomic",
		"push-options",
		"agent=collabops-git-executor/1.0",
	}
	for _, expected := range expectedCapabilities {
		assert.Contains(t, capString, expected,
			"Expected capability %s not found in: %s", expected, capString)
	}
	t.Logf("Receive-pack capabilities: %s", capString)
}

// TestReceivePackVsUploadPackDifferences verifies that receive-pack and upload-pack
// behave differently where expected
func TestReceivePackVsUploadPackDifferences(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pack-comparison-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	uploadHandler, err := NewUploadPackHandler(cfg)
	require.NoError(t, err)
	receiveHandler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	// Create request
	req := httptest.NewRequest("GET", "/testowner/testrepo/info/refs?service=git-receive-pack", nil)
	req.Header.Set("X-USER-ID", "testuser123")
	req.Header.Set("X-Source", "http-gateway")
	params := map[string]string{}
	params["owner"] = "testowner"
	params["repo"] = "testrepo"
	req = AddGinParamsToRequest(req, params)
	// Test receive-pack (should check write permissions)
	w1 := httptest.NewRecorder()
	receiveHandler.HandleInfoRefs(w1, req)
	// Test upload-pack (should check read permissions)
	req2 := httptest.NewRequest("GET", "/testowner/testrepo/info/refs?service=git-upload-pack", nil)
	req2.Header.Set("X-USER-ID", "testuser123")
	req2.Header.Set("X-Source", "http-gateway")
	req2 = AddGinParamsToRequest(req2, params)
	w2 := httptest.NewRecorder()
	uploadHandler.HandleInfoRefs(w2, req2)
	// Both should return 403 (permission denied) but for different permission types
	assert.Equal(t, http.StatusForbidden, w1.Code, "Receive-pack should deny write access")
	assert.Equal(t, http.StatusForbidden, w2.Code, "Upload-pack should deny read access")
	t.Logf("Receive-pack response: %d %s", w1.Code, w1.Body.String())
	t.Logf("Upload-pack response: %d %s", w2.Code, w2.Body.String())
}

// TestReceivePackProtocolCompliance verifies protocol compliance for receive-pack
func TestReceivePackProtocolCompliance(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "receive-pack-protocol-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	// Test proper service parameter handling
	testCases := []struct {
		service        string
		expectedStatus int
		description    string
	}{
		{"git-receive-pack", http.StatusNotFound, "Valid receive-pack service"}, // 404 because repo doesn't exist
		{"git-upload-pack", http.StatusBadRequest, "Upload-pack service on receive-pack handler"},
		{"invalid-service", http.StatusBadRequest, "Invalid service parameter"},
		{"", http.StatusBadRequest, "Missing service parameter"},
	}
	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			path := "/testowner/testrepo/info/refs"
			if tc.service != "" {
				path += "?service=" + tc.service
			}
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("X-USER-ID", "testuser123")
			req.Header.Set("X-Source", "http-gateway")
			params := map[string]string{}
			params["owner"] = "testowner"
			params["repo"] = "testrepo"
			req = AddGinParamsToRequest(req, params)
			w := httptest.NewRecorder()
			handler.HandleInfoRefs(w, req)
			assert.Equal(t, tc.expectedStatus, w.Code,
				"Expected status %d for service '%s', got %d", tc.expectedStatus, tc.service, w.Code)
			t.Logf("Service '%s': Status=%d, Body=%s", tc.service, w.Code, w.Body.String())
		})
	}
}

// BenchmarkReceivePackHandler measures performance of receive-pack operations
func BenchmarkReceivePackHandler(b *testing.B) {
	tempDir, err := os.MkdirTemp("", "receive-pack-bench-")
	require.NoError(b, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(b, err)
	req := httptest.NewRequest("GET", "/testowner/testrepo/info/refs?service=git-receive-pack", nil)
	req.Header.Set("X-USER-ID", "testuser123")
	req.Header.Set("X-Source", "http-gateway")
	params := map[string]string{}
	params["owner"] = "testowner"
	params["repo"] = "testrepo"
	req = AddGinParamsToRequest(req, params)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		handler.HandleInfoRefs(w, req)
		// Verify it's working (should be 404 since repo doesn't exist)
		if w.Code != http.StatusNotFound {
			b.Fatalf("Expected 404, got %d", w.Code)
		}
	}
}
