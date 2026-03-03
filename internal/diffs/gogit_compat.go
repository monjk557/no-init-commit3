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

// GetFileMetadata는 파일 변경에 대한 메타데이터를 추출합니다
func GetFileMetadata(change *object.Change) map[string]interface{} {
	metadata := make(map[string]interface{})

	// 파일 모드 정보 추출
	if change.From.TreeEntry.Mode != 0 {
		metadata["old_mode"] = change.From.TreeEntry.Mode.String()
	}
	if change.To.TreeEntry.Mode != 0 {
		metadata["new_mode"] = change.To.TreeEntry.Mode.String()
	}

	// 파일 해시 정보 추출
	if !change.From.TreeEntry.Hash.IsZero() {
		metadata["old_hash"] = change.From.TreeEntry.Hash.String()
	}
	if !change.To.TreeEntry.Hash.IsZero() {
		metadata["new_hash"] = change.To.TreeEntry.Hash.String()
	}

	return metadata
}
