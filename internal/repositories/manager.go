// Package repositories provides Git object manipulation functionality for REST API operations.
package repositories

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	gogit "github.com/go-git/go-git/v5"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// GitObjectManager provides Git object manipulation capabilities for REST API operations.
// Deprecated: used by merge/overview packages for backward compatibility. New code should use native git.
type GitObjectManager struct {
	pathConfig PathConfig
	logger     *logging.Logger
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

// OpenRepository opens a Git repository for manipulation.
// Deprecated: used by merge/overview packages. New code should use repoPath string + native git.
func (gom *GitObjectManager) OpenRepository(owner, repo string) (*gogit.Repository, error) {
	repoPath, err := GetRepoPath(&gom.pathConfig, owner, repo)
	if err != nil {
		gom.logger.Error("Failed to get repository path",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Error(err))
		return nil, fmt.Errorf("invalid repository path: %w", err)
	}

	if err := ValidateRepoPath(&gom.pathConfig, repoPath); err != nil {
		gom.logger.Error("Repository path validation failed",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("repository validation failed: %w", err)
	}

	repoWrapper, err := OpenRepositorySimple(repoPath)
	if err != nil {
		gom.logger.Error("Failed to open Git repository",
			zap.String("path", repoPath),
			zap.Error(err))
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	return repoWrapper.GetRepo(), nil
}

// GetRepositoryHead gets the current HEAD reference.
// Deprecated: used by merge/overview packages. New code should use native git.
func (gom *GitObjectManager) GetRepositoryHead(repo *gogit.Repository) (*plumbing.Reference, error) {
	headRef, err := repo.Head()
	if err != nil {
		gom.logger.Error("Failed to get repository HEAD", zap.Error(err))
		return nil, fmt.Errorf("failed to get HEAD reference: %w", err)
	}
	return headRef, nil
}

// ResolveReference resolves a Git reference to a commit hash.
// Deprecated: used by merge/overview packages. New code should use native git.
func (gom *GitObjectManager) ResolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	if err := gom.validateRefFormat(ref); err != nil {
		return plumbing.ZeroHash, err
	}

	if ref == "" || ref == "HEAD" {
		headRef, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to get HEAD reference: %w", err)
		}
		return headRef.Hash(), nil
	}

	if strings.HasPrefix(ref, "refs/") {
		return plumbing.ZeroHash, fmt.Errorf("full ref path '%s' not supported, use branch/tag name directly: %w", ref, ErrUnsupportedRef)
	}

	// Branch-First Policy: branch → tag → SHA
	branchRef := "refs/heads/" + ref
	reference, err := repo.Reference(plumbing.ReferenceName(branchRef), true)
	if err == nil {
		return reference.Hash(), nil
	}

	tagRef := "refs/tags/" + ref
	reference, err = repo.Reference(plumbing.ReferenceName(tagRef), true)
	if err == nil {
		tagHash := reference.Hash()
		tagObj, err := repo.TagObject(tagHash)
		if err == nil {
			return tagObj.Target, nil
		}
		return tagHash, nil
	}

	if gom.looksLikeSHA(ref) {
		return gom.resolveSHA(repo, ref)
	}

	return plumbing.ZeroHash, fmt.Errorf("ref '%s' not found: %w", ref, ErrRefNotFound)
}

// GetFileFromTree gets a file from a tree by path.
// Deprecated: used by overview/merge packages. New code should use native git.
func (gom *GitObjectManager) GetFileFromTree(tree *object.Tree, path string) (*object.File, error) {
	file, err := tree.File(path)
	if err != nil {
		if err == object.ErrFileNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get file %s from tree: %w", path, err)
	}
	return file, nil
}

// GetTreeFromCommit gets a tree object from a commit hash.
// Deprecated: used by overview/merge packages. New code should use native git.
func (gom *GitObjectManager) GetTreeFromCommit(repo *gogit.Repository, commitHash plumbing.Hash) (*object.Tree, error) {
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree from commit: %w", err)
	}
	return tree, nil
}

// EnsureProperHEAD ensures HEAD points to a branch, not directly to a commit.
// Deprecated: used by merge packages. New code should use native git symbolic-ref.
func (gom *GitObjectManager) EnsureProperHEAD(repo *gogit.Repository, branchName string) error {
	headRef, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}

	expectedTarget := plumbing.ReferenceName("refs/heads/" + branchName)
	if headRef.Type() == plumbing.SymbolicReference && headRef.Target() == expectedTarget {
		return nil
	}

	branchRef, err := repo.Reference(expectedTarget, true)
	if err != nil {
		return fmt.Errorf("branch %s does not exist: %w", branchName, err)
	}

	newHeadRef := plumbing.NewSymbolicReference(plumbing.HEAD, branchRef.Name())
	if err := repo.Storer.SetReference(newHeadRef); err != nil {
		return fmt.Errorf("failed to update HEAD to branch %s: %w", branchName, err)
	}

	gom.logger.Info("Updated HEAD to point to branch",
		zap.String("branch", branchName),
		zap.String("previous_type", headRef.Type().String()))

	return nil
}

// validateRefFormat validates the basic format of a ref
func (gom *GitObjectManager) validateRefFormat(ref string) error {
	if ref == "" || ref == "HEAD" {
		return nil
	}

	// Remote style ref 금지
	if strings.Contains(ref, "/") && !strings.HasPrefix(ref, "refs/") {
		parts := strings.Split(ref, "/")
		if len(parts) >= 2 {
			remoteNames := []string{"origin", "upstream", "remote", "fork"}
			for _, remoteName := range remoteNames {
				if parts[0] == remoteName {
					return fmt.Errorf("remote style ref '%s' not supported, use branch/tag name directly: %w", ref, ErrUnsupportedRef)
				}
			}
		}
	}

	if strings.ContainsAny(ref, " \t\n\r") {
		return fmt.Errorf("ref contains whitespace: %w", ErrInvalidRef)
	}

	if ref == "." || ref == ".." {
		return fmt.Errorf("ref cannot be '.' or '..': %w", ErrInvalidRef)
	}

	if strings.ContainsAny(ref, "^~@{}") {
		return fmt.Errorf("rev-parse expressions not supported: %w", ErrUnsupportedRef)
	}

	if strings.Contains(ref, ":") && !strings.HasPrefix(ref, "refs/") {
		return fmt.Errorf("tree-ish:path format not supported: %w", ErrUnsupportedRef)
	}

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
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	_, err := hex.DecodeString(ref)
	if err != nil {
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
	return gom.resolveShortSHA(repo, ref)
}

// resolveShortSHA resolves a short SHA by iterating through objects
func (gom *GitObjectManager) resolveShortSHA(repo *gogit.Repository, shortSHA string) (plumbing.Hash, error) {
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
		return plumbing.ZeroHash, fmt.Errorf("ambiguous short SHA '%s' matches %d commits: %w", shortSHA, len(matches), ErrAmbiguousRef)
	}
}
