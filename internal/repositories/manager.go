// Package git provides Git object manipulation functionality for REST API operations.
// This package extends the existing git package with object management capabilities.
package repositories

import (
	"fmt"
	"strings"

	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	gogit "github.com/go-git/go-git/v5"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// GitObjectManager provides Git object manipulation capabilities for REST API operations
type GitObjectManager struct {
	pathConfig PathConfig
	logger     *logging.Logger
}

// FileOperation represents a file operation type
type FileOperation string

const (
	// FileOperationCreate represents creating a new file
	FileOperationCreate FileOperation = "create"
	// FileOperationUpdate represents updating an existing file
	FileOperationUpdate FileOperation = "update"
	// FileOperationDelete represents deleting a file
	FileOperationDelete FileOperation = "delete"
)

// FileChange represents a change to be made to a file
type FileChange struct {
	// Path is the file path relative to repository root
	Path string `json:"path"`
	// Operation is the type of operation to perform
	Operation FileOperation `json:"operation"`
	// Content is the new file content (for create/update operations)
	Content []byte `json:"content,omitempty"`
	// Mode is the file mode (optional, defaults to 0644 for files)
	Mode *filemode.FileMode `json:"mode,omitempty"`
	// OriginalPath is the original path for rename operations
	OriginalPath string `json:"original_path,omitempty"`
}

// CommitInfo represents commit metadata
type CommitInfo struct {
	// Message is the commit message
	Message string `json:"message"`
	// Author information
	Author object.Signature `json:"author"`
	// Committer information (optional, defaults to Author)
	Committer *object.Signature `json:"committer,omitempty"`
	// Parents are the parent commit hashes
	Parents []plumbing.Hash `json:"parents,omitempty"`
}

// TreeManipulationResult represents the result of tree manipulation operations
type TreeManipulationResult struct {
	// TreeHash is the resulting tree hash
	TreeHash plumbing.Hash
	// ChangedFiles lists all files that were modified
	ChangedFiles []string
	// CreatedFiles lists all files that were created
	CreatedFiles []string
	// DeletedFiles lists all files that were deleted
	DeletedFiles []string
}

// CommitResult represents the result of a commit operation
type CommitResult struct {
	// Hash is the new commit hash
	Hash plumbing.Hash
	// TreeHash is the tree hash
	TreeHash plumbing.Hash
	// Message is the commit message
	Message string
	// Author is the commit author
	Author object.Signature
	// Committer is the commit committer
	Committer object.Signature
	// Parents are the parent commit hashes
	Parents []plumbing.Hash
}

// NewGitObjectManager creates a new Git object manager
func NewGitObjectManager(pathConfig *PathConfig, logger *logging.Logger) (*GitObjectManager, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	return &GitObjectManager{
		pathConfig: *pathConfig,
		logger:     logger,
	}, nil
}

// OpenRepository opens a Git repository for manipulation
// Uses cache for better performance (same as v2)
func (gom *GitObjectManager) OpenRepository(owner, repo string) (*gogit.Repository, error) {
	// Get the repository path using existing path validation
	repoPath, err := GetRepoPath(&gom.pathConfig, owner, repo)
	if err != nil {
		gom.logger.Error("Failed to get repository path",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Error(err))
		return nil, fmt.Errorf("invalid repository path: %w", err)
	}

	// Validate repository exists and is accessible
	if err := ValidateRepoPath(&gom.pathConfig, repoPath); err != nil {
		gom.logger.Error("Repository path validation failed",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("repository validation failed: %w", err)
	}

	// Open the repository using cache (same as v2)
	gitRepoInterface, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		gom.logger.Error("Failed to open Git repository",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Convert to *gogit.Repository (extract from GoGitRepository)
	// GoGitRepository는 내부적으로 *gogit.Repository를 가지고 있음
	if gogitRepo, ok := gitRepoInterface.(*git.GoGitRepository); ok {
		return gogitRepo.GetGoGitRepo(), nil
	}

	// Fallback: 직접 열기 (should not happen)
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		gom.logger.Error("Failed to open Git repository (fallback)",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	return gitRepo, nil
}

// GetRepositoryHead gets the current HEAD reference
func (gom *GitObjectManager) GetRepositoryHead(repo *gogit.Repository) (*plumbing.Reference, error) {
	headRef, err := repo.Head()
	if err != nil {
		gom.logger.Error("Failed to get repository HEAD", zap.Error(err))
		return nil, fmt.Errorf("failed to get HEAD reference: %w", err)
	}

	return headRef, nil
}

// ResolveReference resolves a Git reference to a commit hash
func (gom *GitObjectManager) ResolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	// Handle HEAD specially - it resolves to the current branch
	if ref == "HEAD" || ref == "" {
		headRef, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to get HEAD reference: %w", err)
		}
		return headRef.Hash(), nil
	}

	// Try to parse as direct hash first
	if hash := plumbing.NewHash(ref); !hash.IsZero() {
		// Validate that the hash exists in the repository
		_, err := repo.CommitObject(hash)
		if err == nil {
			return hash, nil
		}
	}

	// Try to resolve as a reference name
	// If ref doesn't start with refs/, assume it's a branch name and add refs/heads/ prefix
	refName := ref
	if !strings.HasPrefix(ref, "refs/") {
		refName = "refs/heads/" + ref
	}

	reference, err := repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		gom.logger.Error("Failed to resolve reference",
			zap.String("ref", ref),
			zap.String("ref_name", refName),
			zap.Error(err))
		return plumbing.ZeroHash, fmt.Errorf("failed to resolve reference %s: %w", ref, err)
	}

	hash := reference.Hash()

	return hash, nil
}

// ValidateFileChanges validates a list of file changes
func (gom *GitObjectManager) ValidateFileChanges(changes []FileChange) error {
	if len(changes) == 0 {
		return fmt.Errorf("no file changes provided")
	}

	pathValidator := NewGitAPIPathValidator(&gom.pathConfig)
	seenPaths := make(map[string]bool)

	for i, change := range changes {
		// Validate path
		if change.Path == "" {
			return fmt.Errorf("file change %d: path is required", i)
		}

		// Check for duplicate paths
		if seenPaths[change.Path] {
			return fmt.Errorf("file change %d: duplicate path %s", i, change.Path)
		}
		seenPaths[change.Path] = true

		// Validate file path using our API path validator
		if err := pathValidator.ValidateFilePath(change.Path); err != nil {
			return fmt.Errorf("file change %d: invalid path %s: %w", i, change.Path, err)
		}

		// Validate operation-specific requirements
		switch change.Operation {
		case FileOperationCreate, FileOperationUpdate:
			// Content is required for create/update operations
			if change.Content == nil {
				return fmt.Errorf("file change %d: content is required for %s operation", i, change.Operation)
			}
		case FileOperationDelete:
			// Content should not be provided for delete operations
			if change.Content != nil {
				return fmt.Errorf("file change %d: content should not be provided for delete operation", i)
			}
		default:
			return fmt.Errorf("file change %d: invalid operation %s", i, change.Operation)
		}

		// Validate file mode if provided
		if change.Mode != nil {
			mode := *change.Mode
			if mode != filemode.Regular && mode != filemode.Executable && mode != filemode.Symlink {
				return fmt.Errorf("file change %d: invalid file mode %s", i, mode)
			}
		}
	}

	return nil
}

// GetFileFromTree gets a file from a tree by path
func (gom *GitObjectManager) GetFileFromTree(tree *object.Tree, path string) (*object.File, error) {
	file, err := tree.File(path)
	if err != nil {
		// Check if it's a "file not found" error
		if err == object.ErrFileNotFound {
			return nil, nil // Return nil without error for missing files
		}
		gom.logger.Error("Failed to get file from tree",
			zap.String("path", path),
			zap.Error(err))
		return nil, fmt.Errorf("failed to get file %s from tree: %w", path, err)
	}

	return file, nil
}

// GetTreeFromCommit gets a tree object from a commit hash
func (gom *GitObjectManager) GetTreeFromCommit(repo *gogit.Repository, commitHash plumbing.Hash) (*object.Tree, error) {
	// Get the commit object
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	// Get the tree from the commit
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree from commit: %w", err)
	}

	return tree, nil
}

// EnsureProperHEAD ensures HEAD points to a branch, not directly to a commit
func (gom *GitObjectManager) EnsureProperHEAD(repo *gogit.Repository, branchName string) error {
	// Check current HEAD state
	headRef, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}

	// If HEAD is already pointing to the correct branch, nothing to do
	expectedTarget := plumbing.ReferenceName("refs/heads/" + branchName)
	if headRef.Type() == plumbing.SymbolicReference && headRef.Target() == expectedTarget {
		return nil
	}

	// Check if the target branch exists
	branchRef, err := repo.Reference(expectedTarget, true)
	if err != nil {
		return fmt.Errorf("branch %s does not exist: %w", branchName, err)
	}

	// Update HEAD to point to the branch
	newHeadRef := plumbing.NewSymbolicReference(plumbing.HEAD, branchRef.Name())
	if err := repo.Storer.SetReference(newHeadRef); err != nil {
		return fmt.Errorf("failed to update HEAD to branch %s: %w", branchName, err)
	}

	gom.logger.Info("Updated HEAD to point to branch",
		zap.String("branch", branchName),
		zap.String("previous_type", headRef.Type().String()))

	return nil
}
