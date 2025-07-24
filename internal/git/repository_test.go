package git

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestEntryType_Constants(t *testing.T) {
	tests := []struct {
		name      string
		entryType EntryType
		expected  string
	}{
		{"Blob type", EntryTypeBlob, "blob"},
		{"Tree type", EntryTypeTree, "tree"},
		{"Commit type", EntryTypeCommit, "commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.entryType) != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, string(tt.entryType))
			}
		})
	}
}

func TestTreeEntry_JSONSerialization(t *testing.T) {
	entry := TreeEntry{
		Path: "src/main.go",
		Mode: "100644",
		Type: EntryTypeBlob,
		Hash: "abc123def456",
		Size: 1024,
	}

	// Test marshaling
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Failed to marshal TreeEntry: %v", err)
	}

	// Test unmarshaling
	var unmarshaled TreeEntry
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal TreeEntry: %v", err)
	}

	// Verify all fields
	if unmarshaled.Path != entry.Path {
		t.Errorf("Path mismatch: expected %q, got %q", entry.Path, unmarshaled.Path)
	}
	if unmarshaled.Mode != entry.Mode {
		t.Errorf("Mode mismatch: expected %q, got %q", entry.Mode, unmarshaled.Mode)
	}
	if unmarshaled.Type != entry.Type {
		t.Errorf("Type mismatch: expected %q, got %q", entry.Type, unmarshaled.Type)
	}
	if unmarshaled.Hash != entry.Hash {
		t.Errorf("Hash mismatch: expected %q, got %q", entry.Hash, unmarshaled.Hash)
	}
	if unmarshaled.Size != entry.Size {
		t.Errorf("Size mismatch: expected %d, got %d", entry.Size, unmarshaled.Size)
	}
}

func TestCommit_JSONSerialization(t *testing.T) {
	testTime := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)

	commit := Commit{
		Hash:    "abc123def456",
		Message: "Initial commit",
		Author: CommitAuthor{
			Name:  "John Doe",
			Email: "john@example.com",
			Date:  testTime,
		},
		Committer: CommitAuthor{
			Name:  "Jane Doe",
			Email: "jane@example.com",
			Date:  testTime,
		},
		ParentHashes: []string{"parent1", "parent2"},
		TreeHash:     "tree123",
	}

	// Test marshaling
	data, err := json.Marshal(commit)
	if err != nil {
		t.Fatalf("Failed to marshal Commit: %v", err)
	}

	// Test unmarshaling
	var unmarshaled Commit
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Commit: %v", err)
	}

	// Verify all fields
	if unmarshaled.Hash != commit.Hash {
		t.Errorf("Hash mismatch: expected %q, got %q", commit.Hash, unmarshaled.Hash)
	}
	if unmarshaled.Message != commit.Message {
		t.Errorf("Message mismatch: expected %q, got %q", commit.Message, unmarshaled.Message)
	}
	if unmarshaled.Author.Name != commit.Author.Name {
		t.Errorf("Author name mismatch: expected %q, got %q", commit.Author.Name, unmarshaled.Author.Name)
	}
	if unmarshaled.Author.Email != commit.Author.Email {
		t.Errorf("Author email mismatch: expected %q, got %q", commit.Author.Email, unmarshaled.Author.Email)
	}
	if !unmarshaled.Author.Date.Equal(commit.Author.Date) {
		t.Errorf("Author date mismatch: expected %v, got %v", commit.Author.Date, unmarshaled.Author.Date)
	}
	if len(unmarshaled.ParentHashes) != len(commit.ParentHashes) {
		t.Errorf("ParentHashes length mismatch: expected %d, got %d", len(commit.ParentHashes), len(unmarshaled.ParentHashes))
	}
	for i, parent := range commit.ParentHashes {
		if unmarshaled.ParentHashes[i] != parent {
			t.Errorf("ParentHash[%d] mismatch: expected %q, got %q", i, parent, unmarshaled.ParentHashes[i])
		}
	}
	if unmarshaled.TreeHash != commit.TreeHash {
		t.Errorf("TreeHash mismatch: expected %q, got %q", commit.TreeHash, unmarshaled.TreeHash)
	}
}

func TestCommitAuthor_JSONSerialization(t *testing.T) {
	testTime := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)

	author := CommitAuthor{
		Name:  "John Doe",
		Email: "john@example.com",
		Date:  testTime,
	}

	// Test marshaling
	data, err := json.Marshal(author)
	if err != nil {
		t.Fatalf("Failed to marshal CommitAuthor: %v", err)
	}

	// Test unmarshaling
	var unmarshaled CommitAuthor
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal CommitAuthor: %v", err)
	}

	// Verify all fields
	if unmarshaled.Name != author.Name {
		t.Errorf("Name mismatch: expected %q, got %q", author.Name, unmarshaled.Name)
	}
	if unmarshaled.Email != author.Email {
		t.Errorf("Email mismatch: expected %q, got %q", author.Email, unmarshaled.Email)
	}
	if !unmarshaled.Date.Equal(author.Date) {
		t.Errorf("Date mismatch: expected %v, got %v", author.Date, unmarshaled.Date)
	}
}

func TestMockRepository_Implementation(t *testing.T) {
	// Test that MockRepository implements the Repository interface
	var _ Repository = (*MockRepository)(nil)
}

