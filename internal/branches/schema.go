// Package responses는 Git 브랜치 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// 브랜치 생성, 업데이트 등의 작업 결과를 반환하는 데이터 구조체들을 포함합니다.
package branches

import (
	"time"
)

// CreateBranchResponse는 브랜치 생성 응답을 나타냅니다 (flat, data 래퍼 없음).
type CreateBranchResponse struct {
	Success     bool                `json:"success"`
	BranchName  string              `json:"branch_name"`
	CommitSHA   string              `json:"commit_sha"`
	Source      string              `json:"source"`
	RefPath     string              `json:"ref_path"`
	IsNewBranch bool                `json:"is_new_branch"`
	Message     string              `json:"message"`
	CreatedAt   time.Time           `json:"created_at"`
	Details     CreateBranchDetails `json:"details"`
}

// CreateBranchData는 내부/테스트용 호환 타입 (CreateBranchResponse와 동일 필드).
type CreateBranchData struct {
	BranchName  string              `json:"branch_name"`
	CommitSHA   string              `json:"commit_sha"`
	Source      string              `json:"source"`
	RefPath     string              `json:"ref_path"`
	IsNewBranch bool                `json:"is_new_branch"`
	Message     string              `json:"message"`
	CreatedAt   time.Time           `json:"created_at"`
	Details     CreateBranchDetails `json:"details"`
}

// BranchInfo는 Git 브랜치에 대한 정보를 나타냅니다 (단위 테스트 호환성용).
// 브랜치 이름, 커밋 SHA, 참조 경로 및 HEAD 여부를 포함합니다.
type BranchInfo struct {
	Name       string      `json:"name"`
	CommitSHA  string      `json:"commit_sha"`
	RefPath    string      `json:"ref_path"`
	IsHead     bool        `json:"is_head"`
	LastCommit *CommitInfo `json:"last_commit,omitempty"`
	Date       time.Time   `json:"date"`
}

// GetBranchesResponse는 브랜치 목록 응답을 나타냅니다.
type GetBranchesResponse struct {
	Success    bool           `json:"success"`
	Branches   []BranchInfo   `json:"branches"`
	Pagination PaginationInfo `json:"pagination"`
}

// PaginationInfo는 브랜치 응답의 페이지네이션 정보를 나타냅니다.
// Python PaginatedResponse와 호환되는 필드들을 포함합니다.
type PaginationInfo struct {
	Count       int     `json:"count"`        // 전체 항목 수
	Next        *string `json:"next"`         // 다음 페이지 URL
	Previous    *string `json:"previous"`     // 이전 페이지 URL
	CurrentPage int     `json:"current_page"` // 현재 페이지 번호
	TotalPages  int     `json:"total_pages"`  // 전체 페이지 수
	PageSize    int     `json:"page_size"`    // 페이지당 항목 수
}

// 기본 페이지네이션 값들
const (
	DefaultBranchesLimit = 30
	MaxBranchesLimit     = 100
	MinBranchesLimit     = 1
)

// CreateBranchDetails는 브랜치 생성의 추가 세부 정보를 포함합니다.
type CreateBranchDetails struct {
	Checkout  bool   `json:"checkout"`
	ForceUsed bool   `json:"force_used"`
	Author    string `json:"author"`
}

// UpdateBranchResponse는 브랜치 업데이트 응답을 나타냅니다.
// 브랜치 포인터 업데이트 작업의 결과와 커밋 정보를 포함합니다.
type UpdateBranchResponse struct {
	Success      bool   `json:"success"`
	BranchName   string `json:"branch_name"`
	CommitHash   string `json:"commit_hash"`
	PreviousHash string `json:"previous_hash"`
	Operation    string `json:"operation"`
	FastForward  bool   `json:"fast_forward"`
}

// BranchesListResponse는 브랜치 목록 응답을 나타냅니다 (GitHub 브랜치 페이지 스타일).
type BranchesListResponse struct {
	Default    []BranchListItem `json:"default"`
	Yours      []BranchListItem `json:"yours"`
	Active     []BranchListItem `json:"active"`
	Stale      []BranchListItem `json:"stale"`
	TotalCount int              `json:"total_count"`
}

// BranchListItem은 브랜치 목록의 개별 브랜치 항목을 나타냅니다.
type BranchListItem struct {
	Name          string            `json:"name"`
	Updated       string            `json:"updated"`
	UpdatedBy     string            `json:"updated_by"`
	CommitSHA     string            `json:"commit_sha"`
	CommitMessage string            `json:"commit_message"`
	IsDefault     bool              `json:"is_default"`
	Behind        int               `json:"behind"`
	Ahead         int               `json:"ahead"`
	ChangeRequest ChangeRequestInfo `json:"change_request"`
	// 메타 정보 (분류는 응답 상위에서 처리)
	CreatedBy           string    `json:"created_by"`             // 브랜치 생성자
	DaysSinceLastCommit int       `json:"days_since_last_commit"` // 마지막 커밋으로부터 경과 일수
	SortTime            time.Time `json:"-"`                      // 정렬용 내부 필드 (응답 제외)
}

// ChangeRequestInfo는 브랜치와 관련된 Change Request 정보를 나타냅니다.
type ChangeRequestInfo struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}

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
// 기존 브랜치의 포인터를 다른 커밋으로 변경하거나 브랜치 이름을 변경할 때 사용됩니다.
// target과 new_name 중 하나만 제공해야 합니다 (둘 다 제공하면 에러).
type UpdateBranchRequest struct {
	Target  string `json:"target,omitempty"`   // commit SHA or branch name to point to (for commit update)
	NewName string `json:"new_name,omitempty"` // new branch name (for rename operation)
	Force   bool   `json:"force,omitempty"`    // force update even if not fast-forward, or force rename even if new name exists
	Message string `json:"message,omitempty"`  // optional update message
}

// RenameBranchRequest는 브랜치 이름 변경을 위한 JSON 페이로드를 나타냅니다.
// 기존 브랜치의 이름을 새로운 이름으로 변경할 때 사용됩니다.
type RenameBranchRequest struct {
	OldName string `json:"old_name" binding:"required"` // 기존 브랜치 이름
	NewName string `json:"new_name" binding:"required"` // 새로운 브랜치 이름
	Force   bool   `json:"force,omitempty"`             // force rename even if new name already exists
}

type CommitInfo struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	Date    time.Time `json:"date"`
}

// SimpleBranchInfo는 간단한 브랜치 정보를 나타냅니다.
type SimpleBranchInfo struct {
	Name      string
	Hash      string
	IsRemote  bool
	IsCurrent bool
}

// BranchAheadBehind represents ahead/behind counts for a branch compared to another branch
type BranchAheadBehind struct {
	Ahead  int
	Behind int
}

// ValidateReopenRequest는 CR reopen 전 브랜치 검증 요청입니다.
type ValidateReopenRequest struct {
	SourceBranchName      string `json:"source_branch_name" binding:"required"`
	TargetBranchName      string `json:"target_branch_name" binding:"required"`
	StoredSourceCommitSHA string `json:"stored_source_commit_sha,omitempty"`
}

// ValidateReopenResponse는 CR reopen 브랜치 검증 응답입니다.
type ValidateReopenResponse struct {
	Success         bool   `json:"success"`
	SourceCommitSHA string `json:"source_commit_sha"`
	TargetCommitSHA string `json:"target_commit_sha"`
	LineageValid    bool   `json:"lineage_valid"`
}
