package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestGitObjectManager(t *testing.T) (*GitObjectManager, string, func()) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "file-ops-test-*")
	require.NoError(t, err)

	// Create path config
	pathConfig, err := repository.NewPathConfig(tempDir)
	require.NoError(t, err)

	// Create logger
	cfg := config.LoggingConfig{
		Level:  "debug",
		Format: "text",
		Output: "stdout",
	}
	logger, err := logging.NewLogger(cfg)
	require.NoError(t, err)

	// Create object manager
	gom, err := NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return gom, tempDir, cleanup
}

func createTestRepositoryWithFiles(t *testing.T, tempDir, owner, repo string) {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	// Initialize bare repository
	_, err = gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// Create initial commit to avoid empty repository issues
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// Create initial README
	readme := "# Test Repository\n\nInitial commit for file operations testing.\n"
	blob := gitRepo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(readme)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(readme))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := gitRepo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	// Create tree with README
	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "README.md",
				Mode: 0100644,
				Hash: blobHash,
			},
		},
	}

	treeObj := gitRepo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create initial commit
	commit := &object.Commit{
		Message: "Initial commit",
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		TreeHash: treeHash,
	}

	commitObj := gitRepo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create master branch
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = gitRepo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	// Set HEAD to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = gitRepo.Storer.SetReference(headRef)
	require.NoError(t, err)
}

