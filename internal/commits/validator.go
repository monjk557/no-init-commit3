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

	// Validate move operation
	if op.Operation == OperationMove {
		if op.OriginalPath == "" {
			return fmt.Errorf("original_path is required for move operations")
		}
		if op.OriginalPath == op.Path {
			return fmt.Errorf("original_path and path cannot be the same for move operations")
		}
		if err := ValidateFilePath(op.OriginalPath); err != nil {
			return fmt.Errorf("invalid original_path: %w", err)
		}
		// Move operation can optionally have content (for rename with content change)
		// If content is provided, it will be used; otherwise, original file content will be used
	} else if op.Operation == OperationDelete {
		// Validate that delete operations should not have content
		if op.Content != "" {
			return fmt.Errorf("content should not be provided for delete operations")
		}
	} else if op.Operation == OperationCreate || op.Operation == OperationUpdate {
		// Validate content requirements for create/update
		if op.Content == "" {
			return fmt.Errorf("content is required for %s operations", op.Operation)
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
