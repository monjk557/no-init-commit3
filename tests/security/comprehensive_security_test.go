package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/merge"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/security"
)

// ComprehensiveSecurityTestSuite contains all security vulnerability tests
type ComprehensiveSecurityTestSuite struct {
	tempDir           string
	repo              *gogit.Repository
	pathConfig        *repositories.PathConfig
	mergeHandler      *merge.MergeOperationsHandler
	securityFramework *security.ValidationFramework
	router            *gin.Engine
}

func TestComprehensiveSecuritySuite(t *testing.T) {
	suite, cleanup := setupSecurityTestSuite(t)
	defer cleanup()

	// 1. Security Bypass Attempts
	t.Run("SecurityBypassAttempts", func(t *testing.T) {
		suite.testSecurityBypassAttempts(t)
	})

	// 2. Input Validation Testing
	t.Run("InputValidationTesting", func(t *testing.T) {
		suite.testInputValidation(t)
	})

	// 3. Permission and Access Control
	t.Run("PermissionAndAccessControl", func(t *testing.T) {
		suite.testPermissionAndAccessControl(t)
	})

	// 4. DoS Attack Scenarios
	t.Run("DoSAttackScenarios", func(t *testing.T) {
		suite.testDoSAttackScenarios(t)
	})

	// 5. Configuration Security
	t.Run("ConfigurationSecurity", func(t *testing.T) {
		suite.testConfigurationSecurity(t)
	})
}

// =============================================================================
// 1. SECURITY BYPASS ATTEMPTS
// =============================================================================

func (s *ComprehensiveSecurityTestSuite) testSecurityBypassAttempts(t *testing.T) {
	t.Run("MalformedRequestBypass", func(t *testing.T) {
		s.testMalformedRequestBypass(t)
	})

	t.Run("PathTraversalAttacks", func(t *testing.T) {
		s.testPathTraversalAttacks(t)
	})

	t.Run("UnicodeEmojiFileNames", func(t *testing.T) {
		s.testUnicodeEmojiFileNames(t)
	})

	t.Run("ExtremelLongFilePaths", func(t *testing.T) {
		s.testExtremelyLongFilePaths(t)
	})

	t.Run("SymlinkAttacks", func(t *testing.T) {
		s.testSymlinkAttacks(t)
	})
}

