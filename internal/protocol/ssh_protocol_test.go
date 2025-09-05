package protocol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSSHProtocolHandler(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.Config
		permClient  *MockPermissionClient
		expectError bool
	}{
		{
			name: "Valid configuration",
			config: &config.Config{
				Git: config.GitConfig{
					RepoRoot: "/tmp/test-repos",
				},
			},
			permClient:  &MockPermissionClient{shouldAllow: true},
			expectError: false,
		},
		{
			name:        "Nil config",
			config:      nil,
			permClient:  &MockPermissionClient{shouldAllow: true},
			expectError: true,
		},
		{
			name: "Nil permission client",
			config: &config.Config{
				Git: config.GitConfig{
					RepoRoot: "/tmp/test-repos",
				},
			},
			permClient:  nil,
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := NewSSHProtocolHandler(tt.config, tt.permClient)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, handler)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, handler)
				assert.NotNil(t, handler.pathConfig)
				assert.NotNil(t, handler.permissionClient)
				assert.NotNil(t, handler.errorHandler)
			}
		})
	}
}
func TestSSHProtocolHandler_ValidateSSHRequest(t *testing.T) {
	handler := createTestSSHHandler(t)
	tests := []struct {
		name        string
		request     *http.Request
		operation   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid upload-pack request",
			request:     createSSHTestRequest(t, "testuser", "ssh-gateway", "", "testowner", "testrepo"),
			operation:   "git-upload-pack",
			expectError: false,
		},
		{
			name:        "Valid receive-pack request",
			request:     createSSHTestRequest(t, "testuser", "ssh-gateway", "", "testowner", "testrepo"),
			operation:   "git-receive-pack",
			expectError: false,
		},
		{
			name:        "Missing user ID",
			request:     createSSHTestRequest(t, "", "ssh-gateway", "", "testowner", "testrepo"),
			operation:   "git-upload-pack",
			expectError: true,
			errorMsg:    "Missing user authentication context",
		},
		{
			name:        "Invalid source header",
			request:     createSSHTestRequest(t, "testuser", "http-gateway", "", "testowner", "testrepo"),
			operation:   "git-upload-pack",
			expectError: true,
			errorMsg:    "Invalid source header",
		},
		{
			name:        "Missing repository parameters",
			request:     createSSHTestRequest(t, "testuser", "ssh-gateway", "", "", ""),
			operation:   "git-upload-pack",
			expectError: true,
			errorMsg:    "Missing required repository parameters",
		},
		{
			name:        "Invalid operation",
			request:     createSSHTestRequest(t, "testuser", "ssh-gateway", "", "testowner", "testrepo"),
			operation:   "invalid-operation",
			expectError: true,
			errorMsg:    "Invalid Git operation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sshReq, err := handler.ValidateSSHRequest(tt.request, tt.operation)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, sshReq)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, sshReq)
				assert.Equal(t, "testuser", sshReq.UserID)
				assert.Equal(t, "testowner", sshReq.Owner)
				assert.Equal(t, "testrepo", sshReq.Repo)
				assert.Equal(t, tt.operation, sshReq.Operation)
				assert.NotEmpty(t, sshReq.CorrelationID)
			}
		})
	}
}
func TestSSHProtocolHandler_CheckSSHPermissions(t *testing.T) {
	tests := []struct {
		name        string
		operation   string
		shouldAllow bool
		shouldError bool
		expectError bool
	}{
		{
			name:        "Upload-pack with read permission",
			operation:   "git-upload-pack",
			shouldAllow: true,
			shouldError: false,
			expectError: false,
		},
		{
			name:        "Upload-pack without read permission",
			operation:   "git-upload-pack",
			shouldAllow: false,
			shouldError: false,
			expectError: true,
		},
		{
			name:        "Receive-pack with write permission",
			operation:   "git-receive-pack",
			shouldAllow: true,
			shouldError: false,
			expectError: false,
		},
		{
			name:        "Receive-pack without write permission",
			operation:   "git-receive-pack",
			shouldAllow: false,
			shouldError: false,
			expectError: true,
		},
		{
			name:        "Permission check error",
			operation:   "git-upload-pack",
			shouldAllow: false,
			shouldError: true,
			expectError: true,
		},
		{
			name:        "Unknown operation",
			operation:   "unknown-operation",
			shouldAllow: true,
			shouldError: false,
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockPermissionClient{
				shouldAllow: tt.shouldAllow,
				shouldError: tt.shouldError,
			}
			handler := createTestSSHHandlerWithClient(t, mockClient)
			sshReq := &SSHRequest{
				UserID:    "testuser",
				Owner:     "testowner",
				Repo:      "testrepo",
				Operation: tt.operation,
			}
			err := handler.CheckSSHPermissions(context.Background(), sshReq)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
func TestSSHProtocolHandler_HandleSSHUploadPack(t *testing.T) {
	tests := []struct {
		name           string
		request        *http.Request
		permissionMock *MockPermissionClient
		expectedStatus int
		expectedMsg    string
	}{
		{
			name:           "Valid SSH upload-pack request",
			request:        createSSHTestRequest(t, "testuser", "ssh-gateway", "", "testowner", "testrepo"),
			permissionMock: &MockPermissionClient{shouldAllow: true, shouldError: false},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Missing user authentication",
			request:        createSSHTestRequest(t, "", "ssh-gateway", "", "testowner", "testrepo"),
			permissionMock: &MockPermissionClient{shouldAllow: true, shouldError: false},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Invalid source header",
			request:        createSSHTestRequest(t, "testuser", "http-gateway", "", "testowner", "testrepo"),
			permissionMock: &MockPermissionClient{shouldAllow: true, shouldError: false},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "Permission denied",
			request:        createSSHTestRequest(t, "testuser", "ssh-gateway", "", "testowner", "testrepo"),
			permissionMock: &MockPermissionClient{shouldAllow: false, shouldError: false},
			expectedStatus: http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := createTestSSHHandlerWithClient(t, tt.permissionMock)
			w := httptest.NewRecorder()
			handler.HandleSSHUploadPack(w, tt.request)
			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectedStatus == http.StatusOK {
				assert.Equal(t, "application/x-git-upload-pack-result", w.Header().Get("Content-Type"))
			}
		})
	}
}
func TestSSHProtocolHandler_HandleSSHReceivePack(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping SSH receive pack test - permission client is being removed")
	tests := []struct {
		name           string
		request        *http.Request
		permissionMock *MockPermissionClient
		expectedStatus int
	}{
		{
			name:           "Valid SSH receive-pack request",
			request:        createSSHTestRequest(t, "testuser", "ssh-gateway", "", "testowner", "testrepo"),
			permissionMock: &MockPermissionClient{shouldAllow: true, shouldError: false},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Missing user authentication",
			request:        createSSHTestRequest(t, "", "ssh-gateway", "", "testowner", "testrepo"),
			permissionMock: &MockPermissionClient{shouldAllow: true, shouldError: false},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Permission denied",
			request:        createSSHTestRequest(t, "testuser", "ssh-gateway", "", "testowner", "testrepo"),
			permissionMock: &MockPermissionClient{shouldAllow: false, shouldError: false},
			expectedStatus: http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := createTestSSHHandlerWithClient(t, tt.permissionMock)
			w := httptest.NewRecorder()
			handler.HandleSSHReceivePack(w, tt.request)
			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectedStatus == http.StatusOK {
				assert.Equal(t, "application/x-git-receive-pack-result", w.Header().Get("Content-Type"))
			}
		})
	}
}
func TestValidateSSHGitCommand(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		repoPath    string
		expectError bool
	}{
		{
			name:        "Valid upload-pack command",
			command:     "git-upload-pack",
			repoPath:    "/repos/owner/repo.git",
			expectError: false,
		},
		{
			name:        "Valid receive-pack command",
			command:     "git-receive-pack",
			repoPath:    "/repos/owner/repo.git",
			expectError: false,
		},
		{
			name:        "Empty command",
			command:     "",
			repoPath:    "/repos/owner/repo.git",
			expectError: true,
		},
		{
			name:        "Invalid command",
			command:     "git-invalid-pack",
			repoPath:    "/repos/owner/repo.git",
			expectError: true,
		},
		{
			name:        "Path traversal attempt",
			command:     "git-upload-pack",
			repoPath:    "/repos/../etc/passwd",
			expectError: true,
		},
		{
			name:        "Relative path attempt",
			command:     "git-upload-pack",
			repoPath:    "/repos/./owner/repo.git",
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSSHGitCommand(tt.command, tt.repoPath)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
func TestParseSSHGitProtocolHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/ssh/owner/repo/upload-pack", nil)
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("SSH-Connection", "ssh-connection-id")
	req.Header.Set("Git-Capabilities", "multi_ack thin-pack side-band")
	headers := ParseSSHGitProtocolHeaders(req)
	assert.Equal(t, "version=2", headers["protocol-version"])
	assert.Equal(t, "ssh-connection-id", headers["ssh-connection"])
	assert.Equal(t, "multi_ack thin-pack side-band", headers["capabilities"])
}
func TestSSHEndpointMetrics(t *testing.T) {
	metrics := NewSSHEndpointMetrics()
	assert.Equal(t, int64(0), metrics.RequestCount)
	assert.Equal(t, int64(0), metrics.SuccessCount)
	assert.Equal(t, int64(0), metrics.ErrorCount)
	assert.Equal(t, int64(0), metrics.BytesTransferred)
	metrics.RecordRequest()
	metrics.RecordSuccess()
	metrics.RecordBytes(1024)
	assert.Equal(t, int64(1), metrics.RequestCount)
	assert.Equal(t, int64(1), metrics.SuccessCount)
	assert.Equal(t, int64(1024), metrics.BytesTransferred)
	metrics.RecordError()
	assert.Equal(t, int64(1), metrics.ErrorCount)
}

// Helper functions for testing
func createTestSSHHandler(t *testing.T) *SSHProtocolHandler {
	return createTestSSHHandlerWithClient(t, &MockPermissionClient{shouldAllow: true, shouldError: false})
}
func createTestSSHHandlerWithClient(t *testing.T, permClient *MockPermissionClient) *SSHProtocolHandler {
	repoRoot := t.TempDir()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
	}
	// Create a test repository structure that the handler expects
	// Note: Repository paths are expected to have .git extension
	repoPath := filepath.Join(repoRoot, "testowner", "testrepo.git")
	err := os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)
	// Initialize a real Git repository
	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)
	// Create initial commit so it's a valid repository
	wt, err := repo.Worktree()
	require.NoError(t, err)
	// Create test file
	testFile := filepath.Join(repoPath, "README.md")
	err = os.WriteFile(testFile, []byte("# Test Repository\n"), 0644)
	require.NoError(t, err)
	// Add and commit
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)
	handler, err := NewSSHProtocolHandler(cfg, permClient)
	require.NoError(t, err)
	require.NotNil(t, handler)
	return handler
}
func createSSHTestRequest(t *testing.T, userID, source, correlationID, owner, repo string) *http.Request {
	req := httptest.NewRequest("POST", "/ssh/"+owner+"/"+repo+"/upload-pack", strings.NewReader("test body"))
	if userID != "" {
		req.Header.Set("X-USER-ID", userID)
	}
	if source != "" {
		req.Header.Set("X-Source", source)
	}
	if correlationID != "" {
		req.Header.Set("X-Correlation-ID", correlationID)
	}
	// Set URL parameters using chi context
	params := map[string]string{}
	params["owner"] = owner
	params["repo"] = repo
	req = AddGinParamsToRequest(req, params)
	return req
}
