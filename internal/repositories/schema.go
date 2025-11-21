package repositories

import (
	"time"
)

type CreateRepositoryResponse struct {
	Success       bool                   `json:"success"`
	Repository    string                 `json:"repository"`
	Owner         string                 `json:"owner"`
	Name          string                 `json:"name"`
	Path          string                 `json:"path,omitempty"`
	DefaultBranch string                 `json:"default_branch"`
	InitialCommit string                 `json:"initial_commit"`
	CreatedAt     time.Time              `json:"created_at"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

type CreateRepositoryOptions struct {
	// Path is the filesystem path where the repository should be created
	Path          string
	Bare          bool
	DefaultBranch string
	InitialCommit bool
	Author        *CommitAuthor
	InitialFiles  map[string]string
	Template      string
}

// CreateRepositoryRequest는 저장소 생성을 위한 JSON 페이로드를 나타냅니다.
// 기본 브랜치, 초기 파일 및 메타데이터를 포함합니다.
type CreateRepositoryRequest struct {
	Private       bool                   `json:"private,omitempty"`
	InitialCommit bool                   `json:"initial_commit,omitempty"`
	DefaultBranch string                 `json:"default_branch,omitempty"`
	Author        *CommitAuthor          `json:"author,omitempty"`
	InitialFiles  map[string]string      `json:"initial_files,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

type CommitAuthor struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// RepositoryInfo는 저장소에 대한 메타데이터를 담는 구조체입니다.
type RepositoryInfo struct {
	Owner         string    // Repository owner
	Name          string    // Repository name
	Path          string    // Full filesystem path
	Type          string    // Repository type ("bare", "non-bare", "worktree")
	Exists        bool      // Whether the repository exists
	IsValid       bool      // Whether the repository structure is valid
	IsAccessible  bool      // Whether the repository is accessible (no permission issues)
	IsBare        bool      // Whether the repository is bare
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`
	LastModified  time.Time `json:"last_modified"`
	Size          int64     `json:"size"`
	BranchCount   int       `json:"branch_count"`
	TagCount      int       `json:"tag_count"`
	CommitCount   int       `json:"commit_count"`
}
