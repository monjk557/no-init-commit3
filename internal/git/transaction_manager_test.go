package git

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/stretchr/testify/assert"
)

// Helper function to create a test logger
func createTestLogger() *logging.Logger {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}
	logger, _ := logging.NewLogger(cfg)
	return logger
}

func TestNewTransactionManager(t *testing.T) {
	logger := createTestLogger()

	// Use the existing real GitObjectManager for testing
	tm := NewTransactionManager(nil, logger)

	assert.NotNil(t, tm)
	assert.Equal(t, logger, tm.logger)
	assert.NotNil(t, tm.activeTransactions)
	assert.Equal(t, 5*time.Minute, tm.defaultTimeout)
}

func TestTransactionManager_GetTransaction(t *testing.T) {
	logger := createTestLogger()
	tm := NewTransactionManager(nil, logger)

	txn := &Transaction{
		ID:      "test_txn",
		State:   TransactionStateActive,
		manager: tm,
	}

	// Add transaction to manager
	tm.activeTransactions["test_txn"] = txn

	t.Run("Get existing transaction", func(t *testing.T) {
		result, err := tm.GetTransaction("test_txn")

		assert.NoError(t, err)
		assert.Equal(t, txn, result)
	})

	t.Run("Get non-existent transaction", func(t *testing.T) {
		result, err := tm.GetTransaction("nonexistent")

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "transaction not found")
	})
}

func TestTransactionManager_ListActiveTransactions(t *testing.T) {
	logger := createTestLogger()
	tm := NewTransactionManager(nil, logger)

	txn1 := &Transaction{ID: "txn1", State: TransactionStateActive}
	txn2 := &Transaction{ID: "txn2", State: TransactionStateActive}

	tm.activeTransactions["txn1"] = txn1
	tm.activeTransactions["txn2"] = txn2

	transactions := tm.ListActiveTransactions()

	assert.Len(t, transactions, 2)
	// Order is not guaranteed, so check both are present
	ids := []string{transactions[0].ID, transactions[1].ID}
	assert.Contains(t, ids, "txn1")
	assert.Contains(t, ids, "txn2")
}

func TestTransactionManager_CleanupTransaction(t *testing.T) {
	logger := createTestLogger()
	tm := NewTransactionManager(nil, logger)

	txn := &Transaction{
		ID:           "test_txn",
		State:        TransactionStateCommitted,
		TimeoutTimer: time.NewTimer(time.Hour),
	}

	tm.activeTransactions["test_txn"] = txn

	t.Run("Cleanup existing transaction", func(t *testing.T) {
		tm.CleanupTransaction("test_txn")

		_, exists := tm.activeTransactions["test_txn"]
		assert.False(t, exists)
	})

	t.Run("Cleanup non-existent transaction", func(t *testing.T) {
		// Should not panic
		tm.CleanupTransaction("nonexistent")
	})
}

func TestTransactionManager_AddOperation(t *testing.T) {
	logger := createTestLogger()
	tm := NewTransactionManager(nil, logger)

	// Create a transaction manually for testing
	txn := &Transaction{
		ID:         "test_txn",
		State:      TransactionStateActive,
		Operations: make([]*TransactionOperation, 0),
		manager:    tm,
	}

	operation := TransactionOperation{
		Type: FileOperationCreate,
		FileChange: FileChange{
			Path:    "test.txt",
			Content: []byte("test content"),
		},
	}

	t.Run("Add operation to active transaction", func(t *testing.T) {
		err := tm.AddOperation(context.Background(), txn, operation)

		assert.NoError(t, err)
		assert.Len(t, txn.Operations, 1)
		assert.Equal(t, "test.txt", txn.Operations[0].FileChange.Path)
		assert.NotEmpty(t, txn.Operations[0].ID)
		assert.Equal(t, TransactionStateActive, txn.Operations[0].State)
	})

	t.Run("Cannot add operation to non-active transaction", func(t *testing.T) {
		txn.State = TransactionStateCommitted
		err := tm.AddOperation(context.Background(), txn, operation)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot add operation to transaction in state")
	})
}

