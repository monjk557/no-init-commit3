// repository 패키지는 Git 저장소의 안전한 경로 관리를 제공합니다.
// 이 파일은 Git REST API 작업을 위한 기본 경로 검증을 확장합니다.
package repository

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GitAPIPathValidator는 Git REST API 작업을 위한 특수한 경로 검증을 제공합니다.
type GitAPIPathValidator struct {
	pathConfig *PathConfig
}

// NewGitAPIPathValidator는 새로운 Git API 경로 검증기를 생성합니다.
func NewGitAPIPathValidator(pathConfig *PathConfig) *GitAPIPathValidator {
	return &GitAPIPathValidator{
		pathConfig: pathConfig,
	}
}

// ValidateFilePath는 추가 검사를 통해 Git API 작업의 파일 경로를 검증합니다.
func (v *GitAPIPathValidator) ValidateFilePath(filePath string) error {
	// Basic validation first
	if err := v.validateBasicPath(filePath); err != nil {
		return err
	}

	// Additional Git-specific validations
	if err := v.validateGitSpecificPath(filePath); err != nil {
		return err
	}

	return nil
}

// ValidateRepoAPIPath는 API 작업을 위한 저장소 경로를 검증합니다.
func (v *GitAPIPathValidator) ValidateRepoAPIPath(owner, repo string) error {
	// Use existing validation from path.go
	if err := ValidatePathComponents(owner, repo); err != nil {
		return fmt.Errorf("invalid repository path components: %w", err)
	}

	// Additional API-specific validations
	if err := v.validateAPIPathComponents(owner, repo); err != nil {
		return err
	}

	return nil
}

// ValidateBranchRef는 API 작업을 위한 브랜치/태그 참조를 검증합니다.
func (v *GitAPIPathValidator) ValidateBranchRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("reference cannot be empty")
	}

	// Length check
	if len(ref) > 255 {
		return fmt.Errorf("reference name too long (max 255 characters)")
	}

	// Git reference name rules
	if err := v.validateGitRefName(ref); err != nil {
		return err
	}

	return nil
}

// ValidateCommitHash는 커밋 해시를 검증합니다.
func (v *GitAPIPathValidator) ValidateCommitHash(hash string) error {
	if hash == "" {
		return fmt.Errorf("commit hash cannot be empty")
	}

	// Full SHA-1 hash (40 characters)
	if len(hash) == 40 {
		for _, r := range hash {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return fmt.Errorf("invalid commit hash format (must be lowercase hex)")
			}
		}
		return nil
	}

	// Short hash (7-40 characters)
	if len(hash) >= 7 && len(hash) < 40 {
		for _, r := range hash {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return fmt.Errorf("invalid commit hash format (must be lowercase hex)")
			}
		}
		return nil
	}

	return fmt.Errorf("invalid commit hash length (must be 7-40 characters)")
}

// validateBasicPath performs basic path validation
func (v *GitAPIPathValidator) validateBasicPath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	// Length check
	if len(path) > 4096 {
		return fmt.Errorf("file path too long (max 4096 characters)")
	}

	// Path traversal protection
	if strings.Contains(path, "..") {
		return fmt.Errorf("file path contains path traversal attempt")
	}

	// Absolute path protection
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("file path cannot be absolute")
	}

	// Windows path separator protection
	if strings.Contains(path, "\\") {
		return fmt.Errorf("file path cannot contain backslashes")
	}

	return nil
}

// validateGitSpecificPath performs Git-specific path validation
func (v *GitAPIPathValidator) validateGitSpecificPath(path string) error {
	// Normalize path
	normalizedPath := filepath.Clean(path)

	// Check for Git-specific forbidden paths
	forbiddenPaths := []string{
		".git",
		".git/",
		"refs/",
		"objects/",
		"hooks/",
	}

	// Check if path starts with forbidden Git paths
	for _, forbidden := range forbiddenPaths {
		if strings.HasPrefix(normalizedPath, forbidden) {
			return fmt.Errorf("file path cannot access Git internal directories: %s", forbidden)
		}
	}

	// Check for forbidden file names within the path
	pathParts := strings.Split(normalizedPath, "/")
	for _, part := range pathParts {
		if err := v.validatePathPart(part); err != nil {
			return fmt.Errorf("invalid path component '%s': %w", part, err)
		}
	}

	// Check for .git extension duplication in repository names
	if strings.Contains(normalizedPath, ".git.git") {
		return fmt.Errorf("repository name should not contain duplicated .git extension")
	}

	return nil
}

