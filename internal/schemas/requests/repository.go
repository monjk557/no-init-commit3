package requests

import "github.com/collabops/git-executor/internal/schemas/common"

// CreateRepositoryRequest represents the JSON payload for repository creation
type CreateRepositoryRequest struct {
	Description   string                 `json:"description,omitempty"`
	Private       bool                   `json:"private,omitempty"`
	InitialCommit bool                   `json:"initial_commit,omitempty"`
	DefaultBranch string                 `json:"default_branch,omitempty"`
	Author        *common.CommitAuthor   `json:"author,omitempty"`
	InitialFiles  map[string]string      `json:"initial_files,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}