func TestTransactionManager_RollbackTransaction(t *testing.T) {
	logger := createTestLogger()
	tm := NewTransactionManager(nil, logger)

	txn := &Transaction{
		ID:    "test_txn",
		State: TransactionStateActive,
		Operations: []*TransactionOperation{
			{
				ID:    "op1",
				State: TransactionStateActive,
			},
		},
		TimeoutTimer: time.NewTimer(time.Hour), // Create a timer to test cleanup
		manager:      tm,
	}

	t.Run("Successful rollback", func(t *testing.T) {
		err := tm.RollbackTransaction(context.Background(), txn)

		assert.NoError(t, err)
		assert.Equal(t, TransactionStateRolledBack, txn.State)
		assert.Equal(t, TransactionStateRolledBack, txn.Operations[0].State)
	})

	t.Run("Cannot rollback committed transaction", func(t *testing.T) {
		txn.State = TransactionStateCommitted
		err := tm.RollbackTransaction(context.Background(), txn)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot rollback transaction in state")
	})

	t.Run("Cannot rollback already rolled back transaction", func(t *testing.T) {
		txn.State = TransactionStateRolledBack
		err := tm.RollbackTransaction(context.Background(), txn)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot rollback transaction in state")
	})
}

func TestTransaction_HelperMethods(t *testing.T) {
	txn := &Transaction{
		ID: "test_txn",
		Operations: []*TransactionOperation{
			{ID: "op1", State: TransactionStateActive},
			{ID: "op2", State: TransactionStateActive},
		},
		StartTime: time.Now().Add(-30 * time.Second),
		Options: TransactionOptions{
			Timeout: 1 * time.Minute,
		},
	}

	t.Run("GetOperationByID", func(t *testing.T) {
		op, err := txn.GetOperationByID("op1")
		assert.NoError(t, err)
		assert.Equal(t, "op1", op.ID)

		op, err = txn.GetOperationByID("nonexistent")
		assert.Error(t, err)
		assert.Nil(t, op)
	})

	t.Run("GetOperationCount", func(t *testing.T) {
		count := txn.GetOperationCount()
		assert.Equal(t, 2, count)
	})

	t.Run("IsExpired", func(t *testing.T) {
		// Transaction started 30 seconds ago with 1-minute timeout
		assert.False(t, txn.IsExpired())

		// Change start time to make it expired
		txn.StartTime = time.Now().Add(-2 * time.Minute)
		assert.True(t, txn.IsExpired())
	})
}

func TestTransactionResult_HelperMethods(t *testing.T) {
	result := &TransactionResult{
		SuccessfulOperations: []string{"op1", "op2"},
		FailedOperations:     map[string]error{"op3": errors.New("failed")},
	}

	t.Run("GetSuccessfulOperationCount", func(t *testing.T) {
		count := result.GetSuccessfulOperationCount()
		assert.Equal(t, 2, count)
	})

	t.Run("GetFailedOperationCount", func(t *testing.T) {
		count := result.GetFailedOperationCount()
		assert.Equal(t, 1, count)
	})

	t.Run("IsPartialSuccess", func(t *testing.T) {
		assert.True(t, result.IsPartialSuccess())

		// Test with all successful
		result.FailedOperations = make(map[string]error)
		assert.False(t, result.IsPartialSuccess())

		// Test with all failed
		result.SuccessfulOperations = []string{}
		result.FailedOperations = map[string]error{"op1": errors.New("failed")}
		assert.False(t, result.IsPartialSuccess())
	})
}

func TestTransactionOptions(t *testing.T) {
	t.Run("Default options", func(t *testing.T) {
		options := TransactionOptions{}
		assert.Equal(t, time.Duration(0), options.Timeout)
		assert.False(t, options.ValidateOnly)
		assert.False(t, options.AllowPartialCommit)
		assert.Equal(t, 0, options.MaxRetries)
	})

	t.Run("Custom options", func(t *testing.T) {
		options := TransactionOptions{
			Timeout:            30 * time.Second,
			ValidateOnly:       true,
			AllowPartialCommit: true,
			MaxRetries:         3,
		}
		assert.Equal(t, 30*time.Second, options.Timeout)
		assert.True(t, options.ValidateOnly)
		assert.True(t, options.AllowPartialCommit)
		assert.Equal(t, 3, options.MaxRetries)
	})
}

