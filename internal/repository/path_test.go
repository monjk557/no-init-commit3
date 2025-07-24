package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"errors"
	"sync"

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
	for i := 0; i < b.N; i++ {
		ValidateRepoPath(config, "/var/lib/git/repos/user/project.git")
	}
}

// Tests for EnsureRepoExists function

func TestEnsureRepoExists_ValidBareRepository(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create a mock bare repository structure
	repoPath := filepath.Join(tempDir, "user", "project.git")
	err = os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	// Create bare repository structure
	createBareRepository(t, repoPath)

	// Test EnsureRepoExists
	err = EnsureRepoExists(config, repoPath)
	assert.NoError(t, err)
}

func TestEnsureRepoExists_ValidNonBareRepository(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create a mock non-bare repository structure
	repoPath := filepath.Join(tempDir, "user", "project.git")
	err = os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	// Create non-bare repository structure
	createNonBareRepository(t, repoPath)

	// Test EnsureRepoExists
	err = EnsureRepoExists(config, repoPath)
	assert.NoError(t, err)
}

func TestEnsureRepoExists_ValidWorktreeRepository(t *testing.T) {
	// Create temporary directory for testing
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create repository and worktree structure
	repoPath := filepath.Join(tempDir, "user", "project.git")
	worktreePath := filepath.Join(tempDir, "user", "project-worktree.git")

	// Create main repository
	err = os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)
	createNonBareRepository(t, repoPath)

	// Create worktree directory
	err = os.MkdirAll(worktreePath, 0755)
	require.NoError(t, err)

	// Create .git file pointing to main repository
	gitFilePath := filepath.Join(worktreePath, ".git")
	gitDirRef := fmt.Sprintf("gitdir: %s/.git", repoPath)
	err = os.WriteFile(gitFilePath, []byte(gitDirRef), 0644)
	require.NoError(t, err)

	// Test EnsureRepoExists on worktree
	err = EnsureRepoExists(config, worktreePath)
	assert.NoError(t, err)
}

