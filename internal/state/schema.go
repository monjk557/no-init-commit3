package state

import "time"

// RepositoryStateData represents the response for repository state (matches Python RepositoryStateData)
type RepositoryStateData struct {
	Success        bool           `json:"success"`
	RepositoryName string         `json:"repository_name"`
	Owner          string         `json:"owner"`
	Head           HeadInfo       `json:"head"`
	References     References     `json:"references"`
	RepositoryInfo RepositoryInfo `json:"repository_info"`
	Timestamp      string         `json:"timestamp"`
	Details        Details        `json:"details"`
}

// HeadInfo represents HEAD information
type HeadInfo struct {
	Type       string     `json:"type"`        // Reference type (e.g., "branch", "tag", "commit")
	Name       string     `json:"name"`        // Reference name (short name)
	Target     string     `json:"target"`      // Target reference (full path)
	CommitSHA  string     `json:"commit_sha"`  // Commit SHA
	CommitInfo CommitInfo `json:"commit_info"` // Commit information
	IsBranch   bool       `json:"is_branch"`   // Whether HEAD points to a branch
	Reference  string     `json:"reference"`   // Full reference path (for backward compatibility)
	Commit     CommitInfo `json:"commit"`      // Commit information (for backward compatibility)
	IsDetached bool       `json:"is_detached"` // Whether HEAD is detached (for backward compatibility)
}

// CommitInfo represents commit information
type CommitInfo struct {
	SHA       string     `json:"sha"`
	Message   string     `json:"message"`
	Author    AuthorInfo `json:"author"`
	Committer AuthorInfo `json:"committer"`
	CreatedAt string     `json:"created_at"`
}

// AuthorInfo represents author/committer information
type AuthorInfo struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

// References represents repository references (branches and tags)
type References struct {
	Branches []ReferenceInfo `json:"branches"`
	Tags     []ReferenceInfo `json:"tags"`
	Count    ReferenceCount  `json:"count"` // Count of references
}

// ReferenceCount represents counts of different reference types
type ReferenceCount struct {
	Branches int `json:"branches"` // Number of branches
	Tags     int `json:"tags"`     // Number of tags
	Remotes  int `json:"remotes"`  // Number of remotes
	Total    int `json:"total"`    // Total number of references
}

// ReferenceInfo represents a single reference (branch or tag)
type ReferenceInfo struct {
	Name         string      `json:"name"`          // Short name
	FullName     string      `json:"full_name"`     // Full reference path
	CommitSHA    string      `json:"commit_sha"`    // Commit SHA
	IsHead       bool        `json:"is_head"`       // Whether this is the current HEAD
	IsProtected  bool        `json:"is_protected"`  // Whether this branch is protected
	LastModified string      `json:"last_modified"` // Last modification time
	Commit       *CommitInfo `json:"commit,omitempty"`
}

// RepositoryInfo represents repository information
type RepositoryInfo struct {
	Path          string    `json:"path,omitempty"`
	DefaultBranch string    `json:"default_branch"`
	Size          int64     `json:"size"`
	BranchCount   int       `json:"branch_count"`
	TagCount      int       `json:"tag_count"`
	CommitCount   int       `json:"commit_count"`
	IsBare        bool      `json:"is_bare"`
	HasWorktree   bool      `json:"has_worktree"`  // Whether repository has worktree
	GitDirectory  string    `json:"git_directory"` // Path to .git directory
	CreatedAt     time.Time `json:"created_at,omitempty"`
	LastModified  time.Time `json:"last_modified,omitempty"`
}

// Details represents request details
type Details struct {
	RequestPath string          `json:"request_path,omitempty"`
	Method      string          `json:"method,omitempty"`
	Includes    map[string]bool `json:"includes"`     // Requested includes as dictionary
	RequestType string          `json:"request_type"` // Type of request
}
