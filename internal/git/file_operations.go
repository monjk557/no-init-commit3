package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// SingleFileOperation represents a single file operation request
type SingleFileOperation struct {
	// Repository information
	Owner string `json:"owner"`
	Repo  string `json:"repo"`

	// File information
	Path    string `json:"path"`
	Content []byte `json:"content"`

	// Commit information
	Message        string `json:"message"`
	AuthorName     string `json:"author_name"`
	AuthorEmail    string `json:"author_email"`
	CommitterName  string `json:"committer_name,omitempty"`
	CommitterEmail string `json:"committer_email,omitempty"`

	// Git information
	Branch string `json:"branch"`
}

// MultiFileOperation represents a multi-file operation request
type MultiFileOperation struct {
	// Repository information
	Owner string `json:"owner"`
	Repo  string `json:"repo"`

	// File changes
	Changes []FileChange `json:"changes"`

	// Commit information
	Message        string `json:"message"`
	AuthorName     string `json:"author_name"`
	AuthorEmail    string `json:"author_email"`
	CommitterName  string `json:"committer_name,omitempty"`
	CommitterEmail string `json:"committer_email,omitempty"`

	// Git information
	Branch string `json:"branch"`
}

// FileOperationResult represents the result of a file operation
type FileOperationResult struct {
	// Operation results
	CommitHash string `json:"commit_hash"`
	TreeHash   string `json:"tree_hash"`

	// File tracking
	ChangedFiles []string `json:"changed_files,omitempty"`
	CreatedFiles []string `json:"created_files,omitempty"`
	DeletedFiles []string `json:"deleted_files,omitempty"`

	// Commit metadata
	Message   string    `json:"message"`
	Author    string    `json:"author"`
	Timestamp time.Time `json:"timestamp"`
}

// CreateSingleFile creates a new file in the repository
func (gom *GitObjectManager) CreateSingleFile(ctx context.Context, operation SingleFileOperation) (*FileOperationResult, error) {
	gom.logger.Info("Creating single file",
		zap.String("owner", operation.Owner),
		zap.String("repo", operation.Repo),
		zap.String("path", operation.Path),
		zap.String("branch", operation.Branch))

	// Open repository
	repo, err := gom.OpenRepository(ctx, operation.Owner, operation.Repo)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get current HEAD or branch
	var baseCommitHash plumbing.Hash
	if operation.Branch != "" {
		baseCommitHash, err = gom.ResolveReference(ctx, repo, "refs/heads/"+operation.Branch)
		if err != nil {
			// Branch doesn't exist, get HEAD
			headRef, headErr := gom.GetRepositoryHead(ctx, repo)
			if headErr != nil {
				return nil, fmt.Errorf("failed to resolve branch %s and HEAD: %w", operation.Branch, headErr)
			}
			baseCommitHash = headRef.Hash()
		}
	} else {
		headRef, err := gom.GetRepositoryHead(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository HEAD: %w", err)
		}
		baseCommitHash = headRef.Hash()
	}

	// Get base tree from current commit
	var baseTree *object.Tree
	if !baseCommitHash.IsZero() {
		baseCommit, err := repo.CommitObject(baseCommitHash)
		if err != nil {
			return nil, fmt.Errorf("failed to get base commit: %w", err)
		}
		baseTree, err = baseCommit.Tree()
		if err != nil {
			return nil, fmt.Errorf("failed to get base tree: %w", err)
		}
	}

	// Create file change
	changes := []FileChange{
		{
			Path:      operation.Path,
			Operation: FileOperationCreate,
			Content:   operation.Content,
		},
	}

	// Validate changes
	if err := gom.ValidateFileChanges(ctx, changes); err != nil {
		return nil, fmt.Errorf("file validation failed: %w", err)
	}

	// Create new tree
	treeResult, err := gom.CreateTreeFromChanges(ctx, repo, baseTree, changes)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree: %w", err)
	}

	// Prepare commit info
	commitInfo := CommitInfo{
		Message: operation.Message,
		Author: object.Signature{
			Name:  operation.AuthorName,
			Email: operation.AuthorEmail,
			When:  time.Now(),
		},
	}

	// Set committer if provided
	if operation.CommitterName != "" && operation.CommitterEmail != "" {
		commitInfo.Committer = &object.Signature{
			Name:  operation.CommitterName,
			Email: operation.CommitterEmail,
			When:  time.Now(),
		}
	}

	// Add parent if we have a base commit
	if !baseCommitHash.IsZero() {
		commitInfo.Parents = []plumbing.Hash{baseCommitHash}
	}

	// Create commit
	commitResult, err := gom.CreateCommit(ctx, repo, treeResult.TreeHash, commitInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to create commit: %w", err)
	}

	// Update branch reference if specified
	if operation.Branch != "" {
		refName := plumbing.ReferenceName("refs/heads/" + operation.Branch)
		ref := plumbing.NewHashReference(refName, commitResult.Hash)
		if err := repo.Storer.SetReference(ref); err != nil {
			gom.logger.Warn("Failed to update branch reference",
				zap.String("branch", operation.Branch),
				zap.Error(err))
		}
	}

	return &FileOperationResult{
		CommitHash:   commitResult.Hash.String(),
		TreeHash:     commitResult.TreeHash.String(),
		CreatedFiles: treeResult.CreatedFiles,
		ChangedFiles: treeResult.ChangedFiles,
		DeletedFiles: treeResult.DeletedFiles,
		Message:      commitResult.Message,
		Author:       commitResult.Author.Name,
		Timestamp:    commitResult.Author.When,
	}, nil
}

