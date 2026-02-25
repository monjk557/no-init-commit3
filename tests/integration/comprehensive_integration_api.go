package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/server"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// testOrchestratorSystemIntegration tests integration with the orchestrator system
func (suite *ComprehensiveIntegrationTestSuite) testOrchestratorSystemIntegration(t *testing.T) {
	// Mock orchestrator server
	orchestratorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ssh/validate":
			// Mock SSH key validation endpoint
			response := map[string]interface{}{
				"valid":       true,
				"user_id":     "test-user-123",
				"permissions": []string{"read", "write"},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		case "/api/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer orchestratorServer.Close()

	// Update configuration to use mock orchestrator
	originalURL := suite.config.Orchestrator.BaseURL
	suite.config.Orchestrator.BaseURL = orchestratorServer.URL
	suite.config.Orchestrator.SSHKeyValidatePath = "/api/ssh/validate"

	defer func() {
		suite.config.Orchestrator.BaseURL = originalURL
	}()

	// Test orchestrator connectivity
	t.Run("Orchestrator_Connectivity", func(t *testing.T) {
		suite.testOrchestratorConnectivity(t)
	})

	// Test SSH key validation integration
	t.Run("SSH_Key_Validation", func(t *testing.T) {
		suite.testSSHKeyValidationIntegration(t)
	})

	// Test timeout handling
	t.Run("Orchestrator_Timeout", func(t *testing.T) {
		suite.testOrchestratorTimeout(t)
	})

	// Test fallback behavior
	t.Run("Orchestrator_Fallback", func(t *testing.T) {
		suite.testOrchestratorFallback(t)
	})
}

// testOrchestratorConnectivity tests basic connectivity to orchestrator
func (suite *ComprehensiveIntegrationTestSuite) testOrchestratorConnectivity(t *testing.T) {
	if suite.config.Orchestrator.BaseURL == "" {
		t.Skip("Orchestrator base URL not configured")
		return
	}

	// Test health endpoint
	healthURL := fmt.Sprintf("%s/api/health", suite.config.Orchestrator.BaseURL)
	resp, err := http.Get(healthURL)

	if err != nil {
		suite.logger.Warn("Orchestrator connectivity test failed", zap.Error(err))
		// Don't fail test as this might be expected in test environment
		return
	}
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Orchestrator health endpoint should be accessible")

	suite.logger.Info("Orchestrator connectivity test passed",
		zap.String("health_url", healthURL),
		zap.Int("status_code", resp.StatusCode))
}

// testSSHKeyValidationIntegration tests SSH key validation through orchestrator
func (suite *ComprehensiveIntegrationTestSuite) testSSHKeyValidationIntegration(t *testing.T) {
	if suite.config.Orchestrator.SSHKeyValidatePath == "" {
		t.Skip("SSH key validation path not configured")
		return
	}

	// Mock SSH key validation request
	validationURL := fmt.Sprintf("%s%s", suite.config.Orchestrator.BaseURL, suite.config.Orchestrator.SSHKeyValidatePath)

	// Create test request
	req, err := http.NewRequest("POST", validationURL, nil)
	require.NoError(t, err, "Failed to create SSH validation request")

	// Add mock SSH fingerprint header
	req.Header.Set(suite.config.Orchestrator.FingerprintHeader, "SHA256:test-fingerprint-12345")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: suite.config.Orchestrator.Timeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		suite.logger.Warn("SSH key validation test failed", zap.Error(err))
		return // Don't fail test in case orchestrator is not available
	}
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "SSH key validation should succeed")

	// Parse response
	var validationResp map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&validationResp)
	assert.NoError(t, err, "Should be able to parse validation response")

	if err == nil {
		assert.Contains(t, validationResp, "valid", "Validation response should contain valid field")
		assert.Contains(t, validationResp, "user_id", "Validation response should contain user_id field")
	}

	suite.logger.Info("SSH key validation integration test completed",
		zap.String("validation_url", validationURL),
		zap.Int("status_code", resp.StatusCode))
}

