package security

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/logging"
)

// ValidationConfig holds configuration for the validation framework
type ValidationConfig struct {
	MaxFileSize             int64    `yaml:"max_file_size" json:"max_file_size"`
	MaxCommitMessageLength  int      `yaml:"max_commit_message_length" json:"max_commit_message_length"`
	MaxPathLength           int      `yaml:"max_path_length" json:"max_path_length"`
	MaxFilesPerCommit       int      `yaml:"max_files_per_commit" json:"max_files_per_commit"`
	AllowedFileExtensions   []string `yaml:"allowed_file_extensions" json:"allowed_file_extensions"`
	BannedFileExtensions    []string `yaml:"banned_file_extensions" json:"banned_file_extensions"`
	EnableContentValidation bool     `yaml:"enable_content_validation" json:"enable_content_validation"`
	EnablePathCleaning      bool     `yaml:"enable_path_cleaning" json:"enable_path_cleaning"`
}

// ValidationFramework provides comprehensive validation using validator/v10
type ValidationFramework struct {
	config    *ValidationConfig
	logger    *logging.Logger
	validator *validator.Validate

	// Compiled regex patterns for Git-specific validation
	pathPattern   *regexp.Regexp
	branchPattern *regexp.Regexp
	commitPattern *regexp.Regexp
	refPattern    *regexp.Regexp
}

// Git-specific request validation structures using validator/v10 tags

// FileOperationRequest validates file creation/update requests
type FileOperationRequest struct {
	Content        string `json:"content" validate:"required"`
	CommitMessage  string `json:"commit_message" validate:"required,min=1,max=2048"`
	Branch         string `json:"branch" validate:"required,git_branch"`
	AuthorName     string `json:"author_name" validate:"required,min=1,max=100"`
	AuthorEmail    string `json:"author_email" validate:"required,email"`
	CommitterName  string `json:"committer_name,omitempty" validate:"omitempty,min=1,max=100"`
	CommitterEmail string `json:"committer_email,omitempty" validate:"omitempty,email"`
}

// FileDeleteRequest validates file deletion requests
type FileDeleteRequest struct {
	CommitMessage string `json:"commit_message" validate:"required,min=1,max=2048"`
	Branch        string `json:"branch" validate:"required,git_branch"`
	AuthorName    string `json:"author_name" validate:"required,min=1,max=100"`
	AuthorEmail   string `json:"author_email" validate:"required,email"`
}

// MultiFileCommitRequest validates multi-file commit operations
type MultiFileCommitRequest struct {
	Files         []FileChange `json:"files" validate:"required,min=1,max=1000,dive"`
	CommitMessage string       `json:"commit_message" validate:"required,min=1,max=2048"`
	Branch        string       `json:"branch" validate:"required,git_branch"`
	AuthorName    string       `json:"author_name" validate:"required,min=1,max=100"`
	AuthorEmail   string       `json:"author_email" validate:"required,email"`
}

// FileChange represents a single file change in a multi-file commit
type FileChange struct {
	Path      string `json:"path" validate:"required,git_path"`
	Operation string `json:"operation" validate:"required,oneof=create update delete"`
	Content   string `json:"content" validate:"required_if=Operation create,required_if=Operation update"`
}

// BranchOperationRequest validates branch creation/update requests
type BranchOperationRequest struct {
	BranchName string `json:"branch_name" validate:"required,git_branch"`
	StartPoint string `json:"start_point,omitempty" validate:"omitempty,git_ref"`
}

// MergeRequest validates merge operation requests
type MergeRequest struct {
	SourceBranch  string `json:"source_branch" validate:"required,git_branch"`
	TargetBranch  string `json:"target_branch" validate:"required,git_branch"`
	CommitMessage string `json:"commit_message" validate:"required,min=1,max=2048"`
	AuthorName    string `json:"author_name" validate:"required,min=1,max=100"`
	AuthorEmail   string `json:"author_email" validate:"required,email"`
}

