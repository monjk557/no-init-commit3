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
	"sort"
	"strings"
	"sync"

	"github.com/go-enry/go-enry/v2"
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

// GetTree retrieves the directory structure at a given reference and optional path
func (r *GoGitRepository) GetTree(ctx context.Context, ref string, path string) ([]TreeEntry, error) {
	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 레퍼런스를 커밋으로 해석
	commit, err := r.resolveCommit(ref)
	if err != nil {
		return nil, err
	}

	// 커밋에서 트리 가져오기
	tree, err := commit.Tree()
	if err != nil {
		return nil, NewGitError("get_tree", r.path, ref, "failed to get tree from commit", err)
	}

	// 경로가 제공된 경우 하위 디렉토리로 이동
	if path != "" && path != "/" {
		// go-git의 tree.Tree()는 상대 경로를 기대하므로 앞의 슬래시 제거
		cleanPath := strings.TrimPrefix(filepath.Clean(path), "/")
		subTree, err := tree.Tree(cleanPath)
		if err != nil {
			// 경로를 찾을 수 없는 에러인지 확인
			if err == object.ErrDirectoryNotFound || err == object.ErrEntryNotFound {
				return nil, &PathNotFoundError{Path: path, Ref: ref, Repo: r.path}
			}
			return nil, NewGitError("get_tree", r.path, ref, fmt.Sprintf("failed to get subdirectory tree at path %s", path), err)
		}
		tree = subTree
	}

	// 트리 항목들을 우리 포맷으로 변환
	var entries []TreeEntry
	for _, entry := range tree.Entries {
		// 반복 중 컨텍스트 취소 확인
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		entryType := convertFileMode(entry.Mode)
		size := int64(0)

		// blob인 경우 크기를 가져오기 시도
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

// GetTreeWithCommits는 트리 엔트리들과 각 파일의 최신 커밋 정보를 가져옵니다
func (r *GoGitRepository) GetTreeWithCommits(ctx context.Context, ref string, path string) ([]TreeEntry, error) {
	// 먼저 기본 트리 엔트리들을 GetTree 메서드로 가져오기
	entries, err := r.GetTree(ctx, ref, path)
	if err != nil {
		return nil, err
	}

	// 현재 경로 정규화 (전체 경로 구성용)
	normalizedPath := strings.Trim(path, "/")
	if normalizedPath != "" {
		normalizedPath = normalizedPath + "/"
	}

	// 각 엔트리에 대해 최신 커밋 정보 가져오기
	for i, entry := range entries {
		// 전체 경로 구성 (현재 경로 + 엔트리 이름)
		fullPath := entry.Path
		if normalizedPath != "" {
			fullPath = normalizedPath + entry.Path
		}

		if entry.Type == EntryTypeBlob {
			// Blob(파일)인 경우: 파일의 최신 커밋 가져오기
			latestCommit, err := r.getLatestCommitForFile(ctx, fullPath, ref)
			if err == nil {
				entries[i].LatestCommit = latestCommit
			}
			// 커밋 정보를 가져올 수 없는 경우에도 계속 진행
		} else if entry.Type == EntryTypeTree {
			// Tree(디렉토리)인 경우: 디렉토리 내 모든 파일 중 가장 최근 커밋 가져오기
			latestCommit, err := r.getLatestCommitForDirectory(ctx, fullPath, ref)
			if err == nil {
				entries[i].LatestCommit = latestCommit
			}
			// 커밋 정보를 가져올 수 없는 경우에도 계속 진행
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

// getLatestCommitForDirectory gets the latest commit that modified any file within a directory
// 디렉토리 내 모든 파일 중 가장 최근에 수정된 커밋을 반환합니다
func (r *GoGitRepository) getLatestCommitForDirectory(ctx context.Context, dirPath, ref string) (*Commit, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 디렉토리 경로 정규화 (앞뒤 슬래시 정리)
	normalizedDir := strings.Trim(dirPath, "/")
	if normalizedDir != "" {
		normalizedDir = normalizedDir + "/"
	}

	// Get commit iterator for all files in the directory
	commitIter, err := r.repo.Log(&git.LogOptions{
		From: r.getCommitHash(ref),
		PathFilter: func(path string) bool {
			// 경로가 디렉토리로 시작하는지 확인
			if normalizedDir == "" {
				// 루트 디렉토리인 경우 모든 파일 매칭
				return true
			}
			// 경로가 정규화된 디렉토리 경로로 시작하는지 확인
			return strings.HasPrefix(path, normalizedDir)
		},
	})
	if err != nil {
		return nil, NewGitError("get_latest_commit_for_directory", r.path, dirPath, "failed to get commit iterator", err)
	}
	defer commitIter.Close()

	// Get the first (latest) commit
	commit, err := commitIter.Next()
	if err != nil {
		if err == io.EOF {
			return nil, NewGitError("get_latest_commit_for_directory", r.path, dirPath, "no commits found for directory", nil)
		}
		return nil, NewGitError("get_latest_commit_for_directory", r.path, dirPath, "failed to get commit", err)
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

// GetGoGitRepo returns the underlying go-git repository instance
// This is useful for operations that need direct access to go-git APIs
func (r *GoGitRepository) GetGoGitRepo() *git.Repository {
	return r.repo
}

// GetLanguageDistribution calculates language distribution for files in a specific reference
func (r *GoGitRepository) GetLanguageDistribution(ctx context.Context, ref string) (*LanguageDistribution, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.repo == nil {
		return nil, NewGitError("get_language_distribution", r.path, ref, "repository is closed", nil)
	}

	// Resolve the reference to a commit
	commit, err := r.resolveCommit(ref)
	if err != nil {
		return nil, err
	}

	// Get the tree from the commit
	tree, err := commit.Tree()
	if err != nil {
		return nil, NewGitError("get_language_distribution", r.path, ref, "failed to get tree from commit", err)
	}

	// Language statistics map: language -> {bytes, files}
	langStats := make(map[string]struct {
		bytes int64
		files int
	})

	var totalBytes int64
	var totalFiles int

	// Iterate through all files in the repository
	err = tree.Files().ForEach(func(file *object.File) error {
		// Check for context cancellation during iteration
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Skip binary files and very large files (over 1MB)
		if file.Size > 1024*1024 {
			return nil
		}

		// Get file content to detect language
		reader, err := file.Reader()
		if err != nil {
			// Skip files that can't be read
			return nil
		}

		content, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			// Skip files that can't be read
			return nil
		}

		// Check if file is binary
		if enry.IsBinary(content) {
			return nil
		}

		// Detect language using go-enry
		language := enry.GetLanguage(file.Name, content)

		// If no language detected, try to detect by filename only
		if language == "" {
			language, _ = enry.GetLanguageByFilename(file.Name)
		}

		// If still no language, skip this file
		if language == "" {
			return nil
		}

		// Update statistics
		stats := langStats[language]
		stats.bytes += file.Size
		stats.files++
		langStats[language] = stats

		totalBytes += file.Size
		totalFiles++

		return nil
	})

	if err != nil && err != io.EOF {
		return nil, NewGitError("get_language_distribution", r.path, ref, "failed to iterate files", err)
	}

	// Convert map to slice and calculate percentages
	var languages []LanguageStats
	for lang, stats := range langStats {
		percentage := float64(0)
		if totalBytes > 0 {
			percentage = (float64(stats.bytes) / float64(totalBytes)) * 100
		}

		languages = append(languages, LanguageStats{
			Language:   lang,
			Bytes:      stats.bytes,
			Percentage: percentage,
			Files:      stats.files,
		})
	}

	// Sort by bytes (descending)
	sort.Slice(languages, func(i, j int) bool {
		return languages[i].Bytes > languages[j].Bytes
	})

	return &LanguageDistribution{
		Languages:  languages,
		TotalBytes: totalBytes,
		TotalFiles: totalFiles,
	}, nil
}
