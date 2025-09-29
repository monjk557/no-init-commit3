// Package git은 go-git 라이브러리를 사용한 Git 저장소 인터페이스 구현을 제공합니다.
// Repository 인터페이스의 구체적인 구현체로서 Git 작업을 수행합니다.
package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// GetTreeWithCommits retrieves tree entries with their latest commit information
func (r *GoGitRepository) GetTreeWithCommits(ctx context.Context, ref string) ([]TreeEntry, error) {
	// First get the basic tree entries using the existing GetTree method
	entries, err := r.GetTree(ctx, ref)
	if err != nil {
		return nil, err
	}

	// For each blob entry, get the latest commit information
	for i, entry := range entries {
		if entry.Type == EntryTypeBlob {
			latestCommit, err := r.getLatestCommitForFile(ctx, entry.Path, ref)
			if err == nil {
				entries[i].LatestCommit = latestCommit
			}
			// If we can't get the commit info, we continue without it
		}
	}

	return entries, nil
}

func (r *GoGitRepository) GetCommitsForFile(ctx context.Context, filePath, ref string, limit, skip int) ([]Commit, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Get commit iterator for the file
	commitIter, err := r.repo.Log(&git.LogOptions{
		From: r.getCommitHash(ref),
		PathFilter: func(path string) bool {
			return path == filePath
		},
	})
	if err != nil {
		return nil, NewGitError("get_latest_commit_for_file", r.path, filePath, "failed to get commit iterator", err)
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

// getLatestCommitForFile gets the latest commit that modified a specific file
func (r *GoGitRepository) getLatestCommitForFile(ctx context.Context, filePath, ref string) (*Commit, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Get commit iterator for the file
	commitIter, err := r.repo.Log(&git.LogOptions{
		From: r.getCommitHash(ref),
		PathFilter: func(path string) bool {
			return path == filePath
		},
	})
	if err != nil {
		return nil, NewGitError("get_latest_commit_for_file", r.path, filePath, "failed to get commit iterator", err)
	}
	defer commitIter.Close()

	// Get the first (latest) commit
	commit, err := commitIter.Next()
	if err != nil {
		if err == io.EOF {
			return nil, NewGitError("get_latest_commit_for_file", r.path, filePath, "no commits found for file", nil)
		}
		return nil, NewGitError("get_latest_commit_for_file", r.path, filePath, "failed to get commit", err)
	}

	// Convert to our Commit format
	return &Commit{
		Sha: commit.Hash.String(),
		Author: CommitAuthor{
			Name:  commit.Author.Name,
			Email: commit.Author.Email,
			Date:  commit.Author.When,
		},
		Committer: CommitAuthor{
			Name:  commit.Committer.Name,
			Email: commit.Committer.Email,
			Date:  commit.Committer.When,
		},
		Message:      commit.Message,
		ParentHashes: convertParentHashes(commit.ParentHashes),
		TreeHash:     commit.TreeHash.String(),
	}, nil
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

// GetBlobInfo retrieves detailed information about a file including content, size, and line count
func (r *GoGitRepository) GetBlobInfo(ctx context.Context, path, ref string) (*BlobInfo, error) {
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
		return nil, NewGitError("get_blob_info", r.path, ref, fmt.Sprintf("failed to get file %s", path), err)
	}

	// Read the file contents
	reader, err := file.Reader()
	if err != nil {
		return nil, NewGitError("get_blob_info", r.path, ref, fmt.Sprintf("failed to get reader for file %s", path), err)
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
		return nil, NewGitError("get_blob_info", r.path, ref, fmt.Sprintf("failed to read file %s", path), err)
	}

	// Calculate line count
	lineCount := 1 // At least one line even if empty
	if len(content) > 0 {
		lineCount = bytes.Count(content, []byte("\n"))
		// If file doesn't end with newline, add one more line
		if content[len(content)-1] != '\n' {
			lineCount++
		}
	}

	// Get file mode
	mode := file.Mode.String()

	// Get blob hash
	blob := file.Blob

	return &BlobInfo{
		Content:   content,
		Size:      int64(len(content)),
		LineCount: lineCount,
		Hash:      blob.Hash.String(),
		Mode:      mode,
		Path:      path,
	}, nil
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

// GetBranches retrieves all branches (local and remote)
func (r *GoGitRepository) GetBranches(ctx context.Context) ([]Branch, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return nil, NewGitError("get_branches", r.path, "", "repository is closed", nil)
	}

	var branches []Branch

	// Get local branches
	refs, err := r.repo.References()
	if err != nil {
		return nil, NewGitError("get_branches", r.path, "", "failed to get references", err)
	}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		// Check for context cancellation during iteration
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Only process branch references
		if ref.Name().IsBranch() {
			branch := Branch{
				Name:     ref.Name().Short(),
				Hash:     ref.Hash().String(),
				IsRemote: ref.Name().IsRemote(),
			}

			// Check if this is the current branch
			head, err := r.repo.Head()
			if err == nil && head.Name() == ref.Name() {
				branch.IsCurrent = true
			}

			branches = append(branches, branch)
		}

		return nil
	})

	if err != nil {
		return nil, NewGitError("get_branches", r.path, "", "failed to iterate references", err)
	}

	return branches, nil
}

