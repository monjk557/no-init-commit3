// repository 패키지는 Git 저장소의 안전한 경로 관리를 제공합니다.
//
// 이 패키지는 경로 트래버설 공격, 잘못된 저장소 접근, 무단 파일시스템 작업을 방지하는
// 다층 보안 검증을 통해 포괄적인 저장소 경로 관리 시스템을 구현합니다.
//
// 주요 기능:
//   - 안전한 경로 생성 및 검증
//   - Git 저장소 구조 검증
//   - 크로스 플랫폼 호환성
//   - 스레드 안전 작업
//   - 포괄적인 에러 처리
//
// 기본 사용법:
//
//	// 설정 생성
//	config, err := repository.NewPathConfig("/var/lib/git/repos")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// 저장소 경로 생성 및 검증
//	path, err := repository.GetRepoPath(config, "user", "project")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// 저장소 존재 확인
//	err = repository.EnsureRepoExists(config, path)
//	if err != nil {
//	    log.Fatal(err)
//	}
package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Custom error types for better error handling and classification

// PathError는 추가 컨텍스트와 함께 경로 관련 에러를 나타냅니다.
type PathError struct {
	Op   string // Operation that caused the error (e.g., "validate", "generate", "check")
	Path string // The path that caused the error
	Err  error  // The underlying error
}

func (e *PathError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("repository %s failed for path %s: %v", e.Op, e.Path, e.Err)
	}
	return fmt.Sprintf("repository %s failed: %v", e.Op, e.Err)
}

func (e *PathError) Unwrap() error {
	return e.Err
}

// ValidationError는 검증 관련 에러를 나타냅니다.
type ValidationError struct {
	Component string // Component being validated (e.g., "owner", "repo", "path")
	Value     string // The value that failed validation
	Reason    string // Human-readable reason for failure
}

func (e *ValidationError) Error() string {
	if e.Value != "" {
		return fmt.Sprintf("validation failed for %s '%s': %s", e.Component, e.Value, e.Reason)
	}
	return fmt.Sprintf("validation failed for %s: %s", e.Component, e.Reason)
}

// RepositoryError는 저장소 구조 관련 에러를 나타냅니다.
type RepositoryError struct {
	Path   string // Repository path
	Type   string // Type of repository error (e.g., "not_found", "invalid_structure", "permission_denied")
	Detail string // Additional error details
}

func (e *RepositoryError) Error() string {
	return fmt.Sprintf("repository error at %s (%s): %s", e.Path, e.Type, e.Detail)
}

// Global configuration management with thread safety

var (
	// globalConfig holds the global default configuration
	globalConfig     *PathConfig
	globalConfigOnce sync.Once
	globalConfigMux  sync.RWMutex
)

// SetGlobalConfig sets the global default configuration for the package.
// This function is thread-safe and can be called from multiple goroutines.
// The configuration will be used by all *WithDefault functions.
func SetGlobalConfig(config *PathConfig) error {
	if config == nil {
		return &ValidationError{
			Component: "global_config",
			Reason:    "configuration cannot be nil",
		}
	}

	globalConfigMux.Lock()
	defer globalConfigMux.Unlock()
	globalConfig = config
	return nil
}

// GetGlobalConfig returns the current global configuration.
// This function is thread-safe and returns a copy to prevent modification.
// If no global configuration has been set, it returns the default configuration.
func GetGlobalConfig() (*PathConfig, error) {
	globalConfigMux.RLock()
	defer globalConfigMux.RUnlock()

	if globalConfig != nil {
		// Return a copy to prevent external modification
		return &PathConfig{RepoRoot: globalConfig.RepoRoot}, nil
	}

	// Initialize default configuration lazily
	var err error
	globalConfigOnce.Do(func() {
		defaultConfig, configErr := NewPathConfig(DefaultRepoRoot)
		if configErr != nil {
			err = &PathError{
				Op:  "initialize_global_config",
				Err: configErr,
			}
			return
		}
		globalConfig = defaultConfig
	})

	if err != nil {
		return nil, err
	}

	// Return a copy of the default configuration
	return &PathConfig{RepoRoot: globalConfig.RepoRoot}, nil
}

// ResetGlobalConfig resets the global configuration to nil.
// This is primarily useful for testing scenarios.
// This function is thread-safe.
func ResetGlobalConfig() {
	globalConfigMux.Lock()
	defer globalConfigMux.Unlock()
	globalConfig = nil
	globalConfigOnce = sync.Once{}
}

