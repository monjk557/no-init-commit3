package repositories

import (
	"time"
)

// ParsedRepoURL is a structured view of a git remote URL — host + owner +
// repo. Used by submodule responses so the frontend can decide
// internal-vs-external navigation by comparing host with its own origin,
// without re-parsing the URL on every render.
//
// `.git` suffix is always stripped from Repo. ParseRepoURL (in url_parse.go)
// produces this struct.
type ParsedRepoURL struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

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

// SetDefaultBranchRequest 는 PUT /repos/{owner}/{repo}/head 의 요청 본문이다.
// 이미 존재하는 브랜치 이름만 받는다 (`refs/heads/` 접두는 있어도 되고 없어도 된다).
type SetDefaultBranchRequest struct {
	Branch string `json:"branch" binding:"required"`
}

// SetDefaultBranchResponse 는 default branch 변경 결과다. PreviousDefaultBranch
// 는 호출자가 되돌리기/감사로그를 남길 수 있도록 함께 싣는다.
type SetDefaultBranchResponse struct {
	Success               bool   `json:"success"`
	Owner                 string `json:"owner"`
	Name                  string `json:"name"`
	DefaultBranch         string `json:"default_branch"`
	PreviousDefaultBranch string `json:"previous_default_branch,omitempty"`
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
