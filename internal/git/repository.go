// Package git provides Git repository operations using go-git
package git

import (
	"context"
	"time"
)

// Repository represents a Git repository interface for operations
type Repository interface {
	// Path returns the filesystem path of the repository
	Path() string

	// GetTree retrieves the directory structure at a given reference
	GetTree(ctx context.Context, ref string) ([]TreeEntry, error)

	// GetBlob retrieves the contents of a file at a specific path and reference
	GetBlob(ctx context.Context, path, ref string) ([]byte, error)

	// GetCommits retrieves commit history with pagination
	GetCommits(ctx context.Context, ref string, limit, skip int) ([]Commit, error)

	// Close releases any resources held by the repository
	Close() error
}

// TreeEntry represents an entry in a Git tree (directory listing)
type TreeEntry struct {
	// Path is the relative path of the entry within the tree
	Path string `json:"path"`

	// Mode represents the file mode (permissions and type)
	Mode string `json:"mode"`

	// Type indicates the type of the entry (blob, tree, commit)
	Type EntryType `json:"type"`

	// Hash is the Git object hash
	Hash string `json:"hash"`

	// Size is the size of the entry in bytes (for blobs)
	Size int64 `json:"size"`
}

// EntryType represents the type of a Git tree entry
type EntryType string

const (
	// EntryTypeBlob represents a file
	EntryTypeBlob EntryType = "blob"

	// EntryTypeTree represents a directory
	EntryTypeTree EntryType = "tree"

	// EntryTypeCommit represents a submodule commit
	EntryTypeCommit EntryType = "commit"
)

// Commit represents a Git commit
type Commit struct {
	// Hash is the commit hash
	Hash string `json:"hash"`

	// Author is the commit author information
	Author CommitAuthor `json:"author"`

	// Committer is the committer information
	Committer CommitAuthor `json:"committer"`

	// Message is the commit message
	Message string `json:"message"`

	// ParentHashes are the hashes of parent commits
	ParentHashes []string `json:"parent_hashes"`

	// TreeHash is the hash of the commit's tree object
	TreeHash string `json:"tree_hash"`
}

// CommitAuthor represents author/committer information
type CommitAuthor struct {
	// Name is the author/committer name
	Name string `json:"name"`

	// Email is the author/committer email
	Email string `json:"email"`

	// Date is the timestamp of the commit
	Date time.Time `json:"date"`
}

// OpenRepositoryFunc is a function type for opening repositories
type OpenRepositoryFunc func(path string) (Repository, error)

// RepositoryManager provides high-level repository management operations
type RepositoryManager interface {
	// CreateRepository creates a new Git repository at the specified path
	CreateRepository(ctx context.Context, options *CreateRepositoryOptions) (*RepositoryInfo, error)

	// OpenRepository opens an existing Git repository
	OpenRepository(ctx context.Context, path string) (Repository, error)

	// RepositoryExists checks if a repository exists at the given path
	RepositoryExists(ctx context.Context, path string) (bool, error)

	// DeleteRepository removes a repository from the filesystem
	DeleteRepository(ctx context.Context, path string) error

	// ListRepositories returns a list of repositories in a directory
	ListRepositories(ctx context.Context, rootPath string) ([]RepositoryInfo, error)
}

// CreateRepositoryOptions contains options for creating a new repository
type CreateRepositoryOptions struct {
	// Path is the filesystem path where the repository should be created
	Path string

	// Bare indicates whether to create a bare repository
	Bare bool

	// Description is an optional description for the repository
	Description string

	// DefaultBranch is the name of the default branch (defaults to "main")
	DefaultBranch string

	// InitialCommit specifies whether to create an initial commit
	InitialCommit bool

	// Author information for the initial commit
	Author *CommitAuthor

	// InitialFiles is a map of file paths to their content for the initial commit
	InitialFiles map[string]string

	// Template is an optional template repository to copy from
	Template string
}

// RepositoryInfo contains metadata about a Git repository
type RepositoryInfo struct {
	// Path is the filesystem path of the repository
	Path string `json:"path"`

	// Name is the repository name (derived from path)
	Name string `json:"name"`

	// Description is the repository description
	Description string `json:"description,omitempty"`

	// DefaultBranch is the name of the default branch
	DefaultBranch string `json:"default_branch"`

	// IsBare indicates whether this is a bare repository
	IsBare bool `json:"is_bare"`

	// CreatedAt is the creation timestamp
	CreatedAt time.Time `json:"created_at"`

	// LastModified is the last modification timestamp
	LastModified time.Time `json:"last_modified"`

	// Size is the repository size in bytes
	Size int64 `json:"size"`

	// CommitCount is the total number of commits
	CommitCount int `json:"commit_count"`

	// BranchCount is the total number of branches
	BranchCount int `json:"branch_count"`

	// TagCount is the total number of tags
	TagCount int `json:"tag_count"`
}
