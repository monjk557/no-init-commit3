package git

import (
	"context"
	"fmt"
	"sync"
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

	// description stores repository description
	description string

	// remotes stores all remotes
	remotes []Remote

	// closed indicates if the repository has been closed
	closed bool

	// Mock control fields
	GetTreeError        error
	GetBlobError        error
	GetCommitsError     error
	GetBranchesError    error
	GetTagsError        error
	GetDescriptionError error
	GetRemotesError     error
	CloseError          error
}

// NewMockRepository creates a new mock repository
func NewMockRepository(path string) *MockRepository {
	return &MockRepository{
		path:    path,
		trees:   make(map[string][]TreeEntry),
		blobs:   make(map[string][]byte),
		commits: make(map[string][]Commit),
	}
}

// Path returns the filesystem path of the repository
func (m *MockRepository) Path() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path
}

// GetTree retrieves the directory structure at a given reference
func (m *MockRepository) GetTree(ctx context.Context, ref string) ([]TreeEntry, error) {
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

// GetDescription retrieves the repository description (mock implementation)
func (m *MockRepository) GetDescription(ctx context.Context) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return "", fmt.Errorf("repository is closed")
	}

	if m.GetDescriptionError != nil {
		return "", m.GetDescriptionError
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	return m.description, nil
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
