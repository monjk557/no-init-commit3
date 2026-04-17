package testutils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/server"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// IntegrationTestHarness provides comprehensive end-to-end testing utilities
type IntegrationTestHarness struct {
	// Core components
	Config           *config.Config
	Logger           *logging.Logger
	Server           *server.Server
	PermissionClient *MockPermissionClient

	// Test infrastructure
	TempDir    string
	TestServer *httptest.Server
	TestRepos  map[string]*TestRepository

	// Synchronization
	mu sync.RWMutex

	// Cleanup functions
	cleanupFuncs []func() error
}

// TestRepository represents a test Git repository with known state
type TestRepository struct {
	Name    string
	Path    string
	Refs    map[string]string // ref name -> commit hash
	Commits []TestCommit
	Files   map[string]string // file path -> content
	IsBare  bool
}

// TestCommit represents a test commit with known metadata
type TestCommit struct {
	Hash      string
	Message   string
	Author    string
	Timestamp time.Time
	Files     []string
}

// GitClientSimulator simulates Git client requests for testing
type GitClientSimulator struct {
	BaseURL    string
	UserID     string
	UserAgent  string
	HTTPClient *http.Client
}

// MockPermissionClient provides controllable permission responses for testing
type MockPermissionClient struct {
	mu            sync.RWMutex
	Permissions   map[string]bool // "user:repo" -> allowed
	ShouldError   bool
	ErrorResponse error
	ResponseDelay time.Duration
	CallLog       []PermissionCall
}

// PermissionCall represents a logged permission check call
type PermissionCall struct {
	UserID    string
	RepoOwner string
	RepoName  string
	Operation string
	Timestamp time.Time
	Result    bool
	Error     error
}

// NewIntegrationTestHarness creates a new integration test harness
func NewIntegrationTestHarness(t *testing.T) *IntegrationTestHarness {
	tempDir := t.TempDir()

	zapLogger := zaptest.NewLogger(t)
	logger := &logging.Logger{Logger: zapLogger}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            0, // Let the test server choose a port
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			BasePath:        "/",
			HealthCheckPath: "/health",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level:  "debug",
			Format: "text",
			Output: "stdout",
		},
	}

	permClient := &MockPermissionClient{
		Permissions: make(map[string]bool),
		CallLog:     make([]PermissionCall, 0),
	}

	harness := &IntegrationTestHarness{
		Config:           cfg,
		Logger:           logger,
		PermissionClient: permClient,
		TempDir:          tempDir,
		TestRepos:        make(map[string]*TestRepository),
		cleanupFuncs:     make([]func() error, 0),
	}

	// Initialize server
	srv, err := server.New(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	harness.Server = srv

	// Start test HTTP server
	// Note: For testing, we'll access the router setup method directly
	// In production, this would be handled by the server's Start() method
	router := srv.SetupRouter()
	harness.TestServer = httptest.NewServer(router)
	harness.cleanupFuncs = append(harness.cleanupFuncs, func() error {
		harness.TestServer.Close()
		return nil
	})

	return harness
}

// CreateTestRepository creates a test repository with known state
func (h *IntegrationTestHarness) CreateTestRepository(name string, owner string) *TestRepository {
	h.mu.Lock()
	defer h.mu.Unlock()

	repoKey := fmt.Sprintf("%s/%s", owner, name)
	repoPath := filepath.Join(h.TempDir, owner, fmt.Sprintf("%s.git", name))

	// Create repository directory
	err := os.MkdirAll(repoPath, 0755)
	if err != nil {
		panic(fmt.Sprintf("failed to create test repo directory: %v", err))
	}

	// Create basic Git repository structure
	dirs := []string{"objects", "refs/heads", "refs/tags"}
	for _, dir := range dirs {
		dirPath := filepath.Join(repoPath, dir)
		err := os.MkdirAll(dirPath, 0755)
		if err != nil {
			panic(fmt.Sprintf("failed to create repo subdirectory %s: %v", dir, err))
		}
	}

	// Create basic Git files
	files := map[string]string{
		"HEAD":        "ref: refs/heads/main\n",
		"description": "Test repository for integration testing\n",
		"config":      "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n",
	}

	for filename, content := range files {
		filePath := filepath.Join(repoPath, filename)
		err := os.WriteFile(filePath, []byte(content), 0644)
		if err != nil {
			panic(fmt.Sprintf("failed to create repo file %s: %v", filename, err))
		}
	}

	testRepo := &TestRepository{
		Name:    repoKey,
		Path:    repoPath,
		Refs:    make(map[string]string),
		Commits: make([]TestCommit, 0),
		Files:   make(map[string]string),
		IsBare:  true,
	}

	h.TestRepos[repoKey] = testRepo
	return testRepo
}

