package connection

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// timeoutManager implements TimeoutManager interface
type timeoutManager struct {
	conn           net.Conn
	policy         *TimeoutPolicy
	currentPhase   int32 // atomic access to ConnectionPhase
	logger         *zap.Logger
	stopChan       chan struct{}
	operationStart time.Time
	mu             sync.RWMutex
	stopped        int32

	// Progress monitoring
	progressTracker ProgressTracker
	lastCheckTime   time.Time
}

// TimeoutManagerConfig configures the timeout manager
type TimeoutManagerConfig struct {
	Policy          *TimeoutPolicy
	ProgressTracker ProgressTracker
	Logger          *zap.Logger
	OnTimeout       func(phase ConnectionPhase, duration time.Duration)
}

// NewTimeoutManager creates a new timeout manager
func NewTimeoutManager(conn net.Conn, config *TimeoutManagerConfig) TimeoutManager {
	if config.Policy == nil {
		config.Policy = DefaultTimeoutPolicy()
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	tm := &timeoutManager{
		conn:            conn,
		policy:          config.Policy,
		currentPhase:    int32(PhaseHandshake), // Start with handshake
		logger:          config.Logger,
		stopChan:        make(chan struct{}),
		operationStart:  time.Now(),
		progressTracker: config.ProgressTracker,
		lastCheckTime:   time.Now(),
	}

	// Start monitoring goroutine
	go tm.monitor()

	return tm
}

// SetPhase sets the connection phase and applies appropriate timeout
func (tm *timeoutManager) SetPhase(ctx context.Context, phase ConnectionPhase) error {
	if atomic.LoadInt32(&tm.stopped) == 1 {
		return NewConnectionClosedError("set timeout phase")
	}

	oldPhase := ConnectionPhase(atomic.SwapInt32(&tm.currentPhase, int32(phase)))

	tm.logger.Debug("Connection phase transition",
		zap.String("from", tm.phaseToString(oldPhase)),
		zap.String("to", tm.phaseToString(phase)))

	// Apply timeout for the new phase
	if err := tm.applyPhaseTimeout(ctx, phase); err != nil {
		// Rollback phase change
		atomic.StoreInt32(&tm.currentPhase, int32(oldPhase))
		return WrapError(err, ErrorCategoryTimeout, fmt.Sprintf("failed to apply timeout for phase %v", phase))
	}

	// Reset operation start time for new phase
	tm.mu.Lock()
	tm.operationStart = time.Now()
	tm.lastCheckTime = time.Now()
	tm.mu.Unlock()

	return nil
}

// GetCurrentPhase returns the current connection phase
func (tm *timeoutManager) GetCurrentPhase() ConnectionPhase {
	return ConnectionPhase(atomic.LoadInt32(&tm.currentPhase))
}

// IsWithinLimits checks if the operation is within timeout limits
func (tm *timeoutManager) IsWithinLimits(ctx context.Context) error {
	if atomic.LoadInt32(&tm.stopped) == 1 {
		return NewConnectionClosedError("check timeout limits")
	}

	phase := tm.GetCurrentPhase()

	tm.mu.RLock()
	operationDuration := time.Since(tm.operationStart)
	tm.mu.RUnlock()

	switch phase {
	case PhaseHandshake:
		if operationDuration > tm.policy.HandshakeTimeout {
			return NewTimeoutError(phase, operationDuration, "handshake_timeout")
		}

	case PhaseAuth:
		if operationDuration > tm.policy.AuthTimeout {
			return NewTimeoutError(phase, operationDuration, "auth_timeout")
		}

	case PhaseProtocolNegotiation:
		if operationDuration > tm.policy.ProtocolNegTimeout {
			return NewTimeoutError(phase, operationDuration, "protocol_negotiation_timeout")
		}

	case PhaseOperation:
		// For operation phase, prioritize stall detection over absolute timeout
		// as it's more intelligent and indicates actual progress stoppage

		// Check for stall first if progress tracker is available
		if tm.progressTracker != nil && tm.progressTracker.IsStalled() {
			return NewTimeoutError(phase, operationDuration, "operation_stalled")
		}

		// Fallback to max timeout check
		if operationDuration > tm.policy.OperationMaxTimeout {
			return NewTimeoutError(phase, operationDuration, "operation_max_timeout")
		}
	}

	// Check context timeout
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return nil
}

// Stop stops the timeout manager
func (tm *timeoutManager) Stop() {
	if !atomic.CompareAndSwapInt32(&tm.stopped, 0, 1) {
		return // Already stopped
	}

	close(tm.stopChan)
}

// monitor runs in a separate goroutine to monitor timeouts
func (tm *timeoutManager) monitor() {
	ticker := time.NewTicker(tm.policy.ProgressCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-tm.stopChan:
			return

		case <-ticker.C:
			if atomic.LoadInt32(&tm.stopped) == 1 {
				return
			}

			// Check if we're within limits
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			if err := tm.IsWithinLimits(ctx); err != nil {
				tm.logger.Warn("Timeout detected",
					zap.String("phase", tm.phaseToString(tm.GetCurrentPhase())),
					zap.Error(err))

				// Could trigger callback here if needed
				// For now, just log - the actual connection will be closed
				// by the component using this timeout manager
			}
			cancel()
		}
	}
}

// applyPhaseTimeout applies the appropriate timeout for the given phase
func (tm *timeoutManager) applyPhaseTimeout(ctx context.Context, phase ConnectionPhase) error {
	if tm.conn == nil {
		return nil // No connection to apply timeout to
	}

	var timeout time.Duration

	switch phase {
	case PhaseHandshake:
		timeout = tm.policy.HandshakeTimeout
	case PhaseAuth:
		timeout = tm.policy.AuthTimeout
	case PhaseProtocolNegotiation:
		timeout = tm.policy.ProtocolNegTimeout
	case PhaseOperation:
		// For operation phase, we don't set a hard deadline
		// Instead, we rely on progress monitoring
		return tm.conn.SetDeadline(time.Time{}) // Clear any existing deadline
	default:
		return NewValidationError("phase", phase, "UNKNOWN_PHASE", fmt.Sprintf("unknown phase: %v", phase))
	}

	deadline := time.Now().Add(timeout)
	return tm.conn.SetDeadline(deadline)
}

// phaseToString converts phase to string for logging
func (tm *timeoutManager) phaseToString(phase ConnectionPhase) string {
	switch phase {
	case PhaseHandshake:
		return "handshake"
	case PhaseAuth:
		return "auth"
	case PhaseProtocolNegotiation:
		return "protocol_negotiation"
	case PhaseOperation:
		return "operation"
	default:
		return fmt.Sprintf("unknown(%d)", phase)
	}
}
