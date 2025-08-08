// Package middleware implements HTTP middleware for the Git executor
package middleware

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GitAPIValidationConfig configures the Git API validation middleware
type GitAPIValidationConfig struct {
	// MaxFileSize is the maximum allowed file size for file operations (bytes)
	MaxFileSize int64
	// MaxCommitMessageLength is the maximum allowed commit message length
	MaxCommitMessageLength int
	// MaxPathLength is the maximum allowed file path length
	MaxPathLength int
	// AllowedFileExtensions defines allowed file extensions (nil means all allowed)
	AllowedFileExtensions []string
	// BannedFileExtensions defines banned file extensions
	BannedFileExtensions []string
	// MaxFilesPerCommit is the maximum number of files per commit operation
	MaxFilesPerCommit int
	// EnableContentValidation enables content-based validation
	EnableContentValidation bool
}

// GitAPIValidationMiddleware provides comprehensive validation for Git REST API operations
type GitAPIValidationMiddleware struct {
	config *GitAPIValidationConfig
	logger *logging.Logger

	// Compiled regex patterns
	pathPattern   *regexp.Regexp
	branchPattern *regexp.Regexp
	commitPattern *regexp.Regexp
}

// FileOperationRequest represents the expected structure for file operations
type FileOperationRequest struct {
	Content        string `json:"content"`
	Message        string `json:"message"`
	Branch         string `json:"branch,omitempty"`
	CommitterName  string `json:"committer_name,omitempty"`
	CommitterEmail string `json:"committer_email,omitempty"`
}

// MultiFileOperationRequest represents the expected structure for multi-file operations
type MultiFileOperationRequest struct {
	Files []struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		Operation string `json:"operation"` // create, update, delete
	} `json:"files"`
	Message        string `json:"message"`
	Branch         string `json:"branch,omitempty"`
	CommitterName  string `json:"committer_name,omitempty"`
	CommitterEmail string `json:"committer_email,omitempty"`
}

// BranchOperationRequest represents the expected structure for branch operations
type BranchOperationRequest struct {
	Name      string `json:"name"`
	SourceRef string `json:"source_ref,omitempty"`
	TargetRef string `json:"target_ref,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

// MergeOperationRequest represents the expected structure for merge operations
type MergeOperationRequest struct {
	Base          string `json:"base"`
	Head          string `json:"head"`
	CommitMessage string `json:"commit_message,omitempty"`
	Strategy      string `json:"strategy,omitempty"`
}

// DefaultGitAPIValidationConfig returns a sensible default configuration
func DefaultGitAPIValidationConfig() *GitAPIValidationConfig {
	return &GitAPIValidationConfig{
		MaxFileSize:             50 * 1024 * 1024, // 50MB
		MaxCommitMessageLength:  2048,
		MaxPathLength:           4096,
		MaxFilesPerCommit:       1000,
		EnableContentValidation: true,
		BannedFileExtensions: []string{
			".exe", ".bat", ".cmd", ".com", ".scr", ".pif",
			".jar", ".class", ".war", ".ear",
			".dll", ".so", ".dylib",
		},
	}
}

// NewGitAPIValidationMiddleware creates a new Git API validation middleware
func NewGitAPIValidationMiddleware(config *GitAPIValidationConfig, logger *logging.Logger) (*GitAPIValidationMiddleware, error) {
	if config == nil {
		config = DefaultGitAPIValidationConfig()
	}

	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Compile validation patterns
	pathPattern, err := regexp.Compile(`^[a-zA-Z0-9/_.-]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile path pattern: %w", err)
	}

	branchPattern, err := regexp.Compile(`^[a-zA-Z0-9/_.-]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile branch pattern: %w", err)
	}

	commitPattern, err := regexp.Compile(`^[a-f0-9]{40}$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile commit pattern: %w", err)
	}

	return &GitAPIValidationMiddleware{
		config:        config,
		logger:        logger,
		pathPattern:   pathPattern,
		branchPattern: branchPattern,
		commitPattern: commitPattern,
	}, nil
}

