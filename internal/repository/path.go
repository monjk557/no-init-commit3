package repository

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Path management constants
const (
	// DefaultRepoRoot is the default base path for Git repositories
	DefaultRepoRoot = "/var/lib/git/repos"

	// RepoExtension is the standard Git repository extension
	RepoExtension = ".git"

	// PathSeparator is the standard path separator for repository paths
	PathSeparator = "/"
)

// PathConfig holds configuration for repository path management
type PathConfig struct {
	// RepoRoot is the base directory where all repositories are stored
	RepoRoot string
}

// NewPathConfig creates a new PathConfig with validation
func NewPathConfig(repoRoot string) (*PathConfig, error) {
	if repoRoot == "" {
		return nil, fmt.Errorf("repository root path cannot be empty")
	}

	// Clean the path to ensure consistent formatting
	cleanRoot := filepath.Clean(repoRoot)

	// Ensure the path is absolute
	if !filepath.IsAbs(cleanRoot) {
		return nil, fmt.Errorf("repository root path must be absolute: %s", cleanRoot)
	}

	return &PathConfig{
		RepoRoot: cleanRoot,
	}, nil
}

// GetRepoRoot returns the configured repository root path
func (pc *PathConfig) GetRepoRoot() string {
	return pc.RepoRoot
}

// FormatRepoPath generates the expected path pattern for a repository
// Format: {RepoRoot}/{owner}/{repo}.git
func (pc *PathConfig) FormatRepoPath(owner, repo string) string {
	return filepath.Join(pc.RepoRoot, owner, repo+RepoExtension)
}

// GetRepoPath returns the filesystem path for a given repository
// This function validates the owner and repo components and constructs the full path
func GetRepoPath(pathConfig *PathConfig, owner, repo string) (string, error) {
	// Validate path components for security
	if err := ValidatePathComponents(owner, repo); err != nil {
		return "", fmt.Errorf("invalid path components: %w", err)
	}

	// Generate the repository path
	repoPath := pathConfig.FormatRepoPath(owner, repo)

	return repoPath, nil
}

// GetRepoPathWithDefault is a convenience function that uses the default configuration
// when no PathConfig is provided
func GetRepoPathWithDefault(owner, repo string) (string, error) {
	defaultConfig, err := NewPathConfig(DefaultRepoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to create default path config: %w", err)
	}

	return GetRepoPath(defaultConfig, owner, repo)
}

// ValidateRepoPath validates if a given path is within the allowed repository root
// and prevents directory traversal attacks
func ValidateRepoPath(pathConfig *PathConfig, repoPath string) error {
	if pathConfig == nil {
		return fmt.Errorf("path configuration cannot be nil")
	}

	if repoPath == "" {
		return fmt.Errorf("repository path cannot be empty")
	}

	// Clean the provided path to resolve any relative components
	cleanPath := filepath.Clean(repoPath)

	// Convert to absolute path to handle cases where relative paths are passed
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path for %s: %w", repoPath, err)
	}

	// Ensure the repository root is also absolute for comparison
	absRepoRoot, err := filepath.Abs(pathConfig.RepoRoot)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute repository root path %s: %w", pathConfig.RepoRoot, err)
	}

	// Normalize paths for consistent comparison (handles case sensitivity on Windows)
	absPath = filepath.Clean(absPath)
	absRepoRoot = filepath.Clean(absRepoRoot)

	// Check if the path is within the repository root
	// Use filepath.Rel to check if the path is a subdirectory
	relPath, err := filepath.Rel(absRepoRoot, absPath)
	if err != nil {
		return fmt.Errorf("failed to determine relative path from %s to %s: %w", absRepoRoot, absPath, err)
	}

	// If the relative path starts with ".." or is ".", it's outside or equal to the root
	if strings.HasPrefix(relPath, "..") {
		return fmt.Errorf("repository path %s is outside the allowed repository root %s", repoPath, pathConfig.RepoRoot)
	}

	// Check for exact match to repository root (which we don't allow)
	if relPath == "." {
		return fmt.Errorf("repository path cannot be the repository root itself: %s", repoPath)
	}

	// Additional security check: ensure the path contains expected structure
	// A valid repository path should be at least 2 levels deep: root/owner/repo.git
	pathParts := strings.Split(relPath, string(filepath.Separator))
	if len(pathParts) < 2 {
		return fmt.Errorf("repository path %s does not follow expected structure (owner/repo.git)", repoPath)
	}

	// Validate that the path ends with .git extension
	if !strings.HasSuffix(absPath, RepoExtension) {
		return fmt.Errorf("repository path %s must end with %s extension", repoPath, RepoExtension)
	}

	return nil
}

// ValidateRepoPathWithDefault is a convenience function that uses the default configuration
// when no PathConfig is provided
func ValidateRepoPathWithDefault(repoPath string) error {
	defaultConfig, err := NewPathConfig(DefaultRepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create default path config: %w", err)
	}

	return ValidateRepoPath(defaultConfig, repoPath)
}

// ValidatePathComponents validates owner and repository name components
func ValidatePathComponents(owner, repo string) error {
	if err := validateComponent(owner, "owner"); err != nil {
		return err
	}

	if err := validateComponent(repo, "repository"); err != nil {
		return err
	}

	return nil
}

// validateComponent validates a single path component (owner or repo name)
func validateComponent(component, componentType string) error {
	if component == "" {
		return fmt.Errorf("%s name cannot be empty", componentType)
	}

	// Check for path traversal attempts
	if strings.Contains(component, "..") {
		return fmt.Errorf("%s name cannot contain '..' (path traversal attempt): %s", componentType, component)
	}

	// Check for absolute path attempts
	if strings.HasPrefix(component, "/") {
		return fmt.Errorf("%s name cannot start with '/' (absolute path attempt): %s", componentType, component)
	}

	// Check for invalid characters (basic check for common problematic characters)
	invalidChars := []string{"\\", ":", "*", "?", "\"", "<", ">", "|"}
	for _, char := range invalidChars {
		if strings.Contains(component, char) {
			return fmt.Errorf("%s name contains invalid character '%s': %s", componentType, char, component)
		}
	}

	// Check for reserved names on Windows (even though we're primarily Linux-focused)
	reservedNames := []string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}
	upperComponent := strings.ToUpper(component)
	for _, reserved := range reservedNames {
		if upperComponent == reserved {
			return fmt.Errorf("%s name cannot be a reserved system name: %s", componentType, component)
		}
	}

	return nil
}
