package overview

import (
	"time"

	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/tags"
)

// OverviewResponse is the response type for repository overview API
type RepositoryOverviewResponse struct {
	Success              bool                     `json:"success"`
	Message              string                   `json:"message"`
	Repository           string                   `json:"repository"`
	Owner                string                   `json:"owner"`
	Name                 string                   `json:"name"`
	Path                 string                   `json:"path"`
	Tree                 []GitTreeEntryWithCommit `json:"tree"`
	LatestCommits        GitCommit                `json:"latest_commits"`
	Branches             []GitBranch              `json:"branches"`
	Tags                 []GitTag                 `json:"tags"`
	Count                RepositoryCount          `json:"count"`
	Readme               string                   `json:"readme"`
	LanguageDistribution LanguageDistribution     `json:"language_distribution"`
}

// RepositoryCount provides counts of different repository types
type RepositoryCount struct {
	Commits  int `json:"commits"`
	Branches int `json:"branches"`
	Tags     int `json:"tags"`
	Remotes  int `json:"remotes"`
	Total    int `json:"total"`
}

type LanguageDistribution struct {
	Languages  []LanguageStats `json:"languages"`
	TotalBytes int64           `json:"total_bytes"`
	TotalFiles int             `json:"total_files"`
}

type LanguageStats struct {
	Language   string  `json:"language"`
	Bytes      int64   `json:"bytes"`
	Percentage float64 `json:"percentage"`
	Files      int     `json:"files"`
}

type GitCommit struct {
	SHA          string    `json:"sha"`
	Message      string    `json:"message"`
	Author       GitAuthor `json:"author"`
	Committer    GitAuthor `json:"committer"`
	ParentHashes []string  `json:"parent_hashes"`
	TreeHash     string    `json:"tree_hash"`
}

type GitAuthor struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

type GitBranch struct {
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	IsRemote  bool   `json:"is_remote"`
	IsCurrent bool   `json:"is_current"`
}

type GitTag struct {
	Name        string `json:"name"`
	Hash        string `json:"hash"`
	IsAnnotated bool   `json:"is_annotated"`
}

type GitTreeEntry struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// GitTreeEntryWithCommit는 커밋 정보를 포함한 트리 항목입니다.
type GitTreeEntryWithCommit struct {
	Type         string     `json:"type"`
	Name         string     `json:"name"`
	Path         string     `json:"path"`
	Mode         *string    `json:"mode,omitempty"`          // 파일 모드 (Unix permissions)
	Hash         *string    `json:"hash,omitempty"`          // Git object hash
	Size         *int64     `json:"size,omitempty"`          // 파일 크기 (바이트, blob인 경우만)
	LatestCommit *GitCommit `json:"latest_commit,omitempty"` // 최신 커밋 정보
}

// 변환 함수들 - 다른 패키지의 타입을 overview 패키지 타입으로 변환
func convertCommitToGitCommit(c commits.Commit) GitCommit {
	return GitCommit{
		SHA:          c.Sha,
		Message:      c.Message,
		Author:       convertAuthorToGitAuthor(c.Author),
		Committer:    convertAuthorToGitAuthor(c.Committer),
		ParentHashes: c.ParentHashes,
		TreeHash:     c.TreeHash,
	}
}

func convertAuthorToGitAuthor(a commits.CommitAuthor) GitAuthor {
	return GitAuthor{
		Name:  a.Name,
		Email: a.Email,
		Date:  a.Date,
	}
}

func convertBranchesToGitBranches(branches []BranchInfo) []GitBranch {
	// 인덱스 접근 방식이 더 효율적
	result := make([]GitBranch, len(branches))
	for i := range branches {
		result[i] = GitBranch{
			Name:      branches[i].Name,
			Hash:      branches[i].Hash,
			IsRemote:  branches[i].IsRemote,
			IsCurrent: branches[i].IsCurrent,
		}
	}
	return result
}

func convertTagsToGitTags(tags []tags.Tag) []GitTag {
	// 인덱스 접근 방식이 더 효율적
	result := make([]GitTag, len(tags))
	for i := range tags {
		result[i] = GitTag{
			Name:        tags[i].Name,
			Hash:        tags[i].Hash,
			IsAnnotated: tags[i].IsAnnotated,
		}
	}
	return result
}

