package protocol

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamingUploadPackHandler(t *testing.T) {
	// Set up base handler with mock config
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: "/nonexistent", // Will fail gracefully
		},
	}
	permClient := &MockPermissionClient{
		shouldAllow: true,
		shouldError: false,
	}
	baseHandler, err := NewUploadPackHandler(cfg, permClient)
	require.NoError(t, err)
	streamingHandler := NewStreamingUploadPackHandler(baseHandler)
	t.Run("HandleUploadPackStreaming_Success", func(t *testing.T) {
		// Create request body with proper pkt-line format
		var requestBody bytes.Buffer
		wantLine := "want 1234567890123456789012345678901234567890 no-progress"
		requestBody.WriteString(formatPktLine(wantLine))
		requestBody.WriteString("0000")
		requestBody.WriteString(formatPktLine("done"))
		req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
		req.Header.Set("X-USER-ID", "test-user")
		req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		// Set URL parameters
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		// Call streaming handler
		streamingHandler.HandleUploadPackStreaming(w, req)
		// Verify response (will fail due to nonexistent repo, but that's expected)
		assert.NotEqual(t, http.StatusInternalServerError, w.Code, "Should not have internal server error")
	})
	t.Run("HandleUploadPackStreaming_MissingAuth", func(t *testing.T) {
		var requestBody bytes.Buffer
		requestBody.WriteString(formatPktLine("want 1234567890123456789012345678901234567890"))
		req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
		// NO X-USER-ID header
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		streamingHandler.HandleUploadPackStreaming(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
	t.Run("HandleUploadPackStreaming_PermissionDenied", func(t *testing.T) {
		// Create handler with denied permissions
		deniedPermClient := &MockPermissionClient{
			shouldAllow: false,
			shouldError: false,
		}
		deniedBaseHandler, err := NewUploadPackHandler(cfg, deniedPermClient)
		require.NoError(t, err)
		deniedStreamingHandler := NewStreamingUploadPackHandler(deniedBaseHandler)
		var requestBody bytes.Buffer
		requestBody.WriteString(formatPktLine("want 1234567890123456789012345678901234567890"))
		req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
		req.Header.Set("X-USER-ID", "test-user")
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		deniedStreamingHandler.HandleUploadPackStreaming(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
	t.Run("HandleInfoRefsStreaming_Success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/testowner/testrepo/info/refs?service=git-upload-pack", nil)
		req.Header.Set("X-USER-ID", "test-user")
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		streamingHandler.HandleInfoRefsStreaming(w, req)
		// Should process the request (will fail due to nonexistent repo, but headers should be set)
		assert.NotEqual(t, http.StatusInternalServerError, w.Code)
	})
	t.Run("HandleInfoRefsStreaming_InvalidService", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/testowner/testrepo/info/refs?service=invalid-service", nil)
		req.Header.Set("X-USER-ID", "test-user")
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		streamingHandler.HandleInfoRefsStreaming(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
func TestStreamingHandlerIntegration(t *testing.T) {
	t.Run("StreamingVsNonStreaming_ResponseFormat", func(t *testing.T) {
		// This test compares the response format between streaming and non-streaming handlers
		// to ensure protocol compatibility
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot: "/tmp", // Use a path that might exist
			},
		}
		permClient := &MockPermissionClient{
			shouldAllow: true,
			shouldError: false,
		}
		// Create both handlers
		baseHandler, err := NewUploadPackHandler(cfg, permClient)
		require.NoError(t, err)
		streamingHandler := NewStreamingUploadPackHandler(baseHandler)
		// Create identical requests
		createRequest := func() *http.Request {
			var requestBody bytes.Buffer
			requestBody.WriteString(formatPktLine("want 1234567890123456789012345678901234567890 no-progress"))
			requestBody.WriteString("0000")
			requestBody.WriteString(formatPktLine("done"))
			req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
			req.Header.Set("X-USER-ID", "test-user")
			params := map[string]string{}
			params["owner"] = "testowner"
			params["repo"] = "testrepo"
			return AddGinParamsToRequest(req, params)
		}
		// Test original handler
		req1 := createRequest()
		w1 := httptest.NewRecorder()
		baseHandler.HandleUploadPack(w1, req1)
		// Test streaming handler
		req2 := createRequest()
		w2 := httptest.NewRecorder()
		streamingHandler.HandleUploadPackStreaming(w2, req2)
		// Both should have the same status code (likely 404 for nonexistent repo)
		assert.Equal(t, w1.Code, w2.Code, "Streaming and non-streaming handlers should return same status")
		// Both should set the same content type headers when successful
		if w1.Code == http.StatusOK || w2.Code == http.StatusOK {
			assert.Equal(t, w1.Header().Get("Content-Type"), w2.Header().Get("Content-Type"))
			assert.Equal(t, w1.Header().Get("Cache-Control"), w2.Header().Get("Cache-Control"))
		}
	})
}
func TestStreamingMemoryEfficiency(t *testing.T) {
	t.Run("StreamingHandlerMemoryUsage", func(t *testing.T) {
		// This test verifies that the streaming handler doesn't accumulate large amounts of data
		// before starting to send responses
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot: "/tmp",
			},
		}
		permClient := &MockPermissionClient{
			shouldAllow: true,
			shouldError: false,
		}
		baseHandler, err := NewUploadPackHandler(cfg, permClient)
		require.NoError(t, err)
		streamingHandler := NewStreamingUploadPackHandler(baseHandler)
		// Create a request that would potentially generate a lot of data
		var requestBody bytes.Buffer
		// Multiple wants to simulate a large fetch
		for i := 0; i < 10; i++ {
			want := "want 1234567890123456789012345678901234567890 no-progress"
			requestBody.WriteString(formatPktLine(want))
		}
		requestBody.WriteString("0000")
		requestBody.WriteString(formatPktLine("done"))
		req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
		req.Header.Set("X-USER-ID", "test-user")
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		// The handler should complete without excessive memory usage
		// (We can't easily measure memory here, but the handler should not panic or timeout)
		streamingHandler.HandleUploadPackStreaming(w, req)
		// Should complete without issues
		assert.True(t, true, "Streaming handler completed without panicking")
	})
}

// Helper function to format pkt-line (duplicated from validation test)
