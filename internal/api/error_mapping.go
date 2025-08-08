// Package api provides error mapping functionality to convert go-git errors into standardized API errors.
// This file handles the mapping of various go-git and related library errors to GitAPIError instances.
package api

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GitErrorMapper provides functionality to map go-git errors to API errors
type GitErrorMapper struct{}

// NewGitErrorMapper creates a new Git error mapper instance
func NewGitErrorMapper() *GitErrorMapper {
	return &GitErrorMapper{}
}

// MapError converts a go-git error into a GitAPIError
func (m *GitErrorMapper) MapError(err error, operation string) *GitAPIError {
	if err == nil {
		return nil
	}

	var apiErr *GitAPIError

	// Try to map specific go-git errors first
	if apiErr = m.mapGoGitErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// Try to map plumbing errors
	if apiErr = m.mapPlumbingErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// Try to map object errors
	if apiErr = m.mapObjectErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// Try to map filesystem errors
	if apiErr = m.mapFilesystemErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// Try to map by error message patterns
	if apiErr = m.mapByErrorMessage(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// Fallback to generic internal error
	apiErr = NewInternalError(fmt.Sprintf("Unexpected error: %v", err))
	apiErr.Operation = operation
	apiErr.Cause = err
	return apiErr
}

// mapGoGitErrors maps errors from the main git package
func (m *GitErrorMapper) mapGoGitErrors(err error) *GitAPIError {
	switch err {
	case git.ErrRepositoryNotExists:
		apiErr := NewGitAPIError(ErrorCodeRepositoryNotFound, "Repository does not exist", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrRepositoryAlreadyExists:
		apiErr := NewGitAPIError(ErrorCodeConflict, "Repository already exists", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrRemoteNotFound:
		apiErr := NewGitAPIError(ErrorCodeNotFound, "Remote not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrBranchExists:
		apiErr := NewGitAPIError(ErrorCodeBranchExists, "Branch already exists", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrBranchNotFound:
		apiErr := NewGitAPIError(ErrorCodeBranchNotFound, "Branch not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrTagExists:
		apiErr := NewGitAPIError(ErrorCodeConflict, "Tag already exists", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrTagNotFound:
		apiErr := NewGitAPIError(ErrorCodeNotFound, "Tag not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrInvalidReference:
		apiErr := NewGitAPIError(ErrorCodeInvalidReference, "Invalid reference", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrForceNeeded:
		apiErr := NewGitAPIError(ErrorCodeConflict, "Force push required", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	case git.ErrEmptyCommit:
		apiErr := NewGitAPIError(ErrorCodeBadRequest, "Empty commit not allowed", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case git.ErrWorktreeNotClean:
		apiErr := NewGitAPIError(ErrorCodeConflict, "Working tree is not clean", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	case git.ErrUnstagedChanges:
		apiErr := NewGitAPIError(ErrorCodeConflict, "Unstaged changes present", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	case git.ErrNonFastForwardUpdate:
		apiErr := NewGitAPIError(ErrorCodeConflict, "Non-fast-forward update", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	case git.ErrDeleteRefNotSupported:
		apiErr := NewGitAPIError(ErrorCodeOperationNotSupported, "Delete reference not supported", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	default:
		return nil // Not a recognized go-git error
	}
}

// mapPlumbingErrors maps errors from the plumbing package
func (m *GitErrorMapper) mapPlumbingErrors(err error) *GitAPIError {
	switch err {
	case plumbing.ErrObjectNotFound:
		apiErr := NewGitAPIError(ErrorCodeNotFound, "Git object not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	case plumbing.ErrInvalidType:
		apiErr := NewGitAPIError(ErrorCodeBadRequest, "Invalid object type", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	default:
		return nil // Not a recognized plumbing error
	}
}

// mapObjectErrors maps errors from the object package
func (m *GitErrorMapper) mapObjectErrors(err error) *GitAPIError {
	switch err {
	case object.ErrFileNotFound:
		apiErr := NewGitAPIError(ErrorCodeFileNotFound, "File not found in tree", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	default:
		return nil // Not a recognized object error
	}
}

// mapFilesystemErrors maps filesystem-related errors
func (m *GitErrorMapper) mapFilesystemErrors(err error) *GitAPIError {
	if os.IsNotExist(err) {
		apiErr := NewGitAPIError(ErrorCodeRepositoryNotFound, "Repository or path not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}
	if os.IsPermission(err) {
		apiErr := NewGitAPIError(ErrorCodePermissionDenied, "Permission denied", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	}
	return nil // Not a recognized filesystem error
}

// mapByErrorMessage maps errors based on error message patterns
func (m *GitErrorMapper) mapByErrorMessage(err error) *GitAPIError {
	errMsg := err.Error()
	lowerMsg := strings.ToLower(errMsg)

	var apiErr *GitAPIError

	// Repository-related errors
	if strings.Contains(lowerMsg, "repository not found") || strings.Contains(lowerMsg, "no such file or directory") || strings.Contains(lowerMsg, "repository does not exist") {
		apiErr = NewGitAPIError(ErrorCodeRepositoryNotFound, "Repository not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// Reference-related errors
	if strings.Contains(lowerMsg, "reference not found") || strings.Contains(lowerMsg, "ref not found") {
		apiErr = NewGitAPIError(ErrorCodeReferenceNotFound, "Reference not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// Branch-related errors
	if strings.Contains(lowerMsg, "branch") && strings.Contains(lowerMsg, "not found") {
		apiErr = NewGitAPIError(ErrorCodeBranchNotFound, "Branch not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// Commit-related errors
	if strings.Contains(lowerMsg, "commit") && strings.Contains(lowerMsg, "not found") {
		apiErr = NewGitAPIError(ErrorCodeCommitNotFound, "Commit not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// File-related errors
	if strings.Contains(lowerMsg, "file not found") || strings.Contains(lowerMsg, "path not found") {
		apiErr = NewGitAPIError(ErrorCodeFileNotFound, "File not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// Permission-related errors
	if strings.Contains(lowerMsg, "permission denied") || strings.Contains(lowerMsg, "access denied") {
		apiErr = NewGitAPIError(ErrorCodePermissionDenied, "Permission denied", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	}

	// Path traversal and security
	if strings.Contains(lowerMsg, "path traversal") || strings.Contains(lowerMsg, "invalid path") {
		apiErr = NewGitAPIError(ErrorCodePathTraversal, "Invalid or unsafe path", ErrorSeverityCritical)
		apiErr.Cause = err
		return apiErr
	}

	// Merge conflicts
	if strings.Contains(lowerMsg, "merge conflict") || strings.Contains(lowerMsg, "conflict") {
		apiErr = NewGitAPIError(ErrorCodeMergeConflict, "Merge conflict detected", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	}

	// Corruption and integrity
	if strings.Contains(lowerMsg, "corrupt") || strings.Contains(lowerMsg, "integrity") ||
		strings.Contains(lowerMsg, "checksum") {
		apiErr = NewGitAPIError(ErrorCodeRepositoryCorrupted, "Repository corruption detected", ErrorSeverityHigh)
		apiErr.Cause = err
		return apiErr
	}

	// Size and limits
	if strings.Contains(lowerMsg, "too large") || strings.Contains(lowerMsg, "too big") ||
		strings.Contains(lowerMsg, "size limit") {
		apiErr = NewGitAPIError(ErrorCodeFileTooBig, "File or object too large", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// Invalid format or content
	if strings.Contains(lowerMsg, "invalid format") || strings.Contains(lowerMsg, "malformed") ||
		strings.Contains(lowerMsg, "bad format") {
		apiErr = NewGitAPIError(ErrorCodeBadRequest, "Invalid format or content", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// Network and timeout errors
	if strings.Contains(lowerMsg, "timeout") || strings.Contains(lowerMsg, "connection") {
		apiErr = NewGitAPIError(ErrorCodeInternalError, "Network or timeout error", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	}

	return nil // No pattern matched
}

// MapErrorWithContext is a convenience method that includes additional context
func (m *GitErrorMapper) MapErrorWithContext(err error, operation, resource string) *GitAPIError {
	if err == nil {
		return nil
	}

	apiErr := m.MapError(err, operation)
	if resource != "" {
		apiErr.Resource = resource
	}

	return apiErr
}

// MapErrorWithDetails is a convenience method that includes additional details
func (m *GitErrorMapper) MapErrorWithDetails(err error, operation string, details map[string]interface{}) *GitAPIError {
	if err == nil {
		return nil
	}

	apiErr := m.MapError(err, operation)

	// Add each detail
	if apiErr.Details == nil {
		apiErr.Details = make(map[string]interface{})
	}
	for key, value := range details {
		apiErr.Details[key] = value
	}

	return apiErr
}

// GetDefaultMapper returns a default instance of GitErrorMapper
func GetDefaultMapper() *GitErrorMapper {
	return &GitErrorMapper{}
}
