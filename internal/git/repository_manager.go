package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// repositoryWrapper wraps go-git repository to implement our Repository interface
type repositoryWrapper struct {
	repo *gogit.Repository
	path string
}

// Path returns the filesystem path of the repository
func (rw *repositoryWrapper) Path() string {
	return rw.path
}

// GetTree retrieves the directory structure at a given reference
func (rw *repositoryWrapper) GetTree(ctx context.Context, ref string) ([]TreeEntry, error) {
	// This is a simplified implementation - full implementation would be more complex
	return nil, fmt.Errorf("GetTree not implemented in repository wrapper")
}

// GetBlob retrieves the contents of a file at a specific path and reference
func (rw *repositoryWrapper) GetBlob(ctx context.Context, path, ref string) ([]byte, error) {
	// This is a simplified implementation - full implementation would be more complex
	return nil, fmt.Errorf("GetBlob not implemented in repository wrapper")
}

// GetCommits retrieves commit history with pagination
func (rw *repositoryWrapper) GetCommits(ctx context.Context, ref string, limit, skip int) ([]Commit, error) {
	// This is a simplified implementation - full implementation would be more complex
	return nil, fmt.Errorf("GetCommits not implemented in repository wrapper")
}

// Close releases any resources held by the repository
func (rw *repositoryWrapper) Close() error {
	// go-git repositories don't need explicit closing
	return nil
}

// DefaultRepositoryManager implements the RepositoryManager interface
type DefaultRepositoryManager struct {
	pathConfig    *repository.PathConfig
	objectManager *GitObjectManager
	logger        *logging.Logger
}

// NewRepositoryManager creates a new repository manager
func NewRepositoryManager(pathConfig *repository.PathConfig, logger *logging.Logger) (*DefaultRepositoryManager, error) {
	objectManager, err := NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	return &DefaultRepositoryManager{
		pathConfig:    pathConfig,
		objectManager: objectManager,
		logger:        logger,
	}, nil
}

// CreateRepository creates a new Git repository at the specified path
func (rm *DefaultRepositoryManager) CreateRepository(ctx context.Context, options *CreateRepositoryOptions) (*RepositoryInfo, error) {
	if options == nil {
		return nil, fmt.Errorf("options cannot be nil")
	}

	if options.Path == "" {
		return nil, fmt.Errorf("repository path cannot be empty")
	}

	// Set defaults
	if options.DefaultBranch == "" {
		options.DefaultBranch = "main"
	}

	rm.logger.Info("Creating new repository",
		zap.String("path", options.Path),
		zap.Bool("bare", options.Bare),
		zap.String("default_branch", options.DefaultBranch),
		zap.Bool("initial_commit", options.InitialCommit))

	// Check if repository already exists
	exists, err := rm.RepositoryExists(ctx, options.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to check repository existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("repository already exists at path: %s", options.Path)
	}

	// Validate path
	if err := repository.ValidateRepoPath(rm.pathConfig, options.Path); err != nil {
		return nil, fmt.Errorf("invalid repository path: %w", err)
	}

	// Create directory structure
	if err := os.MkdirAll(filepath.Dir(options.Path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory structure: %w", err)
	}

	// Initialize Git repository
	gitRepo, err := gogit.PlainInit(options.Path, options.Bare)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git repository: %w", err)
	}

	// Set repository description if provided
	if options.Description != "" {
		descPath := filepath.Join(options.Path, "description")
		if err := os.WriteFile(descPath, []byte(options.Description+"\n"), 0644); err != nil {
			rm.logger.Warn("Failed to set repository description",
				zap.String("path", options.Path),
				zap.Error(err))
		}
	}

	// Create repository info
	repoInfo := &RepositoryInfo{
		Path:          options.Path,
		Name:          rm.extractRepositoryName(options.Path),
		Description:   options.Description,
		DefaultBranch: options.DefaultBranch,
		IsBare:        options.Bare,
		CreatedAt:     time.Now(),
		LastModified:  time.Now(),
	}

	// Create initial commit if requested
	if options.InitialCommit {
		commitHash, err := rm.createInitialCommit(gitRepo, options)
		if err != nil {
			rm.logger.Warn("Failed to create initial commit",
				zap.String("path", options.Path),
				zap.Error(err))
		} else {
			rm.logger.Info("Created initial commit",
				zap.String("path", options.Path),
				zap.String("commit_hash", commitHash.String()))
		}
	}

	// Update repository statistics
	if err := rm.updateRepositoryStats(ctx, repoInfo); err != nil {
		rm.logger.Warn("Failed to update repository statistics",
			zap.String("path", options.Path),
			zap.Error(err))
	}

	rm.logger.Info("Repository created successfully",
		zap.String("path", options.Path),
		zap.String("name", repoInfo.Name))

	return repoInfo, nil
}