// Path management constants
const (
	// DefaultRepoRoot is the default base path for Git repositories
	DefaultRepoRoot = "/var/lib/git/repos"

	// RepoExtension is the standard Git repository extension
	RepoExtension = ".git"

	// PathSeparator is the standard path separator for repository paths
	PathSeparator = "/"
)

// PathConfig는 저장소 경로 관리를 위한 설정을 담는 구조체입니다.
type PathConfig struct {
	// RepoRoot is the base directory where all repositories are stored
	RepoRoot string
}

// NewPathConfig는 검증을 통해 새로운 PathConfig를 생성합니다.
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

	// Resolve symlinks to ensure consistent path comparison (fixes macOS /var -> /private/var issue)
	realRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		// If symlink resolution fails, use the original clean path
		realRoot = cleanRoot
	}

	return &PathConfig{
		RepoRoot: realRoot,
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

// GetRepoPath는 주어진 저장소에 대한 파일시스템 경로를 반환합니다.
// 이 함수는 소유자와 저장소 컴포넌트를 검증하고 전체 경로를 구성합니다.
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

	// Resolve symlinks to ensure consistent path comparison (fixes macOS /var -> /private/var issue)
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// If symlink resolution fails, use the original absolute path
		realPath = absPath
	}

	// The repository root should already be resolved to real path by NewPathConfig
	absRepoRoot := pathConfig.RepoRoot

	// Normalize paths for consistent comparison (handles case sensitivity on Windows)
	realPath = filepath.Clean(realPath)
	absRepoRoot = filepath.Clean(absRepoRoot)

	// Check if the path is within the repository root
	// Use filepath.Rel to check if the path is a subdirectory
	relPath, err := filepath.Rel(absRepoRoot, realPath)
	if err != nil {
		return fmt.Errorf("failed to determine relative path from %s to %s: %w", absRepoRoot, realPath, err)
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
	if !strings.HasSuffix(realPath, RepoExtension) {
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

// EnsureRepoExists는 주어진 경로에 Git 저장소가 존재하는지 확인합니다.
// 이 함수는 경로를 검증하고 저장소 구조를 확인합니다.
func EnsureRepoExists(pathConfig *PathConfig, repoPath string) error {
	// First validate the path using our security validation
	if err := ValidateRepoPath(pathConfig, repoPath); err != nil {
		return fmt.Errorf("path validation failed: %w", err)
	}

	// Check if the path exists
	if err := checkPathExists(repoPath); err != nil {
		return err
	}

	// Check if it's a valid Git repository
	if err := validateGitRepository(repoPath); err != nil {
		return err
	}

	return nil
}

// EnsureRepoExistsWithDefault is a convenience function that uses the default configuration
func EnsureRepoExistsWithDefault(repoPath string) error {
	defaultConfig, err := NewPathConfig(DefaultRepoRoot)
	if err != nil {
		return fmt.Errorf("failed to create default path config: %w", err)
	}

	return EnsureRepoExists(defaultConfig, repoPath)
}

// checkPathExists verifies that the given path exists and is accessible
func checkPathExists(path string) error {
	if path == "" {
		return fmt.Errorf("repository path cannot be empty")
	}

	// Use os.Stat to check if path exists and get file info
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("repository does not exist at path: %s", path)
		}
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied accessing repository at path: %s", path)
		}
		return fmt.Errorf("failed to access repository at path %s: %w", path, err)
	}

	// Ensure the path is a directory
	if !info.IsDir() {
		return fmt.Errorf("repository path is not a directory: %s", path)
	}

	return nil
}

// validateGitRepository checks if the path contains a valid Git repository structure
func validateGitRepository(repoPath string) error {
	// Check for bare repository (common for server-side repositories)
	if isBareRepository(repoPath) {
		return nil // Valid bare repository
	}

	// Check for non-bare repository (.git subdirectory)
	if isNonBareRepository(repoPath) {
		return nil // Valid non-bare repository
	}

	// Path exists but doesn't appear to be a Git repository
	return fmt.Errorf("path exists but does not contain a valid Git repository structure: %s", repoPath)
}

// isBareRepository checks if the given path is a bare Git repository
func isBareRepository(repoPath string) bool {
	// Bare repositories contain Git files directly in the root directory
	// Key indicators: HEAD file, refs/ directory, objects/ directory

	// Check for HEAD file
	headPath := filepath.Join(repoPath, "HEAD")
	if _, err := os.Stat(headPath); err != nil {
		return false
	}

	// Check for refs directory
	refsPath := filepath.Join(repoPath, "refs")
	if info, err := os.Stat(refsPath); err != nil || !info.IsDir() {
		return false
	}

	// Check for objects directory
	objectsPath := filepath.Join(repoPath, "objects")
	if info, err := os.Stat(objectsPath); err != nil || !info.IsDir() {
		return false
	}

	// Additional check: config file should exist
	configPath := filepath.Join(repoPath, "config")
	if _, err := os.Stat(configPath); err != nil {
		return false
	}

	return true
}