// UpdateSingleFile updates an existing file in the repository
func (gom *GitObjectManager) UpdateSingleFile(ctx context.Context, operation SingleFileOperation) (*FileOperationResult, error) {
	gom.logger.Info("Updating single file",
		zap.String("owner", operation.Owner),
		zap.String("repo", operation.Repo),
		zap.String("path", operation.Path),
		zap.String("branch", operation.Branch))

	// Use the same logic as CreateSingleFile but with update operation
	return gom.executeSingleFileOperation(ctx, operation, FileOperationUpdate)
}

// DeleteSingleFile deletes a file from the repository
func (gom *GitObjectManager) DeleteSingleFile(ctx context.Context, operation SingleFileOperation) (*FileOperationResult, error) {
	gom.logger.Info("Deleting single file",
		zap.String("owner", operation.Owner),
		zap.String("repo", operation.Repo),
		zap.String("path", operation.Path),
		zap.String("branch", operation.Branch))

	// Use the same logic as CreateSingleFile but with delete operation
	return gom.executeSingleFileOperation(ctx, operation, FileOperationDelete)
}

// executeSingleFileOperation is a helper method to execute single file operations
func (gom *GitObjectManager) executeSingleFileOperation(ctx context.Context, operation SingleFileOperation, fileOp FileOperation) (*FileOperationResult, error) {
	// Open repository
	repo, err := gom.OpenRepository(ctx, operation.Owner, operation.Repo)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get current HEAD or branch
	var baseCommitHash plumbing.Hash
	if operation.Branch != "" {
		baseCommitHash, err = gom.ResolveReference(ctx, repo, "refs/heads/"+operation.Branch)
		if err != nil {
			// Branch doesn't exist, get HEAD
			headRef, headErr := gom.GetRepositoryHead(ctx, repo)
			if headErr != nil {
				return nil, fmt.Errorf("failed to resolve branch %s and HEAD: %w", operation.Branch, headErr)
			}
			baseCommitHash = headRef.Hash()
		}
	} else {
		headRef, err := gom.GetRepositoryHead(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository HEAD: %w", err)
		}
		baseCommitHash = headRef.Hash()
	}

	// Get base tree from current commit
	var baseTree *object.Tree
	if !baseCommitHash.IsZero() {
		baseCommit, err := repo.CommitObject(baseCommitHash)
		if err != nil {
			return nil, fmt.Errorf("failed to get base commit: %w", err)
		}
		baseTree, err = baseCommit.Tree()
		if err != nil {
			return nil, fmt.Errorf("failed to get base tree: %w", err)
		}
	}

	// Create file change based on operation type
	change := FileChange{
		Path:      operation.Path,
		Operation: fileOp,
	}

	// Add content for create/update operations
	if fileOp == FileOperationCreate || fileOp == FileOperationUpdate {
		change.Content = operation.Content
	}

	changes := []FileChange{change}

	// Validate changes
	if err := gom.ValidateFileChanges(ctx, changes); err != nil {
		return nil, fmt.Errorf("file validation failed: %w", err)
	}

	// Create new tree
	treeResult, err := gom.CreateTreeFromChanges(ctx, repo, baseTree, changes)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree: %w", err)
	}

	// Prepare commit info
	commitInfo := CommitInfo{
		Message: operation.Message,
		Author: object.Signature{
			Name:  operation.AuthorName,
			Email: operation.AuthorEmail,
			When:  time.Now(),
		},
	}

	// Set committer if provided
	if operation.CommitterName != "" && operation.CommitterEmail != "" {
		commitInfo.Committer = &object.Signature{
			Name:  operation.CommitterName,
			Email: operation.CommitterEmail,
			When:  time.Now(),
		}
	}

	// Add parent if we have a base commit
	if !baseCommitHash.IsZero() {
		commitInfo.Parents = []plumbing.Hash{baseCommitHash}
	}

	// Create commit
	commitResult, err := gom.CreateCommit(ctx, repo, treeResult.TreeHash, commitInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to create commit: %w", err)
	}

	// Update branch reference if specified
	if operation.Branch != "" {
		refName := plumbing.ReferenceName("refs/heads/" + operation.Branch)
		ref := plumbing.NewHashReference(refName, commitResult.Hash)
		if err := repo.Storer.SetReference(ref); err != nil {
			gom.logger.Warn("Failed to update branch reference",
				zap.String("branch", operation.Branch),
				zap.Error(err))
		}
	}

	return &FileOperationResult{
		CommitHash:   commitResult.Hash.String(),
		TreeHash:     commitResult.TreeHash.String(),
		CreatedFiles: treeResult.CreatedFiles,
		ChangedFiles: treeResult.ChangedFiles,
		DeletedFiles: treeResult.DeletedFiles,
		Message:      commitResult.Message,
		Author:       commitResult.Author.Name,
		Timestamp:    commitResult.Author.When,
	}, nil
}