// GrantPermission grants permission for a user to access a repository
func (h *IntegrationTestHarness) GrantPermission(userID, repoOwner, repoName string) {
	h.PermissionClient.mu.Lock()
	defer h.PermissionClient.mu.Unlock()

	key := fmt.Sprintf("%s:%s/%s", userID, repoOwner, repoName)
	h.PermissionClient.Permissions[key] = true
}

// DenyPermission denies permission for a user to access a repository
func (h *IntegrationTestHarness) DenyPermission(userID, repoOwner, repoName string) {
	h.PermissionClient.mu.Lock()
	defer h.PermissionClient.mu.Unlock()

	key := fmt.Sprintf("%s:%s/%s", userID, repoOwner, repoName)
	h.PermissionClient.Permissions[key] = false
}

// NewGitClientSimulator creates a new Git client simulator
func (h *IntegrationTestHarness) NewGitClientSimulator(userID string) *GitClientSimulator {
	return &GitClientSimulator{
		BaseURL:    h.TestServer.URL,
		UserID:     userID,
		UserAgent:  "git/2.40.0 (test-client)",
		HTTPClient: h.TestServer.Client(),
	}
}

// SimulateGitClone simulates a git clone operation
func (g *GitClientSimulator) SimulateGitClone(owner, repo string) (*http.Response, error) {
	// Step 1: Get info/refs
	infoRefsURL := fmt.Sprintf("%s/%s/%s/info/refs?service=git-upload-pack", g.BaseURL, owner, repo)
	req, err := http.NewRequest("GET", infoRefsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create info/refs request: %w", err)
	}

	req.Header.Set("X-USER-ID", g.UserID)
	req.Header.Set("User-Agent", g.UserAgent)
	req.Header.Set("X-REQUEST-ID", fmt.Sprintf("clone-req-%d", time.Now().Unix()))

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute info/refs request: %w", err)
	}

	// For now, just return the info/refs response
	// In a more complete implementation, we'd parse the response and make the upload-pack request
	return resp, nil
}

// SimulateGitFetch simulates a git fetch operation with specific wants/haves
func (g *GitClientSimulator) SimulateGitFetch(owner, repo string, wants, haves []string) (*http.Response, error) {
	// Create upload-pack request body
	var requestBody bytes.Buffer

	// Write want lines
	for _, want := range wants {
		line := fmt.Sprintf("want %s multi_ack side-band-64k ofs-delta agent=%s\n", want, g.UserAgent)
		requestBody.WriteString(fmt.Sprintf("%04x%s", len(line)+4, line))
	}

	// Write have lines
	for _, have := range haves {
		line := fmt.Sprintf("have %s\n", have)
		requestBody.WriteString(fmt.Sprintf("%04x%s", len(line)+4, line))
	}

	// Write done
	requestBody.WriteString("0009done\n")
	requestBody.WriteString("0000") // Flush packet

	uploadPackURL := fmt.Sprintf("%s/%s/%s/git-upload-pack", g.BaseURL, owner, repo)
	req, err := http.NewRequest("POST", uploadPackURL, &requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create upload-pack request: %w", err)
	}

	req.Header.Set("X-USER-ID", g.UserID)
	req.Header.Set("User-Agent", g.UserAgent)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("X-REQUEST-ID", fmt.Sprintf("fetch-req-%d", time.Now().Unix()))

	return g.HTTPClient.Do(req)
}

// ValidateProtocolResponse validates that a response follows Git Smart HTTP protocol
func (g *GitClientSimulator) ValidateProtocolResponse(resp *http.Response, expectedContentType string) error {
	// Check content type
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, expectedContentType) {
		return fmt.Errorf("invalid content type: expected %s, got %s", expectedContentType, contentType)
	}

	// Read and validate pkt-line format
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	return g.validatePktLineFormat(body)
}