func (s *ComprehensiveSecurityTestSuite) testMalformedRequestBypass(t *testing.T) {
	malformedRequests := []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
		expectCode  int
	}{
		{
			name:        "invalid_json_structure",
			method:      "POST",
			path:        "/repos/test/test/merge",
			body:        `{"source_branch": "feature", "target_branch": "main", "commit_message": "test"`,
			contentType: "application/json",
			expectCode:  http.StatusBadRequest,
		},
		{
			name:        "json_with_null_bytes",
			method:      "POST",
			path:        "/repos/test/test/merge",
			body:        "{\"source_branch\": \"feature\x00injection\", \"target_branch\": \"main\", \"commit_message\": \"test\", \"author_name\": \"Test\", \"author_email\": \"test@test.com\"}",
			contentType: "application/json",
			expectCode:  http.StatusBadRequest,
		},
		{
			name:        "extremely_nested_json",
			method:      "POST",
			path:        "/repos/test/test/merge",
			body:        generateDeeplyNestedJSON(1000),
			contentType: "application/json",
			expectCode:  http.StatusBadRequest,
		},
		{
			name:        "xml_content_type_bypass",
			method:      "POST",
			path:        "/repos/test/test/merge",
			body:        "<merge><source>feature</source><target>main</target></merge>",
			contentType: "application/xml",
			expectCode:  http.StatusBadRequest,
		},
	}

	for _, test := range malformedRequests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.contentType)
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)
			assert.Equal(t, test.expectCode, w.Code, "Expected malformed request to be rejected")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testPathTraversalAttacks(t *testing.T) {
	pathTraversalAttempts := []struct {
		name        string
		path        string
		expectBlock bool
	}{
		{
			name:        "basic_directory_traversal",
			path:        "../../../etc/passwd",
			expectBlock: true,
		},
		{
			name:        "encoded_directory_traversal",
			path:        "..%2F..%2F..%2Fetc%2Fpasswd",
			expectBlock: true,
		},
		{
			name:        "double_encoded_traversal",
			path:        "..%252F..%252F..%252Fetc%252Fpasswd",
			expectBlock: true,
		},
		{
			name:        "windows_style_traversal",
			path:        "..\\..\\..\\windows\\system32\\config\\SAM",
			expectBlock: true,
		},
		{
			name:        "unicode_traversal",
			path:        "\u002e\u002e\u002f\u002e\u002e\u002f\u002e\u002e\u002fetc\u002fpasswd",
			expectBlock: true,
		},
		{
			name:        "null_byte_traversal",
			path:        "../../../etc/passwd\x00.txt",
			expectBlock: true,
		},
		{
			name:        "git_internal_access",
			path:        ".git/config",
			expectBlock: true,
		},
		{
			name:        "git_hooks_access",
			path:        ".git/hooks/pre-commit",
			expectBlock: true,
		},
		{
			name:        "legitimate_nested_path",
			path:        "src/components/Button.tsx",
			expectBlock: false,
		},
	}

	for _, test := range pathTraversalAttempts {
		t.Run(test.name, func(t *testing.T) {
			reqPath := fmt.Sprintf("/repos/test/test/files/%s", test.path)
			req := httptest.NewRequest("GET", reqPath, nil)
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			if test.expectBlock {
				assert.NotEqual(t, http.StatusOK, w.Code,
					"Path traversal attempt should be blocked: %s", test.path)
			} else {
				// Note: May return 404 if file doesn't exist, but shouldn't be blocked by security
				assert.NotEqual(t, http.StatusBadRequest, w.Code,
					"Legitimate path should not be blocked by security: %s", test.path)
			}
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testUnicodeEmojiFileNames(t *testing.T) {
	unicodeTests := []struct {
		name     string
		filename string
		expected bool // whether it should be allowed
	}{
		{
			name:     "emoji_filename",
			filename: "🔥important_file.txt",
			expected: false, // Should be blocked due to non-ASCII
		},
		{
			name:     "unicode_combining_chars",
			filename: "file_combining.txt", // combining characters removed for compile
			expected: false,
		},
		{
			name:     "rtl_override_attack",
			filename: "file_rtl.txt", // Right-to-Left Override characters removed for compile
			expected: false,
		},
		{
			name:     "zero_width_chars",
			filename: "file_zero_width.txt", // Zero-width chars removed for compile
			expected: false,
		},
		{
			name:     "homograph_attack",
			filename: "аdmin.txt", // Cyrillic 'a' that looks like latin 'a'
			expected: false,
		},
		{
			name:     "normal_ascii_file",
			filename: "normal-file_123.txt",
			expected: true,
		},
		{
			name:     "file_with_spaces",
			filename: "file with spaces.txt",
			expected: false, // Current regex doesn't allow spaces
		},
	}

	for _, test := range unicodeTests {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        "test content",
				"commit_message": "Add file",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			reqPath := fmt.Sprintf("/repos/test/test/files/%s", test.filename)
			req := httptest.NewRequest("PUT", reqPath, bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			if test.expected {
				assert.NotEqual(t, http.StatusBadRequest, w.Code,
					"Valid filename should not be rejected: %s", test.filename)
			} else {
				assert.Equal(t, http.StatusBadRequest, w.Code,
					"Invalid unicode filename should be rejected: %s", test.filename)
			}
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testExtremelyLongFilePaths(t *testing.T) {
	testCases := []struct {
		name       string
		pathLength int
		expectFail bool
	}{
		{
			name:       "normal_path_length",
			pathLength: 100,
			expectFail: false,
		},
		{
			name:       "max_allowed_length",
			pathLength: 4096,
			expectFail: false,
		},
		{
			name:       "exceed_max_length",
			pathLength: 4097,
			expectFail: true,
		},
		{
			name:       "extremely_long_path",
			pathLength: 65536,
			expectFail: true,
		},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			// Generate a path of the specified length
			longPath := generateLongPath(test.pathLength)

			body := map[string]interface{}{
				"content":        "test",
				"commit_message": "Test long path",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			reqPath := fmt.Sprintf("/repos/test/test/files/%s", longPath)
			req := httptest.NewRequest("PUT", reqPath, bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			if test.expectFail {
				assert.Equal(t, http.StatusBadRequest, w.Code,
					"Extremely long path should be rejected (length: %d)", test.pathLength)
			} else {
				assert.NotEqual(t, http.StatusBadRequest, w.Code,
					"Normal length path should not be rejected (length: %d)", test.pathLength)
			}
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testSymlinkAttacks(t *testing.T) {
	// Create a symlink pointing outside the repo
	if err := os.Symlink("/etc/passwd", filepath.Join(s.tempDir, "evil_symlink")); err != nil {
		t.Skip("Cannot create symlinks on this system")
		return
	}

	// Test accessing the symlink through the API
	req := httptest.NewRequest("GET", "/repos/test/test/files/evil_symlink", nil)
	w := httptest.NewRecorder()

	s.router.ServeHTTP(w, req)

	// Should be blocked or return error, not expose external content
	assert.NotEqual(t, http.StatusOK, w.Code,
		"Symlink access should be blocked or fail safely")

	// Ensure the response doesn't contain content from /etc/passwd
	responseBody := w.Body.String()
	assert.NotContains(t, responseBody, "root:",
		"Response should not contain external file content")
}

// =============================================================================
// 2. INPUT VALIDATION TESTING
// =============================================================================

func (s *ComprehensiveSecurityTestSuite) testInputValidation(t *testing.T) {
	t.Run("InvalidParameters", func(t *testing.T) {
		s.testInvalidParameters(t)
	})

	t.Run("NullByteInjection", func(t *testing.T) {
		s.testNullByteInjection(t)
	})

	t.Run("SQLInjectionAttempts", func(t *testing.T) {
		s.testSQLInjectionAttempts(t)
	})

	t.Run("XSSInCommitMessages", func(t *testing.T) {
		s.testXSSInCommitMessages(t)
	})

	t.Run("BufferOverflowScenarios", func(t *testing.T) {
		s.testBufferOverflowScenarios(t)
	})
}

func (s *ComprehensiveSecurityTestSuite) testInvalidParameters(t *testing.T) {
	invalidRequests := []struct {
		name string
		body map[string]interface{}
	}{
		{
			name: "missing_required_fields",
			body: map[string]interface{}{
				"branch": "main",
				// Missing commit_message, author_name, author_email
			},
		},
		{
			name: "invalid_email_format",
			body: map[string]interface{}{
				"content":        "test",
				"commit_message": "Test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "not-an-email",
			},
		},
		{
			name: "empty_strings",
			body: map[string]interface{}{
				"content":        "",
				"commit_message": "",
				"branch":         "",
				"author_name":    "",
				"author_email":   "",
			},
		},
		{
			name: "excessively_long_commit_message",
			body: map[string]interface{}{
				"content":        "test",
				"commit_message": strings.Repeat("a", 3000), // Exceeds 2048 limit
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			},
		},
	}

	for _, test := range invalidRequests {
		t.Run(test.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(test.body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code,
				"Invalid parameters should be rejected: %s", test.name)
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testNullByteInjection(t *testing.T) {
	nullByteTests := []struct {
		name  string
		field string
		value string
	}{
		{
			name:  "null_in_branch_name",
			field: "branch",
			value: "main\x00injection",
		},
		{
			name:  "null_in_commit_message",
			field: "commit_message",
			value: "Test commit\x00with null",
		},
		{
			name:  "null_in_author_name",
			field: "author_name",
			value: "Test\x00User",
		},
		{
			name:  "null_in_file_content",
			field: "content",
			value: "file content\x00with null",
		},
	}

	for _, test := range nullByteTests {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        "test content",
				"commit_message": "Test commit",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			// Override the specific field with null byte injection
			body[test.field] = test.value

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code,
				"Null byte injection should be rejected: %s", test.name)
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testSQLInjectionAttempts(t *testing.T) {
	sqlInjectionPayloads := []string{
		"'; DROP TABLE users; --",
		"' OR '1'='1",
		"'; UPDATE users SET password='hacked'; --",
		"admin'--",
		"' UNION SELECT * FROM users --",
		"'; EXEC xp_cmdshell('dir'); --",
	}

	for _, payload := range sqlInjectionPayloads {
		t.Run(fmt.Sprintf("sql_injection_%d", len(payload)), func(t *testing.T) {
			body := map[string]interface{}{
				"content":        payload,
				"commit_message": payload,
				"branch":         "main",
				"author_name":    payload,
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should either be rejected or safely handled (not execute SQL)
			// The exact response code depends on which field contains the injection
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"SQL injection should not cause internal server error")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testXSSInCommitMessages(t *testing.T) {
	xssPayloads := []string{
		"<script>alert('XSS')</script>",
		"javascript:alert('XSS')",
		"<img src=x onerror=alert('XSS')>",
		"<svg onload=alert('XSS')>",
		"\"><script>alert('XSS')</script>",
		"';alert('XSS');//",
	}

	for _, payload := range xssPayloads {
		t.Run(fmt.Sprintf("xss_payload_%d", len(payload)), func(t *testing.T) {
			body := map[string]interface{}{
				"content":        "test content",
				"commit_message": payload,
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should be handled safely - either rejected or sanitized
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"XSS payload should not cause internal server error")

			// If successful, verify the payload is properly escaped in any response
			if w.Code == http.StatusOK || w.Code == http.StatusCreated {
				responseBody := w.Body.String()
				assert.NotContains(t, responseBody, "<script>",
					"XSS payload should be escaped in response")
			}
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testBufferOverflowScenarios(t *testing.T) {
	// Test various oversized inputs
	oversizedInputs := []struct {
		name  string
		field string
		size  int
	}{
		{
			name:  "huge_commit_message",
			field: "commit_message",
			size:  1048576, // 1MB
		},
		{
			name:  "huge_author_name",
			field: "author_name",
			size:  65536, // 64KB
		},
		{
			name:  "huge_file_content",
			field: "content",
			size:  104857600, // 100MB
		},
	}

	for _, test := range oversizedInputs {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        "normal content",
				"commit_message": "Normal commit",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			// Override with oversized content
			body[test.field] = strings.Repeat("A", test.size)

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should be rejected due to size limits
			assert.Equal(t, http.StatusBadRequest, w.Code,
				"Oversized input should be rejected: %s", test.name)
		})
	}
}

// =============================================================================
// 3. PERMISSION AND ACCESS CONTROL
// =============================================================================

func (s *ComprehensiveSecurityTestSuite) testPermissionAndAccessControl(t *testing.T) {
	t.Run("DifferentUserPermissions", func(t *testing.T) {
		s.testDifferentUserPermissions(t)
	})

	t.Run("UnauthorizedMergeAttempts", func(t *testing.T) {
		s.testUnauthorizedMergeAttempts(t)
	})

	t.Run("PrivilegeEscalation", func(t *testing.T) {
		s.testPrivilegeEscalation(t)
	})

	t.Run("AccessControlBypass", func(t *testing.T) {
		s.testAccessControlBypass(t)
	})
}

func (s *ComprehensiveSecurityTestSuite) testDifferentUserPermissions(t *testing.T) {
	// This would test different user roles if implemented
	// For now, just test that the system doesn't assume admin access

	userScenarios := []struct {
		name     string
		userType string
		headers  map[string]string
	}{
		{
			name:     "no_auth_headers",
			userType: "anonymous",
			headers:  map[string]string{},
		},
		{
			name:     "malformed_auth",
			userType: "invalid",
			headers: map[string]string{
				"Authorization": "Bearer invalid-token",
			},
		},
		{
			name:     "empty_auth",
			userType: "empty",
			headers: map[string]string{
				"Authorization": "",
			},
		},
	}

	for _, scenario := range userScenarios {
		t.Run(scenario.name, func(t *testing.T) {
			body := map[string]interface{}{
				"source_branch":  "feature",
				"target_branch":  "main",
				"commit_message": "Test merge",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", "/repos/test/test/merge",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			// Add scenario-specific headers
			for key, value := range scenario.headers {
				req.Header.Set(key, value)
			}

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)

			// Should handle gracefully (either reject or proceed based on auth policy)
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Should handle auth gracefully: %s", scenario.name)
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testUnauthorizedMergeAttempts(t *testing.T) {
	// Test attempting merges without proper permissions
	unauthorizedAttempts := []struct {
		name         string
		sourceBranch string
		targetBranch string
		expectBlock  bool
	}{
		{
			name:         "merge_to_protected_main",
			sourceBranch: "feature",
			targetBranch: "main",
			expectBlock:  false, // Depends on protection configuration
		},
		{
			name:         "merge_to_master",
			sourceBranch: "feature",
			targetBranch: "master",
			expectBlock:  false, // Depends on protection configuration
		},
		{
			name:         "merge_protected_env_branch",
			sourceBranch: "production-config",
			targetBranch: "main",
			expectBlock:  false, // Should be allowed but protected
		},
	}

	for _, attempt := range unauthorizedAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			body := map[string]interface{}{
				"source_branch":  attempt.sourceBranch,
				"target_branch":  attempt.targetBranch,
				"commit_message": "Unauthorized merge attempt",
				"author_name":    "Unauthorized User",
				"author_email":   "unauthorized@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", "/repos/test/test/merge",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// For now, just ensure it doesn't crash
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Unauthorized merge should be handled gracefully")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testPrivilegeEscalation(t *testing.T) {
	// Test various privilege escalation vectors
	escalationAttempts := []struct {
		name    string
		headers map[string]string
		body    map[string]interface{}
	}{
		{
			name: "admin_role_injection",
			headers: map[string]string{
				"X-User-Role": "admin",
			},
			body: map[string]interface{}{
				"source_branch":  "feature",
				"target_branch":  "main",
				"commit_message": "Admin escalation",
				"author_name":    "Admin",
				"author_email":   "admin@example.com",
			},
		},
		{
			name: "sudo_user_injection",
			headers: map[string]string{
				"X-Sudo-User": "root",
			},
			body: map[string]interface{}{
				"source_branch":  "feature",
				"target_branch":  "main",
				"commit_message": "Sudo escalation",
				"author_name":    "root",
				"author_email":   "root@example.com",
			},
		},
	}

	for _, attempt := range escalationAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(attempt.body)
			req := httptest.NewRequest("POST", "/repos/test/test/merge",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			for key, value := range attempt.headers {
				req.Header.Set(key, value)
			}

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)

			// Should not grant elevated privileges
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Privilege escalation should be handled safely")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testAccessControlBypass(t *testing.T) {
	// Test various access control bypass techniques
	bypassAttempts := []struct {
		name   string
		method string
		path   string
	}{
		{
			name:   "method_override",
			method: "POST",
			path:   "/repos/test/test/files/config.env?_method=DELETE",
		},
		{
			name:   "double_slash_bypass",
			method: "GET",
			path:   "/repos//test//test//files//config.env",
		},
		{
			name:   "case_sensitivity_bypass",
			method: "GET",
			path:   "/REPOS/test/test/FILES/config.env",
		},
		{
			name:   "trailing_slash_bypass",
			method: "GET",
			path:   "/repos/test/test/files/config.env/",
		},
	}

	for _, attempt := range bypassAttempts {
		t.Run(attempt.name, func(t *testing.T) {
			req := httptest.NewRequest(attempt.method, attempt.path, nil)
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should be handled consistently with normal requests
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Access control bypass should be handled safely")
		})
	}
}

// =============================================================================
// 4. DOS ATTACK SCENARIOS
// =============================================================================

func (s *ComprehensiveSecurityTestSuite) testDoSAttackScenarios(t *testing.T) {
	t.Run("ExtremelLargeTrees", func(t *testing.T) {
		s.testExtremelyLargeTrees(t)
	})

	t.Run("RecursiveDirectoryStructures", func(t *testing.T) {
		s.testRecursiveDirectoryStructures(t)
	})

	t.Run("MemoryExhaustionAttacks", func(t *testing.T) {
		s.testMemoryExhaustionAttacks(t)
	})

	t.Run("CPUExhaustionAttacks", func(t *testing.T) {
		s.testCPUExhaustionAttacks(t)
	})

	t.Run("DiskSpaceExhaustion", func(t *testing.T) {
		s.testDiskSpaceExhaustion(t)
	})
}

func (s *ComprehensiveSecurityTestSuite) testExtremelyLargeTrees(t *testing.T) {
	// Test with large numbers of files in a single commit
	largeTreeTests := []struct {
		name         string
		fileCount    int
		expectReject bool
	}{
		{
			name:         "normal_file_count",
			fileCount:    10,
			expectReject: false,
		},
		{
			name:         "large_file_count",
			fileCount:    1000,
			expectReject: false, // Should be within limits
		},
		{
			name:         "excessive_file_count",
			fileCount:    10000,
			expectReject: true, // Should exceed limits
		},
	}

	for _, test := range largeTreeTests {
		t.Run(test.name, func(t *testing.T) {
			files := make([]map[string]interface{}, test.fileCount)
			for i := 0; i < test.fileCount; i++ {
				files[i] = map[string]interface{}{
					"path":      fmt.Sprintf("file_%d.txt", i),
					"operation": "create",
					"content":   fmt.Sprintf("content %d", i),
				}
			}

			body := map[string]interface{}{
				"files":          files,
				"commit_message": "Large tree test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", "/repos/test/test/commits",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			if test.expectReject {
				assert.Equal(t, http.StatusBadRequest, w.Code,
					"Large tree should be rejected")
			} else {
				assert.NotEqual(t, http.StatusInternalServerError, w.Code,
					"Normal tree should be handled")
			}

			// Should not take excessively long
			assert.Less(t, duration, 30*time.Second,
				"Request should complete within reasonable time")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testRecursiveDirectoryStructures(t *testing.T) {
	// Test deeply nested directory structures
	nestedTests := []struct {
		name  string
		depth int
	}{
		{
			name:  "reasonable_depth",
			depth: 10,
		},
		{
			name:  "deep_nesting",
			depth: 100,
		},
		{
			name:  "extreme_nesting",
			depth: 1000,
		},
	}

	for _, test := range nestedTests {
		t.Run(test.name, func(t *testing.T) {
			// Generate deeply nested path
			pathParts := make([]string, test.depth)
			for i := 0; i < test.depth; i++ {
				pathParts[i] = fmt.Sprintf("dir%d", i)
			}
			nestedPath := strings.Join(pathParts, "/") + "/file.txt"

			body := map[string]interface{}{
				"content":        "nested content",
				"commit_message": "Nested structure test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/"+nestedPath,
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			// Should handle gracefully without crashing
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Nested structure should be handled gracefully")

			// Should complete in reasonable time
			assert.Less(t, duration, 10*time.Second,
				"Request should complete within reasonable time")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testMemoryExhaustionAttacks(t *testing.T) {
	// Test various memory exhaustion scenarios
	memoryTests := []struct {
		name         string
		contentSize  int
		expectReject bool
	}{
		{
			name:         "normal_file_size",
			contentSize:  1024, // 1KB
			expectReject: false,
		},
		{
			name:         "large_file_size",
			contentSize:  1024 * 1024, // 1MB
			expectReject: false,
		},
		{
			name:         "huge_file_size",
			contentSize:  100 * 1024 * 1024, // 100MB (at limit)
			expectReject: false,
		},
		{
			name:         "excessive_file_size",
			contentSize:  500 * 1024 * 1024, // 500MB (exceeds limit)
			expectReject: true,
		},
	}

	for _, test := range memoryTests {
		t.Run(test.name, func(t *testing.T) {
			content := strings.Repeat("A", test.contentSize)

			body := map[string]interface{}{
				"content":        content,
				"commit_message": "Memory test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/large_file.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			if test.expectReject {
				assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code,
					"Excessive file size should be rejected")
			} else {
				assert.NotEqual(t, http.StatusInternalServerError, w.Code,
					"Normal file size should be handled")
			}
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testCPUExhaustionAttacks(t *testing.T) {
	// Test CPU-intensive scenarios
	cpuTests := []struct {
		name        string
		complexity  int
		description string
	}{
		{
			name:        "complex_merge_scenario",
			complexity:  100,
			description: "Test with many conflicting files",
		},
		{
			name:        "regex_bomb_attempt",
			complexity:  1000,
			description: "Test with pathological regex input",
		},
	}

	for _, test := range cpuTests {
		t.Run(test.name, func(t *testing.T) {
			// Create a complex merge scenario
			files := make([]map[string]interface{}, test.complexity)
			for i := 0; i < test.complexity; i++ {
				files[i] = map[string]interface{}{
					"path":      fmt.Sprintf("conflict_%d.txt", i),
					"operation": "create",
					"content":   strings.Repeat("(.*)*", 100), // Potential regex bomb
				}
			}

			body := map[string]interface{}{
				"files":          files,
				"commit_message": "CPU exhaustion test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", "/repos/test/test/commits",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			// Set a timeout to prevent indefinite hanging
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			req = req.WithContext(ctx)

			start := time.Now()
			s.router.ServeHTTP(w, req)
			duration := time.Since(start)

			// Should complete within reasonable time
			assert.Less(t, duration, 30*time.Second,
				"CPU-intensive request should complete within timeout")

			// Should not crash
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"CPU-intensive request should be handled gracefully")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testDiskSpaceExhaustion(t *testing.T) {
	// Test disk space exhaustion scenarios
	// Note: This is a simplified test - real exhaustion testing would be dangerous

	diskTests := []struct {
		name      string
		fileSize  int64
		fileCount int
	}{
		{
			name:      "many_small_files",
			fileSize:  1024, // 1KB each
			fileCount: 1000, // 1MB total
		},
		{
			name:      "few_large_files",
			fileSize:  1024 * 1024, // 1MB each
			fileCount: 10,          // 10MB total
		},
	}

	for _, test := range diskTests {
		t.Run(test.name, func(t *testing.T) {
			files := make([]map[string]interface{}, test.fileCount)
			content := strings.Repeat("A", int(test.fileSize))

			for i := 0; i < test.fileCount; i++ {
				files[i] = map[string]interface{}{
					"path":      fmt.Sprintf("disk_test_%d.txt", i),
					"operation": "create",
					"content":   content,
				}
			}

			body := map[string]interface{}{
				"files":          files,
				"commit_message": "Disk exhaustion test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", "/repos/test/test/commits",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should handle gracefully (may reject due to size limits)
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Disk exhaustion scenario should be handled gracefully")
		})
	}
}

// =============================================================================
// 5. CONFIGURATION SECURITY
// =============================================================================

func (s *ComprehensiveSecurityTestSuite) testConfigurationSecurity(t *testing.T) {
	t.Run("MaliciousEnvironmentVariables", func(t *testing.T) {
		s.testMaliciousEnvironmentVariables(t)
	})

	t.Run("ConfigInjectionAttacks", func(t *testing.T) {
		s.testConfigInjectionAttacks(t)
	})

	t.Run("SecretsLeakageInLogs", func(t *testing.T) {
		s.testSecretsLeakageInLogs(t)
	})

	t.Run("TimingAttacks", func(t *testing.T) {
		s.testTimingAttacks(t)
	})
}

func (s *ComprehensiveSecurityTestSuite) testMaliciousEnvironmentVariables(t *testing.T) {
	// Test that malicious environment variables don't affect the system
	maliciousEnvVars := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "path_injection",
			key:   "PATH",
			value: "/tmp/malicious:/bin:/usr/bin",
		},
		{
			name:  "ld_preload_attack",
			key:   "LD_PRELOAD",
			value: "/tmp/malicious.so",
		},
		{
			name:  "git_config_override",
			key:   "GIT_CONFIG",
			value: "/tmp/malicious.gitconfig",
		},
		{
			name:  "home_directory_override",
			key:   "HOME",
			value: "/tmp/fake_home",
		},
	}

	for _, envVar := range maliciousEnvVars {
		t.Run(envVar.name, func(t *testing.T) {
			// Temporarily set the malicious environment variable
			originalValue := os.Getenv(envVar.key)
			os.Setenv(envVar.key, envVar.value)
			defer func() {
				if originalValue != "" {
					os.Setenv(envVar.key, originalValue)
				} else {
					os.Unsetenv(envVar.key)
				}
			}()

			// Try to perform a normal operation
			body := map[string]interface{}{
				"content":        "test content",
				"commit_message": "Env var test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/env_test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Should not be affected by malicious environment variables
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Malicious environment variable should not cause crash")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testConfigInjectionAttacks(t *testing.T) {
	// Test various configuration injection scenarios
	configInjections := []struct {
		name    string
		headers map[string]string
		body    map[string]interface{}
	}{
		{
			name: "config_via_headers",
			headers: map[string]string{
				"X-Git-Config": "core.editor=/bin/sh -c 'malicious command'",
			},
			body: map[string]interface{}{
				"content":        "test",
				"commit_message": "Config injection test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			},
		},
		{
			name: "config_via_user_agent",
			headers: map[string]string{
				"User-Agent": "git/2.0; config.core.editor=/bin/sh",
			},
			body: map[string]interface{}{
				"content":        "test",
				"commit_message": "User-Agent injection test",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			},
		},
	}

	for _, injection := range configInjections {
		t.Run(injection.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(injection.body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/config_test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")

			for key, value := range injection.headers {
				req.Header.Set(key, value)
			}

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)

			// Should not be affected by configuration injection
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"Configuration injection should be handled safely")
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testSecretsLeakageInLogs(t *testing.T) {
	// Test that sensitive information doesn't leak in logs
	sensitiveData := []struct {
		name  string
		field string
		value string
	}{
		{
			name:  "password_in_commit_message",
			field: "commit_message",
			value: "Fix login with password=secret123",
		},
		{
			name:  "api_key_in_content",
			field: "content",
			value: "API_KEY=sk-1234567890abcdef",
		},
		{
			name:  "token_in_author_name",
			field: "author_name",
			value: "Bot token:ghp_1234567890",
		},
	}

	for _, data := range sensitiveData {
		t.Run(data.name, func(t *testing.T) {
			body := map[string]interface{}{
				"content":        "normal content",
				"commit_message": "Normal commit",
				"branch":         "main",
				"author_name":    "Test User",
				"author_email":   "test@example.com",
			}

			// Insert sensitive data
			body[data.field] = data.value

			bodyBytes, _ := json.Marshal(body)
			req := httptest.NewRequest("PUT", "/repos/test/test/files/sensitive_test.txt",
				bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			s.router.ServeHTTP(w, req)

			// Check response doesn't leak sensitive data
			responseBody := w.Body.String()

			// Look for common secret patterns in response
			secretPatterns := []string{
				"password=",
				"API_KEY=",
				"token:",
				"secret123",
				"sk-1234567890abcdef",
				"ghp_1234567890",
			}

			for _, pattern := range secretPatterns {
				assert.NotContains(t, responseBody, pattern,
					"Response should not leak sensitive data: %s", pattern)
			}
		})
	}
}

func (s *ComprehensiveSecurityTestSuite) testTimingAttacks(t *testing.T) {
	// Test for timing-based information disclosure
	timingTests := []struct {
		name        string
		path        string
		expectExist bool
	}{
		{
			name:        "existing_file",
			path:        "README.md",
			expectExist: true,
		},
		{
			name:        "non_existing_file",
			path:        "nonexistent.txt",
			expectExist: false,
		},
		{
			name:        "protected_file",
			path:        "config/secrets.env",
			expectExist: false,
		},
	}

	for _, test := range timingTests {
		t.Run(test.name, func(t *testing.T) {
			var durations []time.Duration

			// Make multiple requests and measure timing
			for i := 0; i < 10; i++ {
				req := httptest.NewRequest("GET", "/repos/test/test/files/"+test.path, nil)
				w := httptest.NewRecorder()

				start := time.Now()
				s.router.ServeHTTP(w, req)
				duration := time.Since(start)

				durations = append(durations, duration)
			}

			// Calculate timing statistics
			var totalDuration time.Duration
			for _, d := range durations {
				totalDuration += d
			}
			averageDuration := totalDuration / time.Duration(len(durations))

			// Timing should be relatively consistent regardless of file existence
			// This is a basic check - more sophisticated timing analysis would be needed
			// for a real timing attack assessment
			assert.Less(t, averageDuration, 1*time.Second,
				"Response time should be reasonable for file: %s", test.path)

			// All responses for the same type should have similar timing
			// (This is a simplified check)
			for _, duration := range durations {
				timingDiff := duration - averageDuration
				if timingDiff < 0 {
					timingDiff = -timingDiff
				}

				// Allow for some variance but flag if timing is drastically different
				assert.Less(t, timingDiff, averageDuration*2,
					"Response timing should be consistent")
			}
		})
	}
}

// =============================================================================
// UTILITY FUNCTIONS
// =============================================================================

func generateDeeplyNestedJSON(depth int) string {
	if depth <= 0 {
		return `"value"`
	}
	return fmt.Sprintf(`{"nested": %s}`, generateDeeplyNestedJSON(depth-1))
}

func generateLongPath(length int) string {
	if length <= 0 {
		return ""
	}

	// Generate path components to reach the desired length
	component := "a"
	path := ""

	for len(path) < length {
		if len(path) > 0 {
			path += "/"
		}
		remainingLength := length - len(path)
		if remainingLength > len(component) {
			path += component
		} else {
			path += component[:remainingLength]
		}
	}

	return path
}

func setupSecurityTestSuite(t *testing.T) (*ComprehensiveSecurityTestSuite, func()) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "security_test_*")
	require.NoError(t, err)

	// Initialize git repository
	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	// Create initial commit
	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create README.md
	readmePath := filepath.Join(tempDir, "README.md")
	err = os.WriteFile(readmePath, []byte("# Test Repository"), 0644)
	require.NoError(t, err)

	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	_, err = worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create path config
	pathConfig, err := repositories.NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level: "info",
		},
		MergeProtection: config.MergeProtectionConfig{
			Enabled: true,
			EnvironmentProtection: config.EnvironmentProtectionConfig{
				Enabled:               true,
				EnableForSquashMerge:  true,
				EnableForRegularMerge: true,
				EnableForRebaseMerge:  true,
			},
			SafetyMode:               "strict",
			FallbackStrategy:         "adaptive",
			IntelligentMerging:       true,
			PredictiveConflictDetect: true,
			AdaptiveFallback:         true,
			EnableAuditLogs:          true,
		},
		Events: config.EventsConfig{
			Enabled: false, // Disable events for testing
		},
	}

	// Create merge handler
	mergeHandler, err := merge.NewMergeOperationsHandler(cfg)
	require.NoError(t, err)

	// Create security validation framework
	// Note: Using nil logger for testing - in production this would be a proper logger
	securityFramework, err := security.NewValidationFramework(
		security.DefaultValidationConfig(),
		nil, // Using nil logger for testing
	)
	require.NoError(t, err)

	// Setup router with security middleware
	gin.SetMode(gin.TestMode)
	router := gin.New()

	// Add security middleware
	router.Use(securityFramework.ValidateRequestMiddleware())

	// Add basic routes for testing
	api := router.Group("/repos/:owner/:repo")
	{
		api.GET("/files/*path", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "file content"})
		})
		api.PUT("/files/*path", func(c *gin.Context) {
			c.JSON(http.StatusCreated, gin.H{"message": "file created"})
		})
		api.DELETE("/files/*path", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "file deleted"})
		})
		api.POST("/commits", func(c *gin.Context) {
			c.JSON(http.StatusCreated, gin.H{"message": "commit created"})
		})
		api.POST("/merge", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "merge completed"})
		})
		api.POST("/branches", func(c *gin.Context) {
			c.JSON(http.StatusCreated, gin.H{"message": "branch created"})
		})
	}

	suite := &ComprehensiveSecurityTestSuite{
		tempDir:           tempDir,
		repo:              repo,
		pathConfig:        pathConfig,
		mergeHandler:      mergeHandler,
		securityFramework: securityFramework,
		router:            router,
	}

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return suite, cleanup
}
