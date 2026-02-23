package connection

import (
	"bufio"
	"context"
	"io"
	"net"
	"time"
)

// ConnectionManager manages the lifecycle and behavior of Git connections
type ConnectionManager interface {
	// Lifecycle
	IsAlive() bool
	Close() error
	GetRemoteAddr() net.Addr

	// IO Operations
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Flush() error
	GetReader() *bufio.Reader
	GetWriter() *bufio.Writer

	// Progress and Timeout Management
	SetPhase(phase ConnectionPhase) error
	GetProgressTracker() ProgressTracker
	GetCurrentPhase() ConnectionPhase

	// Metadata
	GetConnectionInfo() (owner, repo, userID string, protoV2 bool)
	SetConnectionInfo(owner, repo, userID string, protoV2 bool)

	// Health and Status
	GetHealthStatus() map[string]interface{}
}

// ProgressTracker tracks transfer progress and detects stalls
type ProgressTracker interface {
	RecordProgress(bytes int64)
	IsStalled() bool
	GetProgress() (bytesTransferred int64, rate float64, duration time.Duration)
	SetPackObjectsPhase(inPhase bool)
}

// TimeoutManager manages connection timeouts across different phases
type TimeoutManager interface {
	SetPhase(ctx context.Context, phase ConnectionPhase) error
	GetCurrentPhase() ConnectionPhase
	IsWithinLimits(ctx context.Context) error
	Stop()
}

// IOWrapper wraps network IO with progress tracking
type IOWrapper interface {
	io.ReadWriter
	GetReader() *bufio.Reader
	GetWriter() *bufio.Writer
	Flush() error
	Close() error
}

// ConnectionLifecycle manages connection state and cleanup
type ConnectionLifecycle interface {
	IsAlive() bool
	IsClosed() bool
	Close() error
	GetRemoteAddr() net.Addr
	UpdateLastUsed()
}

// ConnectionMetadata stores connection-specific information
type ConnectionMetadata interface {
	GetConnectionInfo() (owner, repo, userID string, protoV2 bool)
	SetConnectionInfo(owner, repo, userID string, protoV2 bool)
	GetOwner() string
	GetRepo() string
	GetUserID() string
	IsProtoV2() bool
}

// MetricsReporter reports connection metrics
type MetricsReporter interface {
	RecordProgress(operation OperationType, tenantID string, bytesPerSecond float64)
	RecordTimeout(timeoutType string, operation OperationType)
	RecordStallDetection(operation OperationType, tenantID string)
	RecordConnectionError(errorType string, operation OperationType)
}

// ConnectionFactory creates new connections with proper configuration
type ConnectionFactory interface {
	CreateConnection(conn net.Conn, policy *TimeoutPolicy) (ConnectionManager, error)
	CreateConnectionWithContext(ctx context.Context, conn net.Conn, policy *TimeoutPolicy) (ConnectionManager, error)
	CreateTestConnection(conn net.Conn) (ConnectionManager, error)
}