func convertCommitsToGitCommits(commitsList []commits.Commit) []GitCommit {
	// 인덱스 접근 방식이 더 효율적
	result := make([]GitCommit, len(commitsList))
	for i := range commitsList {
		result[i] = convertCommitToGitCommit(commitsList[i])
	}
	return result
}

// 내부 타입들

type BranchInfo struct {
	Name      string
	Hash      string
	IsRemote  bool
	IsCurrent bool
}

type RemoteInfo struct {
	Name      string
	URL       string
	IsDefault bool
}

// TreeOverviewResponse is the response type for tree overview API
type TreeOverviewResponse struct {
	Success      bool                     `json:"success"`
	Repository   string                   `json:"repository"`
	Owner        string                   `json:"owner"`
	Name         string                   `json:"name"`
	Path         string                   `json:"path"`
	Tree         []GitTreeEntryWithCommit `json:"tree"`
	Branches     []GitBranch              `json:"branches"`
	LatestCommit *GitCommit               `json:"latest_commit"`
	Commits      []GitCommit              `json:"commits"`
}

// BlobOverviewResponse is the response type for blob overview API
type BlobOverviewResponse struct {
	Success      bool                     `json:"success"`
	Content      string                   `json:"content"`
	Lines        int                      `json:"lines"`
	Size         int64                    `json:"size"`
	Tree         []GitTreeEntryWithCommit `json:"tree"`
	Branches     []GitBranch              `json:"branches"`
	LatestCommit *GitCommit               `json:"latest_commit"`
	Commits      []GitCommit              `json:"commits"`
}

// BranchesOverviewResponse is the response type for branches overview API
type BranchesOverviewResponse struct {
	Success    bool             `json:"success"`
	Default    []BranchListItem `json:"default"`
	Yours      []BranchListItem `json:"yours"`
	Active     []BranchListItem `json:"active"`
	Stale      []BranchListItem `json:"stale"`
	TotalCount int              `json:"total_count"`
}

// BranchListItem represents a single branch item in the branches overview
type BranchListItem struct {
	Name                string            `json:"name"`
	Updated             string            `json:"updated"`
	UpdatedBy           string            `json:"updated_by"`
	CommitSHA           string            `json:"commit_sha"`
	CommitMessage       string            `json:"commit_message"`
	IsDefault           bool              `json:"is_default"`
	Behind              int               `json:"behind"`
	Ahead               int               `json:"ahead"`
	ChangeRequest       ChangeRequestInfo `json:"change_request"`
	CreatedBy           string            `json:"created_by"`
	DaysSinceLastCommit int               `json:"days_since_last_commit"`
	SortTime            time.Time         `json:"-"` // Internal field for sorting, excluded from JSON
}

// ChangeRequestInfo represents change request information for a branch
type ChangeRequestInfo struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}

// TreeWithCommitOverviewResponse is the response type for tree with commit overview API
type TreeWithCommitOverviewResponse struct {
	Success    bool                     `json:"success"`
	Repository string                   `json:"repository"`
	Owner      string                   `json:"owner"`
	Name       string                   `json:"name"`
	Path       string                   `json:"path"`
	Tree       []GitTreeEntryWithCommit `json:"tree"`
	Branches   []GitBranch              `json:"branches"`
}

type LatestCommitsBatchRequest struct {
	Ref     string   `json:"ref"`
	DirPath string   `json:"dir_path"`
	Paths   []string `json:"paths"`
}

type LatestCommitsMeta struct {
	ResolvedRefHash string `json:"resolved_ref_hash"`
	Incomplete      bool   `json:"incomplete"`
	ScannedCommits  int    `json:"scanned_commits"`
	TimeBudgetMs    int    `json:"time_budget_ms"`
	CutReason       string `json:"cut_reason"`
}

type LatestCommitsBatchResponse struct {
	Success       bool                  `json:"success"`
	Repository    string                `json:"repository"`
	Owner         string                `json:"owner"`
	Name          string                `json:"name"`
	DirPath       string                `json:"dir_path"`
	CommitsByPath map[string]*GitCommit `json:"commits_by_path"`
	Meta          LatestCommitsMeta     `json:"meta"`
	MissingPaths  []string              `json:"missing_paths,omitempty"`
}
