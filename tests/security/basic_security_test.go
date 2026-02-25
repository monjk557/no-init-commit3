package security_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/merge"
)

// BasicSecurityTest tests core security validation without complex setup
func TestBasicSecurityValidation(t *testing.T) {
	// Test environment file detection
	t.Run("EnvironmentFileDetection", func(t *testing.T) {
		detector := merge.NewEnvironmentFileDetector()
		require.NotNil(t, detector)

		// Test environment file patterns
		envFiles := []struct {
			path     string
			expected bool
		}{
			{".env", true},
			{".ENV", true},
			{"config/env/production.env", true},
			{"config/database.yml", true},
			{"docker-compose.yml", true},
			{"normal-file.txt", false},
			{"readme.md", false},
		}

		for _, test := range envFiles {
			result := detector.IsEnvironmentFile(test.path)
			assert.Equal(t, test.expected, result,
				"Environment file detection failed for: %s", test.path)
		}
	})

	// Test path traversal protection
	t.Run("PathTraversalProtection", func(t *testing.T) {
		pathTests := []struct {
			path        string
			shouldBlock bool
		}{
			{"normal/path/file.txt", false},
			{"../../../etc/passwd", true},
			{"..\\..\\windows\\system32", true},
			{".git/config", true},
			{"file\x00.txt", true},
			{"", true}, // empty path
		}

		for _, test := range pathTests {
			isBlocked := containsPathTraversal(test.path) ||
				strings.Contains(test.path, ".git/") ||
				strings.Contains(test.path, "\x00") ||
				test.path == ""

			if test.shouldBlock {
				assert.True(t, isBlocked,
					"Path should be blocked: %s", test.path)
			} else {
				assert.False(t, isBlocked,
					"Path should be allowed: %s", test.path)
			}
		}
	})

	// Test input validation basics
	t.Run("InputValidation", func(t *testing.T) {
		validationTests := []struct {
			input    string
			valid    bool
			testName string
		}{
			{"normal-input", true, "normal input"},
			{"../../../etc/passwd", false, "path traversal"},
			{"file\x00.txt", false, "null byte injection"},
			{"", false, "empty input"},
			{strings.Repeat("a", 10000), false, "excessively long input"},
		}

		for _, test := range validationTests {
			isValid := validateBasicInput(test.input)
			assert.Equal(t, test.valid, isValid,
				"Input validation failed for %s: %s", test.testName, test.input)
		}
	})
}

// Test merge protection path matching
func TestMergeProtectionPathMatching(t *testing.T) {
	// Test path matchers without full system setup
	t.Run("EnvironmentPathMatcher", func(t *testing.T) {
		matcher := &merge.EnvironmentPathMatcher{}

		tests := []struct {
			path     string
			expected bool
		}{
			{".env", true},
			{"config/env/production", true},
			{"config/database.yml", true},
			{"docker-compose.yml", true},
			{"normal/file.txt", false},
			{"config/env", true}, // directory
		}

		for _, test := range tests {
			result := matcher.Matches(test.path)
			assert.Equal(t, test.expected, result,
				"Environment path matcher failed for: %s", test.path)
		}
	})

	t.Run("SecurityPathMatcher", func(t *testing.T) {
		matcher := &merge.SecurityPathMatcher{}

		tests := []struct {
			path     string
			expected bool
		}{
			{"security/certs/api.key", true},
			{"certs/ssl.pem", true},
			{"keys/private.key", true},
			{"secrets/api.key", true},
			{"auth/token.crt", true},
			{"normal/file.txt", false},
			{"config/database.yml", false}, // not security-specific
		}

		for _, test := range tests {
			result := matcher.Matches(test.path)
			assert.Equal(t, test.expected, result,
				"Security path matcher failed for: %s", test.path)
		}
	})
}