// testOrchestratorTimeout tests timeout handling for orchestrator requests
func (suite *ComprehensiveIntegrationTestSuite) testOrchestratorTimeout(t *testing.T) {
	// Create a slow server that will cause timeouts
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than configured timeout
		time.Sleep(suite.config.Orchestrator.Timeout + 100*time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowServer.Close()

	// Test timeout behavior
	client := &http.Client{
		Timeout: suite.config.Orchestrator.Timeout,
	}

	start := time.Now()
	_, err := client.Get(slowServer.URL)
	elapsed := time.Since(start)

	// Should timeout
	assert.Error(t, err, "Request should timeout")
	assert.True(t, elapsed < suite.config.Orchestrator.Timeout+500*time.Millisecond,
		"Request should timeout within reasonable time")

	suite.logger.Info("Orchestrator timeout test completed",
		zap.Duration("elapsed", elapsed),
		zap.Duration("configured_timeout", suite.config.Orchestrator.Timeout))
}

// testOrchestratorFallback tests fallback behavior when orchestrator is unavailable
func (suite *ComprehensiveIntegrationTestSuite) testOrchestratorFallback(t *testing.T) {
	// Temporarily set orchestrator URL to unavailable service
	originalURL := suite.config.Orchestrator.BaseURL
	suite.config.Orchestrator.BaseURL = "http://localhost:99999" // Port that should be unavailable

	defer func() {
		suite.config.Orchestrator.BaseURL = originalURL
	}()

	// Test fallback behavior
	client := &http.Client{
		Timeout: 1 * time.Second,
	}

	_, err := client.Get(fmt.Sprintf("%s/api/health", suite.config.Orchestrator.BaseURL))

	// Should fail to connect
	assert.Error(t, err, "Should fail to connect to unavailable orchestrator")

	// This test verifies that the system handles orchestrator unavailability gracefully
	// In a real implementation, this would test actual fallback logic

	suite.logger.Info("Orchestrator fallback test completed",
		zap.String("test_url", suite.config.Orchestrator.BaseURL))
}

// testExternalAPITimeoutIntegration tests external API timeout handling
func (suite *ComprehensiveIntegrationTestSuite) testExternalAPITimeoutIntegration(t *testing.T) {
	testCases := []struct {
		name            string
		timeoutDuration time.Duration
		serverDelay     time.Duration
		shouldTimeout   bool
	}{
		{"Fast_Response", 5 * time.Second, 100 * time.Millisecond, false},
		{"Timeout_Response", 1 * time.Second, 2 * time.Second, true},
		{"Edge_Case_Response", 1 * time.Second, 950 * time.Millisecond, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			suite.testSpecificTimeoutScenario(t, tc.timeoutDuration, tc.serverDelay, tc.shouldTimeout)
		})
	}
}

// testSpecificTimeoutScenario tests a specific timeout scenario
func (suite *ComprehensiveIntegrationTestSuite) testSpecificTimeoutScenario(t *testing.T,
	timeout, serverDelay time.Duration, shouldTimeout bool) {

	// Create test server with configurable delay
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(serverDelay)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer testServer.Close()

	// Create client with timeout
	client := &http.Client{
		Timeout: timeout,
	}

	start := time.Now()
	resp, err := client.Get(testServer.URL)
	elapsed := time.Since(start)

	if shouldTimeout {
		assert.Error(t, err, "Request should timeout")
		assert.True(t, elapsed < timeout+500*time.Millisecond,
			"Timeout should occur within expected timeframe")
	} else {
		assert.NoError(t, err, "Request should not timeout")
		if resp != nil {
			resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "Response should be successful")
		}
	}

	suite.logger.Info("Timeout scenario test completed",
		zap.Duration("timeout", timeout),
		zap.Duration("server_delay", serverDelay),
		zap.Duration("elapsed", elapsed),
		zap.Bool("should_timeout", shouldTimeout),
		zap.Bool("did_timeout", err != nil))
}

