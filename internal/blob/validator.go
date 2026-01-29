package blob

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateBlobParams는 blob 요청 파라미터를 검증합니다.
func ValidateBlobParams(owner, repo, path, ref string) error {
	// 필수 파라미터 검증
	if owner == "" || repo == "" {
		return fmt.Errorf("missing required parameters: owner or repo")
	}

	if path == "" {
		return fmt.Errorf("missing required parameter: path")
	}

	if ref == "" {
		return fmt.Errorf("missing required parameter: ref")
	}

	// 저장소 식별자 검증
	if err := validateRepoIdentifier(owner); err != nil {
		return fmt.Errorf("invalid owner format: %w", err)
	}

	if err := validateRepoIdentifier(repo); err != nil {
		return fmt.Errorf("invalid repo format: %w", err)
	}

	// Git 참조 검증
	if err := validateGitRef(ref); err != nil {
		return fmt.Errorf("invalid ref format: %w", err)
	}

	// 파일 경로 검증
	if err := ValidateFilePath(path); err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	return nil
}

// validateGitRef는 Git 참조 형식을 검증합니다.
func validateGitRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref cannot be empty")
	}

	// 기본 검증: 영숫자, 하이픈, 언더스코어, 점, 슬래시 허용
	for _, char := range ref {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' && char != '.' && char != '/' {
			return fmt.Errorf("invalid character in ref: %c", char)
		}
	}

	return nil
}

// ValidateFilePath는 파일 경로를 검증하고 디렉토리 탐색 공격을 방지합니다.
func ValidateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// Null byte 및 개행(로그/헤더 injection) 차단 (디코딩 전 %0a 등도 차단)
	if strings.Contains(path, "\x00") || strings.Contains(path, "\n") || strings.Contains(path, "\r") ||
		strings.Contains(path, "%0a") || strings.Contains(path, "%0d") || strings.Contains(path, "%00") {
		return fmt.Errorf("path contains invalid control character")
	}

	// 디렉토리 탐색 시도 확인
	if strings.Contains(path, "..") {
		return fmt.Errorf("path cannot contain '..' (directory traversal)")
	}

	// 절대 경로 확인
	if filepath.IsAbs(path) {
		return fmt.Errorf("path cannot be absolute")
	}

	// 구분자로 시작하는지 확인
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return fmt.Errorf("path cannot start with separator")
	}

	// 정규화 및 검증
	normalized := filepath.Clean(path)
	if normalized != path && normalized != strings.ReplaceAll(path, "\\", "/") {
		return fmt.Errorf("path contains invalid characters or patterns")
	}

	return nil
}

// validateRepoIdentifier는 저장소 식별자가 유효한지 확인합니다.
func validateRepoIdentifier(identifier string) error {
	if identifier == "" {
		return fmt.Errorf("identifier cannot be empty")
	}

	// 영숫자, 하이픈, 언더스코어, 점만 허용
	for _, char := range identifier {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' && char != '.' {
			return fmt.Errorf("invalid character: %c", char)
		}
	}

	return nil
}
