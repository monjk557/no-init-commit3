package diffs

// gogit_compat.go contains go-git dependent utility functions
// that are still needed by other packages (e.g., merge/preview.go).
// These will be removed when those packages are also migrated to native git.

import (
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GetFileName은 change 객체에서 파일 이름을 추출합니다
func GetFileName(change *object.Change) string {
	if change.To.Name != "" {
		return change.To.Name
	}
	return change.From.Name
}
