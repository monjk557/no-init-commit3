// Package api provides error handling and response framework for Git REST API operations.
// This package defines standardized error structures, response formats, and error mapping utilities.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrorCode represents standardized error codes for Git API operations
type ErrorCode string

const (
	// Generic error codes
	ErrorCodeInternalError    ErrorCode = "internal_error"
	ErrorCodeBadRequest       ErrorCode = "bad_request"
	ErrorCodeUnauthorized     ErrorCode = "unauthorized"
	ErrorCodeForbidden        ErrorCode = "forbidden"
	ErrorCodeNotFound         ErrorCode = "not_found"
	ErrorCodeConflict         ErrorCode = "conflict"
	ErrorCodeValidationFailed ErrorCode = "validation_failed"
	ErrorCodeRateLimited      ErrorCode = "rate_limited"

	// Git-specific error codes
	ErrorCodeRepositoryNotFound    ErrorCode = "repository_not_found"
	ErrorCodeBranchNotFound        ErrorCode = "branch_not_found"
	ErrorCodeCommitNotFound        ErrorCode = "commit_not_found"
	ErrorCodeFileNotFound          ErrorCode = "file_not_found"
	ErrorCodeReferenceNotFound     ErrorCode = "reference_not_found"
	ErrorCodeInvalidReference      ErrorCode = "invalid_reference"
	ErrorCodeInvalidCommit         ErrorCode = "invalid_commit"
	ErrorCodeMergeConflict         ErrorCode = "merge_conflict"
	ErrorCodeBranchExists          ErrorCode = "branch_exists"
	ErrorCodeFileExists            ErrorCode = "file_exists"
	ErrorCodeInvalidPath           ErrorCode = "invalid_path"
	ErrorCodePathTraversal         ErrorCode = "path_traversal"
	ErrorCodeFileTooBig            ErrorCode = "file_too_big"
	ErrorCodeInvalidFileContent    ErrorCode = "invalid_file_content"
	ErrorCodeRepositoryCorrupted   ErrorCode = "repository_corrupted"
	ErrorCodeOperationNotSupported ErrorCode = "operation_not_supported"

	// Permission and security error codes
	ErrorCodePermissionDenied   ErrorCode = "permission_denied"
	ErrorCodeInsufficientAccess ErrorCode = "insufficient_access"
	ErrorCodeSecurityViolation  ErrorCode = "security_violation"
)

// ErrorSeverity represents the severity level of an error
type ErrorSeverity string

const (
	ErrorSeverityLow      ErrorSeverity = "low"
	ErrorSeverityMedium   ErrorSeverity = "medium"
	ErrorSeverityHigh     ErrorSeverity = "high"
	ErrorSeverityCritical ErrorSeverity = "critical"
)

// GitAPIError represents a standardized error for Git API operations
type GitAPIError struct {
	// Code is the standardized error code
	Code ErrorCode `json:"code"`
	// Message is the human-readable error message
	Message string `json:"message"`
	// Details provides additional context about the error
	Details map[string]interface{} `json:"details,omitempty"`
	// Severity indicates the severity level of the error
	Severity ErrorSeverity `json:"severity"`
	// Timestamp when the error occurred
	Timestamp time.Time `json:"timestamp"`
	// TraceID for correlation across services
	TraceID string `json:"trace_id,omitempty"`
	// RequestID for tracking specific requests
	RequestID string `json:"request_id,omitempty"`
	// Operation that caused the error
	Operation string `json:"operation,omitempty"`
	// Resource that was being accessed
	Resource string `json:"resource,omitempty"`
	// Underlying error (not serialized)
	Cause error `json:"-"`
}

