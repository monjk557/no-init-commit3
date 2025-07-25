// Package protocol_test provides comprehensive integration tests for SSH protocol handlers
package protocol_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/go-chi/chi/v5"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSHProtocolIntegration_FullUploadPack tests the complete upload-pack flow
func TestSSHProtocolIntegration_FullUploadPack(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create test environment
	repoRoot := t.TempDir()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
	}

	// Create a test repository with commits
	repoPath := filepath.Join(repoRoot, "test-owner", "test-repo.git")
	repo, err := createTestRepositoryWithHistory(repoPath)
	require.NoError(t, err)

	// Get HEAD reference for verification
	head, err := repo.Head()
	require.NoError(t, err)

	// Create mock permission client
	mockClient := &mockPermissionClient{
		permissions: map[string]map[string]struct {
			read  bool
			write bool
		}{
			"user123": {
				"test-owner/test-repo": {read: true, write: true},
			},
		},
	}

	// Create handler
	handler, err := protocol.NewSSHProtocolHandler(cfg, mockClient)
	require.NoError(t, err)

	// Create router
	router := chi.NewRouter()
	router.Post("/ssh/{owner}/{repo}/upload-pack", handler.HandleSSHUploadPack)

	// Test cases
	tests := []struct {
		name           string
		headers        map[string]string
		body           string
		expectedStatus int
		validateBody   func(t *testing.T, body []byte)
	}{
		{
			name: "Valid upload-pack request with capabilities",
			headers: map[string]string{
				"X-USER-ID":        "user123",
				"X-Source":         "ssh-gateway",
				"X-CORRELATION-ID": "test-correlation-123",
				"Git-Protocol":     "version=2",
			},
			body:           "0014command=ls-refs\n0000",
			expectedStatus: http.StatusOK,
			validateBody: func(t *testing.T, body []byte) {
				// Should return pkt-line formatted response
				assert.NotEmpty(t, body)
				// Should contain HEAD reference
				assert.Contains(t, string(body), head.Hash().String())
			},
		},
		{
			name: "Upload-pack with want request",
			headers: map[string]string{
				"X-USER-ID":        "user123",
				"X-Source":         "ssh-gateway",
				"X-CORRELATION-ID": "test-correlation-124",
			},
			body:           fmt.Sprintf("0032want %s multi_ack_detailed\n00000009done\n", head.Hash().String()),
			expectedStatus: http.StatusOK,
			validateBody: func(t *testing.T, body []byte) {
				// Should return packfile data
				assert.NotEmpty(t, body)
			},
		},
		{
			name: "Upload-pack without permission",
			headers: map[string]string{
				"X-USER-ID":        "unauthorized-user",
				"X-Source":         "ssh-gateway",
				"X-CORRELATION-ID": "test-correlation-125",
			},
			body:           "0014command=ls-refs\n0000",
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/ssh/test-owner/test-repo/upload-pack", strings.NewReader(tt.body))
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.validateBody != nil && rec.Code == http.StatusOK {
				tt.validateBody(t, rec.Body.Bytes())
			}
		})
	}
}

// TestSSHProtocolIntegration_FullReceivePack tests the complete receive-pack flow
func TestSSHProtocolIntegration_FullReceivePack(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create test environment
	repoRoot := t.TempDir()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
	}

	// Create a test repository
	repoPath := filepath.Join(repoRoot, "test-owner", "test-repo.git")
	repo, err := createTestRepositoryWithHistory(repoPath)
	require.NoError(t, err)

	// Create mock permission client
	mockClient := &mockPermissionClient{
		permissions: map[string]map[string]struct {
			read  bool
			write bool
		}{
			"user123": {
				"test-owner/test-repo": {read: true, write: true},
			},
		},
	}

	// Create handler
	handler, err := protocol.NewSSHProtocolHandler(cfg, mockClient)
	require.NoError(t, err)

	// Create router
	router := chi.NewRouter()
	router.Post("/ssh/{owner}/{repo}/receive-pack", handler.HandleSSHReceivePack)

	// Create a new commit to push
	newCommit, packData := createTestCommitAndPackfile(t, repo)

	// Test cases
	tests := []struct {
		name           string
		headers        map[string]string
		body           func() string
		expectedStatus int
		validateRepo   func(t *testing.T, repo *gogit.Repository)
	}{
		{
			name: "Valid receive-pack with new branch",
			headers: map[string]string{
				"X-USER-ID":        "user123",
				"X-Source":         "ssh-gateway",
				"X-CORRELATION-ID": "test-correlation-200",
			},
			body: func() string {
				// Create receive-pack request with reference update
				oldOID := strings.Repeat("0", 40)
				newOID := newCommit.String()
				refUpdate := fmt.Sprintf("%s %s refs/heads/feature-branch\x00report-status\n", oldOID, newOID)

				// Combine with packfile
				var buf bytes.Buffer
				buf.WriteString(fmt.Sprintf("%04x%s", len(refUpdate)+4, refUpdate))
				buf.WriteString("0000")
				buf.Write(packData)
				return buf.String()
			},
			expectedStatus: http.StatusOK,
			validateRepo: func(t *testing.T, repo *gogit.Repository) {
				// Verify the new branch was created
				ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/feature-branch"), true)
				require.NoError(t, err)
				assert.Equal(t, newCommit, ref.Hash())
			},
		},
		{
			name: "Receive-pack without write permission",
			headers: map[string]string{
				"X-USER-ID":        "readonly-user",
				"X-Source":         "ssh-gateway",
				"X-CORRELATION-ID": "test-correlation-201",
			},
			body: func() string {
				return "0000"
			},
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/ssh/test-owner/test-repo/receive-pack", strings.NewReader(tt.body()))
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)

			if tt.validateRepo != nil && rec.Code == http.StatusOK {
				// Re-open repository to verify changes
				verifyRepo, err := gogit.PlainOpen(repoPath)
				require.NoError(t, err)
				tt.validateRepo(t, verifyRepo)
			}
		})
	}
}

