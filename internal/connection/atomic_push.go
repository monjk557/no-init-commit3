package connection

import (
	"fmt"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"go.uber.org/zap"
)

// AtomicPushHandler ensures atomicity of push operations
type AtomicPushHandler struct {
	repo             *gogit.Repository
	refUpdateTracker *AtomicRefUpdater
	hookCoordinator  *HookCoordinator
	logger           *zap.Logger
	mu               sync.RWMutex
	operationID      string
	startTime        time.Time
}

// AtomicRefUpdater manages atomic reference updates
type AtomicRefUpdater struct {
	mu             sync.RWMutex
	pendingUpdates map[string]*RefUpdate
	originalRefs   map[string]*plumbing.Reference
	lockManager    *RefLockManager
	logger         *zap.Logger
}

// RefUpdate represents a single reference update operation
type RefUpdate struct {
	RefName   plumbing.ReferenceName
	OldHash   plumbing.Hash
	NewHash   plumbing.Hash
	Operation RefUpdateOperation
	Timestamp time.Time
	Applied   bool
}

type RefUpdateOperation int

const (
	RefUpdateCreate RefUpdateOperation = iota
	RefUpdateUpdate
	RefUpdateDelete
)

func (r RefUpdateOperation) String() string {
	switch r {
	case RefUpdateCreate:
		return "create"
	case RefUpdateUpdate:
		return "update"
	case RefUpdateDelete:
		return "delete"
	default:
		return "unknown"
	}
}

// RefLockManager manages locks on Git references to prevent concurrent modifications
type RefLockManager struct {
	locks  map[string]*refLock
	mu     sync.RWMutex
	logger *zap.Logger
}

type refLock struct {
	refName  string
	holder   string
	acquired time.Time
	timeout  time.Duration
	mu       sync.Mutex
}

// HookCoordinator manages Git hooks during push operations
type HookCoordinator struct {
	preReceiveHooks  []string
	updateHooks      []string
	postReceiveHooks []string
	hookTimeout      time.Duration
	logger           *zap.Logger
}

// NewAtomicPushHandler creates a new atomic push handler
func NewAtomicPushHandler(repo *gogit.Repository, operationID string, logger *zap.Logger) *AtomicPushHandler {
	return &AtomicPushHandler{
		repo:             repo,
		refUpdateTracker: NewAtomicRefUpdater(logger),
		hookCoordinator:  NewHookCoordinator(logger),
		logger:           logger,
		operationID:      operationID,
		startTime:        time.Now(),
	}
}

// NewAtomicRefUpdater creates a new atomic reference updater
func NewAtomicRefUpdater(logger *zap.Logger) *AtomicRefUpdater {
	return &AtomicRefUpdater{
		pendingUpdates: make(map[string]*RefUpdate),
		originalRefs:   make(map[string]*plumbing.Reference),
		lockManager:    NewRefLockManager(logger),
		logger:         logger,
	}
}

// NewRefLockManager creates a new reference lock manager
func NewRefLockManager(logger *zap.Logger) *RefLockManager {
	return &RefLockManager{
		locks:  make(map[string]*refLock),
		logger: logger,
	}
}

// NewHookCoordinator creates a new hook coordinator
func NewHookCoordinator(logger *zap.Logger) *HookCoordinator {
	configManager := NewConfigManager()
	return &HookCoordinator{
		hookTimeout: configManager.Timeouts.OperationIdle, // Use configured timeout for hooks
		logger:      logger,
	}
}

// LockRef acquires a lock on a Git reference
func (rlm *RefLockManager) LockRef(refName, holder string, timeout time.Duration) error {
	rlm.mu.Lock()
	defer rlm.mu.Unlock()

	if existing, exists := rlm.locks[refName]; exists {
		// Check if lock has expired
		if time.Since(existing.acquired) < existing.timeout {
			return NewConcurrencyError(OperationPush, "REFERENCE_LOCKED", 1, 1)
		}
		// Lock expired, remove it
		delete(rlm.locks, refName)
	}

	// Acquire new lock
	rlm.locks[refName] = &refLock{
		refName:  refName,
		holder:   holder,
		acquired: time.Now(),
		timeout:  timeout,
	}

	rlm.logger.Debug("Reference locked",
		zap.String("ref", refName),
		zap.String("holder", holder),
		zap.Duration("timeout", timeout))

	return nil
}

