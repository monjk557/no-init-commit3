// Package git provides Git object manipulation functionality for REST API operations.
// This package extends the existing git package with object management capabilities.
package git

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"go.uber.org/zap"
)

// TreeBuilder helps build hierarchical Git tree structures
type TreeBuilder struct {
	storer storage.Storer
	logger *logging.Logger
	files  map[string]TreeFileEntry
}

// TreeFileEntry represents a file entry in the tree
type TreeFileEntry struct {
	Hash plumbing.Hash
	Mode filemode.FileMode
}

// NewTreeBuilder creates a new tree builder
func NewTreeBuilder(storer storage.Storer, logger *logging.Logger) *TreeBuilder {
	return &TreeBuilder{
		storer: storer,
		logger: logger,
		files:  make(map[string]TreeFileEntry),
	}
}

// LoadBaseTree loads an existing tree as the base
func (tb *TreeBuilder) LoadBaseTree(baseTree *object.Tree) error {
	return tb.loadTreeRecursive("", baseTree)
}

// loadTreeRecursive loads tree entries recursively
func (tb *TreeBuilder) loadTreeRecursive(prefix string, tree *object.Tree) error {
	for _, entry := range tree.Entries {
		fullPath := entry.Name
		if prefix != "" {
			fullPath = prefix + "/" + entry.Name
		}

		if entry.Mode == filemode.Dir {
			// Load subdirectory
			subTree, err := tree.Tree(entry.Name)
			if err != nil {
				return fmt.Errorf("failed to load subtree %s: %w", entry.Name, err)
			}
			if err := tb.loadTreeRecursive(fullPath, subTree); err != nil {
				return err
			}
		} else {
			// Add file entry
			tb.files[fullPath] = TreeFileEntry{
				Hash: entry.Hash,
				Mode: entry.Mode,
			}
		}
	}
	return nil
}

// HasFile checks if a file exists in the tree
func (tb *TreeBuilder) HasFile(path string) bool {
	_, exists := tb.files[path]
	return exists
}

// AddFile adds a file to the tree
func (tb *TreeBuilder) AddFile(path string, hash plumbing.Hash, mode filemode.FileMode) error {
	tb.files[path] = TreeFileEntry{
		Hash: hash,
		Mode: mode,
	}
	return nil
}

// RemoveFile removes a file from the tree
func (tb *TreeBuilder) RemoveFile(path string) error {
	delete(tb.files, path)
	return nil
}

// BuildTree builds the final tree structure
func (tb *TreeBuilder) BuildTree() (plumbing.Hash, error) {
	return tb.buildTreeRecursive("")
}

// buildTreeRecursive builds a tree for a specific directory path
func (tb *TreeBuilder) buildTreeRecursive(dirPath string) (plumbing.Hash, error) {
	var entries []object.TreeEntry
	subdirs := make(map[string]bool)

	// Find all files and subdirectories in this directory
	for filePath, fileEntry := range tb.files {
		var relativePath string
		if dirPath == "" {
			relativePath = filePath
		} else {
			if !strings.HasPrefix(filePath, dirPath+"/") {
				continue
			}
			relativePath = strings.TrimPrefix(filePath, dirPath+"/")
		}

		// Skip if this file is in a subdirectory
		parts := strings.Split(relativePath, "/")
		if len(parts) > 1 {
			subdirs[parts[0]] = true
			continue
		}

		// Add file entry
		entries = append(entries, object.TreeEntry{
			Name: parts[0],
			Mode: fileEntry.Mode,
			Hash: fileEntry.Hash,
		})
	}

	// Add subdirectory entries
	for subdirName := range subdirs {
		var subdirPath string
		if dirPath == "" {
			subdirPath = subdirName
		} else {
			subdirPath = dirPath + "/" + subdirName
		}

		subdirTreeHash, err := tb.buildTreeRecursive(subdirPath)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to build subtree for %s: %w", subdirPath, err)
		}

		entries = append(entries, object.TreeEntry{
			Name: subdirName,
			Mode: filemode.Dir,
			Hash: subdirTreeHash,
		})
	}

	// Sort entries (Git requirement)
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Name > entries[j].Name {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// Create tree object
	tree := &object.Tree{Entries: entries}

	// Encode and store the tree
	encodedTree := tb.storer.NewEncodedObject()
	if err := tree.Encode(encodedTree); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}

	treeHash, err := tb.storer.SetEncodedObject(encodedTree)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}

	return treeHash, nil
}

