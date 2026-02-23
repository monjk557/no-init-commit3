package connection

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// connectionLifecycle implements ConnectionLifecycle interface
type connectionLifecycle struct {
	conn     net.Conn
	closed   int32
	lastUsed int64 // Unix timestamp
	mu       sync.RWMutex

	// Lifecycle callbacks
	onClose func()
}

// ConnectionLifecycleConfig configures the connection lifecycle
type ConnectionLifecycleConfig struct {
	OnClose func()
}

// NewConnectionLifecycle creates a new connection lifecycle manager
func NewConnectionLifecycle(conn net.Conn, config *ConnectionLifecycleConfig) ConnectionLifecycle {
	lifecycle := &connectionLifecycle{
		conn:     conn,
		lastUsed: time.Now().Unix(),
	}

	if config != nil {
		lifecycle.onClose = config.OnClose
	}

	return lifecycle
}

// IsAlive checks if the connection is alive and healthy
func (cl *connectionLifecycle) IsAlive() bool {
	if cl.IsClosed() {
		return false
	}

	// TODO: Add additional health checks like:
	// - Check if underlying connection is still valid
	// - Ping test for long-idle connections
	// - Resource availability checks

	return true
}

// IsClosed returns whether the connection has been closed
func (cl *connectionLifecycle) IsClosed() bool {
	return atomic.LoadInt32(&cl.closed) == 1
}

// Close closes the connection and performs cleanup
func (cl *connectionLifecycle) Close() error {
	if !atomic.CompareAndSwapInt32(&cl.closed, 0, 1) {
		return nil // Already closed
	}

	var err error
	if cl.conn != nil {
		err = cl.conn.Close()
	}

	// Execute cleanup callback if provided
	if cl.onClose != nil {
		cl.onClose()
	}

	return err
}

// GetRemoteAddr returns the remote address of the connection
func (cl *connectionLifecycle) GetRemoteAddr() net.Addr {
	if cl.conn == nil {
		return nil
	}
	return cl.conn.RemoteAddr()
}

// UpdateLastUsed updates the last used timestamp
func (cl *connectionLifecycle) UpdateLastUsed() {
	atomic.StoreInt64(&cl.lastUsed, time.Now().Unix())
}

// GetLastUsed returns the last used time
func (cl *connectionLifecycle) GetLastUsed() time.Time {
	timestamp := atomic.LoadInt64(&cl.lastUsed)
	return time.Unix(timestamp, 0)
}

// GetIdleDuration returns how long the connection has been idle
func (cl *connectionLifecycle) GetIdleDuration() time.Duration {
	return time.Since(cl.GetLastUsed())
}

// IsIdleTimeout checks if connection has been idle for too long
func (cl *connectionLifecycle) IsIdleTimeout(maxIdleDuration time.Duration) bool {
	return cl.GetIdleDuration() > maxIdleDuration
}

// GetConnectionInfo returns basic connection information
func (cl *connectionLifecycle) GetConnectionInfo() map[string]interface{} {
	info := make(map[string]interface{})

	info["closed"] = cl.IsClosed()
	info["last_used"] = cl.GetLastUsed()
	info["idle_duration"] = cl.GetIdleDuration()

	if cl.conn != nil {
		if remoteAddr := cl.GetRemoteAddr(); remoteAddr != nil {
			info["remote_addr"] = remoteAddr.String()
		}
		if localAddr := cl.conn.LocalAddr(); localAddr != nil {
			info["local_addr"] = localAddr.String()
		}
	}

	return info
}