// UnlockRef releases a lock on a Git reference
func (rlm *RefLockManager) UnlockRef(refName, holder string) error {
	rlm.mu.Lock()
	defer rlm.mu.Unlock()

	lock, exists := rlm.locks[refName]
	if !exists {
		return NewValidationError("refName", refName, "NO_LOCK", fmt.Sprintf("no lock found for reference %s", refName))
	}

	if lock.holder != holder {
		return NewValidationError("holder", holder, "WRONG_HOLDER", fmt.Sprintf("reference %s is locked by %s, not %s", refName, lock.holder, holder))
	}

	delete(rlm.locks, refName)
	rlm.logger.Debug("Reference unlocked",
		zap.String("ref", refName),
		zap.String("holder", holder))

	return nil
}

// PrepareRefUpdate prepares a reference update for atomic execution
func (aru *AtomicRefUpdater) PrepareRefUpdate(repo *gogit.Repository, refName string, oldHash, newHash string) error {
	if repo == nil {
		return NewValidationError("repository", nil, "REQUIRED", "repository cannot be nil")
	}

	aru.mu.Lock()
	defer aru.mu.Unlock()

	refNamePlumbing := plumbing.ReferenceName(refName)
	oldHashPlumbing := plumbing.NewHash(oldHash)
	newHashPlumbing := plumbing.NewHash(newHash)

	// Store original reference state for rollback
	if original, err := repo.Reference(refNamePlumbing, true); err == nil {
		aru.originalRefs[refName] = original
	} else if err != plumbing.ErrReferenceNotFound {
		return WrapError(err, ErrorCategoryProtocol, fmt.Sprintf("failed to get original reference %s", refName))
	}

	// Determine operation type
	var operation RefUpdateOperation
	zeroHash := plumbing.ZeroHash

	if newHashPlumbing == zeroHash {
		operation = RefUpdateDelete
	} else if oldHashPlumbing == zeroHash {
		operation = RefUpdateCreate
	} else {
		operation = RefUpdateUpdate
	}

	// Validate the update
	if err := aru.validateRefUpdate(repo, refNamePlumbing, oldHashPlumbing, newHashPlumbing, operation); err != nil {
		return WrapError(err, ErrorCategoryValidation, "ref update validation failed")
	}

	// Store pending update
	update := &RefUpdate{
		RefName:   refNamePlumbing,
		OldHash:   oldHashPlumbing,
		NewHash:   newHashPlumbing,
		Operation: operation,
		Timestamp: time.Now(),
		Applied:   false,
	}

	aru.pendingUpdates[refName] = update

	aru.logger.Debug("Reference update prepared",
		zap.String("ref", refName),
		zap.String("operation", operation.String()),
		zap.String("old_hash", oldHash),
		zap.String("new_hash", newHash))

	return nil
}

// validateRefUpdate validates a reference update
func (aru *AtomicRefUpdater) validateRefUpdate(repo *gogit.Repository, refName plumbing.ReferenceName, oldHash, newHash plumbing.Hash, operation RefUpdateOperation) error {
	switch operation {
	case RefUpdateCreate:
		// For create operations, ensure reference doesn't already exist
		if _, err := repo.Reference(refName, true); err == nil {
			return NewValidationError("refName", refName, "ALREADY_EXISTS", fmt.Sprintf("reference %s already exists", refName))
		} else if err != plumbing.ErrReferenceNotFound {
			return WrapError(err, ErrorCategoryProtocol, "failed to check reference existence")
		}

	case RefUpdateUpdate:
		// For update operations, ensure old hash matches current state
		currentRef, err := repo.Reference(refName, true)
		if err != nil {
			return NewValidationError("refName", refName, "NOT_EXISTS", fmt.Sprintf("reference %s does not exist for update", refName))
		}
		if currentRef.Hash() != oldHash {
			return NewValidationError("oldHash", oldHash, "HASH_MISMATCH", fmt.Sprintf("reference %s hash mismatch: expected %s, got %s", refName, oldHash, currentRef.Hash()))
		}

	case RefUpdateDelete:
		// For delete operations, ensure reference exists and matches old hash
		currentRef, err := repo.Reference(refName, true)
		if err != nil {
			return NewValidationError("refName", refName, "NOT_EXISTS", fmt.Sprintf("reference %s does not exist for deletion", refName))
		}
		if currentRef.Hash() != oldHash {
			return NewValidationError("oldHash", oldHash, "HASH_MISMATCH", fmt.Sprintf("reference %s hash mismatch for deletion: expected %s, got %s", refName, oldHash, currentRef.Hash()))
		}
	}

	return nil
}

