// Package responses는 Git 브랜치 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// 브랜치 생성, 업데이트 등의 작업 결과를 반환하는 데이터 구조체들을 포함합니다.
package responses

import "github.com/collabops/git-executor/internal/schemas/common"

// CreateBranchResponse는 브랜치 생성 응답을 나타냅니다.
// 생성된 브랜치의 상세 정보와 성공 여부를 포함합니다.
type CreateBranchResponse struct {
	Success bool             `json:"success"`
	Data    CreateBranchData `json:"data"`
}

// CreateBranchData는 생성된 브랜치의 상세 정보를 포함합니다.
type CreateBranchData struct {
	BranchName  string              `json:"branch_name"`
	CommitSHA   string              `json:"commit_sha"`
	Source      string              `json:"source"`
	RefPath     string              `json:"ref_path"`
	IsNewBranch bool                `json:"is_new_branch"`
	Message     string              `json:"message"`
	Details     CreateBranchDetails `json:"details"`
}

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

// BranchInfo는 Git 브랜치에 대한 정보를 나타냅니다 (단위 테스트 호환성용).
// 브랜치 이름, 커밋 SHA, 참조 경로 및 HEAD 여부를 포함합니다.
type BranchInfo struct {
	Name       string             `json:"name"`
	CommitSHA  string             `json:"commit_sha"`
	RefPath    string             `json:"ref_path"`
	IsHead     bool               `json:"is_head"`
	LastCommit *common.CommitInfo `json:"last_commit,omitempty"`
}
