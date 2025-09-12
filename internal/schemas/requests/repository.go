// Package requests는 Git 저장소 관련 HTTP 요청의 JSON 스키마를 정의합니다.
// 새로운 Git 저장소 생성을 위한 데이터 구조체들을 포함합니다.
package requests

import "github.com/collabops/git-executor/internal/schemas/common"

// CreateRepositoryRequest는 저장소 생성을 위한 JSON 페이로드를 나타냅니다.
// 저장소 설명, 기본 브랜치, 초기 파일 및 메타데이터를 포함합니다.
type CreateRepositoryRequest struct {
	Description   string                 `json:"description,omitempty"`
	Private       bool                   `json:"private,omitempty"`
	InitialCommit bool                   `json:"initial_commit,omitempty"`
	DefaultBranch string                 `json:"default_branch,omitempty"`
	Author        *common.CommitAuthor   `json:"author,omitempty"`
	InitialFiles  map[string]string      `json:"initial_files,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}
