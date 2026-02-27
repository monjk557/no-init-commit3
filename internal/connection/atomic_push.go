package connection

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"go.uber.org/zap"
)

// AtomicPushHandler ensures atomicity of push operations using native git update-ref.
type AtomicPushHandler struct {
	repoPath    string
	updates     []gitcmd.RefUpdateCmd
	logger      *zap.Logger
	mu          sync.RWMutex
	operationID string
	startTime   time.Time
}

// HookCoordinator manages Git hooks during push operations
type HookCoordinator struct {
	preReceiveHooks  []string
	updateHooks      []string
	postReceiveHooks []string
	hookTimeout      time.Duration
	logger           *zap.Logger
}

// NewAtomicPushHandler creates a new atomic push handler using native git.
func NewAtomicPushHandler(repoPath string, operationID string, logger *zap.Logger) *AtomicPushHandler {
	return &AtomicPushHandler{
		repoPath:    repoPath,
		logger:      logger,
		operationID: operationID,
		startTime:   time.Now(),
	}
}

// NewHookCoordinator creates a new hook coordinator
func NewHookCoordinator(logger *zap.Logger) *HookCoordinator {
	configManager := NewConfigManager()
	return &HookCoordinator{
		hookTimeout: configManager.Timeouts.OperationIdle,
		logger:      logger,
	}
}

// HandlePushTimeout handles timeout scenarios during push operations.
// With native git update-ref transactions, partial updates are automatically rolled back.
func (aph *AtomicPushHandler) HandlePushTimeout() error {
	aph.mu.Lock()
	defer aph.mu.Unlock()

	aph.logger.Warn("Push operation timed out",
		zap.String("operation_id", aph.operationID),
		zap.Duration("duration", time.Since(aph.startTime)))

	// Native git update-ref transaction handles rollback automatically
	return nil
}

// PreparePushOperation prepares all reference updates for atomic execution.
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

		aph.updates = append(aph.updates, gitcmd.RefUpdateCmd{
			RefName: refName,
			OldOID:  oldHash,
			NewOID:  newHash,
		})
	}

	return nil
}

// ExecutePushOperation atomically executes the prepared push operation
// using native git update-ref --stdin -z with transaction protocol.
func (aph *AtomicPushHandler) ExecutePushOperation() error {
	aph.mu.Lock()
	defer aph.mu.Unlock()

	if len(aph.updates) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := gitcmd.AtomicRefUpdate(ctx, aph.repoPath, aph.updates); err != nil {
		return WrapError(err, ErrorCategoryProtocol, "atomic ref update failed")
	}

	aph.logger.Info("All reference updates applied atomically via native git",
		zap.Int("count", len(aph.updates)),
		zap.String("operation_id", aph.operationID))

	return nil
}

// parseRefUpdate parses a Git protocol ref update line
func parseRefUpdate(refUpdate string) (oldHash, newHash, refName string, err error) {
	if len(refUpdate) == 0 {
		return "", "", "", NewValidationError("refUpdate", refUpdate, "EMPTY", "empty ref update")
	}

	refUpdate = string([]byte(refUpdate))
	parts := make([]string, 0)

	if nullIndex := findNull(refUpdate); nullIndex >= 0 {
		refAndHashes := refUpdate[:nullIndex]
		parts = splitFields(refAndHashes)
	} else {
		parts = splitFields(refUpdate)
	}

	if len(parts) < 3 {
		return "", "", "", NewValidationError("refUpdate", refUpdate, "INVALID_FORMAT", fmt.Sprintf("invalid ref update format: %s", refUpdate))
	}

	return parts[0], parts[1], parts[2], nil
}

func findNull(s string) int {
	for i, c := range s {
		if c == '\x00' {
			return i
		}
	}
	return -1
}

func splitFields(s string) []string {
	return strings.Fields(s)
}
