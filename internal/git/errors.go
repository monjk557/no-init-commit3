package git

import (
	"fmt"
)

// GitError represents errors that occur during Git operations
type GitError struct {
	// Operation is the Git operation that failed
	Operation string

	// Path is the repository or file path involved
	Path string

	// Ref is the Git reference (branch, tag, commit) if applicable
	Ref string

	// Message is the error message
	Message string

	// Err is the underlying error
	Err error
}

// Error implements the error interface
func (e *GitError) Error() string {
	if e.Ref != "" {
		return fmt.Sprintf("git %s failed for path %q at ref %q: %s", e.Operation, e.Path, e.Ref, e.Message)
	}
	return fmt.Sprintf("git %s failed for path %q: %s", e.Operation, e.Path, e.Message)
}

// Unwrap returns the underlying error
func (e *GitError) Unwrap() error {
	return e.Err
}

// NewGitError creates a new GitError
func NewGitError(operation, path, ref, message string, err error) *GitError {
	return &GitError{
		Operation: operation,
		Path:      path,
		Ref:       ref,
		Message:   message,
		Err:       err,
	}
}

// RepositoryNotFoundError indicates that a repository was not found
type RepositoryNotFoundError struct {
	Path string
}

// Error implements the error interface
func (e *RepositoryNotFoundError) Error() string {
	return fmt.Sprintf("repository not found at path: %s", e.Path)
}

// ReferenceNotFoundError indicates that a Git reference was not found
type ReferenceNotFoundError struct {
	Ref  string
	Path string
}

// Error implements the error interface
func (e *ReferenceNotFoundError) Error() string {
	return fmt.Sprintf("reference %q not found in repository at %s", e.Ref, e.Path)
}

// PathNotFoundError indicates that a path was not found in a Git tree
type PathNotFoundError struct {
	Path string
	Ref  string
	Repo string
}

// Error implements the error interface
func (e *PathNotFoundError) Error() string {
	return fmt.Sprintf("path %q not found at ref %q in repository %s", e.Path, e.Ref, e.Repo)
}

// InvalidRepositoryError indicates that a directory is not a valid Git repository
type InvalidRepositoryError struct {
	Path   string
	Reason string
}

// Error implements the error interface
func (e *InvalidRepositoryError) Error() string {
	return fmt.Sprintf("invalid repository at %s: %s", e.Path, e.Reason)
}
