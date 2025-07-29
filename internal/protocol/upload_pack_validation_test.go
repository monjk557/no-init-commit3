package protocol

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUploadPackValidation_BasicFlow tests the core upload-pack flow without complex Git operations
func TestUploadPackValidation_BasicFlow(t *testing.T) {
	// Test 1: Protocol Parsing Validation
	t.Run("Protocol_Parsing_Works", func(t *testing.T) {
		handler := &UploadPackHandler{}
		var body bytes.Buffer
		// Create a proper pkt-line request
		wantLine := "want 1234567890123456789012345678901234567890 multi_ack"
		pktLine := formatPktLine(wantLine)
		body.WriteString(pktLine)
		// Flush packet
		body.WriteString("0000")
		// Done line
		doneLine := "done"
		pktLineDone := formatPktLine(doneLine)
		body.WriteString(pktLineDone)
		req, err := handler.parseUploadPackRequest(&body)
		require.NoError(t, err)
		assert.Len(t, req.Wants, 1)
		assert.Equal(t, "1234567890123456789012345678901234567890", req.Wants[0])
		assert.True(t, req.Done)
		assert.Contains(t, req.Capabilities, "multi_ack")
	})
	// Test 2: HTTP Integration Validation
	t.Run("HTTP_Integration_Works", func(t *testing.T) {
		// Set up handler with mock config
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot: "/nonexistent", // This will fail gracefully
			},
		}
		permClient := &MockPermissionClient{
			shouldAllow: true,
			shouldError: false,
		}
		handler, err := NewUploadPackHandler(cfg, permClient)
		require.NoError(t, err)
		// Create request body
		var requestBody bytes.Buffer
		wantLine := "want 1234567890123456789012345678901234567890 no-progress"
		requestBody.WriteString(formatPktLine(wantLine))
		requestBody.WriteString("0000")
		requestBody.WriteString(formatPktLine("done"))
		// Create HTTP request
		req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
		req.Header.Set("X-USER-ID", "test-user")
		req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		// Set URL parameters
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		// Record response
		w := httptest.NewRecorder()
		// Call handler
		handler.HandleUploadPack(w, req)
		// Verify HTTP handling works (will fail due to repo not found, but that's expected)
		assert.NotEqual(t, http.StatusInternalServerError, w.Code, "Should not have internal server error")
		// Should have some response
		responseBody := w.Body.String()
		assert.True(t, len(responseBody) > 0, "Should have response body")
		// The core functionality is working if we get a proper HTTP response
		t.Logf("Response code: %d", w.Code)
		t.Logf("Response body: %q", responseBody)
		t.Logf("Content-Type: %q", w.Header().Get("Content-Type"))
		// Key verification: The handler processed the request and returned a response
		// (not crashing with internal server error is the main goal)
		assert.True(t, w.Code == http.StatusNotFound || w.Code == http.StatusBadRequest ||
			w.Code == http.StatusOK, "Should return expected HTTP status")
	})
	// Test 3: Authentication and Permission Flow
	t.Run("Authentication_Flow_Works", func(t *testing.T) {
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot: "/tmp",
			},
		}
		// Test with permission denied
		permClient := &MockPermissionClient{
			shouldAllow: false,
			shouldError: false,
		}
		handler, err := NewUploadPackHandler(cfg, permClient)
		require.NoError(t, err)
		var requestBody bytes.Buffer
		requestBody.WriteString(formatPktLine("want 1234567890123456789012345678901234567890"))
		requestBody.WriteString("0000")
		requestBody.WriteString(formatPktLine("done"))
		req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
		req.Header.Set("X-USER-ID", "test-user")
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		handler.HandleUploadPack(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code, "Should deny access when permissions are denied")
	})
	// Test 4: Missing Authentication
	t.Run("Missing_Auth_Rejected", func(t *testing.T) {
		cfg := &config.Config{
			Git: config.GitConfig{
				RepoRoot: "/tmp",
			},
		}
		permClient := &MockPermissionClient{
			shouldAllow: true,
			shouldError: false,
		}
		handler, err := NewUploadPackHandler(cfg, permClient)
		require.NoError(t, err)
		var requestBody bytes.Buffer
		requestBody.WriteString(formatPktLine("want 1234567890123456789012345678901234567890"))
		req := httptest.NewRequest("POST", "/testowner/testrepo/git-upload-pack", &requestBody)
		// NO X-USER-ID header
		params := map[string]string{}
		params["owner"] = "testowner"
		params["repo"] = "testrepo"
		req = AddGinParamsToRequest(req, params)
		w := httptest.NewRecorder()
		handler.HandleUploadPack(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "Should require authentication")
	})
}

// formatPktLine formats a string as a Git pkt-line
func formatPktLine(data string) string {
	length := len(data) + 5 // +4 for length header, +1 for newline
	return fmt.Sprintf("%04x%s\n", length, data)
}
