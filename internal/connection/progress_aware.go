package connection

import (
	"bufio"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ConnectionPhase represents different phases of Git connection lifecycle
type ConnectionPhase int

const (
	PhaseHandshake ConnectionPhase = iota
	PhaseAuth
	PhaseProtocolNegotiation
	PhaseOperation
)

// TimeoutPolicy defines timeout behavior for different connection phases
type TimeoutPolicy struct {
	HandshakeTimeout      time.Duration // Short timeout for initial connection setup
	AuthTimeout           time.Duration // Short timeout for authentication completion
	ProtocolNegTimeout    time.Duration // Medium timeout for Git protocol negotiation
	OperationIdleTimeout  time.Duration // Idle timeout during operation (no data flow)
	OperationMaxTimeout   time.Duration // Absolute maximum timeout for operations
	StallThreshold        time.Duration // Time with zero progress to consider stalled
	ProgressCheckInterval time.Duration // How often to check for progress
}

// DefaultTimeoutPolicy returns sensible defaults based on p-001.md requirements
// Uses centralized configuration manager
func DefaultTimeoutPolicy() *TimeoutPolicy {
	configManager := NewConfigManager()
	return configManager.ToTimeoutPolicy()
}

// TimeoutPolicyFromConfig creates TimeoutPolicy from ConfigManager
func TimeoutPolicyFromConfig(config *ConfigManager) *TimeoutPolicy {
	if config == nil {
		return DefaultTimeoutPolicy()
	}
	return config.ToTimeoutPolicy()
}

// TransferProgressTracker implements ProgressTracker interface
type TransferProgressTracker struct {
	mu                 sync.RWMutex
	bytesTransferred   int64
	lastProgressTime   time.Time
	transferRate       *MovingAverage
	startTime          time.Time
	lastCheckTime      time.Time
	stallThreshold     time.Duration
	isPackObjectsPhase bool // Special handling for pack-objects phase
}

// Ensure TransferProgressTracker implements ProgressTracker interface
var _ ProgressTracker = (*TransferProgressTracker)(nil)

// MovingAverage calculates moving average of transfer rates
type MovingAverage struct {
	window []float64
	size   int
	index  int
	filled bool
	sum    float64
}

// NewMovingAverage creates a new moving average calculator
func NewMovingAverage(size int) *MovingAverage {
	return &MovingAverage{
		window: make([]float64, size),
		size:   size,
	}
}

// Add adds a new value to the moving average
func (ma *MovingAverage) Add(value float64) {
	ma.sum -= ma.window[ma.index]
	ma.window[ma.index] = value
	ma.sum += value

	ma.index = (ma.index + 1) % ma.size
	if ma.index == 0 {
		ma.filled = true
	}
}

// Average returns the current moving average
func (ma *MovingAverage) Average() float64 {
	if ma.filled {
		return ma.sum / float64(ma.size)
	}
	if ma.index == 0 {
		return 0
	}
	return ma.sum / float64(ma.index)
}

// NewTransferProgressTracker creates a new progress tracker
func NewTransferProgressTracker(stallThreshold time.Duration) *TransferProgressTracker {
	now := time.Now()
	return &TransferProgressTracker{
		transferRate:     NewMovingAverage(10), // 10-sample moving average
		startTime:        now,
		lastProgressTime: now,
		lastCheckTime:    now,
		stallThreshold:   stallThreshold,
	}
}

// RecordProgress records bytes transferred and updates progress timestamp
func (t *TransferProgressTracker) RecordProgress(newBytes int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.bytesTransferred += newBytes

	if newBytes > 0 {
		// Any progress resets the stall detection
		t.lastProgressTime = now

		// Calculate transfer rate (bytes per second)
		if timeDiff := now.Sub(t.lastCheckTime).Seconds(); timeDiff > 0 {
			rate := float64(newBytes) / timeDiff
			t.transferRate.Add(rate)
		}
		t.lastCheckTime = now
	}
}

// SetPackObjectsPhase sets whether we're in pack-objects phase
func (t *TransferProgressTracker) SetPackObjectsPhase(inPhase bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.isPackObjectsPhase = inPhase
}

// IsStalled determines if the transfer is stalled vs just slow
func (t *TransferProgressTracker) IsStalled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	timeSinceProgress := now.Sub(t.lastProgressTime)

	// Special handling for pack-objects phase where network silence is normal
	if t.isPackObjectsPhase {
		// In pack-objects phase, allow longer silence
		return timeSinceProgress > (t.stallThreshold * 3)
	}

	// Normal stall detection
	return timeSinceProgress > t.stallThreshold
}