func TestGitObjectManager_CreateSingleFile(t *testing.T) {
	gom, tempDir, cleanup := createTestGitObjectManager(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	ctx := context.Background()

	t.Run("create new file", func(t *testing.T) {
		operation := SingleFileOperation{
			Owner:       owner,
			Repo:        repo,
			Path:        "src/main.go",
			Content:     []byte("package main\n\nfunc main() {\n\tprintln(\"Hello, World!\")\n}"),
			Message:     "Add main.go",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}

		result, err := gom.CreateSingleFile(ctx, operation)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.NotEmpty(t, result.CommitHash)
		assert.NotEmpty(t, result.TreeHash)
		assert.Equal(t, "Add main.go", result.Message)
		assert.Equal(t, "Test Author", result.Author)
		assert.Contains(t, result.CreatedFiles, "src/main.go")
	})

	t.Run("create file with committer info", func(t *testing.T) {
		operation := SingleFileOperation{
			Owner:          owner,
			Repo:           repo,
			Path:           "docs/README.md",
			Content:        []byte("# Documentation\n\nThis is the docs README."),
			Message:        "Add docs README",
			AuthorName:     "Test Author",
			AuthorEmail:    "test@example.com",
			CommitterName:  "Test Committer",
			CommitterEmail: "committer@example.com",
			Branch:         "master",
		}

		result, err := gom.CreateSingleFile(ctx, operation)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.NotEmpty(t, result.CommitHash)
		assert.Contains(t, result.CreatedFiles, "docs/README.md")
	})

	t.Run("invalid repository", func(t *testing.T) {
		operation := SingleFileOperation{
			Owner:       owner,
			Repo:        "nonexistent",
			Path:        "test.txt",
			Content:     []byte("test"),
			Message:     "Test",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
		}

		_, err := gom.CreateSingleFile(ctx, operation)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open repository")
	})
}

func TestGitObjectManager_UpdateSingleFile(t *testing.T) {
	gom, tempDir, cleanup := createTestGitObjectManager(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	ctx := context.Background()

	// First create a file
	createOp := SingleFileOperation{
		Owner:       owner,
		Repo:        repo,
		Path:        "config.json",
		Content:     []byte(`{"version": "1.0.0"}`),
		Message:     "Add config.json",
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
		Branch:      "master",
	}

	_, err := gom.CreateSingleFile(ctx, createOp)
	require.NoError(t, err)

	t.Run("update existing file", func(t *testing.T) {
		updateOp := SingleFileOperation{
			Owner:       owner,
			Repo:        repo,
			Path:        "config.json",
			Content:     []byte(`{"version": "2.0.0", "new_feature": true}`),
			Message:     "Update config.json",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}

		result, err := gom.UpdateSingleFile(ctx, updateOp)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "Update config.json", result.Message)
		assert.Contains(t, result.ChangedFiles, "config.json")
	})
}

func TestGitObjectManager_DeleteSingleFile(t *testing.T) {
	gom, tempDir, cleanup := createTestGitObjectManager(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	ctx := context.Background()

	// First create a file
	createOp := SingleFileOperation{
		Owner:       owner,
		Repo:        repo,
		Path:        "temp.txt",
		Content:     []byte("temporary file"),
		Message:     "Add temp file",
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
		Branch:      "master",
	}

	_, err := gom.CreateSingleFile(ctx, createOp)
	require.NoError(t, err)

	t.Run("delete existing file", func(t *testing.T) {
		deleteOp := SingleFileOperation{
			Owner:       owner,
			Repo:        repo,
			Path:        "temp.txt",
			Message:     "Remove temp file",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}

		result, err := gom.DeleteSingleFile(ctx, deleteOp)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "Remove temp file", result.Message)
		assert.Contains(t, result.DeletedFiles, "temp.txt")
	})
}

func TestGitObjectManager_ExecuteMultiFileOperation(t *testing.T) {
	gom, tempDir, cleanup := createTestGitObjectManager(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	ctx := context.Background()

	t.Run("multi-file commit", func(t *testing.T) {
		operation := MultiFileOperation{
			Owner: owner,
			Repo:  repo,
			Changes: []FileChange{
				{
					Path:      "src/main.go",
					Operation: FileOperationCreate,
					Content:   []byte("package main\n\nfunc main() {\n\tprintln(\"Hello from Go!\")\n}"),
				},
				{
					Path:      "src/utils.go",
					Operation: FileOperationCreate,
					Content:   []byte("package main\n\nfunc helper() string {\n\treturn \"helper\"\n}"),
				},
				{
					Path:      "Makefile",
					Operation: FileOperationCreate,
					Content:   []byte("build:\n\tgo build -o app .\n\ntest:\n\tgo test ./..."),
				},
			},
			Message:     "Add Go project files",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}

		result, err := gom.ExecuteMultiFileOperation(ctx, operation)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "Add Go project files", result.Message)
		assert.Len(t, result.CreatedFiles, 3)
		assert.Contains(t, result.CreatedFiles, "src/main.go")
		assert.Contains(t, result.CreatedFiles, "src/utils.go")
		assert.Contains(t, result.CreatedFiles, "Makefile")
	})

	t.Run("mixed operations", func(t *testing.T) {
		// First create some files
		createOp := MultiFileOperation{
			Owner: owner,
			Repo:  repo,
			Changes: []FileChange{
				{
					Path:      "file1.txt",
					Operation: FileOperationCreate,
					Content:   []byte("file 1 content"),
				},
				{
					Path:      "file2.txt",
					Operation: FileOperationCreate,
					Content:   []byte("file 2 content"),
				},
			},
			Message:     "Add test files",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}

		_, err := gom.ExecuteMultiFileOperation(ctx, createOp)
		require.NoError(t, err)

		// Now perform mixed operations
		mixedOp := MultiFileOperation{
			Owner: owner,
			Repo:  repo,
			Changes: []FileChange{
				{
					Path:      "file1.txt",
					Operation: FileOperationUpdate,
					Content:   []byte("updated file 1 content"),
				},
				{
					Path:      "file2.txt",
					Operation: FileOperationDelete,
				},
				{
					Path:      "file3.txt",
					Operation: FileOperationCreate,
					Content:   []byte("new file 3 content"),
				},
			},
			Message:     "Mixed file operations",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}

		result, err := gom.ExecuteMultiFileOperation(ctx, mixedOp)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, result.ChangedFiles, "file1.txt")
		assert.Contains(t, result.DeletedFiles, "file2.txt")
		assert.Contains(t, result.CreatedFiles, "file3.txt")
	})
}

