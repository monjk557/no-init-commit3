// Package api는 go-git 에러를 표준화된 API 에러로 변환하는 에러 매핑 기능을 제공합니다.
// 이 파일은 다양한 go-git 및 관련 라이브러리 에러를 GitAPIError 인스턴스로 매핑하는 역할을 담당합니다.
package api

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GitErrorMapper는 go-git 에러를 API 에러로 매핑하는 기능을 제공합니다
type GitErrorMapper struct{}

// NewGitErrorMapper는 새로운 Git 에러 매퍼 인스턴스를 생성합니다
func NewGitErrorMapper() *GitErrorMapper {
	return &GitErrorMapper{}
}

// MapError는 go-git 에러를 GitAPIError로 변환합니다
func (m *GitErrorMapper) MapError(err error, operation string) *GitAPIError {
	if err == nil {
		return nil
	}

	var apiErr *GitAPIError

	// 특정 go-git 에러를 먼저 매핑 시도
	if apiErr = m.mapGoGitErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// plumbing 에러 매핑 시도
	if apiErr = m.mapPlumbingErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// object 에러 매핑 시도
	if apiErr = m.mapObjectErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// 파일시스템 에러 매핑 시도
	if apiErr = m.mapFilesystemErrors(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// 에러 메시지 패턴으로 매핑 시도
	if apiErr = m.mapByErrorMessage(err); apiErr != nil {
		apiErr.Operation = operation
		return apiErr
	}

	// 일반적인 내부 에러로 폴백
	apiErr = NewInternalError(fmt.Sprintf("Unexpected error: %v", err))
	apiErr.Operation = operation
	apiErr.Cause = err
	return apiErr
}

// mapGoGitErrors는 메인 git 패키지의 에러를 매핑합니다
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

// mapPlumbingErrors는 plumbing 패키지의 에러를 매핑합니다
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

// mapObjectErrors는 object 패키지의 에러를 매핑합니다
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

// mapFilesystemErrors는 파일시스템 관련 에러를 매핑합니다
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

// mapByErrorMessage는 에러 메시지 패턴을 기반으로 에러를 매핑합니다
func (m *GitErrorMapper) mapByErrorMessage(err error) *GitAPIError {
	errMsg := err.Error()
	lowerMsg := strings.ToLower(errMsg)

	var apiErr *GitAPIError

	// 저장소 관련 에러
	if strings.Contains(lowerMsg, "repository not found") || strings.Contains(lowerMsg, "no such file or directory") || strings.Contains(lowerMsg, "repository does not exist") {
		apiErr = NewGitAPIError(ErrorCodeRepositoryNotFound, "Repository not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// 레퍼런스 관련 에러
	if strings.Contains(lowerMsg, "reference not found") || strings.Contains(lowerMsg, "ref not found") {
		apiErr = NewGitAPIError(ErrorCodeReferenceNotFound, "Reference not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// 브랜치 관련 에러
	if strings.Contains(lowerMsg, "branch") && strings.Contains(lowerMsg, "not found") {
		apiErr = NewGitAPIError(ErrorCodeBranchNotFound, "Branch not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// 커밋 관련 에러
	if strings.Contains(lowerMsg, "commit") && strings.Contains(lowerMsg, "not found") {
		apiErr = NewGitAPIError(ErrorCodeCommitNotFound, "Commit not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// 파일 관련 에러
	if strings.Contains(lowerMsg, "file not found") || strings.Contains(lowerMsg, "path not found") {
		apiErr = NewGitAPIError(ErrorCodeFileNotFound, "File not found", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// 권한 관련 에러
	if strings.Contains(lowerMsg, "permission denied") || strings.Contains(lowerMsg, "access denied") {
		apiErr = NewGitAPIError(ErrorCodePermissionDenied, "Permission denied", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	}

	// 경로 탐색 및 보안 관련 에러
	if strings.Contains(lowerMsg, "path traversal") || strings.Contains(lowerMsg, "invalid path") {
		apiErr = NewGitAPIError(ErrorCodePathTraversal, "Invalid or unsafe path", ErrorSeverityCritical)
		apiErr.Cause = err
		return apiErr
	}

	// 병합 충돌
	if strings.Contains(lowerMsg, "merge conflict") || strings.Contains(lowerMsg, "conflict") {
		apiErr = NewGitAPIError(ErrorCodeMergeConflict, "Merge conflict detected", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	}

	// 손상 및 무결성 관련 에러
	if strings.Contains(lowerMsg, "corrupt") || strings.Contains(lowerMsg, "integrity") ||
		strings.Contains(lowerMsg, "checksum") {
		apiErr = NewGitAPIError(ErrorCodeRepositoryCorrupted, "Repository corruption detected", ErrorSeverityHigh)
		apiErr.Cause = err
		return apiErr
	}

	// 크기 및 제한 관련 에러
	if strings.Contains(lowerMsg, "too large") || strings.Contains(lowerMsg, "too big") ||
		strings.Contains(lowerMsg, "size limit") {
		apiErr = NewGitAPIError(ErrorCodeFileTooBig, "File or object too large", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// 잘못된 형식 또는 내용
	if strings.Contains(lowerMsg, "invalid format") || strings.Contains(lowerMsg, "malformed") ||
		strings.Contains(lowerMsg, "bad format") {
		apiErr = NewGitAPIError(ErrorCodeBadRequest, "Invalid format or content", ErrorSeverityLow)
		apiErr.Cause = err
		return apiErr
	}

	// 네트워크 및 타임아웃 에러
	if strings.Contains(lowerMsg, "timeout") || strings.Contains(lowerMsg, "connection") {
		apiErr = NewGitAPIError(ErrorCodeInternalError, "Network or timeout error", ErrorSeverityMedium)
		apiErr.Cause = err
		return apiErr
	}

	return nil // No pattern matched
}

// MapErrorWithContext는 추가 컨텍스트를 포함하는 편의 메서드입니다
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

// MapErrorWithDetails는 추가 세부 사항을 포함하는 편의 메서드입니다
func (m *GitErrorMapper) MapErrorWithDetails(err error, operation string, details map[string]interface{}) *GitAPIError {
	if err == nil {
		return nil
	}

	apiErr := m.MapError(err, operation)

	// 각 세부 사항 추가
	if apiErr.Details == nil {
		apiErr.Details = make(map[string]interface{})
	}
	for key, value := range details {
		apiErr.Details[key] = value
	}

	return apiErr
}

// GetDefaultMapper는 GitErrorMapper의 기본 인스턴스를 반환합니다
func GetDefaultMapper() *GitErrorMapper {
	return &GitErrorMapper{}
}