// ExecuteMultiFileOperation executes a multi-file operation (commit multiple changes)
func (gom *GitObjectManager) ExecuteMultiFileOperation(ctx context.Context, operation MultiFileOperation) (*FileOperationResult, error) {
	gom.logger.Info("Executing multi-file operation",
		zap.String("owner", operation.Owner),
		zap.String("repo", operation.Repo),
		zap.Int("change_count", len(operation.Changes)),
		zap.String("branch", operation.Branch))

	// Open repository
	repo, err := gom.OpenRepository(ctx, operation.Owner, operation.Repo)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get current HEAD or branch
	var baseCommitHash plumbing.Hash
	if operation.Branch != "" {
		baseCommitHash, err = gom.ResolveReference(ctx, repo, "refs/heads/"+operation.Branch)
		if err != nil {
			// Branch doesn't exist, get HEAD
			headRef, headErr := gom.GetRepositoryHead(ctx, repo)
			if headErr != nil {
				return nil, fmt.Errorf("failed to resolve branch %s and HEAD: %w", operation.Branch, headErr)
			}
			baseCommitHash = headRef.Hash()
		}
	} else {
		headRef, err := gom.GetRepositoryHead(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository HEAD: %w", err)
		}
		baseCommitHash = headRef.Hash()
	}

	// Get base tree from current commit
	var baseTree *object.Tree
	if !baseCommitHash.IsZero() {
		baseCommit, err := repo.CommitObject(baseCommitHash)
		if err != nil {
			return nil, fmt.Errorf("failed to get base commit: %w", err)
		}
		baseTree, err = baseCommit.Tree()
		if err != nil {
			return nil, fmt.Errorf("failed to get base tree: %w", err)
		}
	}

	// Validate changes
	if err := gom.ValidateFileChanges(ctx, operation.Changes); err != nil {
		return nil, fmt.Errorf("file validation failed: %w", err)
	}

	// Create new tree
	treeResult, err := gom.CreateTreeFromChanges(ctx, repo, baseTree, operation.Changes)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree: %w", err)
	}

	// Prepare commit info
	commitInfo := CommitInfo{
		Message: operation.Message,
		Author: object.Signature{
			Name:  operation.AuthorName,
			Email: operation.AuthorEmail,
			When:  time.Now(),
		},
	}

	// Set committer if provided
	if operation.CommitterName != "" && operation.CommitterEmail != "" {
		commitInfo.Committer = &object.Signature{
			Name:  operation.CommitterName,
			Email: operation.CommitterEmail,
			When:  time.Now(),
		}
	}

	// Add parent if we have a base commit
	if !baseCommitHash.IsZero() {
		commitInfo.Parents = []plumbing.Hash{baseCommitHash}
	}

	// Create commit
	commitResult, err := gom.CreateCommit(ctx, repo, treeResult.TreeHash, commitInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to create commit: %w", err)
	}

	// Update branch reference if specified
	if operation.Branch != "" {
		refName := plumbing.ReferenceName("refs/heads/" + operation.Branch)
		ref := plumbing.NewHashReference(refName, commitResult.Hash)
		if err := repo.Storer.SetReference(ref); err != nil {
			gom.logger.Warn("Failed to update branch reference",
				zap.String("branch", operation.Branch),
				zap.Error(err))
		}
	}

	return &FileOperationResult{
		CommitHash:   commitResult.Hash.String(),
		TreeHash:     commitResult.TreeHash.String(),
		CreatedFiles: treeResult.CreatedFiles,
		ChangedFiles: treeResult.ChangedFiles,
		DeletedFiles: treeResult.DeletedFiles,
		Message:      commitResult.Message,
		Author:       commitResult.Author.Name,
		Timestamp:    commitResult.Author.When,
	}, nil
}