// validateAPIPathComponents validates owner and repo components for API operations
func (v *GitAPIPathValidator) validateAPIPathComponents(owner, repo string) error {
	// Reserved API namespace protection
	reservedOwners := []string{
		"api", "admin", "system", "internal", "health", "metrics",
		"version", "status", "config", "logs", "debug",
	}

	for _, reserved := range reservedOwners {
		if strings.EqualFold(owner, reserved) {
			return fmt.Errorf("owner name '%s' is reserved", owner)
		}
	}

	// Additional repository name validations
	reservedRepos := []string{
		"settings", "admin", "api", "webhook", "hooks",
		"refs", "objects", "info", "logs",
	}

	for _, reserved := range reservedRepos {
		if strings.EqualFold(repo, reserved) {
			return fmt.Errorf("repository name '%s' is reserved", repo)
		}
	}

	return nil
}

// validateGitRefName validates Git reference names according to Git rules
func (v *GitAPIPathValidator) validateGitRefName(ref string) error {
	// Git reference name rules:
	// - Cannot begin or end with a slash
	// - Cannot contain consecutive slashes
	// - Cannot contain certain characters
	// - Cannot end with .lock
	// - Cannot be a single @ character
	// - Cannot contain @{ sequence

	if strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") {
		return fmt.Errorf("reference name cannot begin or end with slash")
	}

	if strings.Contains(ref, "//") {
		return fmt.Errorf("reference name cannot contain consecutive slashes")
	}

	if strings.HasSuffix(ref, ".lock") {
		return fmt.Errorf("reference name cannot end with .lock")
	}

	if ref == "@" {
		return fmt.Errorf("reference name cannot be a single @ character")
	}

	if strings.Contains(ref, "@{") {
		return fmt.Errorf("reference name cannot contain @{ sequence")
	}

	// Check for invalid characters
	invalidChars := []string{
		" ", "~", "^", ":", "?", "*", "[", "\\", "\x7f",
	}

	for _, char := range invalidChars {
		if strings.Contains(ref, char) {
			return fmt.Errorf("reference name contains invalid character: %s", char)
		}
	}

	// Check for control characters (ASCII 0-31)
	for _, r := range ref {
		if r < 32 {
			return fmt.Errorf("reference name contains control character")
		}
	}

	return nil
}

// validatePathPart validates individual path components
func (v *GitAPIPathValidator) validatePathPart(part string) error {
	if part == "" {
		return fmt.Errorf("path component cannot be empty")
	}

	if part == "." || part == ".." {
		return fmt.Errorf("path component cannot be dot or dotdot")
	}

	// Check for reserved names
	reservedNames := []string{
		"CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
	}

	upperPart := strings.ToUpper(part)
	for _, reserved := range reservedNames {
		if upperPart == reserved || strings.HasPrefix(upperPart, reserved+".") {
			return fmt.Errorf("path component uses reserved system name: %s", part)
		}
	}

	// Check for invalid characters in file names
	invalidFileChars := []string{
		"<", ">", ":", "\"", "|", "?", "*",
	}

	for _, char := range invalidFileChars {
		if strings.Contains(part, char) {
			return fmt.Errorf("path component contains invalid character: %s", char)
		}
	}

	return nil
}

// GetSafeRepoPath는 API 작업을 위한 안전한 저장소 경로를 생성합니다.
func (v *GitAPIPathValidator) GetSafeRepoPath(owner, repo string) (string, error) {
	// Validate components first
	if err := v.ValidateRepoAPIPath(owner, repo); err != nil {
		return "", err
	}

	// Use existing GetRepoPath function but with additional validation
	if v.pathConfig == nil {
		return "", fmt.Errorf("path configuration is required")
	}

	return GetRepoPath(v.pathConfig, owner, repo)
}

// ValidateFullFilePath validates a complete file path within a repository
func (v *GitAPIPathValidator) ValidateFullFilePath(owner, repo, filePath string) error {
	// Validate repository path
	if err := v.ValidateRepoAPIPath(owner, repo); err != nil {
		return err
	}

	// Validate file path
	if err := v.ValidateFilePath(filePath); err != nil {
		return err
	}

	// Generate the full path to ensure it's within repository bounds
	repoPath, err := v.GetSafeRepoPath(owner, repo)
	if err != nil {
		return fmt.Errorf("failed to generate repository path: %w", err)
	}

	// Construct the full file path
	fullPath := filepath.Join(repoPath, filePath)

	// Validate the constructed path is still within repository bounds
	if err := ValidateRepoPath(v.pathConfig, filepath.Dir(fullPath)); err != nil {
		return fmt.Errorf("constructed file path is unsafe: %w", err)
	}

	return nil
}