// isNonBareRepository checks if the given path contains a .git subdirectory
func isNonBareRepository(repoPath string) bool {
	// Non-bare repositories have a .git subdirectory containing Git metadata
	gitPath := filepath.Join(repoPath, ".git")

	if info, err := os.Stat(gitPath); err != nil {
		return false
	} else if info.IsDir() {
		// .git is a directory - check for key Git files inside
		return validateGitDirectory(gitPath)
	} else {
		// .git might be a file (worktree reference) - still valid but different structure
		return validateGitFile(gitPath)
	}
}

// validateGitDirectory checks if a .git directory contains required Git structure
func validateGitDirectory(gitPath string) bool {
	// Check for HEAD file
	headPath := filepath.Join(gitPath, "HEAD")
	if _, err := os.Stat(headPath); err != nil {
		return false
	}

	// Check for refs directory
	refsPath := filepath.Join(gitPath, "refs")
	if info, err := os.Stat(refsPath); err != nil || !info.IsDir() {
		return false
	}

	// Check for objects directory
	objectsPath := filepath.Join(gitPath, "objects")
	if info, err := os.Stat(objectsPath); err != nil || !info.IsDir() {
		return false
	}

	return true
}

// validateGitFile checks if a .git file contains a valid worktree reference
func validateGitFile(gitPath string) bool {
	// Read the .git file content
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return false
	}

	// .git file should contain "gitdir: <path>" for worktrees
	contentStr := strings.TrimSpace(string(content))
	if !strings.HasPrefix(contentStr, "gitdir: ") {
		return false
	}

	// Extract the referenced git directory path
	gitDir := strings.TrimPrefix(contentStr, "gitdir: ")
	gitDir = strings.TrimSpace(gitDir)

	// If path is relative, make it relative to the .git file location
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(gitPath), gitDir)
	}

	// Validate that the referenced directory exists and is a valid Git directory
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return false
	}

	return validateGitDirectory(gitDir)
}

// High-level integration functions for common workflows

// GetAndValidateRepoPath is a convenience function that generates and validates
// a repository path in a single operation. This is the most common workflow
// and combines GetRepoPath with ValidateRepoPath.
func GetAndValidateRepoPath(pathConfig *PathConfig, owner, repo string) (string, error) {
	// Generate the repository path
	repoPath, err := GetRepoPath(pathConfig, owner, repo)
	if err != nil {
		return "", &PathError{
			Op:  "generate_and_validate",
			Err: err,
		}
	}

	// Validate the generated path
	if err := ValidateRepoPath(pathConfig, repoPath); err != nil {
		return "", &PathError{
			Op:   "generate_and_validate",
			Path: repoPath,
			Err:  err,
		}
	}

	return repoPath, nil
}

// GetAndValidateRepoPathWithDefault uses the global configuration to generate
// and validate a repository path.
func GetAndValidateRepoPathWithDefault(owner, repo string) (string, error) {
	config, err := GetGlobalConfig()
	if err != nil {
		return "", &PathError{
			Op:  "get_global_config",
			Err: err,
		}
	}

	return GetAndValidateRepoPath(config, owner, repo)
}

// VerifyRepositoryAccess performs a complete repository access verification.
// This function combines path generation, validation, and existence checking
// to ensure a repository is accessible and valid.
func VerifyRepositoryAccess(pathConfig *PathConfig, owner, repo string) (string, error) {
	// Generate and validate the path
	repoPath, err := GetAndValidateRepoPath(pathConfig, owner, repo)
	if err != nil {
		return "", err
	}

	// Verify the repository exists and is valid
	if err := EnsureRepoExists(pathConfig, repoPath); err != nil {
		return "", &PathError{
			Op:   "verify_access",
			Path: repoPath,
			Err:  err,
		}
	}

	return repoPath, nil
}

// VerifyRepositoryAccessWithDefault uses the global configuration to perform
// complete repository access verification.
func VerifyRepositoryAccessWithDefault(owner, repo string) (string, error) {
	config, err := GetGlobalConfig()
	if err != nil {
		return "", &PathError{
			Op:  "get_global_config",
			Err: err,
		}
	}

	return VerifyRepositoryAccess(config, owner, repo)
}