// GitObjectManager provides Git object manipulation capabilities for REST API operations
type GitObjectManager struct {
	pathConfig *repository.PathConfig
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

// GitObjectManagerInterface defines the interface for Git object manipulation
type GitObjectManagerInterface interface {
	// OpenRepository opens a Git repository for manipulation
	OpenRepository(ctx context.Context, owner, repo string) (*git.Repository, error)

	// GetRepositoryHead gets the current HEAD reference
	GetRepositoryHead(ctx context.Context, repo *git.Repository) (*plumbing.Reference, error)

	// CreateTreeFromChanges creates a new tree from a list of file changes
	CreateTreeFromChanges(ctx context.Context, repo *git.Repository, baseTree *object.Tree, changes []FileChange) (*TreeManipulationResult, error)

	// CreateCommit creates a new commit with the specified tree and metadata
	CreateCommit(ctx context.Context, repo *git.Repository, treeHash plumbing.Hash, commitInfo CommitInfo) (*CommitResult, error)

	// ValidateFileChanges validates a list of file changes
	ValidateFileChanges(ctx context.Context, changes []FileChange) error

	// GetFileFromTree gets a file from a tree by path
	GetFileFromTree(ctx context.Context, tree *object.Tree, path string) (*object.File, error)

	// ResolveReference resolves a Git reference to a commit hash
	ResolveReference(ctx context.Context, repo *git.Repository, ref string) (plumbing.Hash, error)
}

// NewGitObjectManager creates a new Git object manager
func NewGitObjectManager(pathConfig *repository.PathConfig, logger *logging.Logger) (*GitObjectManager, error) {
	if pathConfig == nil {
		return nil, fmt.Errorf("path configuration is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	return &GitObjectManager{
		pathConfig: pathConfig,
		logger:     logger,
	}, nil
}

// OpenRepository opens a Git repository for manipulation
func (gom *GitObjectManager) OpenRepository(ctx context.Context, owner, repo string) (*git.Repository, error) {
	// Get the repository path using existing path validation
	repoPath, err := repository.GetRepoPath(gom.pathConfig, owner, repo)
	if err != nil {
		gom.logger.Error("Failed to get repository path",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Error(err))
		return nil, fmt.Errorf("invalid repository path: %w", err)
	}

	// Validate repository exists and is accessible
	if err := repository.ValidateRepoPath(gom.pathConfig, repoPath); err != nil {
		gom.logger.Error("Repository path validation failed",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("repository validation failed: %w", err)
	}

	// Open the repository using go-git
	gitRepo, err := git.PlainOpen(repoPath)
	if err != nil {
		gom.logger.Error("Failed to open Git repository",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	gom.logger.Debug("Successfully opened Git repository",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("path", repoPath))

	return gitRepo, nil
}

// GetRepositoryHead gets the current HEAD reference
func (gom *GitObjectManager) GetRepositoryHead(ctx context.Context, repo *git.Repository) (*plumbing.Reference, error) {
	headRef, err := repo.Head()
	if err != nil {
		gom.logger.Error("Failed to get repository HEAD", zap.Error(err))
		return nil, fmt.Errorf("failed to get HEAD reference: %w", err)
	}

	gom.logger.Debug("Retrieved repository HEAD",
		zap.String("ref", headRef.Name().String()),
		zap.String("hash", headRef.Hash().String()))

	return headRef, nil
}

// ResolveReference resolves a Git reference to a commit hash
func (gom *GitObjectManager) ResolveReference(ctx context.Context, repo *git.Repository, ref string) (plumbing.Hash, error) {
	// Try to parse as direct hash first
	if hash := plumbing.NewHash(ref); !hash.IsZero() {
		// Validate that the hash exists in the repository
		_, err := repo.CommitObject(hash)
		if err == nil {
			gom.logger.Debug("Resolved reference as direct hash",
				zap.String("ref", ref),
				zap.String("hash", hash.String()))
			return hash, nil
		}
	}

	// Try to resolve as a reference name
	reference, err := repo.Reference(plumbing.ReferenceName(ref), true)
	if err != nil {
		gom.logger.Error("Failed to resolve reference",
			zap.String("ref", ref),
			zap.Error(err))
		return plumbing.ZeroHash, fmt.Errorf("failed to resolve reference %s: %w", ref, err)
	}

	hash := reference.Hash()
	gom.logger.Debug("Resolved reference",
		zap.String("ref", ref),
		zap.String("resolved_hash", hash.String()))

	return hash, nil
}

// ValidateFileChanges validates a list of file changes
func (gom *GitObjectManager) ValidateFileChanges(ctx context.Context, changes []FileChange) error {
	if len(changes) == 0 {
		return fmt.Errorf("no file changes provided")
	}

	pathValidator := repository.NewGitAPIPathValidator(gom.pathConfig)
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

	gom.logger.Debug("File changes validation passed", zap.Int("change_count", len(changes)))
	return nil
}

// GetFileFromTree gets a file from a tree by path
func (gom *GitObjectManager) GetFileFromTree(ctx context.Context, tree *object.Tree, path string) (*object.File, error) {
	file, err := tree.File(path)
	if err != nil {
		// Check if it's a "file not found" error
		if err == object.ErrFileNotFound {
			gom.logger.Debug("File not found in tree", zap.String("path", path))
			return nil, nil // Return nil without error for missing files
		}
		gom.logger.Error("Failed to get file from tree",
			zap.String("path", path),
			zap.Error(err))
		return nil, fmt.Errorf("failed to get file %s from tree: %w", path, err)
	}

	gom.logger.Debug("Retrieved file from tree",
		zap.String("path", path),
		zap.String("hash", file.Hash.String()))

	return file, nil
}

// CreateTreeFromChanges creates a new tree from a list of file changes
func (gom *GitObjectManager) CreateTreeFromChanges(ctx context.Context, repo *git.Repository, baseTree *object.Tree, changes []FileChange) (*TreeManipulationResult, error) {
	// Validate changes first
	if err := gom.ValidateFileChanges(ctx, changes); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Get the storer to work with objects
	storer := repo.Storer

	// Build a tree structure that handles directories properly
	treeBuilder := NewTreeBuilder(storer, gom.logger)

	// Start with base tree if it exists
	if baseTree != nil {
		if err := treeBuilder.LoadBaseTree(baseTree); err != nil {
			return nil, fmt.Errorf("failed to load base tree: %w", err)
		}
	}

	// Track changes for result
	result := &TreeManipulationResult{
		ChangedFiles: make([]string, 0),
		CreatedFiles: make([]string, 0),
		DeletedFiles: make([]string, 0),
	}

	// Process each file change
	for _, change := range changes {
		switch change.Operation {
		case FileOperationCreate, FileOperationUpdate:
			// Create a blob from the content
			blob := &object.Blob{}
			blob.Size = int64(len(change.Content))

			// Create an encoded object for the blob
			blobReader := strings.NewReader(string(change.Content))
			encodedBlob := &plumbing.MemoryObject{}
			encodedBlob.SetType(plumbing.BlobObject)
			encodedBlob.SetSize(blob.Size)

			// Write content to the encoded object
			writer, err := encodedBlob.Writer()
			if err != nil {
				gom.logger.Error("Failed to create blob writer",
					zap.String("path", change.Path),
					zap.Error(err))
				return nil, fmt.Errorf("failed to create blob writer for %s: %w", change.Path, err)
			}

			if _, err := io.Copy(writer, blobReader); err != nil {
				_ = writer.Close() // errcheck

				gom.logger.Error("Failed to write blob content",
					zap.String("path", change.Path),
					zap.Error(err))
				return nil, fmt.Errorf("failed to write blob content for %s: %w", change.Path, err)
			}
			_ = writer.Close() // errcheck

			// Store the blob object
			blobHash, err := storer.SetEncodedObject(encodedBlob)
			if err != nil {
				gom.logger.Error("Failed to store blob",
					zap.String("path", change.Path),
					zap.Error(err))
				return nil, fmt.Errorf("failed to store blob for %s: %w", change.Path, err)
			}

			// Determine file mode
			mode := filemode.Regular
			if change.Mode != nil {
				mode = *change.Mode
			}

			// Check if this is an update
			isUpdate := treeBuilder.HasFile(change.Path)

			// Add file to tree builder
			if err := treeBuilder.AddFile(change.Path, blobHash, mode); err != nil {
				return nil, fmt.Errorf("failed to add file to tree: %w", err)
			}

			// Track the change
			if isUpdate {
				result.ChangedFiles = append(result.ChangedFiles, change.Path)
			} else {
				result.CreatedFiles = append(result.CreatedFiles, change.Path)
			}

			gom.logger.Debug("Processed file operation",
				zap.String("operation", string(change.Operation)),
				zap.String("path", change.Path),
				zap.String("blob_hash", blobHash.String()),
				zap.Bool("is_update", isUpdate))

		case FileOperationDelete:
			// Check if file exists
			if !treeBuilder.HasFile(change.Path) {
				gom.logger.Warn("Attempted to delete non-existent file",
					zap.String("path", change.Path))
				continue // Skip non-existent files
			}

			// Remove the file from tree builder
			if err := treeBuilder.RemoveFile(change.Path); err != nil {
				return nil, fmt.Errorf("failed to remove file from tree: %w", err)
			}

			result.DeletedFiles = append(result.DeletedFiles, change.Path)

			gom.logger.Debug("Processed file deletion",
				zap.String("path", change.Path))
		}
	}

	// Build the final tree
	treeHash, err := treeBuilder.BuildTree()
	if err != nil {
		return nil, fmt.Errorf("failed to build tree: %w", err)
	}

	result.TreeHash = treeHash

	gom.logger.Debug("Created new tree",
		zap.String("tree_hash", treeHash.String()),
		zap.Int("created_files", len(result.CreatedFiles)),
		zap.Int("changed_files", len(result.ChangedFiles)),
		zap.Int("deleted_files", len(result.DeletedFiles)))

	return result, nil
}

// CreateCommit creates a new commit with the specified tree and metadata
func (gom *GitObjectManager) CreateCommit(ctx context.Context, repo *git.Repository, treeHash plumbing.Hash, commitInfo CommitInfo) (*CommitResult, error) {
	// Validate commit info
	if commitInfo.Message == "" {
		return nil, fmt.Errorf("commit message is required")
	}

	// Set committer to author if not provided
	committer := commitInfo.Author
	if commitInfo.Committer != nil {
		committer = *commitInfo.Committer
	}

	// Ensure timestamps are set
	now := time.Now()
	if commitInfo.Author.When.IsZero() {
		commitInfo.Author.When = now
	}
	if committer.When.IsZero() {
		committer.When = now
	}

	// Create commit object
	commit := &object.Commit{
		Hash:         plumbing.ZeroHash, // Will be calculated when storing
		Author:       commitInfo.Author,
		Committer:    committer,
		Message:      commitInfo.Message,
		TreeHash:     treeHash,
		ParentHashes: commitInfo.Parents,
	}

	// Create encoded object for the commit using the repository storer
	encodedCommit := repo.Storer.NewEncodedObject()
	if err := commit.Encode(encodedCommit); err != nil {
		gom.logger.Error("Failed to encode commit", zap.Error(err))
		return nil, fmt.Errorf("failed to encode commit: %w", err)
	}

	// Store the commit object
	commitHash, err := repo.Storer.SetEncodedObject(encodedCommit)
	if err != nil {
		gom.logger.Error("Failed to store commit object", zap.Error(err))
		return nil, fmt.Errorf("failed to store commit object: %w", err)
	}

	// Create result
	result := &CommitResult{
		Hash:      commitHash,
		TreeHash:  treeHash,
		Message:   commitInfo.Message,
		Author:    commitInfo.Author,
		Committer: committer,
		Parents:   commitInfo.Parents,
	}

	gom.logger.Info("Created new commit",
		zap.String("commit_hash", commitHash.String()),
		zap.String("tree_hash", treeHash.String()),
		zap.String("author", commitInfo.Author.Name),
		zap.String("message", commitInfo.Message),
		zap.Int("parent_count", len(commitInfo.Parents)))

	return result, nil
}