func TestTransactionState(t *testing.T) {
	states := []TransactionState{
		TransactionStateActive,
		TransactionStateValidating,
		TransactionStateCommitting,
		TransactionStateCommitted,
		TransactionStateRolledBack,
		TransactionStateFailed,
	}

	expectedStrings := []string{
		"active",
		"validating",
		"committing",
		"committed",
		"rolled_back",
		"failed",
	}

	for i, state := range states {
		assert.Equal(t, expectedStrings[i], string(state))
	}
}

func TestGenerateRandomID(t *testing.T) {
	t.Run("Generate ID with correct length", func(t *testing.T) {
		id := generateRandomID(8)
		assert.Len(t, id, 8)
	})

	t.Run("Generate different IDs", func(t *testing.T) {
		// Generate multiple IDs to increase confidence they are different
		ids := make([]string, 10)
		for i := 0; i < 10; i++ {
			ids[i] = generateRandomID(10)
			// Add a small delay to ensure different timestamps
			time.Sleep(time.Nanosecond)
		}

		// Check that at least some IDs are different (allowing for potential collisions)
		uniqueIds := make(map[string]bool)
		for _, id := range ids {
			uniqueIds[id] = true
		}

		// We expect at least half the IDs to be unique (very conservative)
		assert.GreaterOrEqual(t, len(uniqueIds), 5, "Expected at least 5 unique IDs out of 10")
	})

	t.Run("Generate ID with zero length", func(t *testing.T) {
		id := generateRandomID(0)
		assert.Empty(t, id)
	})
}

func TestFileOperationConstants(t *testing.T) {
	assert.Equal(t, "create", string(FileOperationCreate))
	assert.Equal(t, "update", string(FileOperationUpdate))
	assert.Equal(t, "delete", string(FileOperationDelete))
}

func TestConcurrentTransactionOperations(t *testing.T) {
	logger := createTestLogger()
	tm := NewTransactionManager(nil, logger)

	// Test concurrent access to transaction manager
	t.Run("Concurrent transaction list access", func(t *testing.T) {
		// Add some transactions manually for testing (avoiding go-git concurrent access)
		tm.mutex.Lock()
		for i := 0; i < 10; i++ {
			txn := &Transaction{
				ID:    fmt.Sprintf("test_txn_%d", i),
				State: TransactionStateActive,
			}
			tm.activeTransactions[txn.ID] = txn
		}
		tm.mutex.Unlock()

		// Test concurrent reads
		done := make(chan bool, 20)

		// 20 goroutines trying to list active transactions concurrently
		for i := 0; i < 20; i++ {
			go func(id int) {
				defer func() { done <- true }()

				// This should be thread-safe
				transactions := tm.ListActiveTransactions()
				assert.Len(t, transactions, 10)
			}(i)
		}

		// Wait for all goroutines to complete
		for i := 0; i < 20; i++ {
			<-done
		}

		// Verify final state
		transactions := tm.ListActiveTransactions()
		assert.Len(t, transactions, 10)
	})
}

func TestTransactionStateTransitions(t *testing.T) {
	logger := createTestLogger()
	tm := NewTransactionManager(nil, logger)

	txn := &Transaction{
		ID:         "test_txn",
		State:      TransactionStateActive,
		Operations: make([]*TransactionOperation, 0),
		Options: TransactionOptions{
			ValidateOnly: true,
		},
		StartTime: time.Now(),
		manager:   tm,
	}

	t.Run("Transaction lifecycle states", func(t *testing.T) {
		// Initial state
		assert.Equal(t, TransactionStateActive, txn.State)

		// Test state transitions without external dependencies
		originalState := txn.State

		// Rollback should work from active state
		err := tm.RollbackTransaction(context.Background(), txn)
		assert.NoError(t, err)
		assert.Equal(t, TransactionStateRolledBack, txn.State)

		// Reset for next test
		txn.State = originalState

		// Should be able to add operations to active transaction
		operation := TransactionOperation{
			Type: FileOperationCreate,
			FileChange: FileChange{
				Path:    "test.txt",
				Content: []byte("content"),
			},
		}

		err = tm.AddOperation(context.Background(), txn, operation)
		assert.NoError(t, err)
		assert.Len(t, txn.Operations, 1)
	})
}