// DefaultValidationConfig returns sensible defaults for validation
func DefaultValidationConfig() *ValidationConfig {
	return &ValidationConfig{
		MaxFileSize:             100 * 1024 * 1024, // 100MB as specified in task
		MaxCommitMessageLength:  2048,
		MaxPathLength:           4096,
		MaxFilesPerCommit:       1000,
		EnableContentValidation: true,
		EnablePathCleaning:      true,
		BannedFileExtensions: []string{
			".exe", ".bat", ".cmd", ".com", ".scr", ".pif", ".msi",
			".jar", ".class", ".war", ".ear",
			".dll", ".so", ".dylib",
			".ps1", ".vbs", ".sh",
		},
	}
}

// NewValidationFramework creates a new validation framework with validator/v10
func NewValidationFramework(config *ValidationConfig, logger *logging.Logger) (*ValidationFramework, error) {
	if config == nil {
		config = DefaultValidationConfig()
	}

	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	validate := validator.New()

	// Register custom Git validators
	if err := validate.RegisterValidation("git_path", validateGitPath); err != nil {
		return nil, fmt.Errorf("failed to register git_path validator: %w", err)
	}

	if err := validate.RegisterValidation("git_branch", validateGitBranch); err != nil {
		return nil, fmt.Errorf("failed to register git_branch validator: %w", err)
	}

	if err := validate.RegisterValidation("git_ref", validateGitRef); err != nil {
		return nil, fmt.Errorf("failed to register git_ref validator: %w", err)
	}

	// Compile regex patterns
	pathPattern, err := regexp.Compile(`^[a-zA-Z0-9/_.-]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile path pattern: %w", err)
	}

	branchPattern, err := regexp.Compile(`^[a-zA-Z0-9/_.-]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile branch pattern: %w", err)
	}

	commitPattern, err := regexp.Compile(`^[a-f0-9]{7,40}$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile commit pattern: %w", err)
	}

	refPattern, err := regexp.Compile(`^[a-zA-Z0-9/_.-]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile ref pattern: %w", err)
	}

	return &ValidationFramework{
		config:        config,
		logger:        logger,
		validator:     validate,
		pathPattern:   pathPattern,
		branchPattern: branchPattern,
		commitPattern: commitPattern,
		refPattern:    refPattern,
	}, nil
}

// ValidateRequestMiddleware returns a Gin middleware for comprehensive request validation
func (vf *ValidationFramework) ValidateRequestMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip validation for non-API endpoints
		if !vf.isAPIEndpoint(c.Request.URL.Path) {
			c.Next()
			return
		}

		vf.logger.Debug("Validating request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.String("remote_addr", c.ClientIP()),
		)

		// Validate request size
		if err := vf.validateRequestSize(c); err != nil {
			vf.logger.Warn("Request size validation failed", zap.Error(err))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error":   "Request too large",
				"code":    "REQUEST_TOO_LARGE",
				"details": err.Error(),
			})
			c.Abort()
			return
		}

		// Validate URL parameters (path traversal protection)
		if err := vf.validateURLParameters(c); err != nil {
			vf.logger.Warn("URL parameter validation failed", zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "Invalid URL parameters",
				"code":    "INVALID_URL_PARAMETERS",
				"details": err.Error(),
			})
			c.Abort()
			return
		}

		// Validate request body if present
		if c.Request.ContentLength > 0 {
			if err := vf.validateRequestBody(c); err != nil {
				vf.logger.Warn("Request body validation failed", zap.Error(err))
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "Invalid request body",
					"code":    "INVALID_REQUEST_BODY",
					"details": err.Error(),
				})
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

// validateRequestSize checks if the request size is within limits
func (vf *ValidationFramework) validateRequestSize(c *gin.Context) error {
	if c.Request.ContentLength > vf.config.MaxFileSize {
		return fmt.Errorf("request size %d bytes exceeds maximum allowed size %d bytes",
			c.Request.ContentLength, vf.config.MaxFileSize)
	}
	return nil
}

