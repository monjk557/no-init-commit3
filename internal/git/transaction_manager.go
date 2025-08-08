// Package git provides transaction management for atomic multi-file Git operations.
package git

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// TransactionState represents the state of a transaction
type TransactionState string

const (
	// TransactionStateActive - transaction is active and can accept operations
	TransactionStateActive TransactionState = "active"
	// TransactionStateValidating - transaction is being validated
	TransactionStateValidating TransactionState = "validating"
	// TransactionStateCommitting - transaction is being committed
	TransactionStateCommitting TransactionState = "committing"
	// TransactionStateCommitted - transaction has been successfully committed
	TransactionStateCommitted TransactionState = "committed"
	// TransactionStateRolledBack - transaction has been rolled back
	TransactionStateRolledBack TransactionState = "rolled_back"
	// TransactionStateFailed - transaction has failed
	TransactionStateFailed TransactionState = "failed"
)

// TransactionOptions configures transaction behavior
type TransactionOptions struct {
	// Timeout is the maximum duration for the transaction
	Timeout time.Duration
	// ValidateOnly performs validation without committing
	ValidateOnly bool
	// AllowPartialCommit allows committing even if some operations fail
	AllowPartialCommit bool
	// MaxRetries is the maximum number of retry attempts
	MaxRetries int
}

// TransactionOperation represents a single operation within a transaction
type TransactionOperation struct {
	// ID is a unique identifier for this operation
	ID string
	// Type is the operation type
	Type FileOperation
	// FileChange contains the file change details
	FileChange FileChange
	// State is the current state of this operation
	State TransactionState
	// Error contains any error that occurred during this operation
	Error error
	// Timestamp is when this operation was added
	Timestamp time.Time
}

// TransactionResult represents the result of a transaction
type TransactionResult struct {
	// TransactionID is the unique identifier for this transaction
	TransactionID string
	// State is the final state of the transaction
	State TransactionState
	// CommitResult contains the commit information if successful
	CommitResult *CommitResult
	// Operations contains details about all operations
	Operations []*TransactionOperation
	// SuccessfulOperations lists IDs of successful operations
	SuccessfulOperations []string
	// FailedOperations lists IDs and errors of failed operations
	FailedOperations map[string]error
	// StartTime is when the transaction started
	StartTime time.Time
	// EndTime is when the transaction completed
	EndTime time.Time
	// Duration is the total transaction duration
	Duration time.Duration
}

// Transaction represents an atomic multi-file operation
type Transaction struct {
	// ID is the unique identifier for this transaction
	ID string
	// State is the current transaction state
	State TransactionState
	// Repository is the target Git repository
	Repository *git.Repository
	// BaseCommitHash is the base commit for this transaction
	BaseCommitHash plumbing.Hash
	// BaseTree is the base tree for this transaction
	BaseTree *object.Tree
	// Operations contains all operations in this transaction
	Operations []*TransactionOperation
	// CommitInfo contains commit metadata
	CommitInfo CommitInfo
	// Options contains transaction configuration
	Options TransactionOptions
	// StartTime is when the transaction was created
	StartTime time.Time
	// TimeoutTimer tracks transaction timeout
	TimeoutTimer *time.Timer
	// mutex protects concurrent access
	mutex sync.RWMutex
	// manager is the transaction manager reference
	manager *TransactionManager
}

// TransactionManager manages Git transactions for atomic multi-file operations
type TransactionManager struct {
	objectManager GitObjectManagerInterface
	logger        *logging.Logger
	// activeTransactions tracks all active transactions
	activeTransactions map[string]*Transaction
	// mutex protects concurrent access to activeTransactions
	mutex sync.RWMutex
	// defaultTimeout is the default transaction timeout
	defaultTimeout time.Duration
}

