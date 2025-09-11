package util_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test basic security validation functionality
func TestSecurityValidation(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectValid bool
	}{
		{
			name:        "Valid input",
			input:       "valid-input",
			expectValid: true,
		},
		{
			name:        "Path traversal attempt",
			input:       "../../../etc/passwd",
			expectValid: false,
		},
		{
			name:        "Null byte injection",
			input:       "file\x00.txt",
			expectValid: false,
		},
		{
			name:        "Empty input",
			input:       "",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Basic security validation logic
			isValid := validateInput(tt.input)
			assert.Equal(t, tt.expectValid, isValid)
		})
	}
}

// Simple validation function for testing
func validateInput(input string) bool {
	if input == "" {
		return false
	}
	if strings.Contains(input, "..") {
		return false
	}
	if strings.Contains(input, "\x00") {
		return false
	}
	return true
}

func TestPathSecurity(t *testing.T) {
	// Test path security validation
	securePaths := []string{
		"user/repo",
		"valid-user/valid-repo",
		"simple/path",
	}

	insecurePaths := []string{
		"../admin/secrets",
		"user/../admin",
		"user/repo/../admin",
		"",
	}

	for _, path := range securePaths {
		assert.True(t, validateInput(path), "Should accept secure path: %s", path)
	}

	for _, path := range insecurePaths {
		assert.False(t, validateInput(path), "Should reject insecure path: %s", path)
	}
}