// GetProgress returns current progress statistics
func (t *TransferProgressTracker) GetProgress() (bytesTransferred int64, rate float64, duration time.Duration) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.bytesTransferred, t.transferRate.Average(), time.Since(t.startTime)
}

// ProgressAwareConnection wraps a TCP connection with progress-aware timeout management
type ProgressAwareConnection struct {
	conn            net.Conn
	reader          *bufio.Reader
	writer          *bufio.Writer
	progressTracker *TransferProgressTracker
	timeoutPolicy   *TimeoutPolicy
	currentPhase    ConnectionPhase
	logger          *zap.Logger

	// Connection metadata
	owner    string
	repo     string
	userID   string
	lastUsed time.Time
	protoV2  bool

	// Lifecycle management
	stopChan chan struct{}
	once     sync.Once
	closed   int32
}

// NewProgressAwareConnection creates a new progress-aware connection wrapper
func NewProgressAwareConnection(conn net.Conn, policy *TimeoutPolicy, logger *zap.Logger) *ProgressAwareConnection {
	if policy == nil {
		policy = DefaultTimeoutPolicy()
	}

	pac := &ProgressAwareConnection{
		conn:            conn,
		reader:          bufio.NewReader(conn),
		writer:          bufio.NewWriter(conn),
		progressTracker: NewTransferProgressTracker(policy.StallThreshold),
		timeoutPolicy:   policy,
		currentPhase:    PhaseHandshake,
		logger:          logger,
		lastUsed:        time.Now(),
		stopChan:        make(chan struct{}),
	}

	// Start progress monitoring
	go pac.monitorProgress()

	return pac
}

// SetPhase transitions the connection to a new phase with appropriate timeout
func (pac *ProgressAwareConnection) SetPhase(phase ConnectionPhase) error {
	if atomic.LoadInt32(&pac.closed) == 1 {
		return NewConnectionClosedError("set connection phase")
	}

	pac.currentPhase = phase

	var timeout time.Duration
	switch phase {
	case PhaseHandshake:
		timeout = pac.timeoutPolicy.HandshakeTimeout
	case PhaseAuth:
		timeout = pac.timeoutPolicy.AuthTimeout
	case PhaseProtocolNegotiation:
		timeout = pac.timeoutPolicy.ProtocolNegTimeout
	case PhaseOperation:
		// Operation phase uses different timeout logic (progress-based)
		timeout = pac.timeoutPolicy.OperationMaxTimeout
	}

	if phase != PhaseOperation {
		// Use absolute timeout for non-operation phases
		deadline := time.Now().Add(timeout)
		return pac.conn.SetDeadline(deadline)
	}

	// For operation phase, clear absolute deadline and rely on progress monitoring
	return pac.conn.SetDeadline(time.Time{})
}