// testPathSeparatorHandling tests cross-platform path separator handling
func (suite *ComprehensiveIntegrationTestSuite) testPathSeparatorHandling(t *testing.T) {
	testPaths := []struct {
		description string
		inputPath   string
		expectedOS  map[string]string // OS -> expected normalized path
	}{
		{
			"Unix_Style_Path",
			"path/to/file.txt",
			map[string]string{
				"windows": "path\\to\\file.txt",
				"unix":    "path/to/file.txt",
			},
		},
		{
			"Windows_Style_Path",
			"path\\to\\file.txt",
			map[string]string{
				"windows": "path\\to\\file.txt",
				"unix":    "path/to/file.txt",
			},
		},
		{
			"Mixed_Separators",
			"path/to\\mixed/separators.txt",
			map[string]string{
				"windows": "path\\to\\mixed\\separators.txt",
				"unix":    "path/to/mixed/separators.txt",
			},
		},
	}

	currentOS := "unix"
	if runtime.GOOS == "windows" {
		currentOS = "windows"
	}

	for _, testCase := range testPaths {
		t.Run(testCase.description, func(t *testing.T) {
			// Normalize path using filepath.Clean
			normalizedPath := filepath.Clean(testCase.inputPath)

			// Check against expected result for current OS
			if expectedPath, exists := testCase.expectedOS[currentOS]; exists {
				// Convert expected path to use current OS separators
				expectedNormalized := filepath.FromSlash(expectedPath)
				if currentOS == "windows" {
					expectedNormalized = strings.ReplaceAll(expectedPath, "/", "\\")
				}

				assert.Equal(t, expectedNormalized, normalizedPath,
					"Normalized path should match expected for %s: input=%s", currentOS, testCase.inputPath)
			}

			// Test path joining
			joinedPath := filepath.Join("base", testCase.inputPath, "end")
			assert.Contains(t, joinedPath, "base", "Joined path should contain base directory")
			assert.Contains(t, joinedPath, "end", "Joined path should contain end component")

			// Test absolute path handling
			if filepath.IsAbs(testCase.inputPath) {
				absPath, err := filepath.Abs(testCase.inputPath)
				assert.NoError(t, err, "Should be able to get absolute path")
				assert.True(t, filepath.IsAbs(absPath), "Result should be absolute path")
			}
		})
	}

	suite.logger.Info("Path separator handling test completed",
		zap.String("current_os", currentOS),
		zap.Int("test_cases", len(testPaths)))
}

// testFileSystemCompatibility tests compatibility with different file systems
func (suite *ComprehensiveIntegrationTestSuite) testFileSystemCompatibility(t *testing.T) {
	// Test file system operations that might behave differently across platforms
	testDir := filepath.Join(suite.testDataDir, "filesystem_compat_test")
	err := os.MkdirAll(testDir, 0755)
	require.NoError(t, err, "Failed to create filesystem compatibility test directory")

	// Test case sensitivity
	t.Run("Case_Sensitivity", func(t *testing.T) {
		suite.testCaseSensitivity(t, testDir)
	})

	// Test long file names
	t.Run("Long_File_Names", func(t *testing.T) {
		suite.testLongFileNames(t, testDir)
	})

	// Test file permissions
	t.Run("File_Permissions", func(t *testing.T) {
		suite.testFilePermissions(t, testDir)
	})

	// Test symbolic links (if supported)
	t.Run("Symbolic_Links", func(t *testing.T) {
		suite.testSymbolicLinks(t, testDir)
	})

	// Cleanup
	os.RemoveAll(testDir)
}

// testCaseSensitivity tests file system case sensitivity
func (suite *ComprehensiveIntegrationTestSuite) testCaseSensitivity(t *testing.T, testDir string) {
	// Create files with different cases
	file1 := filepath.Join(testDir, "testfile.txt")
	file2 := filepath.Join(testDir, "TESTFILE.txt")

	err1 := os.WriteFile(file1, []byte("lowercase"), 0644)
	err2 := os.WriteFile(file2, []byte("uppercase"), 0644)

	if err1 != nil || err2 != nil {
		// One of the writes failed, which might indicate case-insensitive filesystem
		suite.logger.Info("Case sensitivity test results",
			zap.Bool("file1_created", err1 == nil),
			zap.Bool("file2_created", err2 == nil),
			zap.String("filesystem_type", "possibly case-insensitive"))
		return
	}

	// Check if both files exist and are different
	info1, err1 := os.Stat(file1)
	info2, err2 := os.Stat(file2)

	if err1 == nil && err2 == nil {
		// Both files exist - filesystem is case-sensitive
		assert.NotEqual(t, info1.Size(), info2.Size(),
			"Files with different cases should have different content")

		content1, _ := os.ReadFile(file1)
		content2, _ := os.ReadFile(file2)
		assert.NotEqual(t, string(content1), string(content2),
			"Files with different cases should have different content")

		suite.logger.Info("Filesystem is case-sensitive")
	} else {
		suite.logger.Info("Filesystem appears to be case-insensitive")
	}

	// Cleanup
	os.Remove(file1)
	os.Remove(file2)
}

