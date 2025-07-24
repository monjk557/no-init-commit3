package repository

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPathConfig(t *testing.T) {
	tests := []struct {
		name      string
		repoRoot  string
		expectErr bool
		expected  string
	}{
		{
			name:     "valid absolute path",
			repoRoot: "/var/lib/git/repos",
			expected: "/var/lib/git/repos",
		},
		{
			name:     "valid absolute path with trailing slash",
			repoRoot: "/var/lib/git/repos/",
			expected: "/var/lib/git/repos",
		},
		{
			name:     "valid absolute path with extra separators",
			repoRoot: "/var/lib//git///repos",
			expected: "/var/lib/git/repos",
		},
		{
			name:      "empty path",
			repoRoot:  "",
			expectErr: true,
		},
		{
			name:      "relative path",
			repoRoot:  "repos",
			expectErr: true,
		},
		{
			name:      "relative path with dot",
			repoRoot:  "./repos",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := NewPathConfig(tt.repoRoot)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				assert.Equal(t, tt.expected, config.GetRepoRoot())
			}
		})
	}
}

func TestPathConfig_FormatRepoPath(t *testing.T) {
	config, err := NewPathConfig("/var/lib/git/repos")
	require.NoError(t, err)

	tests := []struct {
		name     string
		owner    string
		repo     string
		expected string
	}{
		{
			name:     "simple path",
			owner:    "user",
			repo:     "project",
			expected: filepath.Join("/var/lib/git/repos", "user", "project.git"),
		},
		{
			name:     "path with hyphens",
			owner:    "user-name",
			repo:     "project-name",
			expected: filepath.Join("/var/lib/git/repos", "user-name", "project-name.git"),
		},
		{
			name:     "path with underscores",
			owner:    "user_name",
			repo:     "project_name",
			expected: filepath.Join("/var/lib/git/repos", "user_name", "project_name.git"),
		},
		{
			name:     "path with numbers",
			owner:    "user123",
			repo:     "project456",
			expected: filepath.Join("/var/lib/git/repos", "user123", "project456.git"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := config.FormatRepoPath(tt.owner, tt.repo)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetRepoPath(t *testing.T) {
	config, err := NewPathConfig("/var/lib/git/repos")
	require.NoError(t, err)

	tests := []struct {
		name        string
		owner       string
		repo        string
		expectErr   bool
		errContains string
		expected    string
	}{
		{
			name:     "valid path components",
			owner:    "user",
			repo:     "project",
			expected: filepath.Join("/var/lib/git/repos", "user", "project.git"),
		},
		{
			name:     "valid with hyphens and underscores",
			owner:    "user-name_123",
			repo:     "project-name_456",
			expected: filepath.Join("/var/lib/git/repos", "user-name_123", "project-name_456.git"),
		},
		{
			name:        "empty owner",
			owner:       "",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "empty repo",
			owner:       "user",
			repo:        "",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "path traversal in owner",
			owner:       "../user",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "path traversal in repo",
			owner:       "user",
			repo:        "project/../malicious",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "absolute path in owner",
			owner:       "/user",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "absolute path in repo",
			owner:       "user",
			repo:        "/project",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "invalid characters in owner",
			owner:       "user*",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "invalid characters in repo",
			owner:       "user",
			repo:        "project?",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "reserved name as owner",
			owner:       "CON",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "reserved name as repo",
			owner:       "user",
			repo:        "aux",
			expectErr:   true,
			errContains: "invalid path components",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetRepoPath(config, tt.owner, tt.repo)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Empty(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetRepoPathWithDefault(t *testing.T) {
	tests := []struct {
		name        string
		owner       string
		repo        string
		expectErr   bool
		errContains string
		expected    string
	}{
		{
			name:     "valid path with default config",
			owner:    "user",
			repo:     "project",
			expected: filepath.Join(DefaultRepoRoot, "user", "project.git"),
		},
		{
			name:     "valid complex path with default config",
			owner:    "organization-name",
			repo:     "complex-project_123",
			expected: filepath.Join(DefaultRepoRoot, "organization-name", "complex-project_123.git"),
		},
		{
			name:        "invalid owner with default config",
			owner:       "../malicious",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "invalid repo with default config",
			owner:       "user",
			repo:        "project/../hack",
			expectErr:   true,
			errContains: "invalid path components",
		},
		{
			name:        "empty components with default config",
			owner:       "",
			repo:        "",
			expectErr:   true,
			errContains: "invalid path components",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetRepoPathWithDefault(tt.owner, tt.repo)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Empty(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetRepoPath_NilConfig(t *testing.T) {
	// Test that passing a nil config panics (which is expected behavior)
	assert.Panics(t, func() {
		GetRepoPath(nil, "user", "project")
	}, "GetRepoPath should panic with nil config")
}

func TestValidateRepoPath(t *testing.T) {
	config, err := NewPathConfig("/var/lib/git/repos")
	require.NoError(t, err)

	tests := []struct {
		name        string
		repoPath    string
		expectErr   bool
		errContains string
	}{
		{
			name:     "valid repository path",
			repoPath: "/var/lib/git/repos/user/project.git",
		},
		{
			name:     "valid path with subdirectories",
			repoPath: "/var/lib/git/repos/organization/complex-project-name.git",
		},
		{
			name:        "empty path",
			repoPath:    "",
			expectErr:   true,
			errContains: "repository path cannot be empty",
		},
		{
			name:        "path outside repository root",
			repoPath:    "/tmp/malicious/repo.git",
			expectErr:   true,
			errContains: "outside the allowed repository root",
		},
		{
			name:        "path traversal attempt",
			repoPath:    "/var/lib/git/repos/../../../etc/passwd",
			expectErr:   true,
			errContains: "outside the allowed repository root",
		},
		{
			name:        "repository root itself",
			repoPath:    "/var/lib/git/repos",
			expectErr:   true,
			errContains: "cannot be the repository root itself",
		},
		{
			name:        "path too shallow (missing owner/repo structure)",
			repoPath:    "/var/lib/git/repos/user",
			expectErr:   true,
			errContains: "does not follow expected structure",
		},
		{
			name:        "path without .git extension",
			repoPath:    "/var/lib/git/repos/user/project",
			expectErr:   true,
			errContains: "must end with .git extension",
		},
		{
			name:        "relative path that resolves outside root",
			repoPath:    "/var/lib/git/repos/user/../../outside.git",
			expectErr:   true,
			errContains: "outside the allowed repository root",
		},
		{
			name:     "path with dots but still valid",
			repoPath: "/var/lib/git/repos/user.name/project.git",
		},
		{
			name:     "path with extra slashes",
			repoPath: "/var/lib/git/repos//user//project.git",
		},
		{
			name:        "symlink-like path traversal",
			repoPath:    "/var/lib/git/repos/user/../../../etc/passwd.git",
			expectErr:   true,
			errContains: "outside the allowed repository root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRepoPath(config, tt.repoPath)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRepoPath_NilConfig(t *testing.T) {
	err := ValidateRepoPath(nil, "/some/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path configuration cannot be nil")
}

func TestValidateRepoPathWithDefault(t *testing.T) {
	tests := []struct {
		name        string
		repoPath    string
		expectErr   bool
		errContains string
	}{
		{
			name:     "valid path with default config",
			repoPath: filepath.Join(DefaultRepoRoot, "user", "project.git"),
		},
		{
			name:        "invalid path with default config",
			repoPath:    "/tmp/outside/repo.git",
			expectErr:   true,
			errContains: "outside the allowed repository root",
		},
		{
			name:        "empty path with default config",
			repoPath:    "",
			expectErr:   true,
			errContains: "repository path cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRepoPathWithDefault(tt.repoPath)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRepoPath_RelativePaths(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Change to temp directory for relative path testing
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() {
		err := os.Chdir(originalWd)
		require.NoError(t, err)
	}()

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	tests := []struct {
		name        string
		repoPath    string
		expectErr   bool
		errContains string
	}{
		{
			name:     "absolute path that resolves within root",
			repoPath: filepath.Join(tempDir, "user", "project.git"),
		},
		{
			name:     "relative path with current directory that resolves within root",
			repoPath: "./user/project.git",
		},
		{
			name:        "relative path that tries to escape root",
			repoPath:    "../outside/project.git",
			expectErr:   true,
			errContains: "outside the allowed repository root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRepoPath(config, tt.repoPath)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePathComponents(t *testing.T) {
	tests := []struct {
		name        string
		owner       string
		repo        string
		expectErr   bool
		errContains string
	}{
		{
			name:  "valid components",
			owner: "user",
			repo:  "project",
		},
		{
			name:  "valid with hyphens",
			owner: "user-name",
			repo:  "project-name",
		},
		{
			name:  "valid with underscores",
			owner: "user_name",
			repo:  "project_name",
		},
		{
			name:  "valid with numbers",
			owner: "user123",
			repo:  "project456",
		},
		{
			name:        "empty owner",
			owner:       "",
			repo:        "project",
			expectErr:   true,
			errContains: "owner name cannot be empty",
		},
		{
			name:        "empty repo",
			owner:       "user",
			repo:        "",
			expectErr:   true,
			errContains: "repository name cannot be empty",
		},
		{
			name:        "owner with path traversal",
			owner:       "../user",
			repo:        "project",
			expectErr:   true,
			errContains: "path traversal attempt",
		},
		{
			name:        "repo with path traversal",
			owner:       "user",
			repo:        "project/../",
			expectErr:   true,
			errContains: "path traversal attempt",
		},
		{
			name:        "owner with absolute path",
			owner:       "/user",
			repo:        "project",
			expectErr:   true,
			errContains: "absolute path attempt",
		},
		{
			name:        "repo with absolute path",
			owner:       "user",
			repo:        "/project",
			expectErr:   true,
			errContains: "absolute path attempt",
		},
		{
			name:        "owner with backslash",
			owner:       "user\\name",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid character",
		},
		{
			name:        "repo with colon",
			owner:       "user",
			repo:        "project:branch",
			expectErr:   true,
			errContains: "invalid character",
		},
		{
			name:        "owner with asterisk",
			owner:       "user*",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid character",
		},
		{
			name:        "repo with question mark",
			owner:       "user",
			repo:        "project?",
			expectErr:   true,
			errContains: "invalid character",
		},
		{
			name:        "owner with quotes",
			owner:       "user\"name",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid character",
		},
		{
			name:        "repo with angle brackets",
			owner:       "user",
			repo:        "project<>",
			expectErr:   true,
			errContains: "invalid character",
		},
		{
			name:        "owner with pipe",
			owner:       "user|name",
			repo:        "project",
			expectErr:   true,
			errContains: "invalid character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePathComponents(tt.owner, tt.repo)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePathComponents_ReservedNames(t *testing.T) {
	reservedNames := []string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}

	for _, reserved := range reservedNames {
		t.Run("reserved_name_"+reserved, func(t *testing.T) {
			// Test uppercase
			err := ValidatePathComponents(reserved, "project")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "reserved system name")

			// Test lowercase
			err = ValidatePathComponents("user", strings.ToLower(reserved))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "reserved system name")

			// Test mixed case
			err = ValidatePathComponents(strings.Title(strings.ToLower(reserved)), "project")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "reserved system name")
		})
	}
}

func TestPathConfig_CrossPlatformPaths(t *testing.T) {
	var testRoot string
	if runtime.GOOS == "windows" {
		testRoot = "C:\\repos"
	} else {
		testRoot = "/var/lib/git/repos"
	}

	config, err := NewPathConfig(testRoot)
	require.NoError(t, err)

	result := config.FormatRepoPath("user", "project")

	// The result should use the correct path separator for the platform
	expectedPath := filepath.Join(testRoot, "user", "project.git")
	assert.Equal(t, expectedPath, result)

	// Verify it's a valid path for the current platform
	assert.True(t, filepath.IsAbs(result))
}

func TestGetRepoPath_CrossPlatform(t *testing.T) {
	var testRoot string
	if runtime.GOOS == "windows" {
		testRoot = "C:\\repos"
	} else {
		testRoot = "/var/lib/git/repos"
	}

	config, err := NewPathConfig(testRoot)
	require.NoError(t, err)

	result, err := GetRepoPath(config, "user", "project")
	require.NoError(t, err)

	// The result should use the correct path separator for the platform
	expectedPath := filepath.Join(testRoot, "user", "project.git")
	assert.Equal(t, expectedPath, result)

	// Verify it's a valid path for the current platform
	assert.True(t, filepath.IsAbs(result))
}

func TestValidateRepoPath_CrossPlatform(t *testing.T) {
	var testRoot string
	if runtime.GOOS == "windows" {
		testRoot = "C:\\repos"
	} else {
		testRoot = "/var/lib/git/repos"
	}

	config, err := NewPathConfig(testRoot)
	require.NoError(t, err)

	validPath := filepath.Join(testRoot, "user", "project.git")
	err = ValidateRepoPath(config, validPath)
	assert.NoError(t, err)

	// Test path outside root
	invalidPath := filepath.Join(filepath.Dir(testRoot), "outside", "malicious.git")
	err = ValidateRepoPath(config, invalidPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outside the allowed repository root")
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "/var/lib/git/repos", DefaultRepoRoot)
	assert.Equal(t, ".git", RepoExtension)
	assert.Equal(t, "/", PathSeparator)
}

func BenchmarkValidatePathComponents(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ValidatePathComponents("user", "project")
	}
}

func BenchmarkFormatRepoPath(b *testing.B) {
	config, _ := NewPathConfig("/var/lib/git/repos")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = config.FormatRepoPath("user", "project")
	}
}

func BenchmarkGetRepoPath(b *testing.B) {
	config, _ := NewPathConfig("/var/lib/git/repos")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetRepoPath(config, "user", "project")
	}
}

func BenchmarkGetRepoPathWithDefault(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetRepoPathWithDefault("user", "project")
	}
}

func BenchmarkValidateRepoPath(b *testing.B) {
	config, _ := NewPathConfig("/var/lib/git/repos")
	validPath := "/var/lib/git/repos/user/project.git"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ValidateRepoPath(config, validPath)
	}
}