// ValidateGitAPIRequest is the main middleware function for Git API validation
func (m *GitAPIValidationMiddleware) ValidateGitAPIRequest() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip validation for GET requests (read-only operations)
		if c.Request.Method == http.MethodGet {
			c.Next()
			return
		}

		// Only validate Git API endpoints
		if !m.isGitAPIEndpoint(c.Request.URL.Path) {
			c.Next()
			return
		}

		// Validate Content-Type for write operations
		if err := m.validateContentType(c); err != nil {
			m.logger.Warn("Invalid content type for Git API request",
				zap.String("path", c.Request.URL.Path),
				zap.String("method", c.Request.Method),
				zap.String("content_type", c.GetHeader("Content-Type")),
				zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid content type: " + err.Error(),
			})
			c.Abort()
			return
		}

		// Validate request size
		if err := m.validateRequestSize(c); err != nil {
			m.logger.Warn("Request size validation failed",
				zap.String("path", c.Request.URL.Path),
				zap.Int64("content_length", c.Request.ContentLength),
				zap.Error(err))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "Request too large: " + err.Error(),
			})
			c.Abort()
			return
		}

		// Validate URL parameters
		if err := m.validateURLParameters(c); err != nil {
			m.logger.Warn("URL parameter validation failed",
				zap.String("path", c.Request.URL.Path),
				zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid URL parameters: " + err.Error(),
			})
			c.Abort()
			return
		}

		// Validate request body based on endpoint type
		if err := m.validateRequestBody(c); err != nil {
			m.logger.Warn("Request body validation failed",
				zap.String("path", c.Request.URL.Path),
				zap.String("method", c.Request.Method),
				zap.Error(err))
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid request body: " + err.Error(),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// isGitAPIEndpoint checks if the request is for a Git API endpoint
func (m *GitAPIValidationMiddleware) isGitAPIEndpoint(path string) bool {
	gitAPIPatterns := []string{
		"/repos/", // All repository API endpoints
	}

	for _, pattern := range gitAPIPatterns {
		if strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

// validateContentType validates the Content-Type header for write operations
func (m *GitAPIValidationMiddleware) validateContentType(c *gin.Context) error {
	contentType := c.GetHeader("Content-Type")
	if contentType == "" {
		return fmt.Errorf("Content-Type header is required for write operations")
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("invalid Content-Type format: %w", err)
	}

	allowedTypes := []string{
		"application/json",
		"application/x-git-upload-pack",
		"application/x-git-receive-pack",
	}

	for _, allowed := range allowedTypes {
		if mediaType == allowed {
			return nil
		}
	}

	return fmt.Errorf("unsupported Content-Type: %s", mediaType)
}

// validateRequestSize validates the size of the incoming request
func (m *GitAPIValidationMiddleware) validateRequestSize(c *gin.Context) error {
	if c.Request.ContentLength > m.config.MaxFileSize {
		return fmt.Errorf("request size %d exceeds maximum allowed size %d",
			c.Request.ContentLength, m.config.MaxFileSize)
	}
	return nil
}

// validateURLParameters validates URL path parameters
func (m *GitAPIValidationMiddleware) validateURLParameters(c *gin.Context) error {
	// Validate owner parameter
	owner := c.Param("owner")
	if owner != "" {
		if err := m.validatePathComponent(owner, "owner"); err != nil {
			return err
		}
	}

	// Validate repo parameter
	repo := c.Param("repo")
	if repo != "" {
		if err := m.validatePathComponent(repo, "repository"); err != nil {
			return err
		}
	}

	// Validate path parameter (for file operations)
	// Extract the file path from the URL - handle cases like /repos/owner/repo/files/path/to/file.txt
	path := c.Request.URL.Path
	if strings.Contains(path, "/files/") {
		// Extract the file path portion after /files/
		parts := strings.Split(path, "/files/")
		if len(parts) > 1 {
			filePath := parts[1]
			if filePath != "" {
				if err := m.validateFilePath(filePath); err != nil {
					return err
				}
			}
		}
	}

	// Also check the direct path parameter if it exists
	pathParam := c.Param("path")
	if pathParam != "" {
		// Clean up the path parameter (remove leading slash if present)
		cleanPath := strings.TrimPrefix(pathParam, "/")
		if cleanPath != "" {
			if err := m.validateFilePath(cleanPath); err != nil {
				return err
			}
		}
	}

	// Validate branch names in query parameters
	branch := c.Query("branch")
	if branch != "" {
		if err := m.validateBranchName(branch); err != nil {
			return err
		}
	}

	return nil
}

// validateRequestBody validates the request body based on the endpoint
func (m *GitAPIValidationMiddleware) validateRequestBody(c *gin.Context) error {
	// Only validate JSON bodies
	contentType := c.GetHeader("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return nil
	}

	// Read and validate the request body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}

	// Restore the body for downstream handlers
	c.Request.Body = io.NopCloser(strings.NewReader(string(body)))

	// Validate based on endpoint type
	path := c.Request.URL.Path
	method := c.Request.Method

	if strings.Contains(path, "/files/") && method == http.MethodPut {
		return m.validateFileOperationRequest(body)
	}

	if strings.Contains(path, "/commits") && method == http.MethodPost {
		return m.validateMultiFileOperationRequest(body)
	}

	if strings.Contains(path, "/branches") && (method == http.MethodPost || method == http.MethodPatch) {
		return m.validateBranchOperationRequest(body)
	}

	if strings.Contains(path, "/merges") && method == http.MethodPost {
		return m.validateMergeOperationRequest(body)
	}

	return nil
}

// validateFileOperationRequest validates single file operation requests
func (m *GitAPIValidationMiddleware) validateFileOperationRequest(body []byte) error {
	var req FileOperationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON format: %w", err)
	}

	// Validate content size
	if len(req.Content) > int(m.config.MaxFileSize) {
		return fmt.Errorf("file content exceeds maximum size %d", m.config.MaxFileSize)
	}

	// Validate commit message
	if err := m.validateCommitMessage(req.Message); err != nil {
		return err
	}

	// Validate branch name
	if req.Branch != "" {
		if err := m.validateBranchName(req.Branch); err != nil {
			return err
		}
	}

	// Validate committer information
	if err := m.validateCommitterInfo(req.CommitterName, req.CommitterEmail); err != nil {
		return err
	}

	return nil
}

// validateMultiFileOperationRequest validates multi-file operation requests
func (m *GitAPIValidationMiddleware) validateMultiFileOperationRequest(body []byte) error {
	var req MultiFileOperationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON format: %w", err)
	}

	// Validate number of files
	if len(req.Files) > m.config.MaxFilesPerCommit {
		return fmt.Errorf("number of files %d exceeds maximum %d",
			len(req.Files), m.config.MaxFilesPerCommit)
	}

	// Validate each file
	for i, file := range req.Files {
		if err := m.validateFilePath(file.Path); err != nil {
			return fmt.Errorf("file %d path invalid: %w", i, err)
		}

		if len(file.Content) > int(m.config.MaxFileSize) {
			return fmt.Errorf("file %d content exceeds maximum size %d", i, m.config.MaxFileSize)
		}

		if err := m.validateFileOperation(file.Operation); err != nil {
			return fmt.Errorf("file %d operation invalid: %w", i, err)
		}
	}

	// Validate commit message
	if err := m.validateCommitMessage(req.Message); err != nil {
		return err
	}

	return nil
}

// validateBranchOperationRequest validates branch operation requests
func (m *GitAPIValidationMiddleware) validateBranchOperationRequest(body []byte) error {
	var req BranchOperationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON format: %w", err)
	}

	if err := m.validateBranchName(req.Name); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}

	if req.SourceRef != "" {
		if err := m.validateRefName(req.SourceRef); err != nil {
			return fmt.Errorf("invalid source ref: %w", err)
		}
	}

	if req.TargetRef != "" {
		if err := m.validateRefName(req.TargetRef); err != nil {
			return fmt.Errorf("invalid target ref: %w", err)
		}
	}

	return nil
}