// TransactionManagerInterface defines the interface for transaction management
type TransactionManagerInterface interface {
	// BeginTransaction starts a new transaction
	BeginTransaction(ctx context.Context, repo *git.Repository, baseCommit plumbing.Hash, commitInfo CommitInfo, options TransactionOptions) (*Transaction, error)

	// AddOperation adds an operation to a transaction
	AddOperation(ctx context.Context, txn *Transaction, operation TransactionOperation) error

	// ValidateTransaction validates all operations in a transaction
	ValidateTransaction(ctx context.Context, txn *Transaction) error

	// CommitTransaction commits all operations in a transaction atomically
	CommitTransaction(ctx context.Context, txn *Transaction) (*TransactionResult, error)

	// RollbackTransaction rolls back a transaction
	RollbackTransaction(ctx context.Context, txn *Transaction) error

	// GetTransaction retrieves a transaction by ID
	GetTransaction(txnID string) (*Transaction, error)

	// ListActiveTransactions returns all active transactions
	ListActiveTransactions() []*Transaction

	// CleanupTransaction removes a completed transaction
	CleanupTransaction(txnID string)
}

// NewTransactionManager creates a new transaction manager
func NewTransactionManager(objectManager GitObjectManagerInterface, logger *logging.Logger) *TransactionManager {
	return &TransactionManager{
		objectManager:      objectManager,
		logger:             logger,
		activeTransactions: make(map[string]*Transaction),
		defaultTimeout:     5 * time.Minute, // Default 5-minute timeout
	}
}

// BeginTransaction starts a new transaction
func (tm *TransactionManager) BeginTransaction(ctx context.Context, repo *git.Repository, baseCommit plumbing.Hash, commitInfo CommitInfo, options TransactionOptions) (*Transaction, error) {
	txnID := fmt.Sprintf("txn_%d_%s", time.Now().UnixNano(), generateRandomID(8))

	tm.logger.Info("Beginning new transaction",
		zap.String("transaction_id", txnID),
		zap.String("base_commit", baseCommit.String()),
		zap.Duration("timeout", options.Timeout))

	// Set default timeout if not specified
	if options.Timeout == 0 {
		options.Timeout = tm.defaultTimeout
	}

	// Get base tree from commit
	commitObj, err := repo.CommitObject(baseCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to get base commit: %w", err)
	}

	baseTree, err := commitObj.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	txn := &Transaction{
		ID:             txnID,
		State:          TransactionStateActive,
		Repository:     repo,
		BaseCommitHash: baseCommit,
		BaseTree:       baseTree,
		Operations:     make([]*TransactionOperation, 0),
		CommitInfo:     commitInfo,
		Options:        options,
		StartTime:      time.Now(),
		manager:        tm,
	}

	// Set up timeout timer
	txn.TimeoutTimer = time.AfterFunc(options.Timeout, func() {
		tm.logger.Warn("Transaction timeout",
			zap.String("transaction_id", txnID),
			zap.Duration("timeout", options.Timeout))
		if rollbackErr := tm.RollbackTransaction(ctx, txn); rollbackErr != nil {
			tm.logger.Error("Failed to rollback timed-out transaction",
				zap.String("transaction_id", txnID),
				zap.Error(rollbackErr))
		}
	})

	// Store transaction
	tm.mutex.Lock()
	tm.activeTransactions[txnID] = txn
	tm.mutex.Unlock()

	return txn, nil
}

// AddOperation adds an operation to a transaction
func (tm *TransactionManager) AddOperation(ctx context.Context, txn *Transaction, operation TransactionOperation) error {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State != TransactionStateActive {
		return fmt.Errorf("cannot add operation to transaction in state: %s", txn.State)
	}

	// Generate operation ID if not provided
	if operation.ID == "" {
		operation.ID = fmt.Sprintf("op_%d_%s", len(txn.Operations), generateRandomID(6))
	}

	operation.State = TransactionStateActive
	operation.Timestamp = time.Now()

	tm.logger.Debug("Adding operation to transaction",
		zap.String("transaction_id", txn.ID),
		zap.String("operation_id", operation.ID),
		zap.String("operation_type", string(operation.Type)),
		zap.String("file_path", operation.FileChange.Path))

	txn.Operations = append(txn.Operations, &operation)
	return nil
}

