// Package requests는 파일 조작 관련 HTTP 요청의 JSON 스키마를 정의합니다.
// Git 저장소의 파일 생성, 수정, 삭제 작업을 위한 데이터 구조체들을 포함합니다.
package requests

import "github.com/collabops/git-executor/internal/schemas/common"

// FileOperationRequest는 파일 조작 작업을 위한 JSON 페이로드를 나타냅니다.
// 파일 내용과 커밋 정보, 작성자 정보 등을 포함합니다.
type FileOperationRequest struct {
	Content   string               `json:"content" binding:"required"`
	Message   string               `json:"message" binding:"required"`
	Branch    string               `json:"branch,omitempty"`
	Author    *common.CommitAuthor `json:"author,omitempty"`
	Committer *common.CommitAuthor `json:"committer,omitempty"`
	Encoding  string               `json:"encoding,omitempty"`
	Metadata  map[string]string    `json:"metadata,omitempty"`
}
