package commits

import (
	"fmt"
	"strings"
)

// validateMultiFileOperation validates a single file operation
func validateMultiFileOperation(op *MultiFileOperation) error {
	// Validate operation type
	switch op.Operation {
	case OperationCreate, OperationUpdate, OperationDelete, OperationMove:
		// Valid operations
	default:
		return fmt.Errorf("invalid operation type: %s (must be create, update, delete, or move)", op.Operation)
	}

	// Validate path
	if err := ValidateFilePath(op.Path); err != nil {
		return fmt.Errorf("invalid file path: %w", err)
	}

	// operation 별 추가 검증. create/update 는 검증할 것이 없어 case 가 없다 —
	// content 를 강제하지 않기 때문이다. 빈 파일 생성(create)과 기존 파일을 빈
	// 파일로 만드는 것(update)은 둘 다 정상 동작이고, GitHub
	// PUT /repos/{o}/{r}/contents/{path} 가 content:"" 로 지원하는 것과 같다.
	// 하위 blob 경로도 빈 내용을 이미 처리한다 (writeBlob 의 `data 0`,
	// nativeHashObject 의 빈 stdin → git 빈 blob e69de29...).
	switch op.Operation {
	case OperationMove:
		// original_path 를 쓰는 operation 은 move 뿐이라 세 검사가 여기에만 있다.
		// 특히 세 번째는 보안 가드다 — switch 앞에서 검증하는 것은 op.Path 뿐이고
		// op.OriginalPath 는 여기가 유일한 검문소다. 빼면 "../../etc/passwd" 같은
		// 값이 traversal/절대경로 가드를 통과하지 않은 채 하위로 내려간다.
		if op.OriginalPath == "" {
			return fmt.Errorf("original_path is required for move operations")
		}
		// 자기 자신으로의 이동은 무의미한 no-op 이므로 조용히 통과시키지 않는다.
		if op.OriginalPath == op.Path {
			return fmt.Errorf("original_path and path cannot be the same for move operations")
		}
		if err := ValidateFilePath(op.OriginalPath); err != nil {
			return fmt.Errorf("invalid original_path: %w", err)
		}
		// content 는 선택이다 — 있으면 새 내용으로 이동(rename + 내용 변경),
		// 없으면 원본 blob 을 그대로 옮긴다 (fast_import.go 는 전자를
		// 'D 원본 + 새 blob', 후자를 'R 원본 대상' 으로 처리한다).
	case OperationDelete:
		// content 가 실린 delete 는 모순된 요청이다 — update 를 보내려다
		// operation 을 잘못 넣은 경우가 대부분이다. delete 경로는 content 를
		// 읽지 않으므로(fast_import.go 는 path 만 deletePaths 에 넣는다)
		// 조용히 무시할 수도 있지만, 그러면 "이 내용을 써라" 가 "이 파일을
		// 없애라" 로 실행된다. 유일한 파괴적 operation 이라 모순 입력을
		// 무시하지 않고 거부한다.
		//
		// content:"" 는 통과시킨다 — 빈 문자열은 아무것도 주장하지 않으므로
		// 필드를 항상 채워 보내는 클라이언트를 막을 이유가 없다.
		if op.Content != "" {
			return fmt.Errorf("content should not be provided for delete operations")
		}
	}

	// Validate encoding
	if op.Encoding != "" && op.Encoding != "utf-8" && op.Encoding != "base64" {
		return fmt.Errorf("invalid encoding: %s (must be utf-8 or base64)", op.Encoding)
	}

	return nil
}

// ValidateFilePath는 추가 검사를 통해 Git API 작업의 파일 경로를 검증합니다.
func ValidateFilePath(filePath string) error {
	// Basic validation first
	if err := validateBasicPath(filePath); err != nil {
		return err
	}

	// Git 특정 검증 (추가 검사 없이 기본 검증만 수행)
	return nil
}

// validateBasicPath performs basic path validation
func validateBasicPath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	// Length check
	if len(path) > 4096 {
		return fmt.Errorf("file path too long (max 4096 characters)")
	}

	// Path traversal protection
	if strings.Contains(path, "..") {
		return fmt.Errorf("file path contains path traversal attempt")
	}

	// Absolute path protection
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("file path cannot be absolute")
	}

	// Windows path separator protection
	if strings.Contains(path, "\\") {
		return fmt.Errorf("file path cannot contain backslashes")
	}

	return nil
}
