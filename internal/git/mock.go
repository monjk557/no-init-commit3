package git

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"
)

// MockRepository is a mock implementation of the Repository interface for testing
type MockRepository struct {
	mu sync.RWMutex

	// path is the repository path
	path string

	// trees maps ref -> []TreeEntry
	trees map[string][]TreeEntry

	// blobs maps path:ref -> []byte
	blobs map[string][]byte

	// commits maps ref -> []Commit
	commits map[string][]Commit

	// branches stores all branches
	branches []Branch

	// tags stores all tags
	tags []Tag

	// remotes stores all remotes
	remotes []Remote

	// closed indicates if the repository has been closed
	closed bool

	// Mock control fields
	GetTreeError     error
	GetBlobError     error
	GetCommitsError  error
	GetBranchesError error
	GetTagsError     error
	GetRemotesError  error
	CloseError       error

	// defaultRef is the default branch reference
	defaultRef string
}

// NewMockRepository creates a new mock repository
func NewMockRepository(path string) *MockRepository {
	return &MockRepository{
		path:       path,
		trees:      make(map[string][]TreeEntry),
		blobs:      make(map[string][]byte),
		commits:    make(map[string][]Commit),
		defaultRef: "main", // 기본값으로 "main" 설정
	}
}

// Path returns the filesystem path of the repository
func (m *MockRepository) Path() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path
}

// GetTree retrieves the directory structure at a given reference and optional path
func (m *MockRepository) GetTree(ctx context.Context, ref string, path string) ([]TreeEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("repository is closed")
	}

	if m.GetTreeError != nil {
		return nil, m.GetTreeError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	entries, exists := m.trees[ref]
	if !exists {
		return nil, &ReferenceNotFoundError{Ref: ref, Path: m.path}
	}

	// Return a copy to prevent modifications
	result := make([]TreeEntry, len(entries))
	copy(result, entries)
	return result, nil
}

// GetTreeWithCommits retrieves tree entries with their latest commit information
func (m *MockRepository) GetTreeWithCommits(ctx context.Context, ref string, path string) ([]TreeEntry, error) {
	// First get the basic tree entries using the existing GetTree method (root path)
	entries, err := m.GetTree(ctx, ref, path)
	if err != nil {
		return nil, err
	}

	// For mock purposes, we'll add some sample commit information to blob entries
	for i, entry := range entries {
		if entry.Type == EntryTypeBlob {
			// Add mock commit information
			entries[i].LatestCommit = &Commit{
				Sha: "mock-commit-hash",
				Author: CommitAuthor{
					Name:  "Mock Author",
					Email: "mock@example.com",
					Date:  m.getMockDate(),
				},
				Committer: CommitAuthor{
					Name:  "Mock Committer",
					Email: "mock@example.com",
					Date:  m.getMockDate(),
				},
				Message:      fmt.Sprintf("Mock commit for %s", entry.Path),
				ParentHashes: []string{"parent-hash"},
				TreeHash:     "mock-tree-hash",
			}
		}
	}

	return entries, nil
}

// getMockDate returns a mock date for testing
func (m *MockRepository) getMockDate() time.Time {
	// Return a fixed date for consistency in tests
	return time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
}

// GetBlob retrieves the contents of a file at a specific path and reference
func (m *MockRepository) GetBlob(ctx context.Context, path, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("repository is closed")
	}

	if m.GetBlobError != nil {
		return nil, m.GetBlobError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	key := fmt.Sprintf("%s:%s", path, ref)
	content, exists := m.blobs[key]
	if !exists {
		return nil, &PathNotFoundError{Path: path, Ref: ref, Repo: m.path}
	}

	// Return a copy to prevent modifications
	result := make([]byte, len(content))
	copy(result, content)
	return result, nil
}

// GetBlobInfo retrieves detailed information about a file including content, size, and line count
func (m *MockRepository) GetBlobInfo(ctx context.Context, path, ref string) (*BlobInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("repository is closed")
	}

	if m.GetBlobError != nil {
		return nil, m.GetBlobError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	key := fmt.Sprintf("%s:%s", path, ref)
	content, exists := m.blobs[key]
	if !exists {
		return nil, &PathNotFoundError{Path: path, Ref: ref, Repo: m.path}
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

	// Return a copy to prevent modifications
	result := make([]byte, len(content))
	copy(result, content)

	return &BlobInfo{
		Content:   result,
		Size:      int64(len(result)),
		LineCount: lineCount,
		Hash:      fmt.Sprintf("mock-hash-%s", key),
		Mode:      "100644",
		Path:      path,
	}, nil
}

// GetCommits retrieves commit history with pagination
func (m *MockRepository) GetCommits(ctx context.Context, ref string, limit, skip int) ([]Commit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("repository is closed")
	}

	if m.GetCommitsError != nil {
		return nil, m.GetCommitsError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	allCommits, exists := m.commits[ref]
	if !exists {
		return nil, &ReferenceNotFoundError{Ref: ref, Path: m.path}
	}

	// Apply pagination
	if skip >= len(allCommits) {
		return []Commit{}, nil
	}

	end := skip + limit
	if end > len(allCommits) {
		end = len(allCommits)
	}

	// Return a copy to prevent modifications
	result := make([]Commit, end-skip)
	copy(result, allCommits[skip:end])
	return result, nil
}

// GetBranches retrieves all branches (mock implementation)
func (m *MockRepository) GetBranches(ctx context.Context) ([]Branch, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("repository is closed")
	}

	if m.GetBranchesError != nil {
		return nil, m.GetBranchesError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Return a copy to prevent modifications
	result := make([]Branch, len(m.branches))
	copy(result, m.branches)
	return result, nil
}

// GetTags retrieves all tags (mock implementation)
func (m *MockRepository) GetTags(ctx context.Context) ([]Tag, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("repository is closed")
	}

	if m.GetTagsError != nil {
		return nil, m.GetTagsError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Return a copy to prevent modifications
	result := make([]Tag, len(m.tags))
	copy(result, m.tags)
	return result, nil
}

// GetRemotes retrieves all remote repositories (mock implementation)
func (m *MockRepository) GetRemotes(ctx context.Context) ([]Remote, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("repository is closed")
	}

	if m.GetRemotesError != nil {
		return nil, m.GetRemotesError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Return a copy to prevent modifications
	result := make([]Remote, len(m.remotes))
	copy(result, m.remotes)
	return result, nil
}

// Close releases any resources held by the repository
func (m *MockRepository) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.CloseError != nil {
		return m.CloseError
	}

	m.closed = true
	return nil
}

// Mock helper methods for setting up test data

// SetTree sets the tree entries for a given reference
func (m *MockRepository) SetTree(ref string, entries []TreeEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trees[ref] = entries
}

// SetBlob sets the blob content for a given path and reference
func (m *MockRepository) SetBlob(path, ref string, content []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s:%s", path, ref)
	m.blobs[key] = content
}

// SetCommits sets the commit history for a given reference
func (m *MockRepository) SetCommits(ref string, commits []Commit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits[ref] = commits
}

// IsClosed returns whether the repository has been closed
func (m *MockRepository) IsClosed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}