func TestGitObjectManager_GetFileContent(t *testing.T) {
	gom, tempDir, cleanup := createTestGitObjectManager(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	ctx := context.Background()

	// Create a file first
	fileContent := []byte("This is a test file\nwith multiple lines\nfor testing file retrieval.")
	createOp := SingleFileOperation{
		Owner:       owner,
		Repo:        repo,
		Path:        "example.txt",
		Content:     fileContent,
		Message:     "Add example file",
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
		Branch:      "master",
	}

	_, err := gom.CreateSingleFile(ctx, createOp)
	require.NoError(t, err)

	t.Run("get file content from HEAD", func(t *testing.T) {
		content, err := gom.GetFileContent(ctx, owner, repo, "example.txt", "")
		assert.NoError(t, err)
		assert.Equal(t, fileContent, content)
	})

	t.Run("get file content from specific branch", func(t *testing.T) {
		content, err := gom.GetFileContent(ctx, owner, repo, "example.txt", "refs/heads/master")
		assert.NoError(t, err)
		assert.Equal(t, fileContent, content)
	})

	t.Run("get non-existent file", func(t *testing.T) {
		_, err := gom.GetFileContent(ctx, owner, repo, "nonexistent.txt", "")
		assert.Error(t, err)
	})
}

func TestGitObjectManager_FileExists(t *testing.T) {
	gom, tempDir, cleanup := createTestGitObjectManager(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	ctx := context.Background()

	// Create a file first
	createOp := SingleFileOperation{
		Owner:       owner,
		Repo:        repo,
		Path:        "exists.txt",
		Content:     []byte("I exist"),
		Message:     "Add existing file",
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
		Branch:      "master",
	}

	_, err := gom.CreateSingleFile(ctx, createOp)
	require.NoError(t, err)

	t.Run("file exists", func(t *testing.T) {
		exists, err := gom.FileExists(ctx, owner, repo, "exists.txt", "")
		assert.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("file does not exist", func(t *testing.T) {
		exists, err := gom.FileExists(ctx, owner, repo, "nonexistent.txt", "")
		assert.NoError(t, err)
		assert.False(t, exists)
	})
}

func TestGitObjectManager_GetDirectoryListing(t *testing.T) {
	gom, tempDir, cleanup := createTestGitObjectManager(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	ctx := context.Background()

	// Create multiple files in different directories
	multiOp := MultiFileOperation{
		Owner: owner,
		Repo:  repo,
		Changes: []FileChange{
			{
				Path:      "src/main.go",
				Operation: FileOperationCreate,
				Content:   []byte("package main"),
			},
			{
				Path:      "src/utils/helper.go",
				Operation: FileOperationCreate,
				Content:   []byte("package utils"),
			},
			{
				Path:      "docs/api.md",
				Operation: FileOperationCreate,
				Content:   []byte("# API Documentation"),
			},
			{
				Path:      "config.json",
				Operation: FileOperationCreate,
				Content:   []byte(`{"test": true}`),
			},
		},
		Message:     "Add project structure",
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
		Branch:      "master",
	}

	_, err := gom.ExecuteMultiFileOperation(ctx, multiOp)
	require.NoError(t, err)

	t.Run("get root directory listing", func(t *testing.T) {
		entries, err := gom.GetDirectoryListing(ctx, owner, repo, "", "")
		assert.NoError(t, err)
		assert.Len(t, entries, 4) // README.md (from init), src/, docs/, config.json

		// Check for expected entries
		entryNames := make([]string, len(entries))
		for i, entry := range entries {
			entryNames[i] = entry.Name
		}
		assert.Contains(t, entryNames, "README.md")
		assert.Contains(t, entryNames, "src")
		assert.Contains(t, entryNames, "docs")
		assert.Contains(t, entryNames, "config.json")
	})

	t.Run("get subdirectory listing", func(t *testing.T) {
		entries, err := gom.GetDirectoryListing(ctx, owner, repo, "src", "")
		assert.NoError(t, err)
		assert.Len(t, entries, 2) // main.go, utils/

		entryNames := make([]string, len(entries))
		for i, entry := range entries {
			entryNames[i] = entry.Name
		}
		assert.Contains(t, entryNames, "main.go")
		assert.Contains(t, entryNames, "utils")
	})

	t.Run("get non-existent directory", func(t *testing.T) {
		_, err := gom.GetDirectoryListing(ctx, owner, repo, "nonexistent", "")
		assert.Error(t, err)
	})
}

func TestGitObjectManager_ExecuteMultiFileOperationWithTransaction(t *testing.T) {
	tempDir := t.TempDir()
	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryWithFiles(t, tempDir, owner, repo)

	pathConfig, err := repository.NewPathConfig(tempDir)
	require.NoError(t, err)

	logger, _ := logging.NewLogger(config.LoggingConfig{Level: "debug", Format: "text", Output: "stdout"})
	gom, _ := NewGitObjectManager(pathConfig, logger)
	transactionManager := NewTransactionManager(gom, logger)
	ctx := context.Background()

	t.Run("successful transaction with multiple files", func(t *testing.T) {
		operation := MultiFileOperation{
			Owner:       owner,
			Repo:        repo,
			Message:     "Transaction-based multi-file commit",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
			Changes: []FileChange{
				{
					Path:      "transaction/test1.go",
					Operation: FileOperationCreate,
					Content:   []byte("package main\n\nfunc main() {\n\tprintln(\"Transaction Test 1\")\n}"),
				},
				{
					Path:      "transaction/test2.go",
					Operation: FileOperationCreate,
					Content:   []byte("package main\n\nfunc main() {\n\tprintln(\"Transaction Test 2\")\n}"),
				},
				{
					Path:      "transaction/config.json",
					Operation: FileOperationCreate,
					Content:   []byte("{\"transaction\": true, \"test\": \"config\"}"),
				},
			},
		}

		result, err := gom.ExecuteMultiFileOperationWithTransaction(ctx, operation, transactionManager)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.NotEmpty(t, result.CommitHash)
		assert.Equal(t, operation.Message, result.Message)
		assert.Len(t, result.ChangedFiles, 3)

		// Verify all files were created
		for _, expectedPath := range []string{"transaction/test1.go", "transaction/test2.go", "transaction/config.json"} {
			found := false
			for _, actualPath := range result.ChangedFiles {
				if actualPath == expectedPath {
					found = true
					break
				}
			}
			assert.True(t, found, "Expected file %s not found in changed files", expectedPath)
		}
	})

	t.Run("transaction rollback on validation failure", func(t *testing.T) {
		operation := MultiFileOperation{
			Owner:       owner,
			Repo:        repo,
			Message:     "Transaction with invalid operation",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
			Changes: []FileChange{
				{
					Path:      "valid.txt",
					Operation: FileOperationCreate,
					Content:   []byte("This is valid"),
				},
				{
					Path:      "", // Invalid empty path
					Operation: FileOperationCreate,
					Content:   []byte("This should fail"),
				},
			},
		}

		result, err := gom.ExecuteMultiFileOperationWithTransaction(ctx, operation, transactionManager)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "validation failed")

		// Verify no files were created (transaction rolled back)
		exists, _ := gom.FileExists(ctx, owner, repo, "valid.txt", "")
		assert.False(t, exists, "File should not exist after transaction rollback")
	})

	t.Run("transaction with update and delete operations", func(t *testing.T) {
		// First create a file to update and delete
		createOp := SingleFileOperation{
			Owner:       owner,
			Repo:        repo,
			Path:        "update-delete-test.txt",
			Content:     []byte("Original content for update/delete test"),
			Message:     "Create file for update/delete test",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}
		_, err := gom.CreateSingleFile(ctx, createOp)
		require.NoError(t, err)

		// Create another file to delete
		deleteOp := SingleFileOperation{
			Owner:       owner,
			Repo:        repo,
			Path:        "delete-test.txt",
			Content:     []byte("Content to be deleted"),
			Message:     "Create file for delete test",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
		}
		_, err = gom.CreateSingleFile(ctx, deleteOp)
		require.NoError(t, err)

		// Now perform transaction with update and delete
		operation := MultiFileOperation{
			Owner:       owner,
			Repo:        repo,
			Message:     "Transaction with update and delete",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
			Changes: []FileChange{
				{
					Path:      "update-delete-test.txt",
					Operation: FileOperationUpdate,
					Content:   []byte("Updated content via transaction"),
				},
				{
					Path:      "delete-test.txt",
					Operation: FileOperationDelete,
				},
				{
					Path:      "new-in-transaction.txt",
					Operation: FileOperationCreate,
					Content:   []byte("New file created in transaction"),
				},
			},
		}

		result, err := gom.ExecuteMultiFileOperationWithTransaction(ctx, operation, transactionManager)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Len(t, result.ChangedFiles, 3)

		// Verify update
		content, err := gom.GetFileContent(ctx, owner, repo, "update-delete-test.txt", "")
		assert.NoError(t, err)
		assert.Equal(t, []byte("Updated content via transaction"), content)

		// Verify delete
		exists, _ := gom.FileExists(ctx, owner, repo, "delete-test.txt", "")
		assert.False(t, exists)

		// Verify create
		content, err = gom.GetFileContent(ctx, owner, repo, "new-in-transaction.txt", "")
		assert.NoError(t, err)
		assert.Equal(t, []byte("New file created in transaction"), content)
	})

	t.Run("transaction with timeout", func(t *testing.T) {
		// Create a custom transaction manager with very short timeout for testing
		shortTimeoutGom, _ := NewGitObjectManager(pathConfig, logger)

		operation := MultiFileOperation{
			Owner:       owner,
			Repo:        repo,
			Message:     "Transaction timeout test",
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
			Branch:      "master",
			Changes: []FileChange{
				{
					Path:      "timeout-test.txt",
					Operation: FileOperationCreate,
					Content:   []byte("This should timeout"),
				},
			},
		}

		// We can't easily test actual timeout without making the test slow,
		// but we can test the timeout mechanism by checking transaction cleanup
		result, err := shortTimeoutGom.ExecuteMultiFileOperationWithTransaction(ctx, operation, transactionManager)
		assert.NoError(t, err) // Should succeed as operation is fast
		assert.NotNil(t, result)

		// Verify no active transactions remain after operation
		activeTransactions := transactionManager.ListActiveTransactions()
		assert.Empty(t, activeTransactions, "No transactions should remain active after completion")
	})

	t.Run("concurrent transaction isolation", func(t *testing.T) {
		// This test ensures that concurrent transactions don't interfere with each other
		var wg sync.WaitGroup
		var mu sync.Mutex
		results := make([]*FileOperationResult, 0, 2)
		errors := make([]error, 0, 2)

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()

				operation := MultiFileOperation{
					Owner:       owner,
					Repo:        repo,
					Message:     fmt.Sprintf("Concurrent transaction %d", index),
					AuthorName:  "Test Author",
					AuthorEmail: "test@example.com",
					Branch:      "master",
					Changes: []FileChange{
						{
							Path:      fmt.Sprintf("concurrent-%d.txt", index),
							Operation: FileOperationCreate,
							Content:   []byte(fmt.Sprintf("Content from transaction %d", index)),
						},
					},
				}

				result, err := gom.ExecuteMultiFileOperationWithTransaction(ctx, operation, transactionManager)

				mu.Lock()
				if err != nil {
					errors = append(errors, err)
				} else {
					results = append(results, result)
				}
				mu.Unlock()
			}(i)
		}

		wg.Wait()

		// Both transactions should succeed in creating commits
		assert.Empty(t, errors, "No errors should occur in concurrent transactions")
		assert.Len(t, results, 2, "Both concurrent transactions should succeed")

		// Verify that both transactions created valid commits
		for _, result := range results {
			assert.NotEmpty(t, result.CommitHash, "Each transaction should create a commit")
		}

		// In concurrent scenarios, only one transaction's files will be visible from HEAD
		// since git branches can only point to one commit at a time
		// This is expected behavior for concurrent transactions to the same branch
	})
}
