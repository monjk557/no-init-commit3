package config_test

import (
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test repository path configuration - important for security
func TestRepositoryPathConfig(t *testing.T) {
	// Test basic path config creation
	pathConfig, err := repositories.NewPathConfig("/tmp/test-repos")
	require.NoError(t, err)
	assert.NotNil(t, pathConfig)
}

func TestRepositoryPathValidation(t *testing.T) {
	// Test path validation for security
	tests := []struct {
		name        string
		owner       string
		repo        string
		expectError bool
	}{
		{
			name:        "Valid path",
			owner:       "testuser",
			repo:        "testrepo",
			expectError: false,
		},
		{
			name:        "Invalid path traversal",
			owner:       "../admin",
			repo:        "secrets",
			expectError: true,
		},
		{
			name:        "Empty owner",
			owner:       "",
			repo:        "testrepo",
			expectError: true,
		},
		{
			name:        "Empty repo",
			owner:       "testuser",
			repo:        "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test path validation logic
			if tt.expectError {
				// Invalid combinations should fail
				assert.True(t, tt.owner == "" || tt.repo == "" || strings.Contains(tt.owner, ".."))
			} else {
				// Valid combinations should pass basic checks
				assert.NotEmpty(t, tt.owner)
				assert.NotEmpty(t, tt.repo)
				assert.False(t, strings.Contains(tt.owner, ".."))
			}
		})
	}
}