func TestMockRepository_BasicOperations(t *testing.T) {
	ctx := context.Background()
	mock := NewMockRepository("/test/repo")

	// Test Path()
	if mock.Path() != "/test/repo" {
		t.Errorf("Expected path %q, got %q", "/test/repo", mock.Path())
	}

	// Test GetTree with no data
	_, err := mock.GetTree(ctx, "main")
	if err == nil {
		t.Error("Expected error for non-existent ref, got nil")
	}

	var refErr *ReferenceNotFoundError
	if !isErrorType(err, &refErr) {
		t.Errorf("Expected ReferenceNotFoundError, got %T", err)
	}

	// Test GetBlob with no data
	_, err = mock.GetBlob(ctx, "file.txt", "main")
	if err == nil {
		t.Error("Expected error for non-existent path, got nil")
	}

	var pathErr *PathNotFoundError
	if !isErrorType(err, &pathErr) {
		t.Errorf("Expected PathNotFoundError, got %T", err)
	}

	// Test GetCommits with no data
	_, err = mock.GetCommits(ctx, "main", 10, 0)
	if err == nil {
		t.Error("Expected error for non-existent ref, got nil")
	}

	if !isErrorType(err, &refErr) {
		t.Errorf("Expected ReferenceNotFoundError, got %T", err)
	}

	// Test Close
	err = mock.Close()
	if err != nil {
		t.Errorf("Expected no error on close, got %v", err)
	}

	if !mock.IsClosed() {
		t.Error("Expected repository to be closed")
	}
}

func TestMockRepository_WithData(t *testing.T) {
	ctx := context.Background()
	mock := NewMockRepository("/test/repo")

	// Set up test data
	treeEntries := []TreeEntry{
		{Path: "file1.txt", Mode: "100644", Type: EntryTypeBlob, Hash: "hash1", Size: 100},
		{Path: "dir1", Mode: "040000", Type: EntryTypeTree, Hash: "hash2", Size: 0},
	}
	mock.SetTree("main", treeEntries)

	blob := []byte("Hello, World!")
	mock.SetBlob("file1.txt", "main", blob)

	commits := []Commit{
		{Hash: "commit1", Message: "First commit", TreeHash: "tree1"},
		{Hash: "commit2", Message: "Second commit", TreeHash: "tree2"},
	}
	mock.SetCommits("main", commits)

	// Test GetTree
	entries, err := mock.GetTree(ctx, "main")
	if err != nil {
		t.Fatalf("Failed to get tree: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("Expected 2 entries, got %d", len(entries))
	}

	// Test GetBlob
	content, err := mock.GetBlob(ctx, "file1.txt", "main")
	if err != nil {
		t.Fatalf("Failed to get blob: %v", err)
	}
	if string(content) != "Hello, World!" {
		t.Errorf("Expected %q, got %q", "Hello, World!", string(content))
	}

	// Test GetCommits
	commitList, err := mock.GetCommits(ctx, "main", 10, 0)
	if err != nil {
		t.Fatalf("Failed to get commits: %v", err)
	}
	if len(commitList) != 2 {
		t.Errorf("Expected 2 commits, got %d", len(commitList))
	}

	// Test pagination
	firstCommit, err := mock.GetCommits(ctx, "main", 1, 0)
	if err != nil {
		t.Fatalf("Failed to get first commit: %v", err)
	}
	if len(firstCommit) != 1 {
		t.Errorf("Expected 1 commit, got %d", len(firstCommit))
	}
	if firstCommit[0].Hash != "commit1" {
		t.Errorf("Expected commit1, got %s", firstCommit[0].Hash)
	}

	secondCommit, err := mock.GetCommits(ctx, "main", 1, 1)
	if err != nil {
		t.Fatalf("Failed to get second commit: %v", err)
	}
	if len(secondCommit) != 1 {
		t.Errorf("Expected 1 commit, got %d", len(secondCommit))
	}
	if secondCommit[0].Hash != "commit2" {
		t.Errorf("Expected commit2, got %s", secondCommit[0].Hash)
	}
}

func TestMockRepository_ContextCancellation(t *testing.T) {
	mock := NewMockRepository("/test/repo")

	// Set up test data
	mock.SetTree("main", []TreeEntry{{Path: "file.txt", Type: EntryTypeBlob}})
	mock.SetBlob("file.txt", "main", []byte("content"))
	mock.SetCommits("main", []Commit{{Hash: "commit1"}})

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Test that all operations respect context cancellation
	_, err := mock.GetTree(ctx, "main")
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}

	_, err = mock.GetBlob(ctx, "file.txt", "main")
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}

	_, err = mock.GetCommits(ctx, "main", 10, 0)
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestMockRepository_ErrorInjection(t *testing.T) {
	ctx := context.Background()
	mock := NewMockRepository("/test/repo")

	// Test error injection
	expectedErr := NewGitError("test", "/test/repo", "main", "test error", nil)

	mock.GetTreeError = expectedErr
	_, err := mock.GetTree(ctx, "main")
	if err != expectedErr {
		t.Errorf("Expected injected error, got %v", err)
	}

	mock.GetBlobError = expectedErr
	_, err = mock.GetBlob(ctx, "file.txt", "main")
	if err != expectedErr {
		t.Errorf("Expected injected error, got %v", err)
	}

	mock.GetCommitsError = expectedErr
	_, err = mock.GetCommits(ctx, "main", 10, 0)
	if err != expectedErr {
		t.Errorf("Expected injected error, got %v", err)
	}

	mock.CloseError = expectedErr
	err = mock.Close()
	if err != expectedErr {
		t.Errorf("Expected injected error, got %v", err)
	}
}

// Helper function to check error types
func isErrorType(err error, target interface{}) bool {
	switch target.(type) {
	case **ReferenceNotFoundError:
		_, ok := err.(*ReferenceNotFoundError)
		return ok
	case **PathNotFoundError:
		_, ok := err.(*PathNotFoundError)
		return ok
	default:
		return false
	}
}