// ValidateTransaction validates all operations in a transaction
func (tm *TransactionManager) ValidateTransaction(ctx context.Context, txn *Transaction) error {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State != TransactionStateActive {
		return fmt.Errorf("cannot validate transaction in state: %s", txn.State)
	}

	txn.State = TransactionStateValidating

	tm.logger.Info("Validating transaction",
		zap.String("transaction_id", txn.ID),
		zap.Int("operation_count", len(txn.Operations)))

	// Extract file changes for validation
	changes := make([]FileChange, len(txn.Operations))
	for i, op := range txn.Operations {
		changes[i] = op.FileChange
	}

	// Validate all file changes
	if err := tm.objectManager.ValidateFileChanges(ctx, changes); err != nil {
		txn.State = TransactionStateFailed
		return fmt.Errorf("transaction validation failed: %w", err)
	}

	// Validate individual operations
	for _, op := range txn.Operations {
		op.State = TransactionStateValidating

		// Check for file existence if needed
		if op.Type == FileOperationUpdate || op.Type == FileOperationDelete {
			_, err := tm.objectManager.GetFileFromTree(ctx, txn.BaseTree, op.FileChange.Path)
			if err != nil {
				op.State = TransactionStateFailed
				op.Error = fmt.Errorf("file not found for %s operation: %s", op.Type, op.FileChange.Path)
				if !txn.Options.AllowPartialCommit {
					txn.State = TransactionStateFailed
					return op.Error
				}
			}
		}
	}

	// If we get here, validation passed
	txn.State = TransactionStateActive

	tm.logger.Info("Transaction validation successful",
		zap.String("transaction_id", txn.ID))

	return nil
}

// CommitTransaction commits all operations in a transaction atomically
func (tm *TransactionManager) CommitTransaction(ctx context.Context, txn *Transaction) (*TransactionResult, error) {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State != TransactionStateActive {
		return nil, fmt.Errorf("cannot commit transaction in state: %s", txn.State)
	}

	txn.State = TransactionStateCommitting

	tm.logger.Info("Committing transaction",
		zap.String("transaction_id", txn.ID),
		zap.Int("operation_count", len(txn.Operations)))

	result := &TransactionResult{
		TransactionID:        txn.ID,
		Operations:           txn.Operations,
		SuccessfulOperations: make([]string, 0),
		FailedOperations:     make(map[string]error),
		StartTime:            txn.StartTime,
	}

	// Stop timeout timer
	if txn.TimeoutTimer != nil {
		txn.TimeoutTimer.Stop()
	}

	// If this is a validation-only transaction, return success without committing
	if txn.Options.ValidateOnly {
		result.State = TransactionStateCommitted
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		txn.State = TransactionStateCommitted

		tm.logger.Info("Validation-only transaction completed",
			zap.String("transaction_id", txn.ID))

		return result, nil
	}

	// Prepare file changes for atomic commit
	validChanges := make([]FileChange, 0, len(txn.Operations))
	for _, op := range txn.Operations {
		if op.State != TransactionStateFailed {
			validChanges = append(validChanges, op.FileChange)
			result.SuccessfulOperations = append(result.SuccessfulOperations, op.ID)
		} else {
			result.FailedOperations[op.ID] = op.Error
		}
	}

	// If no valid changes and not allowing partial commits, fail
	if len(validChanges) == 0 && !txn.Options.AllowPartialCommit {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("no valid operations to commit")
	}

	// Create tree from changes
	treeResult, err := tm.objectManager.CreateTreeFromChanges(ctx, txn.Repository, txn.BaseTree, validChanges)
	if err != nil {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("failed to create tree: %w", err)
	}

	// Create commit
	commitResult, err := tm.objectManager.CreateCommit(ctx, txn.Repository, treeResult.TreeHash, txn.CommitInfo)
	if err != nil {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("failed to create commit: %w", err)
	}

	// Success!
	txn.State = TransactionStateCommitted
	result.State = TransactionStateCommitted
	result.CommitResult = commitResult
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	tm.logger.Info("Transaction committed successfully",
		zap.String("transaction_id", txn.ID),
		zap.String("commit_hash", commitResult.Hash.String()),
		zap.Duration("duration", result.Duration),
		zap.Int("successful_operations", len(result.SuccessfulOperations)),
		zap.Int("failed_operations", len(result.FailedOperations)))

	return result, nil
}