// ApplyRefUpdates atomically applies all prepared reference updates
func (aru *AtomicRefUpdater) ApplyRefUpdates(repo *gogit.Repository, operationID string) error {
	aru.mu.Lock()
	defer aru.mu.Unlock()

	if len(aru.pendingUpdates) == 0 {
		return nil
	}

	// Acquire locks for all references
	configManager := NewConfigManager()
	lockTimeout := configManager.Timeouts.OperationMax // Use operation max timeout for locks
	var lockedRefs []string

	defer func() {
		// Always release locks
		for _, refName := range lockedRefs {
			_ = aru.lockManager.UnlockRef(refName, operationID)
		}
	}()

	// Lock all references first
	for refName := range aru.pendingUpdates {
		if err := aru.lockManager.LockRef(refName, operationID, lockTimeout); err != nil {
			return WrapError(err, ErrorCategoryConcurrency, fmt.Sprintf("failed to lock reference %s", refName))
		}
		lockedRefs = append(lockedRefs, refName)
	}

	// Apply all updates atomically
	var appliedUpdates []*RefUpdate

	defer func() {
		// If something fails, we might need to rollback applied updates
		if len(appliedUpdates) != len(aru.pendingUpdates) {
			aru.logger.Warn("Partial ref updates applied, attempting rollback",
				zap.Int("applied", len(appliedUpdates)),
				zap.Int("total", len(aru.pendingUpdates)))
		}
	}()

	for refName, update := range aru.pendingUpdates {
		if err := aru.applyRefUpdate(repo, update); err != nil {
			// Rollback previously applied updates
			aru.rollbackAppliedUpdates(repo, appliedUpdates)
			return WrapError(err, ErrorCategoryProtocol, fmt.Sprintf("failed to apply ref update %s", refName))
		}

		update.Applied = true
		appliedUpdates = append(appliedUpdates, update)

		aru.logger.Info("Reference update applied",
			zap.String("ref", refName),
			zap.String("operation", update.Operation.String()),
			zap.String("new_hash", update.NewHash.String()))
	}

	// Clear pending updates
	aru.pendingUpdates = make(map[string]*RefUpdate)

	aru.logger.Info("All reference updates applied atomically",
		zap.Int("count", len(appliedUpdates)),
		zap.String("operation_id", operationID))

	return nil
}

// applyRefUpdate applies a single reference update
func (aru *AtomicRefUpdater) applyRefUpdate(repo *gogit.Repository, update *RefUpdate) error {
	switch update.Operation {
	case RefUpdateCreate, RefUpdateUpdate:
		ref := plumbing.NewHashReference(update.RefName, update.NewHash)
		return repo.Storer.SetReference(ref)

	case RefUpdateDelete:
		return repo.Storer.RemoveReference(update.RefName)

	default:
		return NewValidationError("operation", update.Operation, "UNKNOWN_OPERATION", fmt.Sprintf("unknown ref update operation: %v", update.Operation))
	}
}

// rollbackAppliedUpdates attempts to rollback applied reference updates
func (aru *AtomicRefUpdater) rollbackAppliedUpdates(repo *gogit.Repository, appliedUpdates []*RefUpdate) {
	for i := len(appliedUpdates) - 1; i >= 0; i-- {
		update := appliedUpdates[i]
		refName := update.RefName.String()

		// Restore original reference state
		if originalRef, exists := aru.originalRefs[refName]; exists {
			if err := repo.Storer.SetReference(originalRef); err != nil {
				aru.logger.Error("Failed to rollback reference update",
					zap.String("ref", refName),
					zap.Error(err))
			} else {
				aru.logger.Info("Reference update rolled back",
					zap.String("ref", refName))
			}
		} else {
			// Reference didn't exist originally, remove it
			if err := repo.Storer.RemoveReference(update.RefName); err != nil {
				aru.logger.Error("Failed to remove reference during rollback",
					zap.String("ref", refName),
					zap.Error(err))
			} else {
				aru.logger.Info("Reference removed during rollback",
					zap.String("ref", refName))
			}
		}
	}
}

