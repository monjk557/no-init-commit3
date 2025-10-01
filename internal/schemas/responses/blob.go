// Package responses는 Git blob(파일 내용) 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// Git 저장소에서 파일 내용을 조회한 결과를 반환하는 데이터 구조체를 포함합니다.
package responses

import "github.com/collabops/git-executor/internal/git"

// BlobResponse는 blob(파일 내용) 요청에 대한 응답을 나타냅니다.
// 성공 여부와 파일의 내용을 포함합니다.
type BlobResponse struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
}

// BlobInfoResponse는 blob의 상세 정보 요청에 대한 응답을 나타냅니다.
// 파일 내용, 크기, 라인 수 등의 메타데이터를 포함합니다.
type BlobInfoResponse struct {
	Success   bool   `json:"success"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	LineCount int    `json:"line_count"`
	Hash      string `json:"hash"`
	Mode      string `json:"mode"`
	Path      string `json:"path"`
}

type BlobDetailResponse struct {
	Success       bool            `json:"success"`
	Content       string          `json:"content"`
	Lines         int             `json:"lines"`
	Size          int64           `json:"size"`
	Tree          []git.TreeEntry `json:"tree"`
	DefaultBranch string          `json:"default_branch"`
	Branches      []git.Branch    `json:"branches"`
	LatestCommit  git.Commit      `json:"latest_commit"`
	Commits       []git.Commit    `json:"commits"`
}
