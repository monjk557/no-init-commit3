// Package git provides Git object manipulation functionality for REST API operations.
// This package extends the existing git package with object management capabilities.
package repositories

import (
	"encoding/hex"
	"fmt"
	"strings"

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

	// Open the repository using cache
	repoWrapper, err := OpenRepositorySimple(repoPath)
	if err != nil {
		gom.logger.Error("Failed to open Git repository",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get the underlying *gogit.Repository from wrapper
	gitRepo := repoWrapper.GetRepo()
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
// 에러 반환 규칙 (docs/test_guides/test_cases.md - 0.1 Ref Input Contract):
// - ErrInvalidRef: ref 문법 오류 (빈 문자열, 공백, hex 아님 등) -> 400
// - ErrUnsupportedRef: 지원하지 않는 ref 형식 (refs/heads/*, HEAD 등) -> 400
// - ErrRefNotFound: branch/tag가 존재하지 않음 -> 404
// - ErrObjectNotFound: commit SHA가 존재하지 않음 -> 404
func (gom *GitObjectManager) ResolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	// Step 1: Basic validation (400 errors)
	if err := gom.validateRefFormat(ref); err != nil {
		return plumbing.ZeroHash, err
	}

	// Handle empty ref - it resolves to HEAD (current branch)
	if ref == "" {
		headRef, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to get HEAD reference: %w", err)
		}
		return headRef.Hash(), nil
	}

	// Step 2: Branch-First Policy (docs/collabops-git/COLLABOPS_RUNTIME_POLICIES.md)
	// 해석 순서: (1) branch/tag name → (2) commit SHA
	// SHA처럼 보이더라도 branch/tag를 먼저 시도

	// refs/ 로 시작하면 full ref path로 처리 (UnsupportedRef)
	if strings.HasPrefix(ref, "refs/") {
		return plumbing.ZeroHash, fmt.Errorf("full ref path '%s' not supported, use branch/tag name directly: %w", ref, ErrUnsupportedRef)
	}

	// Try as branch name first
	branchRef := "refs/heads/" + ref
	reference, err := repo.Reference(plumbing.ReferenceName(branchRef), true)
	if err == nil {
		return reference.Hash(), nil
	}

	// Try as tag name
	tagRef := "refs/tags/" + ref
	reference, err = repo.Reference(plumbing.ReferenceName(tagRef), true)
	if err == nil {
		// Tag can point to a commit directly (lightweight tag) or a tag object (annotated tag)
		// For annotated tags, we need to resolve the target
		tagHash := reference.Hash()
		tagObj, err := repo.TagObject(tagHash)
		if err == nil {
			// Annotated tag - return the target commit hash
			return tagObj.Target, nil
		}
		// Lightweight tag - return the hash directly
		return tagHash, nil
	}

	// Step 3: Branch/Tag not found, try SHA if it looks like one
	if gom.looksLikeSHA(ref) {
		return gom.resolveSHA(repo, ref)
	}

	// Neither branch, tag, nor valid SHA format
	return plumbing.ZeroHash, fmt.Errorf("ref '%s' not found: %w", ref, ErrRefNotFound)
}

// validateRefFormat validates the basic format of a ref
// 반환: ErrInvalidRef (공백, 특수문자 등), ErrUnsupportedRef (지원하지 않는 형식)
func (gom *GitObjectManager) validateRefFormat(ref string) error {
	// Empty ref는 HEAD로 처리되므로 허용
	if ref == "" {
		return nil
	}

	// HEAD는 지원하지 않음 (명시적 ref 입력으로 사용 금지)
	if ref == "HEAD" {
		return fmt.Errorf("HEAD ref not supported, use branch/tag name directly: %w", ErrUnsupportedRef)
	}

	// Remote style ref 금지 (origin/main, upstream/feature 등)
	if strings.Contains(ref, "/") && !strings.HasPrefix(ref, "refs/") {
		// origin/main, upstream/feature 같은 형식 체크
		parts := strings.Split(ref, "/")
		if len(parts) >= 2 {
			// 일반적인 remote 이름 패턴 체크
			remoteNames := []string{"origin", "upstream", "remote", "fork"}
			for _, remoteName := range remoteNames {
				if parts[0] == remoteName {
					return fmt.Errorf("remote style ref '%s' not supported, use branch/tag name directly: %w", ref, ErrUnsupportedRef)
				}
			}
		}
	}

	// 공백/개행 포함 검사
	if strings.ContainsAny(ref, " \t\n\r") {
		return fmt.Errorf("ref contains whitespace: %w", ErrInvalidRef)
	}

	// 빈 문자열로 취급되는 특수 케이스
	if ref == "." || ref == ".." {
		return fmt.Errorf("ref cannot be '.' or '..': %w", ErrInvalidRef)
	}

	// rev-parse 표현식 금지 (main^, main~1 등)
	if strings.ContainsAny(ref, "^~@{}") {
		return fmt.Errorf("rev-parse expressions not supported: %w", ErrUnsupportedRef)
	}

	// tree-ish:path 문법 금지
	if strings.Contains(ref, ":") && !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("tree-ish:path format not supported: %w", ErrUnsupportedRef)
	}

	// 로컬 state ref 금지
	localStateRefs := []string{"FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REBASE_HEAD"}
	for _, sr := range localStateRefs {
		if ref == sr {
			return fmt.Errorf("local state ref '%s' not supported: %w", ref, ErrUnsupportedRef)
		}
	}

	return nil
}

