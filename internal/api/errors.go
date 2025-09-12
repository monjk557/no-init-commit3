// Package api는 Git REST API 작업에 대한 에러 처리 및 응답 프레임워크를 제공합니다.
// 이 패키지는 표준화된 에러 구조, 응답 형식 및 에러 매핑 유틸리티를 정의합니다.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrorCode는 Git API 작업에 대한 표준화된 에러 코드를 나타냅니다
type ErrorCode string

const (
	// 일반적인 에러 코드
	ErrorCodeInternalError    ErrorCode = "internal_error"
	ErrorCodeBadRequest       ErrorCode = "bad_request"
	ErrorCodeUnauthorized     ErrorCode = "unauthorized"
	ErrorCodeForbidden        ErrorCode = "forbidden"
	ErrorCodeNotFound         ErrorCode = "not_found"
	ErrorCodeConflict         ErrorCode = "conflict"
	ErrorCodeValidationFailed ErrorCode = "validation_failed"
	ErrorCodeRateLimited      ErrorCode = "rate_limited"

	// Git 전용 에러 코드
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

	// 권한 및 보안 에러 코드
	ErrorCodePermissionDenied   ErrorCode = "permission_denied"
	ErrorCodeInsufficientAccess ErrorCode = "insufficient_access"
	ErrorCodeSecurityViolation  ErrorCode = "security_violation"
)

// ErrorSeverity는 에러의 심각도 수준을 나타냅니다
type ErrorSeverity string

const (
	ErrorSeverityLow      ErrorSeverity = "low"
	ErrorSeverityMedium   ErrorSeverity = "medium"
	ErrorSeverityHigh     ErrorSeverity = "high"
	ErrorSeverityCritical ErrorSeverity = "critical"
)

// GitAPIError는 Git API 작업에 대한 표준화된 에러를 나타냅니다
type GitAPIError struct {
	// Code는 표준화된 에러 코드입니다
	Code ErrorCode `json:"code"`
	// Message는 사람이 읽을 수 있는 에러 메시지입니다
	Message string `json:"message"`
	// Details는 에러에 대한 추가 컨텍스트를 제공합니다
	Details map[string]interface{} `json:"details,omitempty"`
	// Severity는 에러의 심각도 수준을 나타냅니다
	Severity ErrorSeverity `json:"severity"`
	// Timestamp는 에러가 발생한 시간입니다
	Timestamp time.Time `json:"timestamp"`
	// TraceID는 서비스 간 상관관계를 위한 ID입니다
	TraceID string `json:"trace_id,omitempty"`
	// RequestID는 특정 요청을 추적하기 위한 ID입니다
	RequestID string `json:"request_id,omitempty"`
	// Operation은 에러를 발생시킨 작업입니다
	Operation string `json:"operation,omitempty"`
	// Resource는 접근되고 있던 리소스입니다
	Resource string `json:"resource,omitempty"`
	// Cause는 기저 에러입니다 (직렬화되지 않음)
	Cause error `json:"-"`
}

// Error는 error 인터페이스를 구현합니다
func (e *GitAPIError) Error() string {
	if e.Operation != "" && e.Resource != "" {
		return fmt.Sprintf("%s: %s (operation: %s, resource: %s)", e.Code, e.Message, e.Operation, e.Resource)
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s: %s (operation: %s)", e.Code, e.Message, e.Operation)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap은 에러 체인 호환성을 위해 기저 에러를 반환합니다
func (e *GitAPIError) Unwrap() error {
	return e.Cause
}

// Is는 에러 신원 확인을 구현합니다
func (e *GitAPIError) Is(target error) bool {
	var gitErr *GitAPIError
	if errors.As(target, &gitErr) {
		return e.Code == gitErr.Code
	}
	return false
}

// WithDetails는 에러에 추가 컨텍스트 세부 사항을 추가합니다
func (e *GitAPIError) WithDetails(key string, value interface{}) GitAPIErrorInterface {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithTrace는 상관관계를 위해 추적 ID와 요청 ID를 추가합니다
func (e *GitAPIError) WithTrace(traceID, requestID string) GitAPIErrorInterface {
	e.TraceID = traceID
	e.RequestID = requestID
	return e
}

// WithOperation은 작업 컨텍스트를 설정합니다
func (e *GitAPIError) WithOperation(operation string) GitAPIErrorInterface {
	e.Operation = operation
	return e
}

// WithResource는 리소스 컨텍스트를 설정합니다
func (e *GitAPIError) WithResource(resource string) GitAPIErrorInterface {
	e.Resource = resource
	return e
}

// WithCause는 기저 에러 원인을 설정합니다
func (e *GitAPIError) WithCause(cause error) GitAPIErrorInterface {
	e.Cause = cause
	return e
}

// HTTPStatusCode는 에러에 적절한 HTTP 상태 코드를 반환합니다
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

// GitAPIErrorInterface는 Git API 에러에 대한 인터페이스를 정의합니다
type GitAPIErrorInterface interface {
	error
	// GetCode는 에러 코드를 반환합니다
	GetCode() ErrorCode
	// GetMessage는 에러 메시지를 반환합니다
	GetMessage() string
	// GetDetails는 추가 에러 세부 사항을 반환합니다
	GetDetails() map[string]interface{}
	// GetSeverity는 에러 심각도를 반환합니다
	GetSeverity() ErrorSeverity
	// HTTPStatusCode는 적절한 HTTP 상태 코드를 반환합니다
	HTTPStatusCode() int
	// WithDetails는 추가 컨텍스트를 추가합니다
	WithDetails(key string, value interface{}) GitAPIErrorInterface
	// WithTrace는 상관관계 ID를 추가합니다
	WithTrace(traceID, requestID string) GitAPIErrorInterface
	// WithOperation은 작업 컨텍스트를 설정합니다
	WithOperation(operation string) GitAPIErrorInterface
	// WithResource는 리소스 컨텍스트를 설정합니다
	WithResource(resource string) GitAPIErrorInterface
	// WithCause는 기저 에러를 설정합니다
	WithCause(cause error) GitAPIErrorInterface
}

// GitAPIError가 GitAPIErrorInterface를 구현하는지 확인
var _ GitAPIErrorInterface = (*GitAPIError)(nil)

// 인터페이스 구현 메서드들
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

// NewGitAPIError는 지정된 코드와 메시지로 새로운 GitAPIError를 생성합니다
func NewGitAPIError(code ErrorCode, message string, severity ErrorSeverity) *GitAPIError {
	return &GitAPIError{
		Code:      code,
		Message:   message,
		Severity:  severity,
		Timestamp: time.Now(),
		Details:   make(map[string]interface{}),
	}
}

// 일반적인 시나리오를 위한 사전 정의된 에러 생성자들

// NewInternalError는 내부 서버 에러를 생성합니다
func NewInternalError(message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeInternalError, message, ErrorSeverityHigh)
}

// NewBadRequestError는 잘못된 요청 에러를 생성합니다
func NewBadRequestError(message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeBadRequest, message, ErrorSeverityLow)
}

// NewNotFoundError는 찾을 수 없음 에러를 생성합니다
func NewNotFoundError(resource, message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeNotFound, message, ErrorSeverityLow).
		WithResource(resource).(*GitAPIError)
}

