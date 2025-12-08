package overview

import (
	"time"

	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/tags"
	"github.com/collabops/git-executor/internal/tree"
)

// OverviewResponse is the response type for repository overview API
type RepositoryOverviewResponse struct {
	Repository           string               `json:"repository"`
	Owner                string               `json:"owner"`
	Name                 string               `json:"name"`
	Path                 string               `json:"path"`
	Tree                 []GitTreeEntry       `json:"tree"`
	LatestCommits        GitCommit            `json:"latest_commits"`
	Branches             []GitBranch          `json:"branches"`
	Tags                 []GitTag             `json:"tags"`
	Count                RepositoryCount      `json:"count"`
	Readme               string               `json:"readme"`
	LanguageDistribution LanguageDistribution `json:"language_distribution"`
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
	result := make([]GitBranch, len(branches))
	for i, b := range branches {
		result[i] = GitBranch{
			Name:      b.Name,
			Hash:      b.Hash,
			IsRemote:  b.IsRemote,
			IsCurrent: b.IsCurrent,
		}
	}
	return result
}

func convertTagsToGitTags(tags []tags.Tag) []GitTag {
	result := make([]GitTag, len(tags))
	for i, t := range tags {
		result[i] = GitTag{
			Name:        t.Name,
			Hash:        t.Hash,
			IsAnnotated: t.IsAnnotated,
		}
	}
	return result
}

func convertTreeEntriesToGitTreeEntries(entries []tree.TreeEntry) []GitTreeEntry {
	result := make([]GitTreeEntry, len(entries))
	for i, e := range entries {
		result[i] = GitTreeEntry{
			Type: e.Type,
			Name: e.Name,
			Path: e.Path,
		}
	}
	return result
}

func convertCommitsToGitCommits(commitsList []commits.Commit) []GitCommit {
	result := make([]GitCommit, len(commitsList))
	for i, c := range commitsList {
		result[i] = convertCommitToGitCommit(c)
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
	Success      bool           `json:"success"`
	Tree         []GitTreeEntry `json:"tree"`
	Branches     []GitBranch    `json:"branches"`
	LatestCommit GitCommit      `json:"latest_commit"`
	Commits      []GitCommit    `json:"commits"`
}

// BlobOverviewResponse is the response type for blob overview API
type BlobOverviewResponse struct {
	Success      bool           `json:"success"`
	Content      string         `json:"content"`
	Lines        int            `json:"lines"`
	Size         int64          `json:"size"`
	Tree         []GitTreeEntry `json:"tree"`
	Branches     []GitBranch    `json:"branches"`
	LatestCommit GitCommit      `json:"latest_commit"`
	Commits      []GitCommit    `json:"commits"`
}
