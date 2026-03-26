package revert

import "time"

// RevertRequest represents the JSON payload for revert operations.
// It creates a new branch with a revert commit that undoes the changes
// introduced by a previously merged commit.
type RevertRequest struct {
	// CommitSHA is the merge commit to revert.
	CommitSHA string `json:"commit_sha" binding:"required"`

	// TargetRef is the branch where the merge happened (e.g., "main").
	// The revert branch will be created from the current tip of this ref.
	TargetRef string `json:"target_ref" binding:"required"`

	// BranchName is the name for the new revert branch (e.g., "revert/CR-123").
	// If empty, defaults to "revert/<short-sha>".
	BranchName string `json:"branch_name,omitempty"`

	// ParentNumber selects which parent is the mainline for merge commits.
	// 1 = first parent (target branch before merge, default).
	// For non-merge commits this field is ignored.
	ParentNumber int `json:"parent_number,omitempty"`

	// Message is the custom revert commit message.
	// If empty, a default message is generated.
	Message string `json:"message,omitempty"`

	// Author overrides the revert commit author.
	Author *AuthorInfo `json:"author,omitempty"`

	// Committer overrides the revert commit committer.
	Committer *AuthorInfo `json:"committer,omitempty"`
}

// AuthorInfo represents commit author/committer information.
type AuthorInfo struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date,omitempty"`
}

// RevertResponse represents the response for revert operations.
type RevertResponse struct {
	Success        bool        `json:"success"`
	RevertBranch   string      `json:"revert_branch"`             // Created branch name
	RevertCommit   *CommitInfo `json:"revert_commit,omitempty"`   // The new revert commit
	RevertedCommit *CommitInfo `json:"reverted_commit,omitempty"` // The original commit that was reverted
	Message        string      `json:"message"`
}

// CommitInfo represents commit information in API responses.
type CommitInfo struct {
	SHA       string     `json:"sha"`
	Message   string     `json:"message"`
	Author    AuthorInfo `json:"author"`
	Committer AuthorInfo `json:"committer"`
	CreatedAt string     `json:"created_at"`
	TreeSHA   string     `json:"tree_sha"`
}
