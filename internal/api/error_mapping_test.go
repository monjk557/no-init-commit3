package api

import (
	"errors"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGitErrorMapper(t *testing.T) {
	mapper := NewGitErrorMapper()
	assert.NotNil(t, mapper)
}

func TestGetDefaultMapper(t *testing.T) {
	mapper := GetDefaultMapper()
	assert.NotNil(t, mapper)
}

func TestGitErrorMapper_MapError_NilError(t *testing.T) {
	mapper := NewGitErrorMapper()
	result := mapper.MapError(nil, "test_operation")
	assert.Nil(t, result)
}

func TestGitErrorMapper_MapGoGitErrors(t *testing.T) {
	mapper := NewGitErrorMapper()

	tests := []struct {
		name             string
		err              error
		expectedCode     ErrorCode
		expectedMessage  string
		expectedSeverity ErrorSeverity
	}{
		{
			name:             "ErrRepositoryNotExists",
			err:              git.ErrRepositoryNotExists,
			expectedCode:     ErrorCodeRepositoryNotFound,
			expectedMessage:  "Repository does not exist",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrRepositoryAlreadyExists",
			err:              git.ErrRepositoryAlreadyExists,
			expectedCode:     ErrorCodeConflict,
			expectedMessage:  "Repository already exists",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrRemoteNotFound",
			err:              git.ErrRemoteNotFound,
			expectedCode:     ErrorCodeNotFound,
			expectedMessage:  "Remote not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrBranchExists",
			err:              git.ErrBranchExists,
			expectedCode:     ErrorCodeBranchExists,
			expectedMessage:  "Branch already exists",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrBranchNotFound",
			err:              git.ErrBranchNotFound,
			expectedCode:     ErrorCodeBranchNotFound,
			expectedMessage:  "Branch not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrTagExists",
			err:              git.ErrTagExists,
			expectedCode:     ErrorCodeConflict,
			expectedMessage:  "Tag already exists",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrTagNotFound",
			err:              git.ErrTagNotFound,
			expectedCode:     ErrorCodeNotFound,
			expectedMessage:  "Tag not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrInvalidReference",
			err:              git.ErrInvalidReference,
			expectedCode:     ErrorCodeInvalidReference,
			expectedMessage:  "Invalid reference",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrForceNeeded",
			err:              git.ErrForceNeeded,
			expectedCode:     ErrorCodeConflict,
			expectedMessage:  "Force push required",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "ErrEmptyCommit",
			err:              git.ErrEmptyCommit,
			expectedCode:     ErrorCodeBadRequest,
			expectedMessage:  "Empty commit not allowed",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrWorktreeNotClean",
			err:              git.ErrWorktreeNotClean,
			expectedCode:     ErrorCodeConflict,
			expectedMessage:  "Working tree is not clean",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "ErrUnstagedChanges",
			err:              git.ErrUnstagedChanges,
			expectedCode:     ErrorCodeConflict,
			expectedMessage:  "Unstaged changes present",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "ErrNonFastForwardUpdate",
			err:              git.ErrNonFastForwardUpdate,
			expectedCode:     ErrorCodeConflict,
			expectedMessage:  "Non-fast-forward update",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "ErrDeleteRefNotSupported",
			err:              git.ErrDeleteRefNotSupported,
			expectedCode:     ErrorCodeOperationNotSupported,
			expectedMessage:  "Delete reference not supported",
			expectedSeverity: ErrorSeverityLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapper.MapError(tt.err, "test_operation")
			require.NotNil(t, result)

			assert.Equal(t, tt.expectedCode, result.Code)
			assert.Equal(t, tt.expectedMessage, result.Message)
			assert.Equal(t, tt.expectedSeverity, result.Severity)
			assert.Equal(t, "test_operation", result.Operation)
			assert.Equal(t, tt.err, result.Cause)
			assert.NotZero(t, result.Timestamp)
		})
	}
}

func TestGitErrorMapper_MapPlumbingErrors(t *testing.T) {
	mapper := NewGitErrorMapper()

	tests := []struct {
		name             string
		err              error
		expectedCode     ErrorCode
		expectedMessage  string
		expectedSeverity ErrorSeverity
	}{
		{
			name:             "ErrObjectNotFound",
			err:              plumbing.ErrObjectNotFound,
			expectedCode:     ErrorCodeNotFound,
			expectedMessage:  "Git object not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "ErrInvalidType",
			err:              plumbing.ErrInvalidType,
			expectedCode:     ErrorCodeBadRequest,
			expectedMessage:  "Invalid object type",
			expectedSeverity: ErrorSeverityLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapper.MapError(tt.err, "test_operation")
			require.NotNil(t, result)

			assert.Equal(t, tt.expectedCode, result.Code)
			assert.Equal(t, tt.expectedMessage, result.Message)
			assert.Equal(t, tt.expectedSeverity, result.Severity)
			assert.Equal(t, "test_operation", result.Operation)
			assert.Equal(t, tt.err, result.Cause)
		})
	}
}

