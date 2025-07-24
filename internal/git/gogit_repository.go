package git

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GoGitRepository implements the Repository interface using go-git
type GoGitRepository struct {
	path string
	repo *git.Repository
}

// OpenRepository opens a Git repository at the specified path
func OpenRepository(path string) (Repository, error) {
	return openRepositoryInternal(path, false)
}

// OpenRepositoryWithCache opens a Git repository at the specified path using the global cache
func OpenRepositoryWithCache(path string) (Repository, error) {
	return openRepositoryInternal(path, true)
}

// openRepositoryInternal is the internal implementation that optionally uses caching
func openRepositoryInternal(path string, useCache bool) (Repository, error) {
	// If caching is enabled, try to get from cache first
	if useCache {
		if cached, found := globalCache.Get(path); found {
			return cached, nil
		}
	}

	// Check if the path exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, &RepositoryNotFoundError{Path: path}
	}

	// Try to open the repository
	repo, err := git.PlainOpen(path)
	if err != nil {
		// Check if it's because the directory is not a git repository
		if err == git.ErrRepositoryNotExists {
			return nil, &InvalidRepositoryError{
				Path:   path,
				Reason: "directory is not a Git repository",
			}
		}

		return nil, NewGitError("open", path, "", "failed to open repository", err)
	}

	// Create our repository wrapper
	gitRepo := &GoGitRepository{
		path: path,
		repo: repo,
	}

	// If caching is enabled, store in cache
	if useCache {
		globalCache.Put(path, gitRepo)
	}

	return gitRepo, nil
}

// Path returns the filesystem path of the repository
func (r *GoGitRepository) Path() string {
	return r.path
}

// GetTree retrieves the directory structure at a given reference
func (r *GoGitRepository) GetTree(ctx context.Context, ref string) ([]TreeEntry, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Resolve the reference to a commit
	commit, err := r.resolveCommit(ref)
	if err != nil {
		return nil, err
	}

	// Get the tree from the commit
	tree, err := commit.Tree()
	if err != nil {
		return nil, NewGitError("get_tree", r.path, ref, "failed to get tree from commit", err)
	}

	// Convert tree entries to our format
	var entries []TreeEntry
	for _, entry := range tree.Entries {
		// Check for context cancellation during iteration
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		entryType := convertFileMode(entry.Mode)
		size := int64(0)

		// For blobs, try to get the size
		if entryType == EntryTypeBlob {
			if blob, err := r.repo.BlobObject(entry.Hash); err == nil {
				size = blob.Size
			}
		}

		entries = append(entries, TreeEntry{
			Path: entry.Name,
			Mode: entry.Mode.String(),
			Type: entryType,
			Hash: entry.Hash.String(),
			Size: size,
		})
	}

	return entries, nil
}

// GetBlob retrieves the contents of a file at a specific path and reference
func (r *GoGitRepository) GetBlob(ctx context.Context, path, ref string) ([]byte, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Resolve the reference to a commit
	commit, err := r.resolveCommit(ref)
	if err != nil {
		return nil, err
	}

	// Get the file from the commit
	file, err := commit.File(path)
	if err != nil {
		if err == object.ErrFileNotFound {
			return nil, &PathNotFoundError{Path: path, Ref: ref, Repo: r.path}
		}
		return nil, NewGitError("get_blob", r.path, ref, fmt.Sprintf("failed to get file %s", path), err)
	}

	// Read the file contents
	reader, err := file.Reader()
	if err != nil {
		return nil, NewGitError("get_blob", r.path, ref, fmt.Sprintf("failed to get reader for file %s", path), err)
	}
	defer reader.Close()

	// Check for context cancellation before reading
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Read all content
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, NewGitError("get_blob", r.path, ref, fmt.Sprintf("failed to read file %s", path), err)
	}

	return content, nil
}