// validateURLParameters validates URL parameters for path traversal and other security issues
func (vf *ValidationFramework) validateURLParameters(c *gin.Context) error {
	// Extract path components from URL
	owner := c.Param("owner")
	repo := c.Param("repo")
	path := c.Param("path")
	branch := c.Query("branch")
	ref := c.Query("ref")

	// Validate owner and repo
	if owner != "" {
		if err := vf.validatePathComponent(owner, "owner"); err != nil {
			return fmt.Errorf("invalid owner: %w", err)
		}
	}

	if repo != "" {
		if err := vf.validatePathComponent(repo, "repository"); err != nil {
			return fmt.Errorf("invalid repository: %w", err)
		}
	}

	// Validate file path (comprehensive path traversal protection)
	if path != "" {
		if err := vf.validateFilePath(path); err != nil {
			return fmt.Errorf("invalid file path: %w", err)
		}
	}

	// Validate branch name
	if branch != "" {
		if err := vf.validateBranchName(branch); err != nil {
			return fmt.Errorf("invalid branch name: %w", err)
		}
	}

	// Validate ref
	if ref != "" {
		if err := vf.validateRef(ref); err != nil {
			return fmt.Errorf("invalid ref: %w", err)
		}
	}

	return nil
}

// validateRequestBody validates the request body based on the endpoint
func (vf *ValidationFramework) validateRequestBody(c *gin.Context) error {
	if c.Request.Header.Get("Content-Type") != "application/json" {
		return fmt.Errorf("content-type must be application/json")
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}

	// Restore the body for later handlers
	c.Request.Body = io.NopCloser(strings.NewReader(string(body)))

	// Validate based on endpoint pattern
	switch {
	case strings.Contains(c.Request.URL.Path, "/files/") && c.Request.Method == "PUT":
		return vf.validateFileOperationRequest(body)
	case strings.Contains(c.Request.URL.Path, "/files/") && c.Request.Method == "DELETE":
		return vf.validateFileDeleteRequest(body)
	case strings.Contains(c.Request.URL.Path, "/commits") && c.Request.Method == "POST":
		return vf.validateMultiFileCommitRequest(body)
	case strings.Contains(c.Request.URL.Path, "/branches") && (c.Request.Method == "POST" || c.Request.Method == "PUT"):
		return vf.validateBranchOperationRequest(body)
	case strings.Contains(c.Request.URL.Path, "/merge") && c.Request.Method == "POST":
		return vf.validateMergeRequest(body)
	}

	return nil
}

// Validation functions for specific request types

func (vf *ValidationFramework) validateFileOperationRequest(body []byte) error {
	var req FileOperationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	if err := vf.validator.Struct(req); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Additional custom validation
	if len(req.Content) > int(vf.config.MaxFileSize) {
		return fmt.Errorf("content size exceeds maximum file size")
	}

	return nil
}

func (vf *ValidationFramework) validateFileDeleteRequest(body []byte) error {
	var req FileDeleteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return vf.validator.Struct(req)
}

func (vf *ValidationFramework) validateMultiFileCommitRequest(body []byte) error {
	var req MultiFileCommitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	if err := vf.validator.Struct(req); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Validate file count
	if len(req.Files) > vf.config.MaxFilesPerCommit {
		return fmt.Errorf("too many files in commit: %d (max %d)",
			len(req.Files), vf.config.MaxFilesPerCommit)
	}

	return nil
}

func (vf *ValidationFramework) validateBranchOperationRequest(body []byte) error {
	var req BranchOperationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return vf.validator.Struct(req)
}

func (vf *ValidationFramework) validateMergeRequest(body []byte) error {
	var req MergeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return vf.validator.Struct(req)
}

// Path and component validation functions

func (vf *ValidationFramework) validatePathComponent(component, componentType string) error {
	if component == "" {
		return fmt.Errorf("%s cannot be empty", componentType)
	}

	if len(component) > 255 {
		return fmt.Errorf("%s too long (max 255 characters)", componentType)
	}

	// Check for path traversal
	if strings.Contains(component, "..") {
		return fmt.Errorf("%s contains path traversal attempt", componentType)
	}

	// Check for absolute paths
	if strings.HasPrefix(component, "/") {
		return fmt.Errorf("%s cannot start with slash", componentType)
	}

	// Check for invalid characters
	if !vf.pathPattern.MatchString(component) {
		return fmt.Errorf("%s contains invalid characters", componentType)
	}

	return nil
}

