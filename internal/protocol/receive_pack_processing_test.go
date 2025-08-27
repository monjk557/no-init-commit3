package protocol

import (
	"context"
	"encoding/binary"
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

func TestProcessRefUpdateSimple(t *testing.T) {
	// Create an in-memory repository for testing
	fs := memfs.New()
	storage := memory.NewStorage()
	repo, err := gogit.Init(storage, fs)
	require.NoError(t, err)

	// Create a handler with basic config
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: "/tmp/test",
		},
	}
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Test creating a new branch with a real commit
	// First, create a commit in the repository
	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create a test file
	file, err := worktree.Filesystem.Create("test.txt")
	require.NoError(t, err)
	_, err = file.Write([]byte("test content"))
	require.NoError(t, err)
	err = file.Close()
	require.NoError(t, err)

	// Add and commit the file
	_, err = worktree.Add("test.txt")
	require.NoError(t, err)

	hash, err := worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Test 1: Create new branch pointing to the commit
	cmd := RefUpdateCommand{
		OldHash: plumbing.ZeroHash,
		NewHash: hash,
		RefName: "refs/heads/feature-branch",
	}

	err = handler.processRefUpdate(ctx, repo, cmd)
	assert.NoError(t, err)

	// Verify the reference was created
	ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/feature-branch"), false)
	assert.NoError(t, err)
	assert.Equal(t, hash, ref.Hash())

	// Test 2: Update existing branch
	// Create another commit
	file2, err := worktree.Filesystem.Create("test2.txt")
	require.NoError(t, err)
	_, err = file2.Write([]byte("second file"))
	require.NoError(t, err)
	_ = file2.Close()

	_, err = worktree.Add("test2.txt")
	require.NoError(t, err)

	hash2, err := worktree.Commit("Second commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	updateCmd := RefUpdateCommand{
		OldHash: hash,
		NewHash: hash2,
		RefName: "refs/heads/feature-branch",
	}

	err = handler.processRefUpdate(ctx, repo, updateCmd)
	assert.NoError(t, err)

	// Verify the reference was updated
	ref, err = repo.Reference(plumbing.ReferenceName("refs/heads/feature-branch"), false)
	assert.NoError(t, err)
	assert.Equal(t, hash2, ref.Hash())

	// Test 3: Delete branch
	deleteCmd := RefUpdateCommand{
		OldHash: hash2,
		NewHash: plumbing.ZeroHash,
		RefName: "refs/heads/feature-branch",
	}

	err = handler.processRefUpdate(ctx, repo, deleteCmd)
	assert.NoError(t, err)

	// Verify the reference was deleted
	_, err = repo.Reference(plumbing.ReferenceName("refs/heads/feature-branch"), false)
	assert.Error(t, err) // Should error because reference no longer exists
}

func TestRefValidation(t *testing.T) {
	tests := []struct {
		name     string
		refName  string
		expected bool
	}{
		{"Valid branch", "refs/heads/main", true},
		{"Valid tag", "refs/tags/v1.0.0", true},
		{"Valid remote", "refs/remotes/origin/main", true},
		{"Invalid empty", "", false},
		{"Invalid no refs prefix", "heads/main", false},
		{"Invalid double slash", "refs//heads/main", false},
		{"Invalid ending slash", "refs/heads/main/", false},
		{"Invalid characters", "refs/heads/main~1", false},
		{"Invalid control char", "refs/heads/main\x00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidRefName(tt.refName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestProcessPackfile(t *testing.T) {
	// Create a test repository
	fs := memfs.New()
	storage := memory.NewStorage()
	repo, err := gogit.Init(storage, fs)
	require.NoError(t, err)

	// Create a handler with proper config
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: "/tmp/test",
		},
	}
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Test 1: Empty packfile data
	err = handler.processPackfile(ctx, repo, []byte{})
	assert.NoError(t, err)

	// Test 2: Invalid packfile data
	err = handler.processPackfile(ctx, repo, []byte("invalid"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid packfile format")

	// Test 3: Valid packfile header but too short
	shortPackfile := []byte("PACK")
	err = handler.processPackfile(ctx, repo, shortPackfile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "packfile too short")

	// Test 4: Valid packfile header with correct format
	validPackfile := make([]byte, 12)
	copy(validPackfile[0:4], "PACK")                   // signature
	binary.BigEndian.PutUint32(validPackfile[4:8], 2)  // version 2
	binary.BigEndian.PutUint32(validPackfile[8:12], 0) // 0 objects

	err = handler.processPackfile(ctx, repo, validPackfile)
	assert.NoError(t, err)

	// Test 5: Invalid signature
	invalidSig := make([]byte, 12)
	copy(invalidSig[0:4], "XXXX")
	binary.BigEndian.PutUint32(invalidSig[4:8], 2)
	binary.BigEndian.PutUint32(invalidSig[8:12], 0)

	err = handler.processPackfile(ctx, repo, invalidSig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid packfile signature")

	// Test 6: Invalid version
	invalidVer := make([]byte, 12)
	copy(invalidVer[0:4], "PACK")
	binary.BigEndian.PutUint32(invalidVer[4:8], 5) // unsupported version
	binary.BigEndian.PutUint32(invalidVer[8:12], 0)

	err = handler.processPackfile(ctx, repo, invalidVer)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported packfile version")
}

func TestValidatePackfileFormat(t *testing.T) {
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: "/tmp/test",
		},
	}
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)

	// Test 1: Valid packfile header
	validPackfile := make([]byte, 12)
	copy(validPackfile[0:4], "PACK")
	binary.BigEndian.PutUint32(validPackfile[4:8], 2)
	binary.BigEndian.PutUint32(validPackfile[8:12], 5)

	header, err := handler.validatePackfileFormat(validPackfile)
	assert.NoError(t, err)
	assert.Equal(t, uint32(2), header.Version)
	assert.Equal(t, uint32(5), header.ObjectCount)

	// Test 2: Too short
	_, err = handler.validatePackfileFormat([]byte("PACK"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "packfile too short")

	// Test 3: Invalid signature
	invalidSig := make([]byte, 12)
	copy(invalidSig[0:4], "XXXX")
	binary.BigEndian.PutUint32(invalidSig[4:8], 2)
	binary.BigEndian.PutUint32(invalidSig[8:12], 0)

	_, err = handler.validatePackfileFormat(invalidSig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid packfile signature")

	// Test 4: Invalid version
	invalidVer := make([]byte, 12)
	copy(invalidVer[0:4], "PACK")
	binary.BigEndian.PutUint32(invalidVer[4:8], 1) // too old
	binary.BigEndian.PutUint32(invalidVer[8:12], 0)

	_, err = handler.validatePackfileFormat(invalidVer)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported packfile version")
}