// RollbackTransaction rolls back a transaction
func (tm *TransactionManager) RollbackTransaction(ctx context.Context, txn *Transaction) error {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State == TransactionStateCommitted || txn.State == TransactionStateRolledBack {
		return fmt.Errorf("cannot rollback transaction in state: %s", txn.State)
	}

	tm.logger.Info("Rolling back transaction",
		zap.String("transaction_id", txn.ID),
		zap.String("previous_state", string(txn.State)))

	// Stop timeout timer
	if txn.TimeoutTimer != nil {
		txn.TimeoutTimer.Stop()
	}

	// Mark all operations as rolled back
	for _, op := range txn.Operations {
		if op.State != TransactionStateFailed {
			op.State = TransactionStateRolledBack
		}
	}

	txn.State = TransactionStateRolledBack
	return nil
}

// GetTransaction retrieves a transaction by ID
func (tm *TransactionManager) GetTransaction(txnID string) (*Transaction, error) {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()

	txn, exists := tm.activeTransactions[txnID]
	if !exists {
		return nil, fmt.Errorf("transaction not found: %s", txnID)
	}

	return txn, nil
}

// ListActiveTransactions returns all active transactions
func (tm *TransactionManager) ListActiveTransactions() []*Transaction {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()

	transactions := make([]*Transaction, 0, len(tm.activeTransactions))
	for _, txn := range tm.activeTransactions {
		transactions = append(transactions, txn)
	}

	return transactions
}

// CleanupTransaction removes a completed transaction
func (tm *TransactionManager) CleanupTransaction(txnID string) {
	tm.mutex.Lock()
	defer tm.mutex.Unlock()

	if txn, exists := tm.activeTransactions[txnID]; exists {
		// Stop timeout timer if still running
		if txn.TimeoutTimer != nil {
			txn.TimeoutTimer.Stop()
		}
		delete(tm.activeTransactions, txnID)

		tm.logger.Debug("Cleaned up transaction",
			zap.String("transaction_id", txnID))
	}
}

// Helper function to generate random IDs
func generateRandomID(length int) string {
	// Simple implementation - in production, use crypto/rand
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = chars[time.Now().UnixNano()%int64(len(chars))]
	}
	return string(result)
}

// GetOperationByID returns an operation by its ID
func (txn *Transaction) GetOperationByID(operationID string) (*TransactionOperation, error) {
	txn.mutex.RLock()
	defer txn.mutex.RUnlock()

	for _, op := range txn.Operations {
		if op.ID == operationID {
			return op, nil
		}
	}

	return nil, fmt.Errorf("operation not found: %s", operationID)
}

// GetOperationCount returns the number of operations in the transaction
func (txn *Transaction) GetOperationCount() int {
	txn.mutex.RLock()
	defer txn.mutex.RUnlock()

	return len(txn.Operations)
}

// IsExpired checks if the transaction has exceeded its timeout
func (txn *Transaction) IsExpired() bool {
	return time.Since(txn.StartTime) > txn.Options.Timeout
}

// GetSuccessfulOperationCount returns the count of successful operations
func (result *TransactionResult) GetSuccessfulOperationCount() int {
	return len(result.SuccessfulOperations)
}

// GetFailedOperationCount returns the count of failed operations
func (result *TransactionResult) GetFailedOperationCount() int {
	return len(result.FailedOperations)
}

// IsPartialSuccess returns true if some operations succeeded and some failed
func (result *TransactionResult) IsPartialSuccess() bool {
	return len(result.SuccessfulOperations) > 0 && len(result.FailedOperations) > 0
}