func (vf *ValidationFramework) validateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	if len(path) > vf.config.MaxPathLength {
		return fmt.Errorf("file path too long (max %d characters)", vf.config.MaxPathLength)
	}

	// Comprehensive path traversal protection
	if strings.Contains(path, "..") {
		return fmt.Errorf("file path contains path traversal attempt")
	}

	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("file path cannot be absolute")
	}

	// Clean the path if enabled
	if vf.config.EnablePathCleaning {
		cleaned := filepath.Clean(path)
		if cleaned != path {
			return fmt.Errorf("file path is not clean (would be: %s)", cleaned)
		}
	}

	// Check for Git internal paths
	if strings.HasPrefix(path, ".git/") {
		return fmt.Errorf("access to Git internal paths is forbidden")
	}

	// Validate file extension if enabled
	if vf.config.EnableContentValidation {
		ext := strings.ToLower(filepath.Ext(path))
		for _, banned := range vf.config.BannedFileExtensions {
			if ext == banned {
				return fmt.Errorf("file extension %s is not allowed", ext)
			}
		}
	}

	return nil
}

func (vf *ValidationFramework) validateBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch name cannot be empty")
	}

	if len(branch) > 255 {
		return fmt.Errorf("branch name too long (max 255 characters)")
	}

	if !vf.branchPattern.MatchString(branch) {
		return fmt.Errorf("branch name contains invalid characters")
	}

	// Git-specific branch name rules
	if strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") {
		return fmt.Errorf("invalid branch name format")
	}

	return nil
}

func (vf *ValidationFramework) validateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref cannot be empty")
	}

	// Check if it's a commit hash
	if vf.commitPattern.MatchString(ref) {
		return nil
	}

	// Check if it's a valid ref name
	if vf.refPattern.MatchString(ref) {
		return nil
	}

	return fmt.Errorf("invalid ref format")
}

// isAPIEndpoint checks if the path is a Git REST API endpoint
func (vf *ValidationFramework) isAPIEndpoint(path string) bool {
	apiPrefixes := []string{
		"/repos/",
		"/api/repos/",
	}

	for _, prefix := range apiPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}

// Custom validator functions for validator/v10

func validateGitPath(fl validator.FieldLevel) bool {
	path := fl.Field().String()

	// Basic checks
	if path == "" || len(path) > 4096 {
		return false
	}

	// Check for path traversal
	if strings.Contains(path, "..") {
		return false
	}

	// Check for absolute paths
	if strings.HasPrefix(path, "/") {
		return false
	}

	// Check for Git internal paths
	if strings.HasPrefix(path, ".git/") {
		return false
	}

	return true
}

func validateGitBranch(fl validator.FieldLevel) bool {
	branch := fl.Field().String()

	if branch == "" || len(branch) > 255 {
		return false
	}

	// Basic Git branch name validation
	if strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") {
		return false
	}

	// Check for invalid characters (simplified)
	invalidChars := []string{" ", "\t", "\n", "\r", "~", "^", ":", "?", "*", "[", "\\"}
	for _, char := range invalidChars {
		if strings.Contains(branch, char) {
			return false
		}
	}

	return true
}

func validateGitRef(fl validator.FieldLevel) bool {
	ref := fl.Field().String()

	if ref == "" {
		return false
	}

	// Check if it's a commit hash (7-40 hex characters)
	if matched, _ := regexp.MatchString(`^[a-f0-9]{7,40}$`, ref); matched {
		return true
	}

	// Check if it's a valid ref name (simplified)
	if matched, _ := regexp.MatchString(`^[a-zA-Z0-9/_.-]+$`, ref); matched {
		return true
	}

	return false
}