// validatePktLineFormat validates that the response follows pkt-line format
func (g *GitClientSimulator) validatePktLineFormat(data []byte) error {
	reader := bytes.NewReader(data)

	for {
		// Read 4-byte length header
		lengthBytes := make([]byte, 4)
		n, err := reader.Read(lengthBytes)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read pkt-line length: %w", err)
		}
		if n != 4 {
			return fmt.Errorf("incomplete pkt-line length header")
		}

		// Parse length
		lengthStr := string(lengthBytes)
		if lengthStr == "0000" {
			// Flush packet
			continue
		}

		// Parse hex length
		var length int
		_, err = fmt.Sscanf(lengthStr, "%04x", &length)
		if err != nil {
			return fmt.Errorf("invalid pkt-line length format: %s", lengthStr)
		}

		// Validate length bounds
		if length < 4 || length > 65520 {
			return fmt.Errorf("invalid pkt-line length: %d", length)
		}

		// Read the data portion
		dataLength := length - 4
		dataBytes := make([]byte, dataLength)
		n, err = reader.Read(dataBytes)
		if err != nil {
			return fmt.Errorf("failed to read pkt-line data: %w", err)
		}
		if n != dataLength {
			return fmt.Errorf("incomplete pkt-line data")
		}
	}

	return nil
}

// GetPermissionCallLog returns the log of permission calls made during testing
func (h *IntegrationTestHarness) GetPermissionCallLog() []PermissionCall {
	h.PermissionClient.mu.RLock()
	defer h.PermissionClient.mu.RUnlock()

	// Return a copy to avoid race conditions
	calls := make([]PermissionCall, len(h.PermissionClient.CallLog))
	copy(calls, h.PermissionClient.CallLog)
	return calls
}

// Cleanup cleans up all test resources
func (h *IntegrationTestHarness) Cleanup() error {
	var errors []error

	for _, cleanup := range h.cleanupFuncs {
		if err := cleanup(); err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %v", errors)
	}

	return nil
}

// Mock implementation of PermissionClient interface
func (m *MockPermissionClient) CheckRepositoryAccess(ctx context.Context, userID, repoOwner, repoName, operation string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Add delay if configured
	if m.ResponseDelay > 0 {
		time.Sleep(m.ResponseDelay)
	}

	// Return configured error if set
	if m.ShouldError {
		call := PermissionCall{
			UserID:    userID,
			RepoOwner: repoOwner,
			RepoName:  repoName,
			Operation: operation,
			Timestamp: time.Now(),
			Result:    false,
			Error:     m.ErrorResponse,
		}
		m.CallLog = append(m.CallLog, call)
		return false, m.ErrorResponse
	}

	// Check permission
	key := fmt.Sprintf("%s:%s/%s", userID, repoOwner, repoName)
	allowed, exists := m.Permissions[key]
	if !exists {
		allowed = false // Default deny
	}

	call := PermissionCall{
		UserID:    userID,
		RepoOwner: repoOwner,
		RepoName:  repoName,
		Operation: operation,
		Timestamp: time.Now(),
		Result:    allowed,
		Error:     nil,
	}
	m.CallLog = append(m.CallLog, call)

	return allowed, nil
}

// WaitForCondition waits for a condition to be met or times out
func (h *IntegrationTestHarness) WaitForCondition(condition func() bool, timeout time.Duration, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for condition: %s", message)
		case <-ticker.C:
			if condition() {
				return nil
			}
		}
	}
}

// AssertEndToEndFlow validates a complete end-to-end Git operation flow
func (h *IntegrationTestHarness) AssertEndToEndFlow(t *testing.T, userID, owner, repo string) {
	// Create test repository
	testRepo := h.CreateTestRepository(repo, owner)
	require.NotNil(t, testRepo)

	// Grant permission
	h.GrantPermission(userID, owner, repo)

	// Create Git client simulator
	client := h.NewGitClientSimulator(userID)

	// Simulate git clone (info/refs step)
	resp, err := client.SimulateGitClone(owner, repo)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	defer func() { _ = resp.Body.Close() }()

	// Validate protocol compliance
	err = client.ValidateProtocolResponse(resp, "application/x-git-upload-pack-advertisement")
	require.NoError(t, err)

	// Verify permission was checked
	calls := h.GetPermissionCallLog()
	require.NotEmpty(t, calls)

	lastCall := calls[len(calls)-1]
	require.Equal(t, userID, lastCall.UserID)
	require.Equal(t, owner, lastCall.RepoOwner)
	require.Equal(t, repo, lastCall.RepoName)
	require.True(t, lastCall.Result)
}

// BenchmarkEndToEndPerformance benchmarks end-to-end operation performance
func (h *IntegrationTestHarness) BenchmarkEndToEndPerformance(b *testing.B, operation string) {
	// Setup
	userID := "bench-user"
	owner := "bench-owner"
	repo := "bench-repo"

	h.CreateTestRepository(repo, owner)
	h.GrantPermission(userID, owner, repo)
	client := h.NewGitClientSimulator(userID)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.SimulateGitClone(owner, repo)
			if err != nil {
				b.Errorf("Benchmark failed: %v", err)
				return
			}
			_ = resp.Body.Close()
		}
	})
}
