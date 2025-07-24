package git

import (
	"errors"
	"fmt"
	"testing"
)

func TestGitError(t *testing.T) {
	underlyingErr := errors.New("underlying error")

	tests := []struct {
		name          string
		operation     string
		path          string
		ref           string
		message       string
		err           error
		expectedError string
	}{
		{
			name:          "Error with ref",
			operation:     "clone",
			path:          "/test/repo",
			ref:           "main",
			message:       "failed to clone repository",
			err:           underlyingErr,
			expectedError: `git clone failed for path "/test/repo" at ref "main": failed to clone repository`,
		},
		{
			name:          "Error without ref",
			operation:     "open",
			path:          "/test/repo",
			ref:           "",
			message:       "repository not found",
			err:           underlyingErr,
			expectedError: `git open failed for path "/test/repo": repository not found`,
		},
		{
			name:          "Error with empty message",
			operation:     "read",
			path:          "/test/repo",
			ref:           "main",
			message:       "",
			err:           nil,
			expectedError: `git read failed for path "/test/repo" at ref "main": `,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitErr := NewGitError(tt.operation, tt.path, tt.ref, tt.message, tt.err)

			// Test Error method
			if gitErr.Error() != tt.expectedError {
				t.Errorf("Expected error %q, got %q", tt.expectedError, gitErr.Error())
			}

			// Test Unwrap method
			if gitErr.Unwrap() != tt.err {
				t.Errorf("Expected underlying error %v, got %v", tt.err, gitErr.Unwrap())
			}

			// Test fields
			if gitErr.Operation != tt.operation {
				t.Errorf("Expected operation %q, got %q", tt.operation, gitErr.Operation)
			}
			if gitErr.Path != tt.path {
				t.Errorf("Expected path %q, got %q", tt.path, gitErr.Path)
			}
			if gitErr.Ref != tt.ref {
				t.Errorf("Expected ref %q, got %q", tt.ref, gitErr.Ref)
			}
			if gitErr.Message != tt.message {
				t.Errorf("Expected message %q, got %q", tt.message, gitErr.Message)
			}
		})
	}
}

func TestRepositoryNotFoundError(t *testing.T) {
	path := "/test/non-existent"
	err := &RepositoryNotFoundError{Path: path}

	expectedError := fmt.Sprintf("repository not found at path: %s", path)
	if err.Error() != expectedError {
		t.Errorf("Expected error %q, got %q", expectedError, err.Error())
	}

	if err.Path != path {
		t.Errorf("Expected path %q, got %q", path, err.Path)
	}
}

func TestReferenceNotFoundError(t *testing.T) {
	ref := "feature/branch"
	path := "/test/repo"
	err := &ReferenceNotFoundError{Ref: ref, Path: path}

	expectedError := fmt.Sprintf("reference %q not found in repository at %s", ref, path)
	if err.Error() != expectedError {
		t.Errorf("Expected error %q, got %q", expectedError, err.Error())
	}

	if err.Ref != ref {
		t.Errorf("Expected ref %q, got %q", ref, err.Ref)
	}
	if err.Path != path {
		t.Errorf("Expected path %q, got %q", path, err.Path)
	}
}

func TestPathNotFoundError(t *testing.T) {
	path := "src/main.go"
	ref := "main"
	repo := "/test/repo"
	err := &PathNotFoundError{Path: path, Ref: ref, Repo: repo}

	expectedError := fmt.Sprintf("path %q not found at ref %q in repository %s", path, ref, repo)
	if err.Error() != expectedError {
		t.Errorf("Expected error %q, got %q", expectedError, err.Error())
	}

	if err.Path != path {
		t.Errorf("Expected path %q, got %q", path, err.Path)
	}
	if err.Ref != ref {
		t.Errorf("Expected ref %q, got %q", ref, err.Ref)
	}
	if err.Repo != repo {
		t.Errorf("Expected repo %q, got %q", repo, err.Repo)
	}
}

func TestInvalidRepositoryError(t *testing.T) {
	path := "/test/invalid"
	reason := "not a git repository"
	err := &InvalidRepositoryError{Path: path, Reason: reason}

	expectedError := fmt.Sprintf("invalid repository at %s: %s", path, reason)
	if err.Error() != expectedError {
		t.Errorf("Expected error %q, got %q", expectedError, err.Error())
	}

	if err.Path != path {
		t.Errorf("Expected path %q, got %q", path, err.Path)
	}
	if err.Reason != reason {
		t.Errorf("Expected reason %q, got %q", reason, err.Reason)
	}
}

func TestErrorImplementsError(t *testing.T) {
	// Test that all error types implement the error interface
	var _ error = (*GitError)(nil)
	var _ error = (*RepositoryNotFoundError)(nil)
	var _ error = (*ReferenceNotFoundError)(nil)
	var _ error = (*PathNotFoundError)(nil)
	var _ error = (*InvalidRepositoryError)(nil)
}

func TestGitError_IsUnwrapCompatible(t *testing.T) {
	underlyingErr := errors.New("test error")
	gitErr := NewGitError("test", "/path", "ref", "message", underlyingErr)

	// Test that errors.Is works with GitError
	if !errors.Is(gitErr, underlyingErr) {
		t.Error("Expected errors.Is to find underlying error")
	}

	// Test that errors.Unwrap works
	unwrapped := errors.Unwrap(gitErr)
	if unwrapped != underlyingErr {
		t.Errorf("Expected unwrapped error to be %v, got %v", underlyingErr, unwrapped)
	}
}
