// Package git은 go-git 라이브러리를 사용한 Git 저장소 인터페이스 구현을 제공합니다.
// Repository 인터페이스의 구체적인 구현체로서 Git 작업을 수행합니다.
package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GoGitRepository는 go-git을 사용하여 Repository 인터페이스를 구현합니다.
// 내부적으로 go-git 라이브러리를 사용하여 Git 작업을 수행합니다.
type GoGitRepository struct {
	path string
	repo *git.Repository
	mu   sync.RWMutex // Protects concurrent access to repo
}

// OpenRepository는 지정된 경로에서 Git 저장소를 엽니다.
// 캐시를 사용하지 않는 기본 오픈 방식입니다.
func OpenRepository(path string) (Repository, error) {
	return openRepositoryInternal(path, false)
}

// OpenRepositoryWithCache는 전역 캐시를 사용하여 지정된 경로에서 Git 저장소를 엽니다.
// 성능 향상을 위해 저장소 인스턴스를 캐시합니다.
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
	defer func() { _ = reader.Close() }()

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

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return nil, NewGitError("get_commits", r.path, ref, "repository is closed", nil)
	}

	// Resolve the reference to a commit hash (using unsafe version since we already hold the lock)
	hash, err := r.resolveReferenceUnsafe(ref)
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

		// Check if we've reached the limit
		if limit > 0 && len(commits) >= limit {
			return io.EOF
		}

		commits = append(commits, convertCommit(c))
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
	r.mu.Lock()
	defer r.mu.Unlock()

	// go-git repositories don't need explicit closing
	// but we can set the repo to nil to help with garbage collection
	r.repo = nil
	return nil
}

// Helper methods

// resolveCommit resolves a reference string to a commit object
func (r *GoGitRepository) resolveCommit(ref string) (*object.Commit, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return nil, NewGitError("resolve_commit", r.path, ref, "repository is closed", nil)
	}

	hash, err := r.resolveReferenceUnsafe(ref) // Use unsafe version since we already have the lock
	if err != nil {
		return nil, err
	}

	commit, err := r.repo.CommitObject(hash)
	if err != nil {
		return nil, NewGitError("resolve_commit", r.path, ref, "failed to get commit object", err)
	}

	return commit, nil
}

// resolveReferenceUnsafe is the same as resolveReference but without locking (for internal use)
func (r *GoGitRepository) resolveReferenceUnsafe(ref string) (plumbing.Hash, error) {
	if r.repo == nil {
		return plumbing.ZeroHash, NewGitError("resolve_reference", r.path, ref, "repository is closed", nil)
	}

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

// convertCommit은 go-git 커밋을 내부 Commit 형식으로 변환합니다.
// 커밋 메시지를 정리하고 작성자/커밋터 정보를 설정합니다.
func convertCommit(c *object.Commit) Commit {
	var parentHashes []string
	for _, parent := range c.ParentHashes {
		parentHashes = append(parentHashes, parent.String())
	}

	// Clean commit message by removing trailing newlines and replacing internal newlines with spaces
	message := strings.TrimSpace(c.Message)
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", " ")

	return Commit{
		Sha:     c.Hash.String(),
		Message: message,
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

// GetGoGitRepo returns the underlying go-git repository instance
// This is useful for operations that need direct access to go-git APIs
func (r *GoGitRepository) GetGoGitRepo() *git.Repository {
	return r.repo
}