// NewValidationError는 유효성 검사 에러를 생성합니다
func NewValidationError(message string) *GitAPIError {
	return NewGitAPIError(ErrorCodeValidationFailed, message, ErrorSeverityLow)
}

// NewPermissionError는 권한 거부 에러를 생성합니다
func NewPermissionError(operation, resource string) *GitAPIError {
	return NewGitAPIError(ErrorCodePermissionDenied,
		fmt.Sprintf("Permission denied for %s on %s", operation, resource),
		ErrorSeverityMedium).
		WithOperation(operation).
		WithResource(resource).(*GitAPIError)
}

// NewRepositoryNotFoundError는 저장소를 찾을 수 없음 에러를 생성합니다
func NewRepositoryNotFoundError(owner, repo string) *GitAPIError {
	return NewGitAPIError(ErrorCodeRepositoryNotFound,
		fmt.Sprintf("Repository %s/%s not found", owner, repo),
		ErrorSeverityLow).
		WithResource(fmt.Sprintf("%s/%s", owner, repo)).(*GitAPIError)
}

// NewBranchNotFoundError는 브랜치를 찾을 수 없음 에러를 생성합니다
func NewBranchNotFoundError(branch string) *GitAPIError {
	return NewGitAPIError(ErrorCodeBranchNotFound,
		fmt.Sprintf("Branch '%s' not found", branch),
		ErrorSeverityLow).
		WithResource(branch).(*GitAPIError)
}

// NewCommitNotFoundError는 커밋을 찾을 수 없음 에러를 생성합니다
func NewCommitNotFoundError(commit string) *GitAPIError {
	return NewGitAPIError(ErrorCodeCommitNotFound,
		fmt.Sprintf("Commit '%s' not found", commit),
		ErrorSeverityLow).
		WithResource(commit).(*GitAPIError)
}

// NewFileNotFoundError는 파일을 찾을 수 없음 에러를 생성합니다
func NewFileNotFoundError(path string) *GitAPIError {
	return NewGitAPIError(ErrorCodeFileNotFound,
		fmt.Sprintf("File '%s' not found", path),
		ErrorSeverityLow).
		WithResource(path).(*GitAPIError)
}

// NewMergeConflictError는 병합 충돌 에러를 생성합니다
func NewMergeConflictError(source, target string) *GitAPIError {
	return NewGitAPIError(ErrorCodeMergeConflict,
		fmt.Sprintf("Merge conflict between '%s' and '%s'", source, target),
		ErrorSeverityMedium).
		WithDetails("source", source).
		WithDetails("target", target).(*GitAPIError)
}

// NewPathTraversalError는 경로 탐색 보안 에러를 생성합니다
func NewPathTraversalError(path string) *GitAPIError {
	return NewGitAPIError(ErrorCodePathTraversal,
		fmt.Sprintf("Path traversal detected: %s", path),
		ErrorSeverityCritical).
		WithResource(path).(*GitAPIError)
}

// NewFileTooBigError는 파일 크기 초과 에러를 생성합니다
func NewFileTooBigError(path string, size, maxSize int64) *GitAPIError {
	return NewGitAPIError(ErrorCodeFileTooBig,
		fmt.Sprintf("File '%s' is too big (%d bytes), maximum allowed: %d bytes", path, size, maxSize),
		ErrorSeverityLow).
		WithResource(path).
		WithDetails("size", size).
		WithDetails("max_size", maxSize).(*GitAPIError)
}