func TestGitErrorMapper_MapObjectErrors(t *testing.T) {
	mapper := NewGitErrorMapper()

	tests := []struct {
		name             string
		err              error
		expectedCode     ErrorCode
		expectedMessage  string
		expectedSeverity ErrorSeverity
	}{
		{
			name:             "ErrFileNotFound",
			err:              object.ErrFileNotFound,
			expectedCode:     ErrorCodeFileNotFound,
			expectedMessage:  "File not found in tree",
			expectedSeverity: ErrorSeverityLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapper.MapError(tt.err, "test_operation")
			require.NotNil(t, result)

			assert.Equal(t, tt.expectedCode, result.Code)
			assert.Equal(t, tt.expectedMessage, result.Message)
			assert.Equal(t, tt.expectedSeverity, result.Severity)
			assert.Equal(t, "test_operation", result.Operation)
			assert.Equal(t, tt.err, result.Cause)
		})
	}
}

func TestGitErrorMapper_MapByErrorMessage(t *testing.T) {
	mapper := NewGitErrorMapper()

	tests := []struct {
		name             string
		errorMessage     string
		expectedCode     ErrorCode
		expectedMessage  string
		expectedSeverity ErrorSeverity
	}{
		{
			name:             "Repository not found message",
			errorMessage:     "repository not found",
			expectedCode:     ErrorCodeRepositoryNotFound,
			expectedMessage:  "Repository not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "No such file or directory",
			errorMessage:     "no such file or directory",
			expectedCode:     ErrorCodeRepositoryNotFound,
			expectedMessage:  "Repository not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Reference not found",
			errorMessage:     "reference not found",
			expectedCode:     ErrorCodeReferenceNotFound,
			expectedMessage:  "Reference not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Ref not found",
			errorMessage:     "ref not found",
			expectedCode:     ErrorCodeReferenceNotFound,
			expectedMessage:  "Reference not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Branch not found",
			errorMessage:     "branch master not found",
			expectedCode:     ErrorCodeBranchNotFound,
			expectedMessage:  "Branch not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Commit not found",
			errorMessage:     "commit abc123 not found",
			expectedCode:     ErrorCodeCommitNotFound,
			expectedMessage:  "Commit not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "File not found",
			errorMessage:     "file not found",
			expectedCode:     ErrorCodeFileNotFound,
			expectedMessage:  "File not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Path not found",
			errorMessage:     "path not found",
			expectedCode:     ErrorCodeFileNotFound,
			expectedMessage:  "File not found",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Permission denied",
			errorMessage:     "permission denied",
			expectedCode:     ErrorCodePermissionDenied,
			expectedMessage:  "Permission denied",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "Access denied",
			errorMessage:     "access denied",
			expectedCode:     ErrorCodePermissionDenied,
			expectedMessage:  "Permission denied",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "Path traversal",
			errorMessage:     "path traversal detected",
			expectedCode:     ErrorCodePathTraversal,
			expectedMessage:  "Invalid or unsafe path",
			expectedSeverity: ErrorSeverityCritical,
		},
		{
			name:             "Invalid path",
			errorMessage:     "invalid path specified",
			expectedCode:     ErrorCodePathTraversal,
			expectedMessage:  "Invalid or unsafe path",
			expectedSeverity: ErrorSeverityCritical,
		},
		{
			name:             "Merge conflict",
			errorMessage:     "merge conflict detected",
			expectedCode:     ErrorCodeMergeConflict,
			expectedMessage:  "Merge conflict detected",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "General conflict",
			errorMessage:     "conflict in file",
			expectedCode:     ErrorCodeMergeConflict,
			expectedMessage:  "Merge conflict detected",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "Repository corrupt",
			errorMessage:     "repository is corrupt",
			expectedCode:     ErrorCodeRepositoryCorrupted,
			expectedMessage:  "Repository corruption detected",
			expectedSeverity: ErrorSeverityHigh,
		},
		{
			name:             "Integrity check failed",
			errorMessage:     "integrity check failed",
			expectedCode:     ErrorCodeRepositoryCorrupted,
			expectedMessage:  "Repository corruption detected",
			expectedSeverity: ErrorSeverityHigh,
		},
		{
			name:             "Checksum mismatch",
			errorMessage:     "checksum mismatch",
			expectedCode:     ErrorCodeRepositoryCorrupted,
			expectedMessage:  "Repository corruption detected",
			expectedSeverity: ErrorSeverityHigh,
		},
		{
			name:             "File too large",
			errorMessage:     "file is too large",
			expectedCode:     ErrorCodeFileTooBig,
			expectedMessage:  "File or object too large",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "File too big",
			errorMessage:     "file too big",
			expectedCode:     ErrorCodeFileTooBig,
			expectedMessage:  "File or object too large",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Size limit exceeded",
			errorMessage:     "size limit exceeded",
			expectedCode:     ErrorCodeFileTooBig,
			expectedMessage:  "File or object too large",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Invalid format",
			errorMessage:     "invalid format detected",
			expectedCode:     ErrorCodeBadRequest,
			expectedMessage:  "Invalid format or content",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Malformed data",
			errorMessage:     "malformed data",
			expectedCode:     ErrorCodeBadRequest,
			expectedMessage:  "Invalid format or content",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Bad format",
			errorMessage:     "bad format",
			expectedCode:     ErrorCodeBadRequest,
			expectedMessage:  "Invalid format or content",
			expectedSeverity: ErrorSeverityLow,
		},
		{
			name:             "Connection timeout",
			errorMessage:     "connection timeout",
			expectedCode:     ErrorCodeInternalError,
			expectedMessage:  "Network or timeout error",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "Network timeout",
			errorMessage:     "timeout occurred",
			expectedCode:     ErrorCodeInternalError,
			expectedMessage:  "Network or timeout error",
			expectedSeverity: ErrorSeverityMedium,
		},
		{
			name:             "Connection error",
			errorMessage:     "connection failed",
			expectedCode:     ErrorCodeInternalError,
			expectedMessage:  "Network or timeout error",
			expectedSeverity: ErrorSeverityMedium,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testErr := errors.New(tt.errorMessage)
			result := mapper.MapError(testErr, "test_operation")
			require.NotNil(t, result)

			assert.Equal(t, tt.expectedCode, result.Code)
			assert.Equal(t, tt.expectedMessage, result.Message)
			assert.Equal(t, tt.expectedSeverity, result.Severity)
			assert.Equal(t, "test_operation", result.Operation)
			assert.Equal(t, testErr, result.Cause)
		})
	}
}