// validateMergeOperationRequest validates merge operation requests
func (m *GitAPIValidationMiddleware) validateMergeOperationRequest(body []byte) error {
	var req MergeOperationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON format: %w", err)
	}

	if err := m.validateRefName(req.Base); err != nil {
		return fmt.Errorf("invalid base ref: %w", err)
	}

	if err := m.validateRefName(req.Head); err != nil {
		return fmt.Errorf("invalid head ref: %w", err)
	}

	if req.CommitMessage != "" {
		if err := m.validateCommitMessage(req.CommitMessage); err != nil {
			return err
		}
	}

	if req.Strategy != "" {
		allowedStrategies := []string{"merge", "squash", "rebase"}
		valid := false
		for _, strategy := range allowedStrategies {
			if req.Strategy == strategy {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid merge strategy: %s", req.Strategy)
		}
	}

	return nil
}

// Helper validation functions

func (m *GitAPIValidationMiddleware) validatePathComponent(component, componentType string) error {
	if component == "" {
		return fmt.Errorf("%s cannot be empty", componentType)
	}

	if len(component) > 255 {
		return fmt.Errorf("%s name too long (max 255 characters)", componentType)
	}

	// Check for path traversal
	if strings.Contains(component, "..") {
		return fmt.Errorf("%s contains path traversal attempt", componentType)
	}

	// Check for invalid characters
	if strings.ContainsAny(component, "\\/:*?\"<>|") {
		return fmt.Errorf("%s contains invalid characters", componentType)
	}

	return nil
}