// RollbackPartialUpdates rolls back any partial updates in case of timeout
func (aru *AtomicRefUpdater) RollbackPartialUpdates(repo *gogit.Repository) error {
	aru.mu.Lock()
	defer aru.mu.Unlock()

	var appliedUpdates []*RefUpdate
	for _, update := range aru.pendingUpdates {
		if update.Applied {
			appliedUpdates = append(appliedUpdates, update)
		}
	}

	if len(appliedUpdates) > 0 {
		aru.logger.Warn("Rolling back partial ref updates",
			zap.Int("count", len(appliedUpdates)))
		aru.rollbackAppliedUpdates(repo, appliedUpdates)
	}

	// Clear pending updates
	aru.pendingUpdates = make(map[string]*RefUpdate)
	aru.originalRefs = make(map[string]*plumbing.Reference)

	return nil
}

// HandlePushTimeout handles timeout scenarios during push operations
func (aph *AtomicPushHandler) HandlePushTimeout() error {
	aph.mu.Lock()
	defer aph.mu.Unlock()

	aph.logger.Warn("Push operation timed out, ensuring atomicity",
		zap.String("operation_id", aph.operationID),
		zap.Duration("duration", time.Since(aph.startTime)))

	// Rollback any partial reference updates
	if err := aph.refUpdateTracker.RollbackPartialUpdates(aph.repo); err != nil {
		aph.logger.Error("Failed to rollback partial updates",
			zap.String("operation_id", aph.operationID),
			zap.Error(err))
		return WrapError(err, ErrorCategoryProtocol, "failed to rollback partial updates")
	}

	return nil
}

// PreparePushOperation prepares all reference updates for atomic execution
func (aph *AtomicPushHandler) PreparePushOperation(refUpdates []string) error {
	aph.mu.Lock()
	defer aph.mu.Unlock()

	aph.logger.Info("Preparing atomic push operation",
		zap.String("operation_id", aph.operationID),
		zap.Int("ref_count", len(refUpdates)))

	for _, refUpdate := range refUpdates {
		oldHash, newHash, refName, err := parseRefUpdate(refUpdate)
		if err != nil {
			return WrapError(err, ErrorCategoryValidation, fmt.Sprintf("failed to parse ref update %s", refUpdate))
		}

		if err := aph.refUpdateTracker.PrepareRefUpdate(aph.repo, refName, oldHash, newHash); err != nil {
			return WrapError(err, ErrorCategoryValidation, fmt.Sprintf("failed to prepare ref update %s", refName))
		}
	}

	return nil
}

// ExecutePushOperation atomically executes the prepared push operation
func (aph *AtomicPushHandler) ExecutePushOperation() error {
	aph.mu.Lock()
	defer aph.mu.Unlock()

	return aph.refUpdateTracker.ApplyRefUpdates(aph.repo, aph.operationID)
}

// parseRefUpdate parses a Git protocol ref update line
func parseRefUpdate(refUpdate string) (oldHash, newHash, refName string, err error) {
	// Parse ref update line: "old-hash new-hash ref-name\0capabilities" or "old-hash new-hash ref-name capabilities"
	// Handle null-separated format first
	if len(refUpdate) == 0 {
		return "", "", "", NewValidationError("refUpdate", refUpdate, "EMPTY", "empty ref update")
	}

	// Remove null bytes and parse
	refUpdate = string([]byte(refUpdate)) // Ensure proper string handling
	parts := make([]string, 0)

	// Split by null byte first
	if nullIndex := findNull(refUpdate); nullIndex >= 0 {
		refAndHashes := refUpdate[:nullIndex]
		parts = splitFields(refAndHashes)
	} else {
		// Space-separated format
		parts = splitFields(refUpdate)
	}

	if len(parts) < 3 {
		return "", "", "", NewValidationError("refUpdate", refUpdate, "INVALID_FORMAT", fmt.Sprintf("invalid ref update format: %s", refUpdate))
	}

	return parts[0], parts[1], parts[2], nil
}

// Helper functions for string parsing
func findNull(s string) int {
	for i, c := range s {
		if c == '\x00' {
			return i
		}
	}
	return -1
}

func splitFields(s string) []string {
	var result []string
	var current []rune

	for _, c := range s {
		if c == ' ' || c == '\t' {
			if len(current) > 0 {
				result = append(result, string(current))
				current = current[:0]
			}
		} else {
			current = append(current, c)
		}
	}

	if len(current) > 0 {
		result = append(result, string(current))
	}

	return result
}
