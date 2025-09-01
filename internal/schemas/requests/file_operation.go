package requests

import "github.com/collabops/git-executor/internal/schemas/common"

type FileOperationRequest struct {
	Content   string               `json:"content" binding:"required"`
	Message   string               `json:"message" binding:"required"`
	Branch    string               `json:"branch,omitempty"`
	Author    *common.CommitAuthor `json:"author,omitempty"`
	Committer *common.CommitAuthor `json:"committer,omitempty"`
	Encoding  string               `json:"encoding,omitempty"`
	Metadata  map[string]string    `json:"metadata,omitempty"`
}
