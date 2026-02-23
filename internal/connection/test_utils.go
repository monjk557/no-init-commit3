package connection

import (
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// testTimeoutPolicy creates a standardized timeout policy for testing
func testTimeoutPolicy() *TimeoutPolicy {
	return &TimeoutPolicy{
		HandshakeTimeout:      500 * time.Millisecond,
		AuthTimeout:           1 * time.Second,
		ProtocolNegTimeout:    2 * time.Second,
		OperationIdleTimeout:  5 * time.Second,
		OperationMaxTimeout:   30 * time.Second,
		StallThreshold:        2 * time.Second,
		ProgressCheckInterval: 500 * time.Millisecond,
	}
}

// veryFastTimeoutPolicy creates timeout policy for tests that need very short timeouts
func veryFastTimeoutPolicy() *TimeoutPolicy {
	return &TimeoutPolicy{
		HandshakeTimeout:      50 * time.Millisecond,
		AuthTimeout:           50 * time.Millisecond,
		ProtocolNegTimeout:    50 * time.Millisecond,
		OperationIdleTimeout:  100 * time.Millisecond,
		OperationMaxTimeout:   100 * time.Millisecond,
		StallThreshold:        50 * time.Millisecond,
		ProgressCheckInterval: 10 * time.Millisecond,
	}
}

// integrationTimeoutPolicy creates timeout policy suitable for integration tests
func integrationTimeoutPolicy() *TimeoutPolicy {
	return &TimeoutPolicy{
		HandshakeTimeout:      5 * time.Second,
		AuthTimeout:           10 * time.Second,
		ProtocolNegTimeout:    15 * time.Second,
		OperationIdleTimeout:  60 * time.Second,
		OperationMaxTimeout:   5 * time.Minute,
		StallThreshold:        2 * time.Second,
		ProgressCheckInterval: 500 * time.Millisecond,
	}
}

// createTestManager creates a standardized connection manager for testing
func createTestManager(t *testing.T, conn net.Conn) ConnectionManager {
	logger := zaptest.NewLogger(t)
	config := &ConnectionManagerConfig{
		Policy: testTimeoutPolicy(),
		Logger: logger,
	}

	manager, err := NewConnectionManager(conn, config)
	if err != nil {
		t.Fatalf("Failed to create test manager: %v", err)
	}
	return manager
}
