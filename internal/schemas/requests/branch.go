package requests

// CreateBranchRequest represents the JSON payload for branch creation
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

// UpdateBranchRequest represents the JSON payload for branch updates
type UpdateBranchRequest struct {
	Target  string `json:"target" binding:"required"` // commit SHA or branch name to point to
	Force   bool   `json:"force,omitempty"`           // force update even if not fast-forward
	Message string `json:"message,omitempty"`         // optional update message
}