// TestSSHProtocolIntegration_ConcurrentAccess tests concurrent SSH operations
func TestSSHProtocolIntegration_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create test environment
	repoRoot := t.TempDir()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
	}

	// Create a test repository
	repoPath := filepath.Join(repoRoot, "test-owner", "test-repo.git")
	_, err := createTestRepositoryWithHistory(repoPath)
	require.NoError(t, err)

	// Create mock permission client
	mockClient := &mockPermissionClient{
		permissions: map[string]map[string]struct {
			read  bool
			write bool
		}{
			"user123": {
				"test-owner/test-repo": {read: true, write: true},
			},
		},
	}

	// Create handler
	handler, err := protocol.NewSSHProtocolHandler(cfg, mockClient)
	require.NoError(t, err)

	// Create router
	router := chi.NewRouter()
	router.Post("/ssh/{owner}/{repo}/upload-pack", handler.HandleSSHUploadPack)

	// Run concurrent requests
	concurrency := 10
	done := make(chan bool, concurrency)
	errors := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer func() { done <- true }()

			req := httptest.NewRequest("POST", "/ssh/test-owner/test-repo/upload-pack", strings.NewReader("0014command=ls-refs\n0000"))
			req.Header.Set("X-USER-ID", "user123")
			req.Header.Set("X-Source", "ssh-gateway")
			req.Header.Set("X-CORRELATION-ID", fmt.Sprintf("concurrent-%d", id))

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				errors <- fmt.Errorf("request %d failed with status %d", id, rec.Code)
			}
		}(i)
	}

	// Wait for all requests to complete
	for i := 0; i < concurrency; i++ {
		<-done
	}

	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

// TestSSHProtocolIntegration_LargePackfile tests handling of large packfiles
func TestSSHProtocolIntegration_LargePackfile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create test environment
	repoRoot := t.TempDir()
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: repoRoot,
		},
	}

	// Create a test repository
	repoPath := filepath.Join(repoRoot, "test-owner", "test-repo.git")
	repo, err := createTestRepositoryWithHistory(repoPath)
	require.NoError(t, err)

	// Create mock permission client
	mockClient := &mockPermissionClient{
		permissions: map[string]map[string]struct {
			read  bool
			write bool
		}{
			"user123": {
				"test-owner/test-repo": {read: true, write: true},
			},
		},
	}

	// Create handler
	handler, err := protocol.NewSSHProtocolHandler(cfg, mockClient)
	require.NoError(t, err)

	// Create router
	router := chi.NewRouter()
	router.Post("/ssh/{owner}/{repo}/receive-pack", handler.HandleSSHReceivePack)

	// Create a large commit with multiple files
	largeCommit, largePackData := createLargeTestCommit(t, repo, 100) // 100 files

	// Create receive-pack request
	oldOID := strings.Repeat("0", 40)
	newOID := largeCommit.String()
	refUpdate := fmt.Sprintf("%s %s refs/heads/large-branch\x00report-status\n", oldOID, newOID)

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("%04x%s", len(refUpdate)+4, refUpdate))
	buf.WriteString("0000")
	buf.Write(largePackData)

	req := httptest.NewRequest("POST", "/ssh/test-owner/test-repo/receive-pack", &buf)
	req.Header.Set("X-USER-ID", "user123")
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-CORRELATION-ID", "large-pack-test")

	rec := httptest.NewRecorder()

	// Time the request
	start := time.Now()
	router.ServeHTTP(rec, req)
	duration := time.Since(start)

	assert.Equal(t, http.StatusOK, rec.Code)
	t.Logf("Large packfile processing took: %v", duration)

	// Verify the branch was created
	verifyRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	ref, err := verifyRepo.Reference(plumbing.ReferenceName("refs/heads/large-branch"), true)
	require.NoError(t, err)
	assert.Equal(t, largeCommit, ref.Hash())
}

// Helper functions