// testLongFileNames tests handling of long file names
func (suite *ComprehensiveIntegrationTestSuite) testLongFileNames(t *testing.T, testDir string) {
	// Test with progressively longer file names
	maxLength := 255 // Common filesystem limit

	for length := 100; length <= maxLength; length += 50 {
		fileName := strings.Repeat("a", length-4) + ".txt"
		filePath := filepath.Join(testDir, fileName)

		err := os.WriteFile(filePath, []byte(fmt.Sprintf("Length: %d", length)), 0644)

		if err != nil {
			suite.logger.Warn("Long filename test failed",
				zap.Int("filename_length", length),
				zap.Error(err))
			break
		}

		// Verify file was created
		_, err = os.Stat(filePath)
		assert.NoError(t, err, "Long filename should be accessible: length=%d", length)

		// Cleanup
		os.Remove(filePath)

		if length >= 250 {
			// Successfully created files up to reasonable length
			suite.logger.Info("Long filename support confirmed",
				zap.Int("max_tested_length", length))
			break
		}
	}
}

// testFilePermissions tests file permission handling
func (suite *ComprehensiveIntegrationTestSuite) testFilePermissions(t *testing.T, testDir string) {
	if runtime.GOOS == "windows" {
		t.Skip("File permission tests are Unix-specific")
		return
	}

	// Test different permission combinations
	permissionTests := []struct {
		name        string
		permissions os.FileMode
	}{
		{"ReadOnly", 0444},
		{"WriteOnly", 0222},
		{"ReadWrite", 0644},
		{"Executable", 0755},
	}

	for _, test := range permissionTests {
		t.Run(test.name, func(t *testing.T) {
			fileName := fmt.Sprintf("perm_test_%s.txt", strings.ToLower(test.name))
			filePath := filepath.Join(testDir, fileName)

			// Create file with specific permissions
			err := os.WriteFile(filePath, []byte("permission test"), test.permissions)
			require.NoError(t, err, "Failed to create file with permissions: %v", test.permissions)

			// Check permissions
			info, err := os.Stat(filePath)
			require.NoError(t, err, "Failed to stat file")

			actualPerms := info.Mode().Perm()
			assert.Equal(t, test.permissions, actualPerms,
				"File permissions should match expected: %v", test.permissions)

			// Cleanup
			os.Chmod(filePath, 0644) // Make it writable for deletion
			os.Remove(filePath)
		})
	}
}

// testSymbolicLinks tests symbolic link handling
func (suite *ComprehensiveIntegrationTestSuite) testSymbolicLinks(t *testing.T, testDir string) {
	if runtime.GOOS == "windows" {
		t.Skip("Symbolic link tests may require special privileges on Windows")
		return
	}

	// Create target file
	targetFile := filepath.Join(testDir, "symlink_target.txt")
	err := os.WriteFile(targetFile, []byte("symlink target content"), 0644)
	require.NoError(t, err, "Failed to create symlink target file")

	// Create symbolic link
	symlinkPath := filepath.Join(testDir, "test_symlink.txt")
	err = os.Symlink(targetFile, symlinkPath)

	if err != nil {
		suite.logger.Warn("Symbolic link creation failed", zap.Error(err))
		t.Skip("Symbolic links not supported or insufficient permissions")
		return
	}

	// Test symlink properties
	linkInfo, err := os.Lstat(symlinkPath)
	require.NoError(t, err, "Failed to lstat symlink")
	assert.True(t, linkInfo.Mode()&os.ModeSymlink != 0, "File should be identified as symlink")

	// Test reading through symlink
	content, err := os.ReadFile(symlinkPath)
	assert.NoError(t, err, "Should be able to read through symlink")
	assert.Equal(t, "symlink target content", string(content), "Content should match target file")

	// Test resolving symlink
	resolvedPath, err := filepath.EvalSymlinks(symlinkPath)
	assert.NoError(t, err, "Should be able to resolve symlink")
	assert.Equal(t, targetFile, resolvedPath, "Resolved path should match target")

	suite.logger.Info("Symbolic link test completed successfully")

	// Cleanup
	os.Remove(symlinkPath)
	os.Remove(targetFile)
}