// OpenRepository opens an existing Git repository
func (rm *DefaultRepositoryManager) OpenRepository(ctx context.Context, path string) (Repository, error) {
	// For now, we'll return a simple wrapper around go-git repository
	// This can be extended later to implement our Repository interface properly
	gitRepo, err := gogit.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Return a wrapper that implements our Repository interface
	return &repositoryWrapper{repo: gitRepo, path: path}, nil
}

// RepositoryExists checks if a repository exists at the given path
func (rm *DefaultRepositoryManager) RepositoryExists(ctx context.Context, path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("path cannot be empty")
	}

	// Check if directory exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to check path: %w", err)
	}

	// Check if it's a valid Git repository
	_, err := gogit.PlainOpen(path)
	if err != nil {
		// If it's not a valid git repository, return false without error
		return false, nil
	}

	return true, nil
}

// DeleteRepository removes a repository from the filesystem
func (rm *DefaultRepositoryManager) DeleteRepository(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// Validate path to prevent accidental deletion outside repo root
	if err := repository.ValidateRepoPath(rm.pathConfig, path); err != nil {
		return fmt.Errorf("invalid repository path: %w", err)
	}

	rm.logger.Info("Deleting repository",
		zap.String("path", path))

	// Check if repository exists
	exists, err := rm.RepositoryExists(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to check repository existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("repository does not exist at path: %s", path)
	}

	// Remove the repository directory
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("failed to remove repository directory: %w", err)
	}

	rm.logger.Info("Repository deleted successfully",
		zap.String("path", path))

	return nil
}

// ListRepositories returns a list of repositories in a directory
func (rm *DefaultRepositoryManager) ListRepositories(ctx context.Context, rootPath string) ([]RepositoryInfo, error) {
	if rootPath == "" {
		rootPath = rm.pathConfig.GetRepoRoot()
	}

	rm.logger.Debug("Listing repositories",
		zap.String("root_path", rootPath))

	var repositories []RepositoryInfo

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Skip directories we can't access
			return nil
		}

		// Skip if not a directory
		if !info.IsDir() {
			return nil
		}

		// Check if this is a Git repository
		exists, err := rm.RepositoryExists(ctx, path)
		if err != nil {
			// Skip directories that cause errors
			return nil
		}

		if exists {
			repoInfo := &RepositoryInfo{
				Path:         path,
				Name:         rm.extractRepositoryName(path),
				IsBare:       rm.isBareRepository(path),
				CreatedAt:    info.ModTime(),
				LastModified: info.ModTime(),
			}

			// Try to get description
			descPath := filepath.Join(path, "description")
			if descData, err := os.ReadFile(descPath); err == nil {
				repoInfo.Description = strings.TrimSpace(string(descData))
			}

			// Update statistics
			if err := rm.updateRepositoryStats(ctx, repoInfo); err != nil {
				rm.logger.Debug("Failed to update repository statistics",
					zap.String("path", path),
					zap.Error(err))
			}

			repositories = append(repositories, *repoInfo)

			// Don't recurse into Git repositories
			return filepath.SkipDir
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk repository directory: %w", err)
	}

	rm.logger.Debug("Found repositories",
		zap.String("root_path", rootPath),
		zap.Int("count", len(repositories)))

	return repositories, nil
}