// looksLikeSHA checks if a ref looks like a SHA (hex string of valid length)
func (gom *GitObjectManager) looksLikeSHA(ref string) bool {
	// Full SHA: 40 hex chars, Short SHA: 7+ hex chars
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}

	// Check if all characters are valid hex
	_, err := hex.DecodeString(ref)
	if err != nil {
		// For odd-length strings, try padding
		if len(ref)%2 == 1 {
			_, err = hex.DecodeString(ref + "0")
		}
		if err != nil {
			return false
		}
	}

	return true
}

// resolveSHA resolves a full or short SHA to a commit hash
func (gom *GitObjectManager) resolveSHA(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	// Full SHA (40 chars)
	if len(ref) == 40 {
		hash := plumbing.NewHash(ref)
		if hash.IsZero() {
			return plumbing.ZeroHash, fmt.Errorf("invalid SHA format '%s': %w", ref, ErrInvalidRef)
		}
		_, err := repo.CommitObject(hash)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("commit '%s' not found: %w", ref, ErrObjectNotFound)
		}
		return hash, nil
	}

	// Short SHA (7-39 chars) - need to find matching commit
	return gom.resolveShortSHA(repo, ref)
}

// resolveShortSHA resolves a short SHA by iterating through objects
func (gom *GitObjectManager) resolveShortSHA(repo *gogit.Repository, shortSHA string) (plumbing.Hash, error) {
	// Iterate through all commits to find matching prefix
	iter, err := repo.CommitObjects()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get commit objects: %w", err)
	}
	defer iter.Close()

	var matches []plumbing.Hash
	prefix := strings.ToLower(shortSHA)

	err = iter.ForEach(func(c *object.Commit) error {
		hashStr := c.Hash.String()
		if strings.HasPrefix(hashStr, prefix) {
			matches = append(matches, c.Hash)
		}
		return nil
	})
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to iterate commits: %w", err)
	}

	switch len(matches) {
	case 0:
		return plumbing.ZeroHash, fmt.Errorf("commit '%s' not found: %w", shortSHA, ErrObjectNotFound)
	case 1:
		return matches[0], nil
	default:
		// Multiple matches - ambiguous short SHA
		return plumbing.ZeroHash, fmt.Errorf("ambiguous short SHA '%s' matches %d commits: %w", shortSHA, len(matches), ErrAmbiguousRef)
	}
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