func TestGitErrorMapper_MapError_UnknownError(t *testing.T) {
	mapper := NewGitErrorMapper()
	unknownErr := errors.New("some completely unknown error")

	result := mapper.MapError(unknownErr, "test_operation")
	require.NotNil(t, result)

	assert.Equal(t, ErrorCodeInternalError, result.Code)
	assert.Contains(t, result.Message, "Unexpected error")
	assert.Contains(t, result.Message, unknownErr.Error())
	assert.Equal(t, ErrorSeverityHigh, result.Severity)
	assert.Equal(t, "test_operation", result.Operation)
	assert.Equal(t, unknownErr, result.Cause)
}

func TestGitErrorMapper_MapErrorWithContext(t *testing.T) {
	mapper := NewGitErrorMapper()
	testErr := git.ErrRepositoryNotExists

	result := mapper.MapErrorWithContext(testErr, "test_operation", "test_resource")
	require.NotNil(t, result)

	assert.Equal(t, ErrorCodeRepositoryNotFound, result.Code)
	assert.Equal(t, "test_operation", result.Operation)
	assert.Equal(t, "test_resource", result.Resource)
	assert.Equal(t, testErr, result.Cause)
}

func TestGitErrorMapper_MapErrorWithContext_NilError(t *testing.T) {
	mapper := NewGitErrorMapper()

	result := mapper.MapErrorWithContext(nil, "test_operation", "test_resource")
	assert.Nil(t, result)
}

func TestGitErrorMapper_MapErrorWithContext_EmptyResource(t *testing.T) {
	mapper := NewGitErrorMapper()
	testErr := git.ErrBranchNotFound

	result := mapper.MapErrorWithContext(testErr, "test_operation", "")
	require.NotNil(t, result)

	assert.Equal(t, ErrorCodeBranchNotFound, result.Code)
	assert.Equal(t, "test_operation", result.Operation)
	assert.Empty(t, result.Resource) // Should remain empty
}