// monitorProgress runs in a goroutine to monitor transfer progress
func (pac *ProgressAwareConnection) monitorProgress() {
	ticker := time.NewTicker(pac.timeoutPolicy.ProgressCheckInterval)
	defer ticker.Stop()

	operationStartTime := time.Now()

	for {
		select {
		case <-ticker.C:
			if atomic.LoadInt32(&pac.closed) == 1 {
				return
			}

			// Only monitor during operation phase
			if pac.currentPhase != PhaseOperation {
				continue
			}

			// Check for stall
			if pac.progressTracker.IsStalled() {
				pac.logger.Warn("Connection stalled, closing",
					zap.String("owner", pac.owner),
					zap.String("repo", pac.repo),
					zap.Duration("stall_duration", time.Since(pac.progressTracker.lastProgressTime)))
				pac.Close()
				return
			}

			// Check for absolute maximum timeout
			if time.Since(operationStartTime) > pac.timeoutPolicy.OperationMaxTimeout {
				pac.logger.Warn("Operation exceeded maximum timeout, closing",
					zap.String("owner", pac.owner),
					zap.String("repo", pac.repo),
					zap.Duration("duration", time.Since(operationStartTime)))
				pac.Close()
				return
			}

		case <-pac.stopChan:
			return
		}
	}
}

// Read wraps the underlying connection read with progress tracking
func (pac *ProgressAwareConnection) Read(p []byte) (int, error) {
	if atomic.LoadInt32(&pac.closed) == 1 {
		return 0, NewConnectionClosedError("read")
	}

	n, err := pac.reader.Read(p)
	if n > 0 {
		// Record the actual bytes read (incremental)
		pac.progressTracker.RecordProgress(int64(n))
		pac.lastUsed = time.Now()
	}

	return n, err
}

// Write wraps the underlying connection write with progress tracking
func (pac *ProgressAwareConnection) Write(p []byte) (int, error) {
	if atomic.LoadInt32(&pac.closed) == 1 {
		return 0, NewConnectionClosedError("write")
	}

	n, err := pac.writer.Write(p)
	if n > 0 {
		// Record the actual bytes written (incremental)
		pac.progressTracker.RecordProgress(int64(n))
		pac.lastUsed = time.Now()
	}

	return n, err
}

// Flush flushes the buffered writer
func (pac *ProgressAwareConnection) Flush() error {
	if atomic.LoadInt32(&pac.closed) == 1 {
		return NewConnectionClosedError("flush")
	}

	return pac.writer.Flush()
}

// Close closes the connection and stops monitoring
func (pac *ProgressAwareConnection) Close() error {
	pac.once.Do(func() {
		atomic.StoreInt32(&pac.closed, 1)
		close(pac.stopChan)
		if pac.conn != nil {
			pac.conn.Close()
		}
	})
	return nil
}

// GetReader returns the buffered reader
func (pac *ProgressAwareConnection) GetReader() *bufio.Reader {
	return pac.reader
}

// GetWriter returns the buffered writer
func (pac *ProgressAwareConnection) GetWriter() *bufio.Writer {
	return pac.writer
}

// SetConnectionInfo sets connection metadata
func (pac *ProgressAwareConnection) SetConnectionInfo(owner, repo, userID string, protoV2 bool) {
	pac.owner = owner
	pac.repo = repo
	pac.userID = userID
	pac.protoV2 = protoV2
}

// GetConnectionInfo returns connection metadata
func (pac *ProgressAwareConnection) GetConnectionInfo() (owner, repo, userID string, protoV2 bool) {
	return pac.owner, pac.repo, pac.userID, pac.protoV2
}

// GetProgressTracker returns the progress tracker for direct manipulation
func (pac *ProgressAwareConnection) GetProgressTracker() *TransferProgressTracker {
	return pac.progressTracker
}

// GetRemoteAddr returns the remote address
func (pac *ProgressAwareConnection) GetRemoteAddr() net.Addr {
	if pac.conn != nil {
		return pac.conn.RemoteAddr()
	}
	return nil
}

// IsAlive checks if the connection is still alive and not stalled
func (pac *ProgressAwareConnection) IsAlive() bool {
	return atomic.LoadInt32(&pac.closed) == 0 && !pac.progressTracker.IsStalled()
}
