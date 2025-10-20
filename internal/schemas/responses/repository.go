// Package responses는 Git 저장소 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// 새 저장소 생성 작업의 결과를 반환하는 데이터 구조체들을 포함합니다.
package responses

import (
	"time"

	"github.com/collabops/git-executor/internal/git"
)

// CreateRepositoryResponse는 저장소 생성 응답을 나타냅니다.
// 생성된 저장소의 상세 정보와 메타데이터를 포함합니다.
type CreateRepositoryResponse struct {
	Success       bool                   `json:"success"`
	Repository    string                 `json:"repository"`
	Owner         string                 `json:"owner"`
	Name          string                 `json:"name"`
	Path          string                 `json:"path,omitempty"`
	Description   string                 `json:"description"`
	DefaultBranch string                 `json:"default_branch"`
	InitialCommit string                 `json:"initial_commit"`
	CreatedAt     time.Time              `json:"created_at"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// RepositoryDetailResponse는 저장소 상세 정보 응답을 나타냅니다.
type RepositoryDetailResponse struct {
	Repository    string          `json:"repository"`
	Owner         string          `json:"owner"`
	Name          string          `json:"name"`
	Path          string          `json:"path"`
	Description   string          `json:"description"`
	DefaultBranch string          `json:"default_branch,"`
	Tree          []git.TreeEntry `json:"tree,"`
	LatestCommits git.Commit      `json:"latest_commits,"`
	Branches      []git.Branch    `json:"branches,"`
	Tags          []git.Tag       `json:"tag,"`
	Count         RepositoryCount `json:"count,"`
	Readme        string          `json:"readme,"`
}

// RepositoryTreeResponse는 저장소 트리 정보 응답을 나타냅니다.
type RepositoryTreeResponse struct {
	Repository    string                     `json:"repository"`
	Owner         string                     `json:"owner"`
	Name          string                     `json:"name"`
	Path          string                     `json:"path"`
	Description   string                     `json:"description"`
	DefaultBranch string                     `json:"default_branch"`
	Tree          []git.TreeEntry            `json:"tree"`
	SidebarTree   map[string][]git.TreeEntry `json:"sidebar_tree"`
	LatestCommits git.Commit                 `json:"latest_commits"`
	Branches      []git.Branch               `json:"branches"`
	TreePath      string                     `json:"tree_path"`
	Readme        string                     `json:"readme"`
}

// RepositoryCount는 저장소의 각종 카운트 정보를 나타냅니다.
type RepositoryCount struct {
	Commits  int `json:"commits"`
	Branches int `json:"branches"`
	Tags     int `json:"tags"`
	Remotes  int `json:"remotes"`
	Total    int `json:"total"`
}
