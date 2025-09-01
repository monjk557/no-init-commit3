package responses

import "github.com/collabops/git-executor/internal/schemas/common"

// CreateBranchResponse represents the response for branch creation
type CreateBranchResponse struct {
	Success bool             `json:"success"`
	Data    CreateBranchData `json:"data"`
}

type CreateBranchData struct {
	BranchName  string              `json:"branch_name"`
	CommitSHA   string              `json:"commit_sha"`
	Source      string              `json:"source"`
	RefPath     string              `json:"ref_path"`
	IsNewBranch bool                `json:"is_new_branch"`
	Message     string              `json:"message"`
	Details     CreateBranchDetails `json:"details"`
}

type CreateBranchDetails struct {
	Checkout  bool   `json:"checkout"`
	ForceUsed bool   `json:"force_used"`
	Author    string `json:"author"`
}

// UpdateBranchResponse represents the response for branch updates
type UpdateBranchResponse struct {
	Success      bool   `json:"success"`
	BranchName   string `json:"branch_name"`
	CommitHash   string `json:"commit_hash"`
	PreviousHash string `json:"previous_hash"`
	Operation    string `json:"operation"`
	FastForward  bool   `json:"fast_forward"`
}

// BranchInfo represents information about a Git branch (for unit test compatibility)
type BranchInfo struct {
	Name       string             `json:"name"`
	CommitSHA  string             `json:"commit_sha"`
	RefPath    string             `json:"ref_path"`
	IsHead     bool               `json:"is_head"`
	LastCommit *common.CommitInfo `json:"last_commit,omitempty"`
}
