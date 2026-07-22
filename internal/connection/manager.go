package connection

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sync"

	"go.uber.org/zap"
)

// connectionManager implements ConnectionManager interface by composing smaller components
type connectionManager struct {
	// Component dependencies
	lifecycle       ConnectionLifecycle
	ioWrapper       IOWrapper
	progressTracker ProgressTracker
	timeoutManager  TimeoutManager
	metadata        ConnectionMetadata
	logger          *zap.Logger

	// Coordination
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

// ConnectionManagerConfig configures the connection manager
type ConnectionManagerConfig struct {
	Policy          *TimeoutPolicy
	Logger          *zap.Logger
	MetricsReporter MetricsReporter
	IOConfig        *IOWrapperConfig
}

// NewConnectionManager creates a new connection manager with proper component composition
func NewConnectionManager(conn net.Conn, config *ConnectionManagerConfig) (ConnectionManager, error) {
	if conn == nil {
		return nil, NewValidationError("connection", nil, "REQUIRED", "connection cannot be nil")
	}

	if config == nil {
		config = &ConnectionManagerConfig{}
	}

	if config.Policy == nil {
		config.Policy = DefaultTimeoutPolicy()
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	if config.IOConfig == nil {
		config.IOConfig = DefaultIOWrapperConfig()
	}

	// Create context for coordination
	ctx, cancel := context.WithCancel(context.Background())

	// Create metadata component
	metadata := NewConnectionMetadata()

	// Create progress tracker
	progressTracker := NewTransferProgressTracker(config.Policy.StallThreshold)

	// Configure IO wrapper with progress callback
	config.IOConfig.ProgressCallback = func(bytes int64) {
		progressTracker.RecordProgress(bytes)
	}

	// Create IO wrapper
	ioWrapper := NewIOWrapper(conn, config.IOConfig)

	// Create lifecycle manager
	lifecycle := NewConnectionLifecycle(conn, &ConnectionLifecycleConfig{
		OnClose: func() {
			// Cleanup when connection closes
			cancel()
			if ioWrapper != nil {
				_ = ioWrapper.Close()
			}
		},
	})

	// Create timeout manager
	timeoutManager := NewTimeoutManager(conn, &TimeoutManagerConfig{
		Policy:          config.Policy,
		ProgressTracker: progressTracker,
		Logger:          config.Logger,
	})

	manager := &connectionManager{
		lifecycle:       lifecycle,
		ioWrapper:       ioWrapper,
		progressTracker: progressTracker,
		timeoutManager:  timeoutManager,
		metadata:        metadata,
		logger:          config.Logger,
		ctx:             ctx,
		cancel:          cancel,
	}

	config.Logger.Debug("Connection manager created",
		zap.String("remote_addr", conn.RemoteAddr().String()))

	return manager, nil
}

// IsAlive checks if the connection is alive and healthy
func (cm *connectionManager) IsAlive() bool {
	return cm.lifecycle.IsAlive() && !cm.progressTracker.IsStalled()
}

// Close closes the connection and all components
func (cm *connectionManager) Close() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.logger.Debug("Closing connection manager")

	// Stop timeout manager first
	if cm.timeoutManager != nil {
		cm.timeoutManager.Stop()
	}

	// Cancel context to signal all components
	if cm.cancel != nil {
		cm.cancel()
	}

	// Close lifecycle (which closes the underlying connection)
	if cm.lifecycle != nil {
		return cm.lifecycle.Close()
	}

	return nil
}

// GetRemoteAddr returns the remote address
func (cm *connectionManager) GetRemoteAddr() net.Addr {
	return cm.lifecycle.GetRemoteAddr()
}

// Read implements io.Reader with progress tracking and timeout management
func (cm *connectionManager) Read(p []byte) (int, error) {
	// Check timeout limits before reading
	if err := cm.timeoutManager.IsWithinLimits(cm.ctx); err != nil {
		return 0, fmt.Errorf("timeout check failed: %w", err)
	}

	// Perform the read
	n, err := cm.ioWrapper.Read(p)

	// Update last used time
	if n > 0 {
		cm.lifecycle.UpdateLastUsed()
	}

	return n, err
}

// Write implements io.Writer with progress tracking and timeout management
func (cm *connectionManager) Write(p []byte) (int, error) {
	// Check timeout limits before writing
	if err := cm.timeoutManager.IsWithinLimits(cm.ctx); err != nil {
		return 0, fmt.Errorf("timeout check failed: %w", err)
	}

	// Perform the write
	n, err := cm.ioWrapper.Write(p)

	// Update last used time
	if n > 0 {
		cm.lifecycle.UpdateLastUsed()
	}

	return n, err
}

// Flush flushes the write buffer
func (cm *connectionManager) Flush() error {
	return cm.ioWrapper.Flush()
}

// GetReader returns the buffered reader
func (cm *connectionManager) GetReader() *bufio.Reader {
	return cm.ioWrapper.GetReader()
}

// GetWriter returns the buffered writer
func (cm *connectionManager) GetWriter() *bufio.Writer {
	return cm.ioWrapper.GetWriter()
}

// SetPhase sets the connection phase
func (cm *connectionManager) SetPhase(phase ConnectionPhase) error {
	cm.logger.Debug("Setting connection phase", zap.String("phase", phase.String()))
	return cm.timeoutManager.SetPhase(cm.ctx, phase)
}

// GetProgressTracker returns the progress tracker
func (cm *connectionManager) GetProgressTracker() ProgressTracker {
	return cm.progressTracker
}

// GetConnectionInfo returns connection metadata
func (cm *connectionManager) GetConnectionInfo() (owner, repo, userID string, protoV2 bool) {
	return cm.metadata.GetConnectionInfo()
}

// SetConnectionInfo sets connection metadata
func (cm *connectionManager) SetConnectionInfo(owner, repo, userID string, protoV2 bool) {
	cm.metadata.SetConnectionInfo(owner, repo, userID, protoV2)
}

// GetCurrentPhase returns the current connection phase
func (cm *connectionManager) GetCurrentPhase() ConnectionPhase {
	return cm.timeoutManager.GetCurrentPhase()
}

// GetHealthStatus returns comprehensive health information
func (cm *connectionManager) GetHealthStatus() map[string]interface{} {
	status := make(map[string]interface{})

	// Connection health
	status["alive"] = cm.IsAlive()
	status["closed"] = cm.lifecycle.IsClosed()

	// Phase information
	status["current_phase"] = cm.GetCurrentPhase().String()

	// Progress information
	if cm.progressTracker != nil {
		bytes, rate, duration := cm.progressTracker.GetProgress()
		status["bytes_transferred"] = bytes
		status["transfer_rate"] = rate
		status["duration"] = duration
		status["stalled"] = cm.progressTracker.IsStalled()
	}

	// Connection metadata
	owner, repo, userID, protoV2 := cm.GetConnectionInfo()
	if owner != "" || repo != "" {
		status["repository"] = fmt.Sprintf("%s/%s", owner, repo)
		status["user_id"] = userID
		status["proto_v2"] = protoV2
	}

	return status
}

// Add ConnectionPhase.String() method for better logging
func (cp ConnectionPhase) String() string {
	switch cp {
	case PhaseHandshake:
		return "handshake"
	case PhaseAuth:
		return "auth"
	case PhaseProtocolNegotiation:
		return "protocol_negotiation"
	case PhaseOperation:
		return "operation"
	default:
		return fmt.Sprintf("unknown(%d)", cp)
	}
}