// GetTags retrieves all tags
func (r *GoGitRepository) GetTags(ctx context.Context) ([]Tag, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return nil, NewGitError("get_tags", r.path, "", "repository is closed", nil)
	}

	var tags []Tag

	// Get all tag references
	refs, err := r.repo.References()
	if err != nil {
		return nil, NewGitError("get_tags", r.path, "", "failed to get references", err)
	}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		// Check for context cancellation during iteration
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Only process tag references
		if ref.Name().IsTag() {
			tag := Tag{
				Name: ref.Name().Short(),
				Hash: ref.Hash().String(),
			}

			// Try to get tag object for annotated tags
			tagObj, err := r.repo.TagObject(ref.Hash())
			if err == nil {
				// This is an annotated tag
				tag.IsAnnotated = true
				tag.Message = tagObj.Message
				tag.Tagger = &CommitAuthor{
					Name:  tagObj.Tagger.Name,
					Email: tagObj.Tagger.Email,
					Date:  tagObj.Tagger.When,
				}
			}

			tags = append(tags, tag)
		}

		return nil
	})

	if err != nil {
		return nil, NewGitError("get_tags", r.path, "", "failed to iterate references", err)
	}

	return tags, nil
}

// GetDescription retrieves the repository description
func (r *GoGitRepository) GetDescription(ctx context.Context) (string, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return "", NewGitError("get_description", r.path, "", "repository is closed", nil)
	}

	// Read the description file
	// For bare repositories, description is directly in the repo root
	// For non-bare repositories, description is in .git/description
	var descriptionPath string

	// Check if it's a bare repository by looking for .git directory
	gitDir := filepath.Join(r.path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		// This is a bare repository, description is in the root
		descriptionPath = filepath.Join(r.path, "description")
	} else {
		// This is a non-bare repository, description is in .git/description
		descriptionPath = filepath.Join(r.path, ".git", "description")
	}

	// Check if description file exists
	if _, err := os.Stat(descriptionPath); os.IsNotExist(err) {
		// Return default description if file doesn't exist
		return "Unnamed repository; edit this file 'description' to name the repository.", nil
	}

	// Read the description file
	content, err := os.ReadFile(descriptionPath)
	if err != nil {
		return "", NewGitError("get_description", r.path, "", "failed to read description file", err)
	}

	description := strings.TrimSpace(string(content))

	// Return default if description is empty or default
	if description == "" || description == "Unnamed repository; edit this file 'description' to name the repository." {
		return "", nil
	}

	return description, nil
}

// GetRemotes retrieves all remote repositories
func (r *GoGitRepository) GetRemotes(ctx context.Context) ([]Remote, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return nil, NewGitError("get_remotes", r.path, "", "repository is closed", nil)
	}

	var remotes []Remote

	// Get all remote references
	remoteList, err := r.repo.Remotes()
	if err != nil {
		return nil, NewGitError("get_remotes", r.path, "", "failed to get remotes", err)
	}

	for _, remote := range remoteList {
		remoteInfo := Remote{
			Name:      remote.Config().Name,
			URL:       remote.Config().URLs[0], // Get the first URL
			IsDefault: remote.Config().Name == "origin",
		}
		remotes = append(remotes, remoteInfo)
	}

	return remotes, nil
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

// resolveReference resolves a reference to a commit hash
func (r *GoGitRepository) resolveReference(ref string) (plumbing.Hash, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resolveReferenceUnsafe(ref)
}

// getCommitHash gets the commit hash for a reference
func (r *GoGitRepository) getCommitHash(ref string) plumbing.Hash {
	hash, _ := r.resolveReference(ref)
	return hash
}

// convertParentHashes converts parent hashes to string slice
func convertParentHashes(parents []plumbing.Hash) []string {
	result := make([]string, len(parents))
	for i, parent := range parents {
		result[i] = parent.String()
	}
	return result
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

// GetRefs retrieves the default branch reference (HEAD)
func (r *GoGitRepository) GetRefs(ctx context.Context) (string, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return "", NewGitError("get_refs", r.path, "", "repository is closed", nil)
	}

	// 먼저 go-git의 Head() 메서드 시도
	head, err := r.repo.Head()
	if err == nil {
		// HEAD가 브랜치를 가리키는지 확인
		if head.Name().IsBranch() {
			return head.Name().Short(), nil
		}
		// HEAD가 직접 커밋을 가리키는 경우 (detached HEAD)
		return head.Hash().String(), nil
	}

	// go-git에서 실패한 경우, 직접 HEAD 파일 읽기
	headPath := filepath.Join(r.path, "HEAD")
	headContent, err := os.ReadFile(headPath)
	if err != nil {
		// HEAD 파일도 없는 경우 기본값 반환
		return "main", nil
	}

	// HEAD 파일 내용 파싱
	headStr := strings.TrimSpace(string(headContent))
	if strings.HasPrefix(headStr, "ref: refs/heads/") {
		// ref: refs/heads/master -> master
		branchName := strings.TrimPrefix(headStr, "ref: refs/heads/")
		return branchName, nil
	}

	// HEAD가 직접 커밋 해시를 가리키는 경우
	if len(headStr) == 40 { // SHA-1 해시 길이
		return headStr, nil
	}

	// 기본값 반환
	return "main", nil
}

// GetGoGitRepo returns the underlying go-git repository instance
// This is useful for operations that need direct access to go-git APIs
func (r *GoGitRepository) GetGoGitRepo() *git.Repository {
	return r.repo
}