// ExecuteMultiFileOperationWithTransaction executes a multi-file operation using transaction management for atomicity
func (gom *GitObjectManager) ExecuteMultiFileOperationWithTransaction(ctx context.Context, operation MultiFileOperation, transactionManager *TransactionManager) (*FileOperationResult, error) {
	gom.logger.Info("Executing multi-file operation with transaction",
		zap.String("owner", operation.Owner),
		zap.String("repo", operation.Repo),
		zap.Int("change_count", len(operation.Changes)),
		zap.String("branch", operation.Branch))

	// Open repository
	repo, err := gom.OpenRepository(ctx, operation.Owner, operation.Repo)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get current HEAD or branch
	var baseCommitHash plumbing.Hash
	if operation.Branch != "" {
		baseCommitHash, err = gom.ResolveReference(ctx, repo, "refs/heads/"+operation.Branch)
		if err != nil {
			// Branch doesn't exist, get HEAD
			headRef, headErr := gom.GetRepositoryHead(ctx, repo)
			if headErr != nil {
				return nil, fmt.Errorf("failed to resolve branch %s and HEAD: %w", operation.Branch, headErr)
			}
			baseCommitHash = headRef.Hash()
		}
	} else {
		headRef, err := gom.GetRepositoryHead(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository HEAD: %w", err)
		}
		baseCommitHash = headRef.Hash()
	}

	// Prepare commit info
	commitInfo := CommitInfo{
		Message: operation.Message,
		Author: object.Signature{
			Name:  operation.AuthorName,
			Email: operation.AuthorEmail,
			When:  time.Now(),
		},
	}

	if operation.CommitterName != "" && operation.CommitterEmail != "" {
		commitInfo.Committer = &object.Signature{
			Name:  operation.CommitterName,
			Email: operation.CommitterEmail,
			When:  time.Now(),
		}
	}

	// Begin transaction
	txnOptions := TransactionOptions{
		Timeout:            5 * time.Minute, // Default timeout
		ValidateOnly:       false,
		AllowPartialCommit: false,
		MaxRetries:         3,
	}

	txn, err := transactionManager.BeginTransaction(ctx, repo, baseCommitHash, commitInfo, txnOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Ensure cleanup
	defer func() {
		if txn.State == TransactionStateActive || txn.State == TransactionStateValidating {
			if rollbackErr := transactionManager.RollbackTransaction(ctx, txn); rollbackErr != nil {
				// Log rollback error but don't return it since we're in cleanup
				fmt.Printf("Warning: Failed to rollback transaction %s: %v\n", txn.ID, rollbackErr)
			}
		}
		transactionManager.CleanupTransaction(txn.ID)
	}()

	// Add all operations to transaction
	for _, change := range operation.Changes {
		operation := TransactionOperation{
			Type:       change.Operation,
			FileChange: change,
		}

		if err := transactionManager.AddOperation(ctx, txn, operation); err != nil {
			gom.logger.Error("Failed to add operation to transaction",
				zap.String("transaction_id", txn.ID),
				zap.String("file_path", change.Path),
				zap.Error(err))
			return nil, fmt.Errorf("failed to add operation for %s: %w", change.Path, err)
		}
	}

	// Validate transaction
	if err := transactionManager.ValidateTransaction(ctx, txn); err != nil {
		gom.logger.Error("Transaction validation failed",
			zap.String("transaction_id", txn.ID),
			zap.Error(err))
		return nil, fmt.Errorf("transaction validation failed: %w", err)
	}

	// Commit transaction
	result, err := transactionManager.CommitTransaction(ctx, txn)
	if err != nil {
		gom.logger.Error("Transaction commit failed",
			zap.String("transaction_id", txn.ID),
			zap.Error(err))
		return nil, fmt.Errorf("transaction commit failed: %w", err)
	}

	// Update branch reference if specified
	if operation.Branch != "" && result.CommitResult != nil {
		refName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", operation.Branch))
		newRef := plumbing.NewHashReference(refName, result.CommitResult.Hash)
		if err := repo.Storer.SetReference(newRef); err != nil {
			// This is a critical error as the commit succeeded but branch update failed
			gom.logger.Error("Failed to update branch reference after successful commit",
				zap.String("transaction_id", txn.ID),
				zap.String("branch", operation.Branch),
				zap.String("commit_hash", result.CommitResult.Hash.String()),
				zap.Error(err))
			return nil, fmt.Errorf("failed to update branch reference: %w", err)
		}
	}

	// Convert transaction result to file operation result
	fileResult := &FileOperationResult{
		CommitHash:   result.CommitResult.Hash.String(),
		Message:      operation.Message,
		ChangedFiles: make([]string, len(result.SuccessfulOperations)),
	}

	// Copy the file paths from successful operations
	for i, opID := range result.SuccessfulOperations {
		if op, err := txn.GetOperationByID(opID); err == nil {
			fileResult.ChangedFiles[i] = op.FileChange.Path
		}
	}

	gom.logger.Info("Multi-file transaction completed successfully",
		zap.String("transaction_id", txn.ID),
		zap.String("commit_hash", fileResult.CommitHash),
		zap.Int("files_changed", len(fileResult.ChangedFiles)),
		zap.Duration("duration", result.Duration))

	return fileResult, nil
}

// GetFileContent retrieves the content of a file from a specific commit or branch
func (gom *GitObjectManager) GetFileContent(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	gom.logger.Debug("Getting file content",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("path", path),
		zap.String("ref", ref))

	// Open repository
	repository, err := gom.OpenRepository(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Resolve reference to commit hash
	var commitHash plumbing.Hash
	if ref != "" {
		commitHash, err = gom.ResolveReference(ctx, repository, ref)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve reference %s: %w", ref, err)
		}
	} else {
		// Get HEAD
		headRef, err := gom.GetRepositoryHead(ctx, repository)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository HEAD: %w", err)
		}
		commitHash = headRef.Hash()
	}

	// Get commit
	commit, err := repository.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	// Get tree
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Get file from tree
	file, err := gom.GetFileFromTree(ctx, tree, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get file from tree: %w", err)
	}
	if file == nil {
		return nil, fmt.Errorf("file not found")
	}

	// Read file content
	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("failed to get file reader: %w", err)
	}
	defer reader.Close()

	content := make([]byte, file.Size)
	_, err = reader.Read(content)
	if err != nil {
		return nil, fmt.Errorf("failed to read file content: %w", err)
	}

	return content, nil
}