func TestGitErrorMapper_MapErrorWithDetails(t *testing.T) {
	mapper := NewGitErrorMapper()
	testErr := git.ErrBranchExists
	details := map[string]interface{}{
		"branch_name": "main",
		"commit_hash": "abc123",
		"retry_count": 3,
	}

	result := mapper.MapErrorWithDetails(testErr, "test_operation", details)
	require.NotNil(t, result)

	assert.Equal(t, ErrorCodeBranchExists, result.Code)
	assert.Equal(t, "test_operation", result.Operation)
	assert.Equal(t, testErr, result.Cause)

	// Check that all details were added
	require.NotNil(t, result.Details)
	assert.Equal(t, "main", result.Details["branch_name"])
	assert.Equal(t, "abc123", result.Details["commit_hash"])
	assert.Equal(t, 3, result.Details["retry_count"])
}

func TestGitErrorMapper_MapErrorWithDetails_NilError(t *testing.T) {
	mapper := NewGitErrorMapper()
	details := map[string]interface{}{
		"key": "value",
	}

	result := mapper.MapErrorWithDetails(nil, "test_operation", details)
	assert.Nil(t, result)
}

func TestGitErrorMapper_MapErrorWithDetails_EmptyDetails(t *testing.T) {
	mapper := NewGitErrorMapper()
	testErr := git.ErrTagNotFound

	result := mapper.MapErrorWithDetails(testErr, "test_operation", map[string]interface{}{})
	require.NotNil(t, result)

	assert.Equal(t, ErrorCodeNotFound, result.Code)
	assert.Equal(t, "test_operation", result.Operation)
	assert.NotNil(t, result.Details) // Should be initialized but empty
	assert.Empty(t, result.Details)
}

func TestGitErrorMapper_CaseInsensitiveMatching(t *testing.T) {
	mapper := NewGitErrorMapper()

	tests := []struct {
		name         string
		errorMessage string
		expectedCode ErrorCode
	}{
		{
			name:         "Uppercase repository not found",
			errorMessage: "REPOSITORY NOT FOUND",
			expectedCode: ErrorCodeRepositoryNotFound,
		},
		{
			name:         "Mixed case permission denied",
			errorMessage: "Permission DENIED for user",
			expectedCode: ErrorCodePermissionDenied,
		},
		{
			name:         "Uppercase merge conflict",
			errorMessage: "MERGE CONFLICT in file.txt",
			expectedCode: ErrorCodeMergeConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testErr := errors.New(tt.errorMessage)
			result := mapper.MapError(testErr, "test_operation")
			require.NotNil(t, result)

			assert.Equal(t, tt.expectedCode, result.Code)
		})
	}
}

func TestGitErrorMapper_ErrorMessagePriority(t *testing.T) {
	mapper := NewGitErrorMapper()

	// Test that specific go-git errors take priority over message matching
	result := mapper.MapError(git.ErrRepositoryNotExists, "test_operation")
	require.NotNil(t, result)

	assert.Equal(t, ErrorCodeRepositoryNotFound, result.Code)
	assert.Equal(t, "Repository does not exist", result.Message)
	// Should have the specific message, not the generic "Repository not found" from message matching
}

func TestGitErrorMapper_CompleteFlow(t *testing.T) {
	mapper := NewGitErrorMapper()
	testErr := git.ErrForceNeeded

	// Test complete flow with all context
	result := mapper.MapErrorWithContext(testErr, "push_operation", "origin/main")
	require.NotNil(t, result)

	// Add details
	result = mapper.MapErrorWithDetails(testErr, "push_operation", map[string]interface{}{
		"local_commit":  "abc123",
		"remote_commit": "def456",
	})
	require.NotNil(t, result)

	// Verify all properties
	assert.Equal(t, ErrorCodeConflict, result.Code)
	assert.Equal(t, "Force push required", result.Message)
	assert.Equal(t, ErrorSeverityMedium, result.Severity)
	assert.Equal(t, "push_operation", result.Operation)
	assert.Equal(t, testErr, result.Cause)
	assert.NotZero(t, result.Timestamp)

	// Verify details
	require.NotNil(t, result.Details)
	assert.Equal(t, "abc123", result.Details["local_commit"])
	assert.Equal(t, "def456", result.Details["remote_commit"])
}