// createInitialCommit creates an initial commit in the repository
func (rm *DefaultRepositoryManager) createInitialCommit(gitRepo *gogit.Repository, options *CreateRepositoryOptions) (plumbing.Hash, error) {
	storer := gitRepo.Storer

	// Prepare initial files
	initialFiles := options.InitialFiles
	if len(initialFiles) == 0 {
		// Create default README
		initialFiles = map[string]string{
			"README.md": fmt.Sprintf("# Repository\n\n%s\n\nCreated: %s\n",
				options.Description, time.Now().Format(time.RFC3339)),
		}
	}

	// Create blobs for initial files
	var treeEntries []object.TreeEntry
	for filename, content := range initialFiles {
		// Create blob object
		obj := storer.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		obj.SetSize(int64(len(content)))

		writer, err := obj.Writer()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to create blob writer for %s: %w", filename, err)
		}

		_, err = writer.Write([]byte(content))
		if err != nil {
			writer.Close()
			return plumbing.ZeroHash, fmt.Errorf("failed to write blob content for %s: %w", filename, err)
		}

		err = writer.Close()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to close blob writer for %s: %w", filename, err)
		}

		blobHash, err := storer.SetEncodedObject(obj)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to store blob for %s: %w", filename, err)
		}

		// Add to tree entries
		treeEntries = append(treeEntries, object.TreeEntry{
			Name: filename,
			Mode: 0644,
			Hash: blobHash,
		})
	}

	// Create tree object
	tree := &object.Tree{Entries: treeEntries}
	treeObj := storer.NewEncodedObject()
	err := tree.Encode(treeObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}

	treeHash, err := storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}

	// Set default author if not provided
	author := options.Author
	if author == nil {
		author = &CommitAuthor{
			Name:  "CollabOps Git Executor",
			Email: "git-executor@collabops.dev",
			Date:  time.Now(),
		}
	}

	// Create initial commit
	commit := &object.Commit{
		Message:  "Initial commit",
		TreeHash: treeHash,
		Author: object.Signature{
			Name:  author.Name,
			Email: author.Email,
			When:  author.Date,
		},
		Committer: object.Signature{
			Name:  author.Name,
			Email: author.Email,
			When:  author.Date,
		},
	}

	commitObj := storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode commit: %w", err)
	}

	commitHash, err := storer.SetEncodedObject(commitObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store commit: %w", err)
	}

	// Create default branch reference
	branchRef := plumbing.NewHashReference(
		plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", options.DefaultBranch)),
		commitHash,
	)
	err = storer.SetReference(branchRef)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to create branch reference: %w", err)
	}

	// Set HEAD to point to the default branch
	headRef := plumbing.NewSymbolicReference(
		plumbing.HEAD,
		plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", options.DefaultBranch)),
	)
	err = storer.SetReference(headRef)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to set HEAD reference: %w", err)
	}

	return commitHash, nil
}

// extractRepositoryName extracts the repository name from a path
func (rm *DefaultRepositoryManager) extractRepositoryName(path string) string {
	name := filepath.Base(path)
	// Remove .git suffix if present
	return strings.TrimSuffix(name, ".git")
}

// isBareRepository checks if a repository is bare
func (rm *DefaultRepositoryManager) isBareRepository(path string) bool {
	// Check for presence of .git directory (indicates non-bare)
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return false
	}

	// Check for typical bare repository files
	configPath := filepath.Join(path, "config")
	headPath := filepath.Join(path, "HEAD")

	_, configErr := os.Stat(configPath)
	_, headErr := os.Stat(headPath)

	return configErr == nil && headErr == nil
}

// updateRepositoryStats updates repository statistics
func (rm *DefaultRepositoryManager) updateRepositoryStats(ctx context.Context, repoInfo *RepositoryInfo) error {
	// Open repository to get statistics
	gitRepo, err := gogit.PlainOpen(repoInfo.Path)
	if err != nil {
		return fmt.Errorf("failed to open repository for stats: %w", err)
	}

	// Get repository size
	if size, err := rm.getDirectorySize(repoInfo.Path); err == nil {
		repoInfo.Size = size
	}

	// Get default branch
	head, err := gitRepo.Head()
	if err == nil && head.Name().IsBranch() {
		repoInfo.DefaultBranch = head.Name().Short()
	}

	// Count references
	refs, err := gitRepo.References()
	if err == nil {
		branchCount := 0
		tagCount := 0

		err = refs.ForEach(func(ref *plumbing.Reference) error {
			if ref.Name().IsBranch() {
				branchCount++
			} else if ref.Name().IsTag() {
				tagCount++
			}
			return nil
		})

		if err == nil {
			repoInfo.BranchCount = branchCount
			repoInfo.TagCount = tagCount
		}
	}

	// Count commits (this can be expensive for large repos)
	if head != nil {
		commitIter, err := gitRepo.Log(&gogit.LogOptions{From: head.Hash()})
		if err == nil {
			commitCount := 0
			err = commitIter.ForEach(func(c *object.Commit) error {
				commitCount++
				return nil
			})
			if err == nil {
				repoInfo.CommitCount = commitCount
			}
		}
	}

	return nil
}

// getDirectorySize calculates the total size of a directory
func (rm *DefaultRepositoryManager) getDirectorySize(path string) (int64, error) {
	var size int64

	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files that cause errors
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})

	return size, err
}
