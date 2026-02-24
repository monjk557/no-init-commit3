package branches

import (
	"fmt"
	"strings"
)

// validateBranchName은 Git 브랜치 명명 규칙에 따라 브랜치 이름을 검증합니다.
// 예약어, 잠재적 문제가 있는 문자 및 패턴을 검사합니다.
func (h *BranchOperationsHandler) validateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name cannot be empty")
	}

	// Check for reserved names (case-insensitive)
	reservedNames := []string{"HEAD", "ORIG_HEAD", "FETCH_HEAD", "MERGE_HEAD"}
	for _, reserved := range reservedNames {
		if strings.EqualFold(name, reserved) {
			return fmt.Errorf("branch name '%s' is reserved", name)
		}
	}

	// Git branch name restrictions
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("branch name cannot end with a dot")
	}

	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("branch name cannot start or end with a slash")
	}

	if strings.Contains(name, "//") {
		return fmt.Errorf("branch name cannot contain consecutive slashes")
	}

	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name cannot contain consecutive dots")
	}

	if strings.Contains(name, " ") {
		return fmt.Errorf("branch name cannot contain spaces")
	}

	// git check-ref-format: @{ sequence is forbidden
	if strings.Contains(name, "@{") {
		return fmt.Errorf("branch name cannot contain '@{' sequence")
	}

	// git check-ref-format: cannot be exactly "@"
	if name == "@" {
		return fmt.Errorf("branch name cannot be '@'")
	}

	// git check-ref-format: no slash-separated component may start with "." or end with ".lock"
	// applies to every component, not just the full name (e.g. "feature/.hidden", "feature.lock/fix")
	for _, component := range strings.Split(name, "/") {
		if component == "" {
			continue // leading/trailing/consecutive slashes already caught above
		}
		if strings.HasPrefix(component, ".") {
			return fmt.Errorf("branch name component cannot start with a dot")
		}
		if strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("branch name component cannot end with '.lock'")
		}
	}

	// Check for invalid characters using pattern if available (blocklist: control chars, ~, ^, :, ?, *, [, \)
	if h.branchNamePattern != nil && !h.branchNamePattern.MatchString(name) {
		return fmt.Errorf("branch name contains invalid characters")
	}

	return nil
}
