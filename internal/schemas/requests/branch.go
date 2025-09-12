// Package requests는 Git 브랜치 관련 HTTP 요청의 JSON 스키마를 정의합니다.
// Git 브랜치 생성 및 업데이트 작업을 위한 데이터 구조체들을 포함합니다.
package requests

// CreateBranchRequest는 브랜치 생성을 위한 JSON 페이로드를 나타냅니다.
// 새로운 Git 브랜치를 만들 때 필요한 모든 정보를 포함합니다.
type CreateBranchRequest struct {
	Name         string            `json:"name" binding:"required"`
	Source       string            `json:"source,omitempty"`        // for unit test compatibility
	SourceBranch string            `json:"source_branch,omitempty"` // for integration test compatibility
	Force        bool              `json:"force,omitempty"`         // force creation even if branch exists
	Checkout     bool              `json:"checkout,omitempty"`      // set as default branch (HEAD)
	AuthorName   string            `json:"author_name,omitempty"`   // author name (for compatibility)
	AuthorEmail  string            `json:"author_email,omitempty"`  // author email (for compatibility)
	Metadata     map[string]string `json:"metadata,omitempty"`      // additional metadata
}

// UpdateBranchRequest는 브랜치 업데이트를 위한 JSON 페이로드를 나타냅니다.
// 기존 브랜치의 포인터를 다른 커밋으로 변경할 때 사용됩니다.
type UpdateBranchRequest struct {
	Target  string `json:"target" binding:"required"` // commit SHA or branch name to point to
	Force   bool   `json:"force,omitempty"`           // force update even if not fast-forward
	Message string `json:"message,omitempty"`         // optional update message
}
