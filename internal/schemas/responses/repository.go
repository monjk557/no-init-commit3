// Package responses는 Git 저장소 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// 새 저장소 생성 작업의 결과를 반환하는 데이터 구조체들을 포함합니다.
package responses

import "time"

// CreateRepositoryResponse는 저장소 생성 응답을 나타냅니다.
// 생성된 저장소의 상세 정보와 메타데이터를 포함합니다.
type CreateRepositoryResponse struct {
	Success       bool                   `json:"success"`
	Repository    string                 `json:"repository"`
	Owner         string                 `json:"owner"`
	Name          string                 `json:"name"`
	Path          string                 `json:"path,omitempty"`
	Description   string                 `json:"description"`
	DefaultBranch string                 `json:"default_branch"`
	InitialCommit string                 `json:"initial_commit,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}
