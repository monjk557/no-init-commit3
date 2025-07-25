package protocol

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessRefUpdateSimple tests reference updates with a simple in-memory repository
func TestProcessRefUpdateSimple(t *testing.T) {
	// Create an in-memory repository to avoid file system issues
	storage := memory.NewStorage()
	fs := memfs.New()

	repo, err := gogit.Init(storage, fs)
	require.NoError(t, err)

	// Create a simple commit
	w, err := repo.Worktree()
	require.NoError(t, err)

	// Create a file
	file, err := fs.Create("test.txt")
	require.NoError(t, err)
	_, err = file.Write([]byte("Hello, World!"))
	require.NoError(t, err)
	file.Close()

	// Add and commit
	_, err = w.Add("test.txt")
	require.NoError(t, err)

	commitHash, err := w.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create a temporary directory for the path config
	tempDir, err := os.MkdirTemp("", "ref-update-simple-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()

	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	mockPermClient := &MockPermissionClient{
		shouldAllow: true,
		shouldError: false,
	}

	handler, err := NewReceivePackHandler(cfg, mockPermClient)
	require.NoError(t, err)

	ctx := context.Background()

	tests := []struct {
		name        string
		setupRef    func() error
		cmd         RefUpdateCommand
		expectError bool
		errorMsg    string
		checkRef    func(t *testing.T)
	}{
		{
			name: "Create new branch",
			cmd: RefUpdateCommand{
				RefName: "refs/heads/feature",
				OldHash: plumbing.ZeroHash,
				NewHash: commitHash,
			},
			expectError: false,
			checkRef: func(t *testing.T) {
				ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/feature"), true)
				assert.NoError(t, err)
				assert.Equal(t, commitHash, ref.Hash())
			},
		},
		{
			name: "Update existing branch",
			setupRef: func() error {
				// Create the branch first
				ref := plumbing.NewHashReference("refs/heads/update-test", commitHash)
				return repo.Storer.SetReference(ref)
			},
			cmd: RefUpdateCommand{
				RefName: "refs/heads/update-test",
				OldHash: commitHash,
				NewHash: commitHash, // Same hash, but still a valid update
			},
			expectError: false,
			checkRef: func(t *testing.T) {
				ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/update-test"), true)
				assert.NoError(t, err)
				assert.Equal(t, commitHash, ref.Hash())
			},
		},
		{
			name: "Delete existing branch",
			setupRef: func() error {
				// Create the branch first
				ref := plumbing.NewHashReference("refs/heads/to-delete", commitHash)
				return repo.Storer.SetReference(ref)
			},
			cmd: RefUpdateCommand{
				RefName: "refs/heads/to-delete",
				OldHash: commitHash,
				NewHash: plumbing.ZeroHash,
			},
			expectError: false,
			checkRef: func(t *testing.T) {
				_, err := repo.Reference(plumbing.ReferenceName("refs/heads/to-delete"), true)
				assert.Error(t, err)
				assert.Equal(t, plumbing.ErrReferenceNotFound, err)
			},
		},
		{
			name: "Invalid reference name",
			cmd: RefUpdateCommand{
				RefName: "not-a-valid-ref",
				OldHash: plumbing.ZeroHash,
				NewHash: commitHash,
			},
			expectError: true,
			errorMsg:    "invalid reference name",
		},
		{
			name: "Non-existent commit",
			cmd: RefUpdateCommand{
				RefName: "refs/heads/bad-commit",
				OldHash: plumbing.ZeroHash,
				NewHash: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			},
			expectError: true,
			errorMsg:    "not found in repository",
		},
		{
			name: "Concurrent modification check",
			setupRef: func() error {
				// Create a branch
				ref := plumbing.NewHashReference("refs/heads/concurrent", commitHash)
				return repo.Storer.SetReference(ref)
			},
			cmd: RefUpdateCommand{
				RefName: "refs/heads/concurrent",
				OldHash: plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), // Wrong old hash
				NewHash: commitHash,
			},
			expectError: true,
			errorMsg:    "has been modified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run setup if provided
			if tt.setupRef != nil {
				err := tt.setupRef()
				require.NoError(t, err)
			}

			// Execute the reference update
			err := handler.processRefUpdate(ctx, repo, tt.cmd)

			// Check error expectations
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}

			// Run additional checks if provided
			if tt.checkRef != nil {
				tt.checkRef(t)
			}
		})
	}
}

// TestRefValidation tests the reference name validation
func TestRefValidation(t *testing.T) {
	tests := []struct {
		refName string
		valid   bool
	}{
		{"refs/heads/main", true},
		{"refs/tags/v1.0.0", true},
		{"refs/remotes/origin/main", true},
		{"", false},
		{"heads/main", false},
		{"refs/heads/", false},
		{"refs/heads/main.lock", false},
		{"refs/heads/main..other", false},
		{"refs/heads/main~1", false},
		{"refs/heads/main^", false},
		{"refs/heads/main:other", false},
		{"refs/heads/main?", false},
		{"refs/heads/main*", false},
		{"refs/heads/main[", false},
		{"refs/heads/main\\", false},
		{"refs/heads//main", false},
		{"refs/heads/main@{", false},
		{"refs/heads/ main", false},
	}

	for _, tt := range tests {
		t.Run(tt.refName, func(t *testing.T) {
			result := isValidRefName(tt.refName)
			assert.Equal(t, tt.valid, result, "Reference name validation mismatch for %q", tt.refName)
		})
	}
}