// Error implements the error interface
func (e *GitAPIError) Error() string {
	if e.Operation != "" && e.Resource != "" {
		return fmt.Sprintf("%s: %s (operation: %s, resource: %s)", e.Code, e.Message, e.Operation, e.Resource)
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s: %s (operation: %s)", e.Code, e.Message, e.Operation)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying error for error chain compatibility
func (e *GitAPIError) Unwrap() error {
	return e.Cause
}

// Is implements error identity checking
func (e *GitAPIError) Is(target error) bool {
	var gitErr *GitAPIError
	if errors.As(target, &gitErr) {
		return e.Code == gitErr.Code
	}
	return false
}

// WithDetails adds additional context details to the error
func (e *GitAPIError) WithDetails(key string, value interface{}) GitAPIErrorInterface {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithTrace adds trace and request IDs for correlation
func (e *GitAPIError) WithTrace(traceID, requestID string) GitAPIErrorInterface {
	e.TraceID = traceID
	e.RequestID = requestID
	return e
}

// WithOperation sets the operation context
func (e *GitAPIError) WithOperation(operation string) GitAPIErrorInterface {
	e.Operation = operation
	return e
}

// WithResource sets the resource context
func (e *GitAPIError) WithResource(resource string) GitAPIErrorInterface {
	e.Resource = resource
	return e
}

// WithCause sets the underlying error cause
func (e *GitAPIError) WithCause(cause error) GitAPIErrorInterface {
	e.Cause = cause
	return e
}

// HTTPStatusCode returns the appropriate HTTP status code for the error
func (e *GitAPIError) HTTPStatusCode() int {
	switch e.Code {
	case ErrorCodeBadRequest, ErrorCodeValidationFailed, ErrorCodeInvalidPath,
		ErrorCodeInvalidReference, ErrorCodeInvalidCommit, ErrorCodePathTraversal,
		ErrorCodeInvalidFileContent:
		return http.StatusBadRequest
	case ErrorCodeUnauthorized:
		return http.StatusUnauthorized
	case ErrorCodeForbidden, ErrorCodePermissionDenied, ErrorCodeInsufficientAccess,
		ErrorCodeSecurityViolation:
		return http.StatusForbidden
	case ErrorCodeNotFound, ErrorCodeRepositoryNotFound, ErrorCodeBranchNotFound,
		ErrorCodeCommitNotFound, ErrorCodeFileNotFound, ErrorCodeReferenceNotFound:
		return http.StatusNotFound
	case ErrorCodeConflict, ErrorCodeMergeConflict, ErrorCodeBranchExists, ErrorCodeFileExists:
		return http.StatusConflict
	case ErrorCodeFileTooBig:
		return http.StatusRequestEntityTooLarge
	case ErrorCodeRateLimited:
		return http.StatusTooManyRequests
	case ErrorCodeOperationNotSupported:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

// GitAPIErrorInterface defines the interface for Git API errors
type GitAPIErrorInterface interface {
	error
	// GetCode returns the error code
	GetCode() ErrorCode
	// GetMessage returns the error message
	GetMessage() string
	// GetDetails returns additional error details
	GetDetails() map[string]interface{}
	// GetSeverity returns the error severity
	GetSeverity() ErrorSeverity
	// HTTPStatusCode returns the appropriate HTTP status code
	HTTPStatusCode() int
	// WithDetails adds additional context
	WithDetails(key string, value interface{}) GitAPIErrorInterface
	// WithTrace adds correlation IDs
	WithTrace(traceID, requestID string) GitAPIErrorInterface
	// WithOperation sets operation context
	WithOperation(operation string) GitAPIErrorInterface
	// WithResource sets resource context
	WithResource(resource string) GitAPIErrorInterface
	// WithCause sets underlying error
	WithCause(cause error) GitAPIErrorInterface
}

// Ensure GitAPIError implements GitAPIErrorInterface
var _ GitAPIErrorInterface = (*GitAPIError)(nil)

// Interface implementation methods
func (e *GitAPIError) GetCode() ErrorCode {
	return e.Code
}

func (e *GitAPIError) GetMessage() string {
	return e.Message
}

func (e *GitAPIError) GetDetails() map[string]interface{} {
	return e.Details
}

func (e *GitAPIError) GetSeverity() ErrorSeverity {
	return e.Severity
}

// NewGitAPIError creates a new GitAPIError with the specified code and message
func NewGitAPIError(code ErrorCode, message string, severity ErrorSeverity) *GitAPIError {
	return &GitAPIError{
		Code:      code,
		Message:   message,
		Severity:  severity,
		Timestamp: time.Now(),
		Details:   make(map[string]interface{}),
	}
}

// Predefined error constructors for common scenarios

// NewInternalError creates an internal server error
func NewInternalError(message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeInternalError, message, ErrorSeverityHigh)
}

// NewBadRequestError creates a bad request error
func NewBadRequestError(message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeBadRequest, message, ErrorSeverityLow)
}

// NewNotFoundError creates a not found error
func NewNotFoundError(resource, message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeNotFound, message, ErrorSeverityLow).
		WithResource(resource).(*GitAPIError)
}

// NewValidationError creates a validation error
func NewValidationError(message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeValidationFailed, message, ErrorSeverityLow)
}

// NewPermissionError creates a permission denied error
func NewPermissionError(operation, resource string) *GitAPIError {
	return NewGitAPIError(ErrorCodePermissionDenied,
		fmt.Sprintf("Permission denied for %s on %s", operation, resource),
		ErrorSeverityMedium).
		WithOperation(operation).
		WithResource(resource).(*GitAPIError)
}

// NewRepositoryNotFoundError creates a repository not found error
func NewRepositoryNotFoundError(owner, repo string) *GitAPIError {
	return NewGitAPIError(ErrorCodeRepositoryNotFound,
		fmt.Sprintf("Repository %s/%s not found", owner, repo),
		ErrorSeverityLow).
		WithResource(fmt.Sprintf("%s/%s", owner, repo)).(*GitAPIError)
}

// NewBranchNotFoundError creates a branch not found error
func NewBranchNotFoundError(branch string) *GitAPIError {
	return NewGitAPIError(ErrorCodeBranchNotFound,
		fmt.Sprintf("Branch '%s' not found", branch),
		ErrorSeverityLow).
		WithResource(branch).(*GitAPIError)
}

// NewCommitNotFoundError creates a commit not found error
func NewCommitNotFoundError(commit string) *GitAPIError {
	return NewGitAPIError(ErrorCodeCommitNotFound,
		fmt.Sprintf("Commit '%s' not found", commit),
		ErrorSeverityLow).
		WithResource(commit).(*GitAPIError)
}

// NewFileNotFoundError creates a file not found error
func NewFileNotFoundError(path string) *GitAPIError {
	return NewGitAPIError(ErrorCodeFileNotFound,
		fmt.Sprintf("File '%s' not found", path),
		ErrorSeverityLow).
		WithResource(path).(*GitAPIError)
}

// NewMergeConflictError creates a merge conflict error
func NewMergeConflictError(source, target string) *GitAPIError {
	return NewGitAPIError(ErrorCodeMergeConflict,
		fmt.Sprintf("Merge conflict between '%s' and '%s'", source, target),
		ErrorSeverityMedium).
		WithDetails("source", source).
		WithDetails("target", target).(*GitAPIError)
}

// NewPathTraversalError creates a path traversal security error
func NewPathTraversalError(path string) *GitAPIError {
	return NewGitAPIError(ErrorCodePathTraversal,
		fmt.Sprintf("Path traversal detected: %s", path),
		ErrorSeverityCritical).
		WithResource(path).(*GitAPIError)
}

// NewFileTooBigError creates a file too big error
func NewFileTooBigError(path string, size, maxSize int64) *GitAPIError {
	return NewGitAPIError(ErrorCodeFileTooBig,
		fmt.Sprintf("File '%s' is too big (%d bytes), maximum allowed: %d bytes", path, size, maxSize),
		ErrorSeverityLow).
		WithResource(path).
		WithDetails("size", size).
		WithDetails("max_size", maxSize).(*GitAPIError)
}