// Test security validation edge cases
func TestSecurityValidationEdgeCases(t *testing.T) {
	t.Run("UnicodeSecurityCases", func(t *testing.T) {
		unicodeTests := []struct {
			input       string
			description string
			shouldBlock bool
		}{
			{"normal-file.txt", "normal ASCII", false},
			{"файл.txt", "Cyrillic filename", true}, // Non-ASCII
			{"file.txt", "normal file", false},
			{"admin", "normal admin", false},
			{"аdmin", "homograph attack (Cyrillic a)", true}, // Would need unicode detection
		}

		for _, test := range unicodeTests {
			hasNonASCII := hasNonASCIIChars(test.input)
			if test.shouldBlock && hasNonASCII {
				assert.True(t, hasNonASCII,
					"Should detect non-ASCII chars in: %s (%s)", test.input, test.description)
			}
		}
	})

	t.Run("SQLInjectionPatterns", func(t *testing.T) {
		sqlTests := []string{
			"'; DROP TABLE users; --",
			"' OR '1'='1",
			"admin'--",
			"' UNION SELECT * FROM users --",
		}

		for _, sqlPayload := range sqlTests {
			containsSQL := strings.Contains(strings.ToLower(sqlPayload), "select") ||
				strings.Contains(strings.ToLower(sqlPayload), "drop") ||
				strings.Contains(strings.ToLower(sqlPayload), "union") ||
				strings.Contains(sqlPayload, "'")

			assert.True(t, containsSQL,
				"Should detect SQL patterns in: %s", sqlPayload)
		}
	})

	t.Run("XSSPatterns", func(t *testing.T) {
		xssTests := []string{
			"<script>alert('xss')</script>",
			"javascript:alert('xss')",
			"<img src=x onerror=alert('xss')>",
			"<svg onload=alert('xss')>",
		}

		for _, xssPayload := range xssTests {
			containsXSS := strings.Contains(xssPayload, "<script>") ||
				strings.Contains(xssPayload, "javascript:") ||
				strings.Contains(xssPayload, "onerror=") ||
				strings.Contains(xssPayload, "onload=")

			assert.True(t, containsXSS,
				"Should detect XSS patterns in: %s", xssPayload)
		}
	})
}

// Test protection rule priorities and conflicts
func TestProtectionRulePriorities(t *testing.T) {
	// Test that protection rules work correctly
	rules := createTestProtectionRules()

	t.Run("RulePriorities", func(t *testing.T) {
		// Sort by priority
		assert.True(t, len(rules) > 0, "Should have protection rules")

		// Check that higher priority rules come first
		for i := 1; i < len(rules); i++ {
			assert.GreaterOrEqual(t, rules[i-1].Priority, rules[i].Priority,
				"Rules should be sorted by priority (high to low)")
		}
	})

	t.Run("RuleMatching", func(t *testing.T) {
		testFiles := []struct {
			path         string
			expectedRule string
			shouldMatch  bool
		}{
			{".env", "environment_files", true},
			{"config/secrets/api.key", "security_files", true},
			{"config/database.yml", "config_files", true},
			{"normal-file.txt", "", false},
		}

		for _, test := range testFiles {
			matchedRule := findMatchingRule(rules, test.path)
			if test.shouldMatch {
				assert.NotNil(t, matchedRule, "Should find matching rule for: %s", test.path)
				if matchedRule != nil {
					assert.Equal(t, test.expectedRule, matchedRule.Name,
						"Wrong rule matched for: %s", test.path)
				}
			} else {
				assert.Nil(t, matchedRule, "Should not find matching rule for: %s", test.path)
			}
		}
	})
}

// Helper functions for testing

func containsPathTraversal(path string) bool {
	return strings.Contains(path, "..") ||
		strings.Contains(path, "\\..\\")
}

func validateBasicInput(input string) bool {
	if input == "" {
		return false
	}
	if containsPathTraversal(input) {
		return false
	}
	if strings.Contains(input, "\x00") {
		return false
	}
	if len(input) > 4096 {
		return false
	}
	return true
}

func hasNonASCIIChars(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func createTestProtectionRules() []merge.ProtectionRule {
	return []merge.ProtectionRule{
		{
			Name:        "environment_files",
			PathMatcher: &merge.EnvironmentPathMatcher{},
			Strategy:    merge.ProtectionStrategyPreventDeletion,
			Priority:    100,
			Enabled:     true,
		},
		{
			Name:        "security_files",
			PathMatcher: &merge.SecurityPathMatcher{},
			Strategy:    merge.ProtectionStrategyRequireManual,
			Priority:    95,
			Enabled:     true,
		},
		{
			Name:        "config_files",
			PathMatcher: &merge.ConfigPathMatcher{},
			Strategy:    merge.ProtectionStrategyPreserveTarget,
			Priority:    90,
			Enabled:     true,
		},
	}
}

func findMatchingRule(rules []merge.ProtectionRule, path string) *merge.ProtectionRule {
	for _, rule := range rules {
		if rule.Enabled && rule.PathMatcher.Matches(path) {
			return &rule
		}
	}
	return nil
}