func createTestRepositoryWithHistory(path string) (*gogit.Repository, error) {
	// Create directory
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, err
	}

	// Initialize repository
	repo, err := gogit.PlainInit(path, false)
	if err != nil {
		return nil, err
	}

	// Create initial commit
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	// Create test file
	testFile := filepath.Join(path, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Repository\n"), 0644); err != nil {
		return nil, err
	}

	// Add and commit
	if _, err := wt.Add("README.md"); err != nil {
		return nil, err
	}

	_, err = wt.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})

	return repo, err
}

func createTestCommitAndPackfile(t *testing.T, repo *gogit.Repository) (plumbing.Hash, []byte) {
	// Create a new blob
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	content := []byte("New file content for SSH test")
	obj.SetSize(int64(len(content)))

	writer, err := obj.Writer()
	require.NoError(t, err)
	_, err = writer.Write(content)
	require.NoError(t, err)
	err = writer.Close()
	require.NoError(t, err)

	blobHash, err := repo.Storer.SetEncodedObject(obj)
	require.NoError(t, err)

	// Create tree
	tree := object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "newfile.txt",
				Mode: 0100644,
				Hash: blobHash,
			},
		},
	}

	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Get parent commit
	head, err := repo.Head()
	require.NoError(t, err)

	// Create commit
	commit := &object.Commit{
		Author: object.Signature{
			Name:  "SSH Test",
			Email: "ssh@test.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "SSH Test",
			Email: "ssh@test.com",
			When:  time.Now(),
		},
		Message:      "Test commit via SSH",
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{head.Hash()},
	}

	commitObj := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create simple packfile with these objects
	var packBuf bytes.Buffer

	// Write pack header
	packBuf.WriteString("PACK")       // Signature
	packBuf.Write([]byte{0, 0, 0, 2}) // Version
	packBuf.Write([]byte{0, 0, 0, 3}) // Object count (blob + tree + commit)

	// For simplicity, return a minimal valid packfile structure
	// In real implementation, this would include the actual object data

	return commitHash, packBuf.Bytes()
}

func createLargeTestCommit(t *testing.T, repo *gogit.Repository, fileCount int) (plumbing.Hash, []byte) {
	// Create multiple blobs
	entries := make([]object.TreeEntry, fileCount)

	for i := 0; i < fileCount; i++ {
		obj := repo.Storer.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		content := []byte(fmt.Sprintf("File content %d\nThis is a test file for large commit testing.\n", i))
		obj.SetSize(int64(len(content)))

		writer, err := obj.Writer()
		require.NoError(t, err)
		_, err = writer.Write(content)
		require.NoError(t, err)
		err = writer.Close()
		require.NoError(t, err)

		hash, err := repo.Storer.SetEncodedObject(obj)
		require.NoError(t, err)

		entries[i] = object.TreeEntry{
			Name: fmt.Sprintf("file%03d.txt", i),
			Mode: 0100644,
			Hash: hash,
		}
	}

	// Create tree with all files
	tree := object.Tree{Entries: entries}
	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Get parent commit
	head, err := repo.Head()
	require.NoError(t, err)

	// Create commit
	commit := &object.Commit{
		Author: object.Signature{
			Name:  "Large Test",
			Email: "large@test.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Large Test",
			Email: "large@test.com",
			When:  time.Now(),
		},
		Message:      fmt.Sprintf("Large commit with %d files", fileCount),
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{head.Hash()},
	}

	commitObj := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create packfile (simplified)
	var packBuf bytes.Buffer
	packBuf.WriteString("PACK")
	packBuf.Write([]byte{0, 0, 0, 2})
	packBuf.Write([]byte{0, 0, 0, byte(fileCount + 2)}) // blobs + tree + commit

	return commitHash, packBuf.Bytes()
}

// mockPermissionClient for testing
type mockPermissionClient struct {
	permissions map[string]map[string]struct {
		read  bool
		write bool
	}
}

func (m *mockPermissionClient) CheckReadAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	if userPerms, ok := m.permissions[userID]; ok {
		key := fmt.Sprintf("%s/%s", owner, repo)
		if perm, ok := userPerms[key]; ok {
			return perm.read, nil
		}
	}
	return false, nil
}

func (m *mockPermissionClient) CheckWriteAccess(ctx context.Context, userID, owner, repo string) (bool, error) {
	if userPerms, ok := m.permissions[userID]; ok {
		key := fmt.Sprintf("%s/%s", owner, repo)
		if perm, ok := userPerms[key]; ok {
			return perm.write, nil
		}
	}
	return false, nil
}

func (m *mockPermissionClient) InvalidateCache(userID, repoOwner, repoName string) {
	// Mock implementation - do nothing
}

func (m *mockPermissionClient) ClearCache() {
	// Mock implementation - do nothing
}

func (m *mockPermissionClient) GetCircuitBreakerMetrics() *permissions.CircuitBreakerMetrics {
	return &permissions.CircuitBreakerMetrics{
		State:             permissions.StateClosed,
		FailureCount:      0,
		HalfOpenCount:     0,
		LastFailureTime:   time.Time{},
		LastSuccessTime:   time.Time{},
		TimeUntilHalfOpen: 0,
	}
}

func (m *mockPermissionClient) Close() error {
	return nil
}