// GetCommits retrieves commit history with pagination
func (r *GoGitRepository) GetCommits(ctx context.Context, ref string, limit, skip int) ([]Commit, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Resolve the reference to a commit hash
	hash, err := r.resolveReference(ref)
	if err != nil {
		return nil, err
	}

	// Get commit iterator starting from the resolved hash
	commitIter, err := r.repo.Log(&git.LogOptions{
		From: hash,
	})
	if err != nil {
		return nil, NewGitError("get_commits", r.path, ref, "failed to get commit log", err)
	}
	defer commitIter.Close()

	var commits []Commit
	current := 0

	err = commitIter.ForEach(func(c *object.Commit) error {
		// Check for context cancellation during iteration
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Skip commits if needed
		if current < skip {
			current++
			return nil
		}

		// Stop if we've reached our limit
		if len(commits) >= limit {
			return io.EOF // Signal to stop iteration
		}

		// Convert to our commit format
		commit := convertCommit(c)
		commits = append(commits, commit)
		current++

		return nil
	})

	if err != nil && err != io.EOF {
		return nil, NewGitError("get_commits", r.path, ref, "failed to iterate commits", err)
	}

	return commits, nil
}

// Close releases any resources held by the repository
func (r *GoGitRepository) Close() error {
	// go-git repositories don't need explicit closing
	// but we can set the repo to nil to help with garbage collection
	r.repo = nil
	return nil
}

// Helper methods

// resolveCommit resolves a reference string to a commit object
func (r *GoGitRepository) resolveCommit(ref string) (*object.Commit, error) {
	hash, err := r.resolveReference(ref)
	if err != nil {
		return nil, err
	}

	commit, err := r.repo.CommitObject(hash)
	if err != nil {
		return nil, NewGitError("resolve_commit", r.path, ref, "failed to get commit object", err)
	}

	return commit, nil
}

// resolveReference resolves a reference string to a hash
func (r *GoGitRepository) resolveReference(ref string) (plumbing.Hash, error) {
	// Try to parse as a direct hash first
	if hash := plumbing.NewHash(ref); !hash.IsZero() && len(ref) >= 4 {
		// Verify the object exists
		if _, err := r.repo.Object(plumbing.AnyObject, hash); err == nil {
			return hash, nil
		}
	}

	// Try to resolve as a reference (branch, tag, etc.)
	reference, err := r.repo.Reference(plumbing.ReferenceName(ref), true)
	if err != nil {
		// Try with refs/heads/ prefix for branches
		if branchRef, branchErr := r.repo.Reference(plumbing.NewBranchReferenceName(ref), true); branchErr == nil {
			return branchRef.Hash(), nil
		}

		// Try with refs/tags/ prefix for tags
		if tagRef, tagErr := r.repo.Reference(plumbing.NewTagReferenceName(ref), true); tagErr == nil {
			return tagRef.Hash(), nil
		}

		// Try HEAD if ref is empty or "HEAD"
		if ref == "" || ref == "HEAD" {
			if headRef, headErr := r.repo.Head(); headErr == nil {
				return headRef.Hash(), nil
			}
		}

		return plumbing.ZeroHash, &ReferenceNotFoundError{Ref: ref, Path: r.path}
	}

	return reference.Hash(), nil
}

// convertFileMode converts go-git file mode to our EntryType
func convertFileMode(mode filemode.FileMode) EntryType {
	switch mode {
	case filemode.Dir: // Directory (040000)
		return EntryTypeTree
	case filemode.Submodule: // Git submodule (160000)
		return EntryTypeCommit
	default: // Regular file, executable, symlink
		return EntryTypeBlob
	}
}

// convertCommit converts a go-git commit to our Commit format
func convertCommit(c *object.Commit) Commit {
	var parentHashes []string
	for _, parent := range c.ParentHashes {
		parentHashes = append(parentHashes, parent.String())
	}

	return Commit{
		Hash:    c.Hash.String(),
		Message: c.Message,
		Author: CommitAuthor{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			Date:  c.Author.When,
		},
		Committer: CommitAuthor{
			Name:  c.Committer.Name,
			Email: c.Committer.Email,
			Date:  c.Committer.When,
		},
		ParentHashes: parentHashes,
		TreeHash:     c.TreeHash.String(),
	}
}