// FileExists checks if a file exists in the repository at a specific commit or branch
func (gom *GitObjectManager) FileExists(ctx context.Context, owner, repo, path, ref string) (bool, error) {
	_, err := gom.GetFileContent(ctx, owner, repo, path, ref)
	if err != nil {
		// Check if it's a "file not found" error vs other errors
		if strings.Contains(err.Error(), "file not found") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetDirectoryListing gets a list of files and directories at a specific path
func (gom *GitObjectManager) GetDirectoryListing(ctx context.Context, owner, repo, path, ref string) ([]DirectoryEntry, error) {
	gom.logger.Debug("Getting directory listing",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("path", path),
		zap.String("ref", ref))

	// Open repository
	repository, err := gom.OpenRepository(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Resolve reference to commit hash
	var commitHash plumbing.Hash
	if ref != "" {
		commitHash, err = gom.ResolveReference(ctx, repository, ref)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve reference %s: %w", ref, err)
		}
	} else {
		// Get HEAD
		headRef, err := gom.GetRepositoryHead(ctx, repository)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository HEAD: %w", err)
		}
		commitHash = headRef.Hash()
	}

	// Get commit
	commit, err := repository.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	// Get tree
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Navigate to subdirectory if path is provided
	if path != "" && path != "/" {
		cleanPath := filepath.Clean(path)
		subTree, err := tree.Tree(cleanPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get subdirectory tree: %w", err)
		}
		tree = subTree
	}

	// Get directory entries
	var entries []DirectoryEntry
	for _, entry := range tree.Entries {
		entryType := "file"
		if entry.Mode == filemode.Dir {
			entryType = "directory"
		}

		entries = append(entries, DirectoryEntry{
			Name: entry.Name,
			Type: entryType,
			Mode: entry.Mode.String(),
			Hash: entry.Hash.String(),
		})
	}

	return entries, nil
}

// DirectoryEntry represents an entry in a directory listing
type DirectoryEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "file" or "directory"
	Mode string `json:"mode"`
	Hash string `json:"hash"`
}
