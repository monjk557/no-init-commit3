package responses

import "time"

type CreateRepositoryResponse struct {
	Success       bool                   `json:"success"`
	Repository    string                 `json:"repository"`
	Owner         string                 `json:"owner"`
	Name          string                 `json:"name"`
	Path          string                 `json:"path,omitempty"`
	Description   string                 `json:"description,omitempty"`
	DefaultBranch string                 `json:"default_branch"`
	InitialCommit string                 `json:"initial_commit,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}
