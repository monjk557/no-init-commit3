package git

import (
	"fmt"
)

// GitError는 Git 작업 중 발생하는 에러를 나타냅니다.
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

// NewGitError는 새로운 GitError를 생성합니다.
func NewGitError(operation, path, ref, message string, err error) *GitError {
	return &GitError{
		Operation: operation,
		Path:      path,
		Ref:       ref,
		Message:   message,
		Err:       err,
	}
}

// RepositoryNotFoundError는 저장소를 찾을 수 없음을 나타냅니다.
type RepositoryNotFoundError struct {
	Path string
}

// Error implements the error interface
func (e *RepositoryNotFoundError) Error() string {
	return fmt.Sprintf("repository not found at path: %s", e.Path)
}

// ReferenceNotFoundError는 Git 참조를 찾을 수 없음을 나타냅니다.
type ReferenceNotFoundError struct {
	Ref  string
	Path string
}

// Error implements the error interface
func (e *ReferenceNotFoundError) Error() string {
	return fmt.Sprintf("reference %q not found in repository at %s", e.Ref, e.Path)
}

// PathNotFoundError는 Git 트리에서 경로를 찾을 수 없음을 나타냅니다.
type PathNotFoundError struct {
	Path string
	Ref  string
	Repo string
}

// Error implements the error interface
func (e *PathNotFoundError) Error() string {
	return fmt.Sprintf("path %q not found at ref %q in repository %s", e.Path, e.Ref, e.Repo)
}

// InvalidRepositoryError는 디렉토리가 유효한 Git 저장소가 아님을 나타냅니다.
type InvalidRepositoryError struct {
	Path   string
	Reason string
}

// Error implements the error interface
func (e *InvalidRepositoryError) Error() string {
	return fmt.Sprintf("invalid repository at %s: %s", e.Path, e.Reason)
}