func (m *GitAPIValidationMiddleware) validateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	if len(path) > m.config.MaxPathLength {
		return fmt.Errorf("file path too long (max %d characters)", m.config.MaxPathLength)
	}

	// Check for path traversal
	if strings.Contains(path, "..") {
		return fmt.Errorf("file path contains path traversal attempt")
	}

	// Check for absolute paths
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("file path cannot be absolute")
	}

	// Validate file extension if content validation is enabled
	if m.config.EnableContentValidation {
		ext := strings.ToLower(filepath.Ext(path))
		for _, banned := range m.config.BannedFileExtensions {
			if ext == banned {
				return fmt.Errorf("file extension %s is not allowed", ext)
			}
		}
	}

	return nil
}

func (m *GitAPIValidationMiddleware) validateBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch name cannot be empty")
	}

	if len(branch) > 250 {
		return fmt.Errorf("branch name too long (max 250 characters)")
	}

	if !m.branchPattern.MatchString(branch) {
		return fmt.Errorf("branch name contains invalid characters")
	}

	// Git branch name restrictions
	if strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") {
		return fmt.Errorf("invalid branch name format")
	}

	return nil
}

func (m *GitAPIValidationMiddleware) validateRefName(ref string) error {
	if ref == "" {
		return fmt.Errorf("reference name cannot be empty")
	}

	// Check if it's a commit hash
	if m.commitPattern.MatchString(ref) {
		return nil
	}

	// Check if it's a valid branch/tag name
	return m.validateBranchName(ref)
}

func (m *GitAPIValidationMiddleware) validateCommitMessage(message string) error {
	if message == "" {
		return fmt.Errorf("commit message cannot be empty")
	}

	if len(message) > m.config.MaxCommitMessageLength {
		return fmt.Errorf("commit message too long (max %d characters)", m.config.MaxCommitMessageLength)
	}

	return nil
}

func (m *GitAPIValidationMiddleware) validateCommitterInfo(name, email string) error {
	if name != "" && len(name) > 100 {
		return fmt.Errorf("committer name too long (max 100 characters)")
	}

	if email != "" {
		if len(email) > 254 {
			return fmt.Errorf("committer email too long (max 254 characters)")
		}
		if !strings.Contains(email, "@") {
			return fmt.Errorf("invalid email format")
		}
	}

	return nil
}

func (m *GitAPIValidationMiddleware) validateFileOperation(operation string) error {
	allowedOps := []string{"create", "update", "delete"}
	for _, allowed := range allowedOps {
		if operation == allowed {
			return nil
		}
	}
	return fmt.Errorf("invalid file operation: %s", operation)
}
