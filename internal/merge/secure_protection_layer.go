package merge

import (
	"context"
	"fmt"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"go.uber.org/zap"
)

// SecureProtectionController ensures immutable protection layer control
type SecureProtectionController struct {
	mu                       sync.RWMutex
	protectionEnabled        bool
	envProtectionEnabled     bool
	squashProtectionEnabled  bool
	regularProtectionEnabled bool
	rebaseProtectionEnabled  bool
	initialized              bool
	immutable                bool // Once set, cannot be changed
}

// NewSecureProtectionController creates an immutable protection controller
func NewSecureProtectionController(enabled, envEnabled, squash, regular, rebase bool) *SecureProtectionController {
	return &SecureProtectionController{
		protectionEnabled:        enabled,
		envProtectionEnabled:     envEnabled,
		squashProtectionEnabled:  squash,
		regularProtectionEnabled: regular,
		rebaseProtectionEnabled:  rebase,
		initialized:              true,
		immutable:                true,
	}
}

// IsProtectionEnabled returns protection status in a secure way
func (spc *SecureProtectionController) IsProtectionEnabled(isSquash bool) bool {
	spc.mu.RLock()
	defer spc.mu.RUnlock()

	if !spc.initialized {
		return false
	}

	if !spc.protectionEnabled || !spc.envProtectionEnabled {
		return false
	}

	if isSquash {
		return spc.squashProtectionEnabled
	}

	return spc.regularProtectionEnabled
}

// IsRebaseProtectionEnabled returns rebase protection status
func (spc *SecureProtectionController) IsRebaseProtectionEnabled() bool {
	spc.mu.RLock()
	defer spc.mu.RUnlock()

	return spc.initialized && spc.protectionEnabled && spc.envProtectionEnabled && spc.rebaseProtectionEnabled
}

// String returns protection status for logging (without exposing internal state)
func (spc *SecureProtectionController) String() string {
	spc.mu.RLock()
	defer spc.mu.RUnlock()

	if !spc.initialized {
		return "protection_uninitialized"
	}

	return fmt.Sprintf("protection_enabled=%v,env_enabled=%v,squash=%v,regular=%v,rebase=%v",
		spc.protectionEnabled, spc.envProtectionEnabled,
		spc.squashProtectionEnabled, spc.regularProtectionEnabled,
		spc.rebaseProtectionEnabled)
}

// SecureTreeProtectionLayer provides tamper-resistant protection
type SecureTreeProtectionLayer struct {
	mu               sync.RWMutex
	controller       *SecureProtectionController
	baseLayer        *TreeProtectionLayer
	logger           *zap.Logger
	initialized      time.Time
	operationCounter uint64
}

// NewSecureTreeProtectionLayer creates a tamper-resistant protection layer
func NewSecureTreeProtectionLayer(controller *SecureProtectionController, logger *zap.Logger) *SecureTreeProtectionLayer {
	var baseLayer *TreeProtectionLayer
	if controller.IsProtectionEnabled(false) || controller.IsProtectionEnabled(true) {
		baseLayer = NewTreeProtectionLayer(logger)
	}

	return &SecureTreeProtectionLayer{
		controller:  controller,
		baseLayer:   baseLayer,
		logger:      logger,
		initialized: time.Now(),
	}
}

// ProtectFallback applies protection if and only if flags allow it
func (stpl *SecureTreeProtectionLayer) ProtectFallback(
	ctx context.Context,
	repo *gogit.Repository,
	fallbackTree plumbing.Hash,
	context FallbackContext,
) (plumbing.Hash, error) {
	stpl.mu.Lock()
	stpl.operationCounter++
	opId := stpl.operationCounter
	stpl.mu.Unlock()

	stpl.mu.RLock()
	defer stpl.mu.RUnlock()

	// CRITICAL: Check if protection should be applied
	if !stpl.controller.IsProtectionEnabled(context.IsSquash) {
		stpl.logger.Info("Protection DISABLED - allowing file deletion",
			zap.Bool("is_squash", context.IsSquash),
			zap.Uint64("operation_id", opId),
			zap.String("controller_status", stpl.controller.String()))

		// Return the original fallback tree without any protection
		// This allows files to be deleted as per the original merge behavior
		return fallbackTree, nil
	}

	// Protection is enabled - apply protection layer
	if stpl.baseLayer == nil {
		stpl.logger.Error("Protection enabled but base layer is nil - configuration error",
			zap.Uint64("operation_id", opId))
		return fallbackTree, fmt.Errorf("protection layer misconfigured")
	}

	stpl.logger.Info("Protection ENABLED - applying file protection",
		zap.Bool("is_squash", context.IsSquash),
		zap.Uint64("operation_id", opId),
		zap.String("controller_status", stpl.controller.String()))

	return stpl.baseLayer.ProtectFallback(ctx, repo, fallbackTree, context)
}

// IsProtectionActive returns whether protection is currently active
func (stpl *SecureTreeProtectionLayer) IsProtectionActive(isSquash bool) bool {
	stpl.mu.RLock()
	defer stpl.mu.RUnlock()

	if stpl.baseLayer == nil {
		return false
	}

	return stpl.controller.IsProtectionEnabled(isSquash)
}

// GetProtectionController returns a read-only view of the controller status
func (stpl *SecureTreeProtectionLayer) GetProtectionController() string {
	stpl.mu.RLock()
	defer stpl.mu.RUnlock()

	return stpl.controller.String()
}