func TestEnsureRepoExists_NonExistentRepository(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	nonExistentPath := filepath.Join(tempDir, "user", "nonexistent.git")

	err = EnsureRepoExists(config, nonExistentPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository does not exist at path")
}

func TestEnsureRepoExists_PathIsFile(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create directory structure
	userDir := filepath.Join(tempDir, "user")
	err = os.MkdirAll(userDir, 0755)
	require.NoError(t, err)

	// Create a file instead of directory
	filePath := filepath.Join(userDir, "project.git")
	err = os.WriteFile(filePath, []byte("not a directory"), 0644)
	require.NoError(t, err)

	err = EnsureRepoExists(config, filePath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository path is not a directory")
}

func TestEnsureRepoExists_InvalidRepository(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create empty directory (not a Git repository)
	repoPath := filepath.Join(tempDir, "user", "project.git")
	err = os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	err = EnsureRepoExists(config, repoPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not contain a valid Git repository structure")
}

func TestEnsureRepoExists_PathValidationFailure(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Use a path outside the configured root
	invalidPath := "/tmp/outside/repo.git"

	err = EnsureRepoExists(config, invalidPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path validation failed")
}

func TestEnsureRepoExistsWithDefault(t *testing.T) {
	// Test the default configuration function
	tests := []struct {
		name        string
		repoPath    string
		expectError bool
	}{
		{
			name:        "valid default path format",
			repoPath:    "/var/lib/git/repos/user/project.git",
			expectError: true, // Will fail because path doesn't exist, but validation should pass
		},
		{
			name:        "invalid path outside default root",
			repoPath:    "/tmp/outside/repo.git",
			expectError: true, // Should fail validation
		},
		{
			name:        "empty path",
			repoPath:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EnsureRepoExistsWithDefault(tt.repoPath)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Test repository detection functions

func TestIsBareRepository(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name     string
		setup    func(string) string
		expected bool
	}{
		{
			name: "valid bare repository",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "bare.git")
				createBareRepository(t, repoPath)
				return repoPath
			},
			expected: true,
		},
		{
			name: "incomplete bare repository (missing HEAD)",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "incomplete.git")
				os.MkdirAll(filepath.Join(repoPath, "refs"), 0755)
				os.MkdirAll(filepath.Join(repoPath, "objects"), 0755)
				// Missing HEAD file
				return repoPath
			},
			expected: false,
		},
		{
			name: "non-bare repository",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "nonbare")
				createNonBareRepository(t, repoPath)
				return repoPath
			},
			expected: false,
		},
		{
			name: "empty directory",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "empty")
				os.MkdirAll(repoPath, 0755)
				return repoPath
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath := tt.setup(tempDir)
			result := isBareRepository(repoPath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsNonBareRepository(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name     string
		setup    func(string) string
		expected bool
	}{
		{
			name: "valid non-bare repository",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "nonbare")
				createNonBareRepository(t, repoPath)
				return repoPath
			},
			expected: true,
		},
		{
			name: "bare repository",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "bare.git")
				createBareRepository(t, repoPath)
				return repoPath
			},
			expected: false,
		},
		{
			name: "worktree with .git file",
			setup: func(base string) string {
				// Create main repo
				mainRepoPath := filepath.Join(base, "main")
				createNonBareRepository(t, mainRepoPath)

				// Create worktree
				worktreePath := filepath.Join(base, "worktree")
				os.MkdirAll(worktreePath, 0755)
				gitFilePath := filepath.Join(worktreePath, ".git")
				gitDirRef := fmt.Sprintf("gitdir: %s/.git", mainRepoPath)
				os.WriteFile(gitFilePath, []byte(gitDirRef), 0644)
				return worktreePath
			},
			expected: true,
		},
		{
			name: "invalid .git file",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "invalid")
				os.MkdirAll(repoPath, 0755)
				gitFilePath := filepath.Join(repoPath, ".git")
				os.WriteFile(gitFilePath, []byte("invalid content"), 0644)
				return repoPath
			},
			expected: false,
		},
		{
			name: "empty directory",
			setup: func(base string) string {
				repoPath := filepath.Join(base, "empty")
				os.MkdirAll(repoPath, 0755)
				return repoPath
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath := tt.setup(tempDir)
			result := isNonBareRepository(repoPath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Helper functions to create mock Git repository structures

func createBareRepository(t *testing.T, repoPath string) {
	t.Helper()

	// Create directory structure
	err := os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	// Create required directories
	dirs := []string{"refs", "refs/heads", "refs/tags", "objects", "objects/info", "objects/pack"}
	for _, dir := range dirs {
		err := os.MkdirAll(filepath.Join(repoPath, dir), 0755)
		require.NoError(t, err)
	}

	// Create required files
	files := map[string]string{
		"HEAD":        "ref: refs/heads/main\n",
		"config":      "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n",
		"description": "Unnamed repository; edit this file 'description' to name the repository.\n",
	}

	for filename, content := range files {
		err := os.WriteFile(filepath.Join(repoPath, filename), []byte(content), 0644)
		require.NoError(t, err)
	}
}

func createNonBareRepository(t *testing.T, repoPath string) {
	t.Helper()

	// Create directory structure
	err := os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	// Create .git directory
	gitDir := filepath.Join(repoPath, ".git")
	err = os.MkdirAll(gitDir, 0755)
	require.NoError(t, err)

	// Create required directories inside .git
	dirs := []string{"refs", "refs/heads", "refs/tags", "objects", "objects/info", "objects/pack"}
	for _, dir := range dirs {
		err := os.MkdirAll(filepath.Join(gitDir, dir), 0755)
		require.NoError(t, err)
	}

	// Create required files inside .git
	files := map[string]string{
		"HEAD":        "ref: refs/heads/main\n",
		"config":      "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n",
		"description": "Unnamed repository; edit this file 'description' to name the repository.\n",
	}

	for filename, content := range files {
		err := os.WriteFile(filepath.Join(gitDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}
}

// Integration Tests for Package-Level Features

func TestCustomErrorTypes(t *testing.T) {
	// Test PathError
	t.Run("PathError", func(t *testing.T) {
		originalErr := fmt.Errorf("original error")
		pathErr := &PathError{
			Op:   "test_operation",
			Path: "/test/path",
			Err:  originalErr,
		}

		assert.Contains(t, pathErr.Error(), "test_operation")
		assert.Contains(t, pathErr.Error(), "/test/path")
		assert.Contains(t, pathErr.Error(), "original error")
		assert.Equal(t, originalErr, pathErr.Unwrap())

		// Test without path
		pathErrNoPath := &PathError{
			Op:  "test_operation",
			Err: originalErr,
		}
		assert.Contains(t, pathErrNoPath.Error(), "test_operation")
		assert.NotContains(t, pathErrNoPath.Error(), "/test/path")
	})

	// Test ValidationError
	t.Run("ValidationError", func(t *testing.T) {
		validationErr := &ValidationError{
			Component: "owner",
			Value:     "invalid*name",
			Reason:    "contains invalid character",
		}

		assert.Contains(t, validationErr.Error(), "owner")
		assert.Contains(t, validationErr.Error(), "invalid*name")
		assert.Contains(t, validationErr.Error(), "contains invalid character")

		// Test without value
		validationErrNoValue := &ValidationError{
			Component: "owner",
			Reason:    "cannot be empty",
		}
		assert.Contains(t, validationErrNoValue.Error(), "owner")
		assert.NotContains(t, validationErrNoValue.Error(), "invalid*name")
	})

	// Test RepositoryError
	t.Run("RepositoryError", func(t *testing.T) {
		repoErr := &RepositoryError{
			Path:   "/test/repo",
			Type:   "not_found",
			Detail: "repository does not exist",
		}

		assert.Contains(t, repoErr.Error(), "/test/repo")
		assert.Contains(t, repoErr.Error(), "not_found")
		assert.Contains(t, repoErr.Error(), "repository does not exist")
	})
}

func TestGlobalConfiguration(t *testing.T) {
	// Reset global config before each test
	defer ResetGlobalConfig()

	t.Run("SetAndGetGlobalConfig", func(t *testing.T) {
		// Test setting global config
		customConfig, err := NewPathConfig("/custom/repo/path")
		require.NoError(t, err)

		err = SetGlobalConfig(customConfig)
		assert.NoError(t, err)

		// Test getting global config
		retrievedConfig, err := GetGlobalConfig()
		require.NoError(t, err)
		assert.Equal(t, "/custom/repo/path", retrievedConfig.GetRepoRoot())

		// Verify it returns a copy (modification doesn't affect original)
		retrievedConfig.RepoRoot = "/modified/path"
		retrievedConfig2, err := GetGlobalConfig()
		require.NoError(t, err)
		assert.Equal(t, "/custom/repo/path", retrievedConfig2.GetRepoRoot())
	})

	t.Run("GetGlobalConfigDefault", func(t *testing.T) {
		ResetGlobalConfig()

		// Should return default configuration
		config, err := GetGlobalConfig()
		require.NoError(t, err)
		assert.Equal(t, DefaultRepoRoot, config.GetRepoRoot())
	})

	t.Run("SetGlobalConfigNil", func(t *testing.T) {
		err := SetGlobalConfig(nil)
		assert.Error(t, err)

		var validationErr *ValidationError
		assert.True(t, errors.As(err, &validationErr))
		assert.Equal(t, "global_config", validationErr.Component)
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		// Test concurrent access to global configuration
		const numGoroutines = 100
		var wg sync.WaitGroup
		errors := make(chan error, numGoroutines)

		// Test concurrent reads
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := GetGlobalConfig()
				if err != nil {
					errors <- err
				}
			}()
		}

		wg.Wait()
		close(errors)

		// Check no errors occurred
		for err := range errors {
			t.Errorf("Concurrent read error: %v", err)
		}
	})
}

func TestGetAndValidateRepoPath(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	t.Run("ValidPath", func(t *testing.T) {
		path, err := GetAndValidateRepoPath(config, "user", "project")
		assert.NoError(t, err)
		assert.Equal(t, filepath.Join(tempDir, "user", "project.git"), path)
	})

	t.Run("InvalidComponents", func(t *testing.T) {
		_, err := GetAndValidateRepoPath(config, "../malicious", "project")
		assert.Error(t, err)

		var pathErr *PathError
		assert.True(t, errors.As(err, &pathErr))
		assert.Equal(t, "generate_and_validate", pathErr.Op)
	})
}

func TestGetAndValidateRepoPathWithDefault(t *testing.T) {
	defer ResetGlobalConfig()

	// Set custom global config
	customConfig, err := NewPathConfig("/test/custom/root")
	require.NoError(t, err)
	err = SetGlobalConfig(customConfig)
	require.NoError(t, err)

	path, err := GetAndValidateRepoPathWithDefault("user", "project")
	assert.NoError(t, err)
	assert.Equal(t, "/test/custom/root/user/project.git", path)
}

func TestVerifyRepositoryAccess(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	t.Run("ValidRepository", func(t *testing.T) {
		// Create a valid repository
		repoPath := filepath.Join(tempDir, "user", "project.git")
		createBareRepository(t, repoPath)

		path, err := VerifyRepositoryAccess(config, "user", "project")
		assert.NoError(t, err)
		assert.Equal(t, repoPath, path)
	})

	t.Run("NonExistentRepository", func(t *testing.T) {
		_, err := VerifyRepositoryAccess(config, "nonexistent", "repo")
		assert.Error(t, err)

		var pathErr *PathError
		assert.True(t, errors.As(err, &pathErr))
		assert.Equal(t, "verify_access", pathErr.Op)
	})
}

func TestGetRepositoryInfo(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	t.Run("ValidBareRepository", func(t *testing.T) {
		repoPath := filepath.Join(tempDir, "user", "bare-repo.git")
		createBareRepository(t, repoPath)

		info, err := GetRepositoryInfo(config, "user", "bare-repo")
		require.NoError(t, err)

		assert.Equal(t, "user", info.Owner)
		assert.Equal(t, "bare-repo", info.Name)
		assert.Equal(t, repoPath, info.Path)
		assert.Equal(t, "bare", info.Type)
		assert.True(t, info.Exists)
		assert.True(t, info.IsValid)
		assert.True(t, info.IsAccessible)
	})

	t.Run("ValidNonBareRepository", func(t *testing.T) {
		repoPath := filepath.Join(tempDir, "user", "nonbare-repo.git")
		createNonBareRepository(t, repoPath)

		info, err := GetRepositoryInfo(config, "user", "nonbare-repo")
		require.NoError(t, err)

		assert.Equal(t, "nonbare-repo", info.Name)
		assert.Equal(t, "non-bare", info.Type)
		assert.True(t, info.Exists)
		assert.True(t, info.IsValid)
		assert.True(t, info.IsAccessible)
	})

	t.Run("WorktreeRepository", func(t *testing.T) {
		// Create main repo
		mainRepoPath := filepath.Join(tempDir, "user", "main-repo.git")
		createNonBareRepository(t, mainRepoPath)

		// Create worktree
		worktreePath := filepath.Join(tempDir, "user", "worktree-repo.git")
		os.MkdirAll(worktreePath, 0755)
		gitFilePath := filepath.Join(worktreePath, ".git")
		gitDirRef := fmt.Sprintf("gitdir: %s/.git", mainRepoPath)
		os.WriteFile(gitFilePath, []byte(gitDirRef), 0644)

		info, err := GetRepositoryInfo(config, "user", "worktree-repo")
		require.NoError(t, err)

		assert.Equal(t, "worktree", info.Type)
		assert.True(t, info.IsValid)
	})

	t.Run("NonExistentRepository", func(t *testing.T) {
		info, err := GetRepositoryInfo(config, "user", "missing")
		require.NoError(t, err)

		assert.Equal(t, "user", info.Owner)
		assert.Equal(t, "missing", info.Name)
		assert.False(t, info.Exists)
		assert.False(t, info.IsValid)
		assert.False(t, info.IsAccessible)
	})

	t.Run("InvalidRepository", func(t *testing.T) {
		// Create empty directory
		repoPath := filepath.Join(tempDir, "user", "invalid-repo.git")
		os.MkdirAll(repoPath, 0755)

		info, err := GetRepositoryInfo(config, "user", "invalid-repo")
		require.NoError(t, err)

		assert.True(t, info.Exists)
		assert.True(t, info.IsAccessible)
		assert.False(t, info.IsValid)
	})
}

func TestGetBatchRepositoryInfo(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create test repositories
	bareRepoPath := filepath.Join(tempDir, "user1", "bare-repo.git")
	createBareRepository(t, bareRepoPath)

	nonBareRepoPath := filepath.Join(tempDir, "user2", "nonbare-repo.git")
	createNonBareRepository(t, nonBareRepoPath)

	// Create invalid repo (empty directory)
	invalidRepoPath := filepath.Join(tempDir, "user3", "invalid-repo.git")
	os.MkdirAll(invalidRepoPath, 0755)

	repos := []struct{ Owner, Repo string }{
		{"user1", "bare-repo"},
		{"user2", "nonbare-repo"},
		{"user3", "invalid-repo"},
		{"user4", "missing-repo"},
		{"../malicious", "attack"},
	}

	batchInfo := GetBatchRepositoryInfo(config, repos)

	assert.Equal(t, 5, batchInfo.TotalCount)
	assert.Equal(t, 2, batchInfo.ValidCount) // Only bare and non-bare repos are valid
	assert.Equal(t, 1, batchInfo.ErrorCount) // Malicious path should error
	assert.Len(t, batchInfo.Repositories, 5)
	assert.Len(t, batchInfo.Errors, 1)

	// Check specific repositories
	assert.Equal(t, "bare", batchInfo.Repositories[0].Type)
	assert.True(t, batchInfo.Repositories[0].IsValid)

	assert.Equal(t, "non-bare", batchInfo.Repositories[1].Type)
	assert.True(t, batchInfo.Repositories[1].IsValid)

	assert.False(t, batchInfo.Repositories[2].IsValid) // Invalid repo
	assert.False(t, batchInfo.Repositories[3].Exists)  // Missing repo
}

// Performance and Concurrency Tests

func TestConcurrentRepositoryAccess(t *testing.T) {
	tempDir := t.TempDir()
	config, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create a test repository
	repoPath := filepath.Join(tempDir, "user", "concurrent-test.git")
	createBareRepository(t, repoPath)

	const numGoroutines = 50
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	// Test concurrent repository access
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Test various operations
			_, err1 := GetAndValidateRepoPath(config, "user", "concurrent-test")
			_, err2 := VerifyRepositoryAccess(config, "user", "concurrent-test")
			_, err3 := GetRepositoryInfo(config, "user", "concurrent-test")

			if err1 != nil {
				errors <- err1
			}
			if err2 != nil {
				errors <- err2
			}
			if err3 != nil {
				errors <- err3
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check no errors occurred
	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}
}

// Benchmarks for performance validation

func BenchmarkGetAndValidateRepoPath(b *testing.B) {
	config, _ := NewPathConfig("/var/lib/git/repos")
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		GetAndValidateRepoPath(config, "user", "project")
	}
}

func BenchmarkGetRepositoryInfo(b *testing.B) {
	tempDir := b.TempDir()
	config, _ := NewPathConfig(tempDir)

	// Create test repository
	repoPath := filepath.Join(tempDir, "user", "benchmark.git")

	// Create directory structure directly for benchmarks
	err := os.MkdirAll(repoPath, 0755)
	if err != nil {
		b.Fatal(err)
	}

	// Create required directories
	dirs := []string{"refs", "refs/heads", "refs/tags", "objects", "objects/info", "objects/pack"}
	for _, dir := range dirs {
		err := os.MkdirAll(filepath.Join(repoPath, dir), 0755)
		if err != nil {
			b.Fatal(err)
		}
	}

	// Create required files
	files := map[string]string{
		"HEAD":        "ref: refs/heads/main\n",
		"config":      "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n",
		"description": "Unnamed repository; edit this file 'description' to name the repository.\n",
	}

	for filename, content := range files {
		err := os.WriteFile(filepath.Join(repoPath, filename), []byte(content), 0644)
		if err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		GetRepositoryInfo(config, "user", "benchmark")
	}
}

func BenchmarkGlobalConfigAccess(b *testing.B) {
	config, _ := NewPathConfig("/var/lib/git/repos")
	SetGlobalConfig(config)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		GetGlobalConfig()
	}
}