// RepositoryInfo는 저장소에 대한 메타데이터를 담는 구조체입니다.
type RepositoryInfo struct {
	Owner        string // Repository owner
	Name         string // Repository name
	Path         string // Full filesystem path
	Type         string // Repository type ("bare", "non-bare", "worktree")
	Exists       bool   // Whether the repository exists
	IsValid      bool   // Whether the repository structure is valid
	IsAccessible bool   // Whether the repository is accessible (no permission issues)
}

// GetRepositoryInfo returns comprehensive information about a repository.
// This function performs all repository checks and returns detailed metadata.
func GetRepositoryInfo(pathConfig *PathConfig, owner, repo string) (*RepositoryInfo, error) {
	info := &RepositoryInfo{
		Owner: owner,
		Name:  repo,
	}

	// Generate repository path
	repoPath, err := GetRepoPath(pathConfig, owner, repo)
	if err != nil {
		return info, &PathError{
			Op:  "get_repository_info",
			Err: err,
		}
	}
	info.Path = repoPath

	// Validate path
	if err := ValidateRepoPath(pathConfig, repoPath); err != nil {
		return info, &PathError{
			Op:   "get_repository_info",
			Path: repoPath,
			Err:  err,
		}
	}

	// Check repository existence and structure
	if err := EnsureRepoExists(pathConfig, repoPath); err != nil {
		// Check what kind of error this is by examining the error message or unwrapping
		errStr := err.Error()

		if strings.Contains(errStr, "repository does not exist at path") {
			info.Exists = false
			info.IsAccessible = false
			info.IsValid = false
		} else if strings.Contains(errStr, "permission denied") {
			info.Exists = true
			info.IsAccessible = false
			info.IsValid = false
		} else {
			// Repository exists but structure is invalid
			info.Exists = true
			info.IsAccessible = true
			info.IsValid = false
		}
		return info, nil
	}

	// Repository is fully valid and accessible
	info.Exists = true
	info.IsAccessible = true
	info.IsValid = true

	// Determine repository type
	if isBareRepository(repoPath) {
		info.Type = "bare"
	} else if isNonBareRepository(repoPath) {
		// Check if it's a worktree
		gitPath := filepath.Join(repoPath, ".git")
		if fileInfo, err := os.Stat(gitPath); err == nil && !fileInfo.IsDir() {
			info.Type = "worktree"
		} else {
			info.Type = "non-bare"
		}
	}

	return info, nil
}

// GetRepositoryInfoWithDefault uses the global configuration to get repository information.
func GetRepositoryInfoWithDefault(owner, repo string) (*RepositoryInfo, error) {
	config, err := GetGlobalConfig()
	if err != nil {
		return nil, &PathError{
			Op:  "get_global_config",
			Err: err,
		}
	}

	return GetRepositoryInfo(config, owner, repo)
}

// BatchRepositoryInfo represents information about multiple repositories
type BatchRepositoryInfo struct {
	Repositories []*RepositoryInfo // Information for each repository
	TotalCount   int               // Total number of repositories processed
	ValidCount   int               // Number of valid repositories
	ErrorCount   int               // Number of repositories with errors
	Errors       []error           // Errors encountered during processing
}

// GetBatchRepositoryInfo retrieves information for multiple repositories in a single operation.
// This function processes multiple owner/repo pairs and returns comprehensive information.
func GetBatchRepositoryInfo(pathConfig *PathConfig, repos []struct{ Owner, Repo string }) *BatchRepositoryInfo {
	result := &BatchRepositoryInfo{
		Repositories: make([]*RepositoryInfo, 0, len(repos)),
		TotalCount:   len(repos),
	}

	for _, repo := range repos {
		info, err := GetRepositoryInfo(pathConfig, repo.Owner, repo.Repo)
		if err != nil {
			result.ErrorCount++
			result.Errors = append(result.Errors, err)
			// Add minimal info even on error
			if info == nil {
				info = &RepositoryInfo{
					Owner: repo.Owner,
					Name:  repo.Repo,
				}
			}
		} else if info.IsValid && info.IsAccessible {
			result.ValidCount++
		}

		result.Repositories = append(result.Repositories, info)
	}

	return result
}

// GetBatchRepositoryInfoWithDefault uses the global configuration for batch repository information.
func GetBatchRepositoryInfoWithDefault(repos []struct{ Owner, Repo string }) (*BatchRepositoryInfo, error) {
	config, err := GetGlobalConfig()
	if err != nil {
		return nil, &PathError{
			Op:  "get_global_config",
			Err: err,
		}
	}

	return GetBatchRepositoryInfo(config, repos), nil
}
